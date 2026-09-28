# Tasks: Scheduler Module for v4

**Feature**: 026-scheduler-v4 | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Tests are MANDATORY and precede implementation in every phase (Constitution IV).
`[P]` = parallelizable (different files, no dependency on an unfinished task);
`[US#]` = user story. Paths are relative to `go-tangra-scheduler-v4/` unless a repository
is named. Module `github.com/go-tangra/go-tangra-scheduler/v4`, SDK
`github.com/go-tangra/go-tangra-scheduler/sdk/v4`. Mirror go-tangra-ticket-v4 (app,
httpapi, store, audit, stream, manifest, UI) and go-tangra-ipam-v4 (nested sdk module).
Release tasks (push, tags, images, deploy) are marked **(release)** and are not done in
this change.

## Phase 1: Setup

- [X] T001 Orphan branch `v4` of go-tangra-scheduler as worktree `go-tangra-scheduler-v4`; copy `specs/026-scheduler-v4/`; `go.mod` (module …/v4, go 1.26.3, toolchain 1.26.8, `replace …/sdk/v4 => ./sdk`), `.gitignore`, `.dockerignore`.
- [ ] T002 [P] SDK module `sdk/` (`go.mod`, `buf.yaml` with the platform lint exceptions, `buf.gen.yaml`), `sdk/api/proto/scheduler/v1/scheduler.proto` (contracts/grpc-contract.md), generated `*.pb.go`; `buf lint` clean.
- [X] T003 [P] `Makefile` (lint, vuln incl. sdk, test, test-integration, cover, fuzz, generate, ui-build, build, build-ui, image), `scripts/coverage-gate.sh` (≥ 80 %, 100 % authz/cron/payload/registry), `scripts/vulncheck.sh`, `Dockerfile` (ui → build -tags ui → alpine + tzdata, `schedulersvc`), `.github/workflows/ci.yaml` (go vet/test service + sdk, UI lint/unit, buf lint sdk, docker image `ghcr.io/go-tangra/go-tangra-scheduler`, semver tags, no latest).
- [ ] T004 [P] UI scaffold `ui/` from go-tangra-ticket-v4/ui (package `go-tangra-scheduler-ui`, `@go-tangra/ui` ^4.2.1, vite base `/m/scheduler/`, federation remote `scheduler`, `layer(utilities)` import, `breakpointSpecificity`), `ui/embed.go`, `ui/embed_stub.go`.

## Phase 2: Foundational

### Tests (write first, must fail)
- [X] T005 [P] `internal/config/config_test.go` — defaults, unknown keys refused, required db/valkey/gateway, production guards (sslmode, plaintext valkey, insecure enroll), engine/limits bounds, warnings.
- [ ] T006 [P] `internal/cron/cron_test.go` + `cron_fuzz_test.go` — fields, lists, ranges, steps, names, dow 7, dom/dow OR, invalid expressions, never-fires, Next in UTC and Europe/Sofia/America/New_York across DST gap (skipped) and overlap (runs once), preview count, min-interval check. 100 %.
- [ ] T007 [P] `internal/payload/payload_test.go` + fuzz — compile schema (remote refs refused, non-object schema refused, ≤ 64 KiB), validate (missing required, wrong type, range, email format) naming the instance path, size and depth limits, nil schema accepts any object, non-object refused. 100 %.
- [X] T008 [P] `internal/authz/authz_test.go` — permission set, Require for user/service/system actors, platform admin, platform-scoped types, tenant scope. 100 %.
- [X] T009 [P] `internal/audit/audit_test.go` — vocabulary, validation, redaction of payload/result/email/secret keys, writer flush/close/drop.
- [ ] T010 [P] SDK `sdk/pkg/taskexec/taskexec_test.go` — caller refused (nil func, other service), tenant rules (uuid, empty only for platform types), payload bounds, unknown type permanent, panic → retryable, message/result cut, DecodeStrict. 100 %.
- [ ] T011 [P] SDK `sdk/pkg/schedulerclient/client_test.go` — descriptor mapping, Register/Unregister over bufconn, Registrar retry-then-refresh with fake dialer.
- [X] T012 [P] `pkg/schedulermanifest/manifest_test.go` — routes from OpenAPI (every route has a known permission or is public), permissions, roles (administrator/operator/viewer), grants, abilities, nav.
- [X] T013 [P] `tests/contract/openapi_test.go` — document parses/validates; every declared route mounted; no undeclared route; error envelope.

### Implementation
- [X] T014 `internal/config/config.go` — framework config inline + db, valkey, gateway, mesh_enroll, engine (tick, workers, misfire_grace, lease_grace, retention_days, retention_interval), limits_scheduler (max_payload_bytes, max_result_bytes, max_timeout_seconds, min_interval_seconds, max_page_size, max_backup_bytes), platform_tenant_id, events.
- [ ] T015 `internal/cron/cron.go` — Parse, Schedule.Next(after, loc), Preview, MinGap; ValidateTimezone.
- [ ] T016 `internal/payload/payload.go` — Compile (no remote loader), Validate (size, depth, object, schema) returning FieldError{Field, Message}.
- [X] T017 [P] `internal/authz/authz.go` — Subjects, actor kinds, permissions (`scheduler:read`, `tasks:manage`, `tasks:delete`, `tasks:control`, `backup:manage`), Checker, Require, platform admin.
- [X] T018 [P] `internal/audit/audit.go` — closed vocabulary (contracts/audit-events.md), redaction, async writer.
- [X] T019 `internal/store/` — migrations `0001_schema.sql` (types, tasks, executions, indexes per data-model.md), `0002_audit.sql` (hypertable), `0003_rls.sql` (FORCE RLS + grants); `store.go` (pool, Migrate under advisory lock, Tx with Scope), `ids.go` (UUIDv7), `models.go`.
- [X] T020 `internal/repo/repo.go` — storage contract (types, tasks, executions, engine ops, overview, retention, backup, audit) + sentinel errors; `internal/memstore/` fake implementing it (+ tests).
- [X] T021 `internal/repo/repodb/` — pgx implementation + `repodb_integration_test.go` (`//go:build integration`): migrations, RLS isolation between two tenants, name uniqueness, SKIP LOCKED planning by two concurrent engines (no duplicate occurrence), lease recovery, retention delete.
- [X] T022 [P] `internal/metrics/metrics.go` — runs, duration, retries, skipped, missed, registered types (observable); nil-safe.
- [X] T023 [P] `internal/stream/` (copy ticket hub/sse/client/valkeykv) + `internal/events/events.go` (`scheduler.execution`, `scheduler.task`; ids/status only) + tests.
- [ ] T024 [P] SDK `sdk/pkg/taskexec/taskexec.go` and `sdk/pkg/schedulerclient/client.go` (contracts/grpc-contract.md).
- [ ] T025 `api/openapi/scheduler.yaml` (contracts/scheduler-api.md) + `embed.go`.
- [X] T026 `internal/httpapi/` — server (OpenAPI validation, authenticate, authorize, 501 until wired), errors, JSON helpers, stream route, remote serving.
- [X] T027 `pkg/schedulermanifest/manifest.go` — module `scheduler`, prefix `/api/scheduler`, permissions, roles, grants, abilities (`SchedulerTask`, `SchedulerExecution`, `SchedulerOverview`, `SchedulerBackup`), nav (Tasks, Overview).
- [X] T028 `internal/app/` — Build (runtime + mesh enrol, store, auth peers, stream, metrics, services, HTTP, gRPC), Run (verifier, gateway lease, auth registration loop, engine), permissions.go; `cmd/schedulersvc/{main.go,version.go}` (+ `bootstrap` migrate subcommand).

## Phase 3: User Story 1 — Types and periodic tasks (P1) 🎯 MVP

**Goal**: modules register types; administrators create validated periodic tasks that fire.
**Independent test**: a test module registers a schema'd type; a valid task fires at its next cron time; invalid payload/cron refused naming the field.

### Tests (write first, must fail)
- [X] T029 [P] [US1] `internal/registry/registry_test.go` — owner from peer, prefix rule, foreign name refused, limits (count, sizes, schema compile, cron, retries), idempotent upsert, unregister marks unavailable, schema change re-validates tasks (payload_invalid / cleared). 100 %.
- [X] T030 [P] [US1] `internal/grpcapi/registration_test.go` — bufconn with stamped SPIFFE peers: register/unregister, missing peer → Unauthenticated, foreign namespace → PermissionDenied, audit rows.
- [X] T031 [P] [US1] `internal/tasks/tasks_test.go` — create (defaults from type, validation reasons, name uniqueness, several tasks per type, platform type refused for tenant admins, next_run_at), get/list scoped (tenant vs platform admin), update (next run recomputed, payload re-validated), enable/disable, delete (queued attempts cancelled), audit.
- [X] T032 [P] [US1] `internal/engine/engine_test.go` — fake clock + fake dispatcher: due periodic task planned once, request carries tenant/payload/attempt/scheduled time, next_run_at advanced, overlap skipped, type unavailable / payload invalid skipped with reason, missed occurrences counted, catch-up once when allowed.
- [X] T033 [P] [US1] `internal/dispatch/dispatch_test.go` — bufconn module: request mapping (platform → empty tenant), error classification table (contracts/grpc-contract.md), deadline → timed_out, result/message bounds.
- [X] T034 [P] [US1] `tests/contract/tasks_test.go` — task-types list (platform types hidden), cron preview, tasks CRUD over the HTTP handler with memstore + static checker: 422 reasons with field, 404 cross-tenant, 403 without permission.

### Implementation
- [X] T035 [US1] `internal/registry/registry.go` — Register/Unregister/Retire + descriptor validation + re-validation of tasks.
- [X] T036 [US1] `internal/grpcapi/server.go` — `scheduler.v1.Registration` server (peer → module), error mapping.
- [X] T037 [US1] `internal/tasks/tasks.go` — task service (create/get/list/update/delete, validation, scope rules, audit, events).
- [X] T038 [US1] `internal/dispatch/dispatch.go` — lazy per-module mesh client, ExecuteTask with deadline, classification.
- [X] T039 [US1] `internal/engine/engine.go` — plan/claim/dispatch/complete loop, worker bound, overlap, validity skip, missed/catch-up.
- [X] T040 [US1] `internal/httpapi/tasks.go`, `types.go`, `cron.go` — handlers for task types, module retire, cron preview, tasks CRUD.
- [ ] T041 [P] [US1] UI schemas/stores `ui/src/schemas/task.ts`, `ui/src/api/{client,types}.ts`, `ui/src/stores/{tasks,types}.ts` + `ui/src/utils/cron.ts` (plain-words description) + unit tests.
- [ ] T042 [US1] UI `ui/src/components/SchemaForm.vue` (JSON Schema → fields, JSON fallback editor, required/min/max/enum/format) + `ui/src/views/tasks/index.vue` (list: type, module, schedule in words, next run, state, last result) + `ui/src/views/tasks/drawer.vue` (create/edit, type picker pre-fills cron/retries, cron preview) + unit tests.

## Phase 4: User Story 2 — History, results, retries (P1)

**Independent test**: first attempt fails retryably, second succeeds → two attempts of one occurrence; permanent failure → one attempt.

### Tests (write first, must fail)
- [X] T043 [P] [US2] engine tests — retry with backoff (30 s × 2^(n-1), cap 10 min), permanent stops, unreachable module retried, timeout → timed_out then retry, final attempt updates task last_* and run_count, lease expiry recovery, retention prune.
- [X] T044 [P] [US2] `tests/contract/executions_test.go` — list filters (status, failed_only, trigger, time range), paging + counts, get with result (JSON vs text, truncated flag), cross-tenant 404.

### Implementation
- [X] T045 [US2] engine retry/recover/retention in `internal/engine/`.
- [X] T046 [US2] `internal/tasks/executions.go` + `internal/httpapi/executions.go`.
- [ ] T047 [US2] UI history: `ui/src/stores/executions.ts`, `ui/src/views/tasks/history.vue` (drawer tab; failed-only filter; counts; paging) + `ui/src/views/tasks/execution.vue` (message + result) + unit tests.

## Phase 5: User Story 3 — One-shot and wait-for-result (P2)

### Tests (write first, must fail)
- [X] T048 [P] [US3] tasks/engine tests — delayed by delay/absolute/past/empty runs once then completed; restart/start-all never re-fire; wait_result create returns execution id at once; cancel before firing; run again after completion (manual).

### Implementation
- [X] T049 [US3] one-shot planning/completion + cancel + run-again in `internal/tasks` / `internal/engine`; HTTP `run`, `cancel`.
- [ ] T050 [US3] UI: one-shot fields (delay / run at), `ui/src/components/ExecutionFollow.vue` (SSE + 3 s polling until final, shows message/result), `ui/src/stores/live.ts` + unit tests.

## Phase 6: User Story 4 — Control (P2)

### Tests (write first, must fail)
- [X] T050a [P] [US4] tasks tests — start/stop/restart periodic only (409 for one-shot), run now manual with triggering user and unchanged schedule, bulk start/stop/restart scoped (tenant vs platform admin) with affected counts, audit for each.

### Implementation
- [X] T051 [US4] control + bulk in `internal/tasks/control.go`; HTTP `start|stop|restart|run|cancel`, `bulk/{action}`.
- [ ] T052 [US4] UI actions (row menu + bulk bar) hidden by CASL abilities + unit tests.

## Phase 7: User Story 5 — Ported v3 task types (P2)

Consumers use branch `026-scheduler-tasks`; the scheduler SDK comes through a TEMP local
`replace github.com/go-tangra/go-tangra-scheduler/sdk/v4 => ../go-tangra-scheduler-v4/sdk`
committed LAST ("TEMP" in the subject).

### go-tangra-notification-v4
- [ ] T053 [P] [US5] Tests: `internal/notify` SendCustom (tenant default or chosen email channel, disabled/wrong type/missing → errors, recipient validation, body escaped, audited, logged) and `internal/taskexec` executor (payload validation, permanent vs retryable mapping, tenant scoping, caller check); system template `lcm.certificates_expiring` seeded.
- [ ] T054 [US5] `notify.Sender.SendCustom`, `internal/taskexec` (`notification:send-test-email`), system template `lcm.certificates_expiring`, registrar + executor wiring (`scheduler` config section, `discovery.static.scheduler`), `deploy/policy.yaml` (`scheduler-execute`; `svc/lcm` in `modules-send`).

### go-tangra-lcm-v4
- [ ] T055 [P] [US5] Tests: repo `ExpiringCertificates(tenant, now, before)` (memstore + integration), executor (`daysBeforeExpiry` bounds/default, recipients required 1–20 valid addresses, nothing expiring message, digest variables, notification retryable vs permanent), tenant scoping.
- [ ] T056 [US5] repo method (repodb + memstore), `internal/taskexec` (`lcm:check-expiring-certificates`), lazy notification client (`notifyclient.SendKey`), registrar + executor wiring, config (`scheduler`, `notification` sections), `deploy/policy.yaml` (`scheduler-execute`).

### go-tangra-ipam-v4
- [ ] T057 [P] [US5] Tests: executor (subnet id / CIDR / all, IPv4 only, skipped for in-progress / IPv6 / too large, failed for others, message counts, tenant scoping, invalid payload permanent, unknown subnet permanent).
- [ ] T058 [US5] `internal/taskexec` (`ipam:scan-network` → `scan.Service.StartScan`), registrar + executor wiring, config `scheduler` section, `deploy/policy.yaml` (`scheduler-execute`).

### Quality (each consumer)
- [ ] T059 [US5] vet, race tests, integration, `make cover` (existing 100 % packages stay 100 %), `make vuln`, buf lint, UI unchanged; TEMP replace commit last.

## Phase 8: User Story 6 — Overview and metrics (P3)

### Tests (write first, must fail)
- [X] T060 [P] [US6] overview tests (counts by state/validity, 24 h runs/failures, next due, failing; scope) + metrics tests (instruments recorded with closed labels; no tenant/payload labels).

### Implementation
- [X] T061 [US6] `internal/tasks/overview.go` + `internal/httpapi/overview.go`; engine metric calls; registered-types observable gauge.
- [ ] T062 [US6] UI `ui/src/views/overview/index.vue` + store + unit tests.

## Phase 9: Platform integration & polish

- [X] T063 [P] Tests: `internal/backup/backup_test.go` — export scope, import into caller tenant only, types only for platform admins as unavailable, size limit, malformed input; fuzz decode.
- [X] T064 `internal/backup/backup.go` + HTTP `POST /backup/export`, `POST /backup/import`.
- [X] T065 `deploy/policy.yaml`, `deploy/container.yaml` (stack-shaped example), `deploy/README.md` (ports, DB/role, policies, gateway allow-list, consumer rules).
- [X] T066 [P] `README.md`, `SECURITY.md` (reporting channel, threat notes).
- [ ] T067 go-tangra `deploy/stack` on branch `026-scheduler`: `compose.yaml` (scheduler-token, scheduler service, valkey user, gateway allow `/api/scheduler`, consumer discovery), `configs/scheduler.yaml`, `init-db.sql` (database + `scheduler_app`), consumer configs (`scheduler` sections + `discovery.static.scheduler`, lcm `notification`), README.
- [ ] T068 go-tangra-docker branch `v4` (local commit): `configs/scheduler.yaml`, `policies/scheduler.yaml` + consumer policy rules, compose examples (dev + production), `init-db.sql`, `.env.example` (`SCHEDULER_VERSION`), `scripts/prod-init.sh` (scheduler policy), consumer configs.
- [ ] T069 Security review (STRIDE in research.md re-checked against the code): foreign registration, forged executor calls, cross-tenant, platform escalation, payload/result leak grep over logs/metrics/audit (SC-008).
- [X] T070 Quality gates in the scheduler: `go vet`, `make test`, `make test-integration`, `make cover`, `make vuln`, `make lint`, buf lint (sdk), UI lint/unit/build, docker build.
- [ ] T071 Mark completed tasks; commit per phase (conventional, no AI trailers).

## Release (not done in this change)

- [ ] T072 **(release)** GitHub: push `v3` (from current `main`) and make `v4` the new `main` of go-tangra-scheduler; open PR; CI green.
- [ ] T073 **(release)** Tag `sdk/v4.0.0` then `v4.0.0`; image `ghcr.io/go-tangra/go-tangra-scheduler:4.0.0`.
- [ ] T074 **(release)** Consumers: drop the TEMP replace, require `sdk/v4.0.0`, release notification, lcm, ipam minors; images.
- [ ] T075 **(release)** Stack/prod: merge `026-scheduler` stack branch, docker `v4` pins, create DB/role, gateway allow-list, policies, deploy; UI smoke (operator sign-in) of quickstart Scenarios 1–6.

## Dependencies & sequencing

Setup → Foundational → US1 (MVP) → US2 → US3/US4 (parallel) → US6; US5 consumers can
start once the SDK (T010/T011/T024, T002) exists and proceed in parallel per repository;
Phase 9 last. Within a phase, tests precede implementation.

## Parallel execution examples

- T005–T013 in parallel (independent test files).
- T053/T055/T057 (three consumer repositories) in parallel.
- UI tasks T041/T047/T050/T062 alongside backend handlers once the OpenAPI (T025) is fixed.

## Implementation strategy

MVP = Phases 1–3 (types registered, periodic tasks fire, validated). Then history
(US2), one-shot/control (US3/US4), consumers (US5), overview (US6), polish.
