# Research: Scheduler Module for v4

**Feature**: 026-scheduler-v4 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

References are `file:line` in the repositories under `/home/jadmin/projects/go-tangra/`.
"v3" is `go-tangra-scheduler` (branch `main`), "common" is `go-tangra-common`.

## Findings (v3 and v4 as they are)

| # | Finding | Evidence |
|---|---------|----------|
| F1 | v3 schedules through asynq on Redis: every registered type gets an asynq handler, every task becomes an asynq periodic/one-shot job. | go-tangra-scheduler/internal/server/asynq.go:20-58, internal/service/task_service.go:338-374 |
| F2 | v3 forwards to the owning module with `common.service.v1.TaskExecutorService/ExecuteTask`; the module is resolved from the type name prefix, not from registration. | internal/executor/remote_executor.go:56-121, internal/executor/registry.go:69-80 |
| F3 | v3 takes the owner from `req.module_id` (any allow-listed caller can register or unregister another module's types). | internal/service/task_type_service.go:79,124 |
| F4 | v3 `ExecuteTaskRequest.tenant_id` is `uint32` and is never filled (always 0). | common/protos/common/service/v1/task_executor.proto:37; remote_executor.go:84-92 (`TenantID` of an empty RemoteTaskData) |
| F5 | v3 re-enqueues one-shot tasks on every "start all" and at start-up. | internal/server/asynq.go:53-56; task_service.go:220-241 |
| F6 | v3 blocks the caller's request until a wait-result task finishes. | task_service.go:366-369 |
| F7 | v3 metrics collector counts nothing. | internal/metrics/collector.go:17-60 |
| F8 | v3 task types: ipam `ipam:scan-network` (cron `0 3 * * *`, 2 retries), lcm `lcm:check-expiring-certificates` (`0 8 * * *`, 1 retry, `daysBeforeExpiry` 1–365 default 7), notification `notification:send-test-email` (no cron, 1 retry, `recipient` required). | go-tangra-ipam/internal/service/task_executor.go:19-35 + scheduler_registration.go:60-71; go-tangra-lcm/internal/service/scheduler_registration.go:71-83; go-tangra-notification/internal/service/scheduler_registration.go:62-81 |
| F9 | v4 modules read the SPIFFE peer with `authn.FromContext(ctx)` (`PeerIdentity.ID.ServiceName()`), and the per-module `deploy/policy.yaml` admits callers before any handler runs. | go-tangra/authn/peer.go:11-27; go-tangra/identity/spiffeid.go:87; go-tangra-ticket-v4/internal/grpcapi/server.go:27-45 |
| F10 | v4 modules dial peers by logical name through `freya.App.Client(ctx, name)` resolved by `discovery.static`. | go-tangra/freya.go:437; go-tangra/deploy/stack/configs/ticket.yaml (`discovery.static`) |
| F11 | v4 module anatomy (ticket, ipam): OpenAPI-declared HTTP API with `x-freya-permission`, module checks the permission itself through auth `Authorization/Check`, RLS store with goose migrations, audit hypertable, Valkey stream + SSE, manifest registration with the gateway, module roles registered with auth (feature 019). | go-tangra-ticket-v4/internal/httpapi/middleware.go:61-108, internal/app/permissions.go:18-52, internal/store/migrations/0003_rls.sql, pkg/ticketmanifest/manifest.go:43-204 |
| F12 | Module-owned SDKs are nested modules (`sdk/`, `.../sdk/v4`) replaced locally by the service (`replace ... => ./sdk`) and tagged `sdk/vX.Y.Z`. | go-tangra-ipam-v4/go.mod:115, sdk/go.mod:1, .github/workflows/ci.yaml:8 |
| F13 | notification-v4 sends system templates by key (`template_key`, key must start with the caller's service name) through the tenant's default email channel, else the platform channel; only svc/warden and svc/auth may call `Notifier/Send` today. | go-tangra-notification-v4/sdk/api/proto/notification/v1/notification.proto:33-45; internal/notify/systemtemplates.go:31-93; deploy/policy.yaml |
| F14 | auth-v4 exposes no user e-mail address to services: `Profiles.Lookup` returns user id, display name and avatar only. | go-tangra-auth/sdk/api/proto/auth/v1/auth.proto:211-240; go-tangra-ticket-v4/internal/agents/agents.go:1-8 |
| F15 | lcm-v4 has no tenant-scoped "expiring within" query; the active predicate is `status IN ('active','expiring') AND superseded_by IS NULL AND not_after > now`; index `(tenant_id, status, not_after)` exists. | go-tangra-lcm-v4/internal/store/repos.go:425-433,497-505,900; migrations/0001_schema.sql:96 |
| F16 | The running dev stack is `go-tangra/deploy/stack` on `main` (images pinned by `TANGRA_VERSION`, configs bind-mounted); gateway route allow-list is seeded by `gateway-bootstrap -allow`. | go-tangra/deploy/stack/compose.yaml:171-190 |

## D1. Scheduling engine: PostgreSQL work table with leases (asynq rejected)

**Decision**: the scheduler keeps its own state in PostgreSQL (TimescaleDB, RLS) and
runs a small in-process engine on every replica:

1. **Plan** (every `engine.tick`, default 1 s): in one system-scoped transaction,
   `SELECT … FROM scheduler_tasks WHERE enabled AND status='active' AND next_run_at <= now()
   ORDER BY next_run_at FOR UPDATE SKIP LOCKED LIMIT n`. For each row the engine
   decides the occurrence (run, skip or missed), inserts the first attempt into
   `scheduler_executions` (status `queued`) and advances `next_run_at` in the same
   transaction. A row is locked by exactly one replica, so an occurrence is planned
   exactly once (FR-014, SC-003). A partial unique index
   `(task_id, occurrence_at, attempt) WHERE trigger <> 'manual'` is the second guard.
2. **Claim**: `UPDATE scheduler_executions SET status='running', lease_owner, lease_until
   = now()+timeout+grace … WHERE id IN (SELECT … WHERE status='queued' AND due_at<=now()
   FOR UPDATE SKIP LOCKED LIMIT free_slots) RETURNING …`.
3. **Dispatch**: the claimed attempt is sent to the owning module (D3) with the
   task timeout as the call deadline. The outcome is written only while the row is
   still `running` with this replica as `lease_owner` (fencing).
4. **Retry**: a retryable failure with attempts left inserts the next attempt
   (`queued`, `due_at = now + backoff(attempt)`) in the same transaction (D6).
5. **Recover**: `running` rows whose `lease_until` passed (replica crashed) become
   `timed_out` ("scheduler instance lost the run") and are retried like a timeout;
   nothing is lost silently (Edge case "multiple scheduler instances").
6. **Retention**: hourly, executions older than `retention_days` (default 90) are
   deleted (FR-018).

**Rationale**: v4 prefers PostgreSQL + RLS for durable, tenant-owned state; the
schedule, the history and the queue are the same rows, so RLS protects all of them,
backup is a plain export (FR-024) and there is no second copy to reconcile (v3 kept
tasks in Postgres and jobs in Redis and drifted: F5). `FOR UPDATE SKIP LOCKED` gives
multi-replica exactly-once planning without leader election. The load (≤1,000 enabled
tasks, minute resolution, SC-002 5 s) is tiny for a 1 s poll with an index on
`next_run_at`.

**Alternatives rejected**: *asynq on Valkey* — adds a dependency with its own
scheduler state outside RLS, needs Lua/`EVAL` permissions on the shared Valkey,
reproduces F5 unless re-engineered, and periodic tasks in asynq run on every
scheduler instance unless a separate leader is elected. *Valkey leader lease + in-memory
cron* — leader failover windows lose or double occurrences. *Advisory locks per task* —
session-scoped locks do not survive pool reconnects cleanly; row locks in the planning
transaction are simpler.

## D2. Cron, time zones, missed occurrences and overlap

**Decision**: an in-repository 5-field cron package (`internal/cron`): `*`, lists,
ranges, steps (`*/n`, `a-b/n`, `a/n`), month and weekday names, weekday `0`–`7`
(`7` = Sunday), Vixie day-of-month/day-of-week OR rule when both are restricted.
`Next(after, loc)` walks local wall-clock candidates:

- a local time that does not exist (DST spring-forward gap) is **skipped** — detected
  when `time.Date` normalises the wall clock to another hour/minute;
- a local time that exists twice (fall-back) **runs once** — only the first instant
  is generated and every candidate must be strictly after `after`;
- no occurrence within 5 years → the expression is refused ("never fires").

Validation also enforces the platform minimum interval (`min_interval_seconds`,
default 60 s; SR-006) over the next 10 occurrences, and the time zone must be an
IANA name loadable by `time.LoadLocation` (the image ships `tzdata`, D13). Default
time zone `UTC`.

**Missed occurrences**: when a due task's `next_run_at` is older than
`engine.misfire_grace` (default 60 s), the engine counts the cron times between
`next_run_at` and now as missed (`missed_count` on the task, metric), runs **one**
catch-up (`trigger=catch_up`) only if the task has `catch_up=true`, and sets
`next_run_at` to the first occurrence after now (FR-016).

**Overlap**: when a task already has a `queued` or `running` execution, a due
occurrence is recorded as `skipped` "previous run still in progress" (FR-014).

**Rationale**: the spec's DST rules are precise; robfig/cron's DST handling differs
(it fires spring-forward-gap jobs at the transition) and it is effectively
unmaintained. The parser is small, pure and fully testable (100 % + fuzz), which
suits Constitution IV/VI. **Alternative rejected**: `github.com/robfig/cron/v3`.

## D3. Executor transport and contract

**Decision**: the scheduler calls `scheduler.v1.TaskExecutor/ExecuteTask` on the owning
module over the Freya SPIFFE mesh (`a.Freya.Client(ctx, module)`, resolved by
`discovery.static`). Each executing module's `deploy/policy.yaml` allows only
`spiffe://<td>/svc/scheduler` to call that method, and the module's executor server
re-checks the peer (`authn.FromContext`, service name `scheduler`) — defence in depth
(SR-002). The request carries `execution_id`, `occurrence_id`, `task_id`,
`task_type`, `payload` (JSON bytes), `attempt`, `max_attempts`, `tenant_id` (UUID
text; empty only for platform-scoped types) and `scheduled_at` (the intended
occurrence time, not the dispatch time). The response keeps v3's fields
(`success`, `message`, `result_data`, `permanent_failure`). Field numbers of v3 are kept
where the meaning is unchanged; v3's `uint32 tenant_id = 10` is reserved and replaced
by `string tenant_id = 12` (F4, Assumption "Shared contract").

**Where the contract lives**: in the scheduler SDK
(`github.com/go-tangra/go-tangra-scheduler/sdk/v4`, `api/proto/scheduler/v1`), not in
the framework. Modules depend on the scheduler SDK exactly as they depend on the
lcm/notification SDKs; the framework stays free of module contracts and the
contract is versioned with its only server-side counterpart. The SDK also ships:

- `pkg/taskexec` — a ready executor server: caller check (fail closed without a
  caller function), tenant check (UUID, empty only for types the module declared
  platform-scoped), payload bound (64 KiB, JSON object), unknown type → permanent
  failure, panic → failure, message bounded to 4 KiB, result bounded to 64 KiB;
- `pkg/schedulerclient` — the registration client and a `Registrar` loop (retry every
  5 s until accepted, then re-register every 5 min so a scheduler restart or database
  reset re-learns the types; modules no longer unregister on shutdown).

**Alternatives rejected**: *keep go-tangra-common* — v4 has no common module and the
v3 package path/types (uint32 tenant) are wrong for v4; *framework package* — would
couple every module's framework version to the scheduler contract; *HTTP callback* —
off the mesh policy model.

## D4. Registration authenticity (SR-001, FR-002)

**Decision**: `scheduler.v1.Registration/RegisterTaskTypes` and `/UnregisterTaskTypes`
take the owner from the verified SPIFFE peer (`PeerIdentity.ID.ServiceName()`); the
request has no module field (v3 `module_id = 1` is reserved). Every descriptor name
must match `^<module>:[a-z][a-z0-9-]{0,62}$`. A name already owned by another module
is refused for the whole request (`PERMISSION_DENIED type_owned_by_other_module`).
Limits: ≤ 50 descriptors, display name ≤ 200, description ≤ 2,000, schema ≤ 64 KiB
and must compile as JSON Schema with remote references disabled, default cron empty
or valid, default max retries 0–10. The scheduler policy admits only the modules that
execute tasks (`svc/ipam`, `svc/lcm`, `svc/notification`, extend per module).
Registration is idempotent (upsert, `available=true`, `registered_at`). Unregistration
marks the caller's types `available=false` (tasks kept, shown "type unavailable",
occurrences skipped). A platform administrator may retire a removed module's types
from the UI/API (`POST /modules/{module}/unregister`).

When a schema changes, every task of the type is re-validated; failures set
`validity='payload_invalid'` with the validation message; fixing the payload clears
it (edge case "payload schema changes").

## D5. Tenancy, platform scope and authorization

**Decision**:

- Tasks, executions and audit rows carry `tenant_id` (UUID) under FORCE RLS
  (`app.tenant_id`, `app.system`), as every v4 module. Platform-scoped tasks use the
  nil UUID `00000000-0000-0000-0000-000000000000` as their tenant (never a real tenant),
  and their execution request carries an empty `tenant_id` (FR-027).
- Task types are a platform catalog (no tenant) read by every tenant; platform-scoped
  types are filtered out for non-platform-admins (SR-003: "not found").
- Every browser route declares one permission (`x-freya-permission`) checked by the
  module through auth `Authorization/Check` (defence in depth behind the gateway).
  Permission set (feature 019): `scheduler:read` (tasks, types, history, overview,
  stream), `tasks:manage` (create, edit, enable/disable), `tasks:delete`,
  `tasks:control` (start, stop, restart, run now, run again, cancel, bulk),
  `backup:manage`.
- Module roles: **administrator** (all five), **operator** (all except `tasks:delete`
  and `backup:manage`), **viewer** (`scheduler:read`). Built-in grants: owner/admin →
  administrator set, operator → operator set, member/auditor → viewer set.
- Platform administrators (`platform-admin` role in the platform token) additionally
  see and act on every tenant's tasks (the read path switches to the system scope only
  after this check) and are the only ones who may create/edit/control tasks of
  platform-scoped types. Every other caller gets `not_found` for foreign or platform
  rows (SR-003).

**Alternative rejected**: `tasks:manage` covering delete — the spec's Operator role
("all except delete") needs delete as its own permission.

## D6. Retries, timeouts and bounds

- Max retries per task 0–10 (default from the type, else 3); `max_attempts = retries+1`.
- Backoff before attempt *n+1*: `30 s × 2^(n-1)`, capped at 10 min (30 s, 1 min, 2 min,
  4 min, 8 min, 10 min …).
- Retryable: `success=false` without `permanent_failure`, transport errors
  (`Unavailable`, `DeadlineExceeded`, `ResourceExhausted`, `Aborted`, `Internal`,
  `Unknown`, dial failure → "module unavailable"), timeout → status `timed_out`.
  Not retryable: `permanent_failure`, `PermissionDenied`/`Unauthenticated`/
  `InvalidArgument`/`Unimplemented` from the module (a mis-configured policy is not
  fixed by retrying).
- Timeout per attempt default 300 s, max `max_timeout_seconds` (default 3,600).
- Payload ≤ 64 KiB (config `max_payload_bytes`), nesting depth ≤ 32, JSON object only;
  result data ≤ 64 KiB, larger is truncated and flagged; message ≤ 4 KiB.
- Concurrency: `engine.workers` (default 16) attempts in flight per replica.

## D7. Execution history, WAIT_RESULT and live follow

- One `scheduler_executions` row per attempt: id (= execution id sent to the module),
  occurrence id, task, type, module, tenant, trigger (`schedule|manual|catch_up`),
  triggering user, scheduled/due/start/finish times, duration, attempt/max, status
  (`queued|running|succeeded|failed|skipped|timed_out|cancelled`), message, result
  (bytea, truncated flag). The task row keeps the derived last-run fields and counters
  (FR-017).
- **Wait-for-result**: creating the task inserts its first attempt in the same
  transaction and returns `execution_id` at once (FR-020, v3 F6 not kept). "Run now"
  returns the execution id too.
- **Live follow**: every execution state change publishes `scheduler.execution`
  (`{execution_id, task_id, status, attempt}`, no payload/result) to the tenant's
  platform stream (`platform:events:<tenant>`, platform-scoped tasks to the platform
  tenant); the UI listens on `GET /stream` (SSE) and re-reads
  `GET /executions/{id}`; it also polls every 3 s while the run is not final (a
  platform admin following another tenant's run gets no events from that stream).

## D8. Overview and metrics (US6)

`GET /overview` returns, for the caller's scope, task counts by state (enabled,
stopped, completed, cancelled, type unavailable, payload invalid), runs and failures in
the last 24 h, the next 10 runs due and tasks whose last run failed. OpenTelemetry
instruments on the framework meter (admin listener `/metrics`), labels from closed
vocabularies only (type and module names are registered identifiers, never tenant ids
or payload values; SR-007, SC-008):
`scheduler.runs{type,module,status}`, `scheduler.run.duration{type,module}` (s),
`scheduler.retries{type,module}`, `scheduler.occurrences.skipped{type,reason}`,
`scheduler.occurrences.missed{type}`, `scheduler.types.registered{module}` (observable).

## D9. Audit

Closed vocabulary in `scheduler_audit_events` (hypertable, RLS): `task.create`,
`task.update`, `task.delete`, `task.enable`, `task.disable`, `task.start`, `task.stop`,
`task.restart`, `task.run`, `task.cancel`, `tasks.bulk`, `tasktype.register`,
`tasktype.unregister`, `backup.export`, `backup.import`, `access.refused`. Detail is
redacted (keys containing payload, result, body, secret, token, password, email,
recipient are dropped; strings ≤ 256 bytes). Payload values never appear (SR-005).

## D10. Backup (FR-024)

`POST /backup/export` exports the caller's scope (tenant; platform admin: optionally all) as
JSON `{version, exported_at, task_types, tasks, executions}`; `POST /backup/import` imports it
(`backup:manage`). Tasks and executions are restored into the caller's tenant only
(ids kept, existing ids skipped, names made unique). Task types are restored only for
platform administrators, only when absent, and always as `available=false` (a module
must re-register to make them runnable) — a backup can never forge a module's type.

## D11. Ported task types (US5)

| Type | Module work | Notes |
|------|-------------|-------|
| `ipam:scan-network` | queue scans through ipam's existing scan service for the request tenant (subnet id, CIDR or all subnets); in-progress / IPv6 / too-large → skipped; message "queued N scan(s), skipped M[, failed K]" | default cron `0 3 * * *`, retries 2 |
| `lcm:check-expiring-certificates` | new tenant-scoped query `ExpiringCertificates(tenant, before)` (F15); one digest e-mail per configured recipient through notification (`Notifier/Send`, `template_key=lcm.certificates_expiring`); nothing expiring → success "No certificates expiring within N days" | default cron `0 8 * * *`, retries 1; payload `daysBeforeExpiry` 1–365 (7) + `recipients` (1–20 e-mail addresses) |
| `notification:send-test-email` | send through the tenant's chosen or default e-mail channel with the optional subject/body; invalid recipient / missing, disabled or non-e-mail channel → permanent failure | no cron, retries 1 |

**Deviation (lcm recipients)**: the spec assumes lcm e-mails "the tenant's users holding
the LCM administrator role". auth-v4 gives services no e-mail addresses (F14), so a
role → address lookup would need a new auth contract. v4 therefore sends to the
task's configured `recipients` (the US5 independent test's "configured recipients");
resolving role holders is a follow-up that needs an auth `Profiles` e-mail RPC
restricted by policy. The notification module gains the system template
`lcm.certificates_expiring` (variables `days`, `count`, `certificates` — a pre-rendered,
HTML-escaped list) and a policy rule allowing `svc/lcm` to call `Notifier/Send`.

The modules' internal loops (ipam host-sync/scan executor, lcm auto-renewal,
notification send worker, asset sweep, inventory upgrade policy) are untouched
(FR-028).

## D12. UI

A federated remote (`@go-tangra/ui` 4.2.x, `layer(utilities)` import and the
`breakpointSpecificity` plugin as ticket/ipam), served by the module at `/ui/`
and relayed by the gateway at `/m/scheduler/`. Pages: Tasks (list with state, schedule
in words, next run, last result; control actions; bulk), task drawer (create/edit with
a form generated from the type's JSON Schema — string/number/integer/boolean/enum/
array-of-string/`format: email|uuid`, required markers, min/max — and a raw JSON editor
fallback; cron field with the server-side 5-run preview; kind/time zone/options),
task detail with history (filters: failed only, status, time range; server paging;
execution drawer with message and result), wait-for-result follow, Overview. Actions
the user may not perform are hidden (CASL abilities from the manifest).

## D13. Packaging

Module `github.com/go-tangra/go-tangra-scheduler/v4` on the orphan branch `v4`
(worktree `go-tangra-scheduler-v4`), SDK `.../sdk/v4` in `sdk/` (replaced locally),
binary `schedulersvc` (`version` subcommand, `bootstrap` migrate subcommand), ports
gRPC 9905, HTTP 9906, admin 127.0.0.1:9800. Image `ghcr.io/go-tangra/go-tangra-scheduler`
(semver tags, no `latest`), alpine + `tzdata`. No KEK: the scheduler stores no
secrets (payloads must not carry secrets; Security Requirements).

## Supply-chain note (Constitution VI)

New direct dependencies: none beyond those other v4 modules already use —
`github.com/go-tangra/go-tangra/v4`, auth/portal SDKs, `pgx/v5`, `goose/v3`,
`kin-openapi`, `valkey-go`, `santhosh-tekuri/jsonschema/v6` (already in lcm,
notification, portal, warden), `testcontainers-go` (tests), OpenTelemetry metric API.
The cron parser is in-repository (D2). The SDK depends on grpc + protobuf only.

## STRIDE summary

| Threat | Scenario | Mitigation |
|--------|----------|------------|
| **S**poofing | A module registers another module's types (v3 F3). | Owner = SPIFFE peer; name prefix check; foreign name refused; policy allow-list (D4). |
| **S**poofing | A forged execution request to a module. | Module policy admits only `svc/scheduler` for `ExecuteTask`; `taskexec` re-checks the peer, fails closed (D3). |
| **T**ampering | A backup import forges task types or cross-tenant rows. | Types imported only by platform admins, absent only, as unavailable; rows forced into the caller's tenant (D10). |
| **T**ampering | Two replicas run the same occurrence. | Row locks + SKIP LOCKED in the planning transaction, unique occurrence index, lease fencing on completion (D1). |
| **R**epudiation | Who changed or ran what. | Audit for every mutation, control, manual run and (un)registration with actor, tenant, task/type, action, outcome (D9). |
| **I**nformation disclosure | Tenant reads another tenant's tasks/history. | RLS on every row; tenant scope from the platform token; platform scope only after the platform-admin check; foreign rows → not found (D5). |
| **I**nformation disclosure | Payload/result values in logs, metrics, audit, events. | Payload and result never logged; metric labels closed; audit detail redaction; stream events carry ids/status only (D7–D9). |
| **D**enial of service | Very frequent cron or retry storms; oversized/deep payloads; many registrations. | 1-minute minimum interval, retries ≤ 10 with exponential backoff, bounded workers, payload/result/message/descriptor limits, depth ≤ 32, JSON Schema compiled without remote loading (D2, D4, D6). |
| **E**levation of privilege | Tenant admin creates or controls a platform-scoped task. | Platform-scoped types invisible and refused unless platform-admin; server-side permission on every route (D5). |
| **E**levation of privilege | Executing module trusts the tenant blindly. | Module validates the tenant UUID and the payload again and scopes all work to that tenant; empty tenant only for its own platform-scoped types (SR-002). |
