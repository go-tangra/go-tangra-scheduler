# Data Model: Scheduler Module for v4

**Feature**: 026-scheduler-v4 | [research.md](./research.md) D1, D4, D5, D7

Database `scheduler` (TimescaleDB image, PostgreSQL 16). Roles: `postgres` runs
migrations; `scheduler_app` (LOGIN, NOBYPASSRLS) serves requests. Every
tenant-owned table has FORCE ROW LEVEL SECURITY with the platform policy
`tenant_id = current_setting('app.tenant_id')::uuid OR current_setting('app.system') = 'on'`.
The engine and platform-admin reads run under the system scope, only after the
caller has been authorised (D5). `PLATFORM` = the nil UUID
`00000000-0000-0000-0000-000000000000` (tenant of platform-scoped tasks).

## scheduler_task_types (platform catalog, no tenant, no RLS)

| Column | Type | Notes |
|--------|------|-------|
| name | text PK | `^<module>:[a-z][a-z0-9-]{0,62}$` |
| module | text NOT NULL | owner = SPIFFE service name of the registering peer |
| display_name | text NOT NULL | 1–200 |
| description | text NOT NULL DEFAULT '' | ≤ 2,000 |
| payload_schema | jsonb | NULL = any JSON object; compiled JSON Schema, ≤ 64 KiB |
| default_cron | text NOT NULL DEFAULT '' | empty or a valid 5-field cron |
| default_max_retries | int NOT NULL DEFAULT 3 | 0–10 |
| scope | text NOT NULL DEFAULT 'tenant' | `tenant` \| `platform` |
| available | boolean NOT NULL DEFAULT true | false after unregistration/retirement |
| registered_at | timestamptz NOT NULL | last (re-)registration |
| unregistered_at | timestamptz | set when made unavailable |
| schema_hash | text NOT NULL DEFAULT '' | SHA-256 of the canonical schema (re-validation trigger) |

Grants: `scheduler_app` SELECT, INSERT, UPDATE (no DELETE).
Index: `(module)`.

## scheduler_tasks (RLS)

| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | UUIDv7 |
| tenant_id | uuid NOT NULL | tenant, or PLATFORM for platform-scoped types |
| name | text NOT NULL | 1–200, unique per tenant (case-insensitive) |
| type_name | text NOT NULL | FK-free reference to `scheduler_task_types.name` (types are never deleted) |
| module | text NOT NULL | denormalised owner at creation |
| kind | text NOT NULL | `periodic` \| `delayed` \| `wait_result` |
| payload | jsonb NOT NULL DEFAULT '{}' | object, ≤ 64 KiB, depth ≤ 32 |
| cron | text NOT NULL DEFAULT '' | required for periodic, empty otherwise |
| timezone | text NOT NULL DEFAULT 'UTC' | IANA name |
| run_at | timestamptz | one-shot fire time (NULL/past = now) |
| enabled | boolean NOT NULL DEFAULT true | false = stopped |
| status | text NOT NULL DEFAULT 'active' | `active` \| `completed` \| `cancelled` (one-shot end states) |
| validity | text NOT NULL DEFAULT 'ok' | `ok` \| `type_unavailable` \| `payload_invalid` |
| validity_message | text NOT NULL DEFAULT '' | e.g. JSON Schema error (field path + message) |
| max_retries | int NOT NULL | 0–10 |
| timeout_seconds | int NOT NULL DEFAULT 300 | 1–platform max |
| catch_up | boolean NOT NULL DEFAULT false | one catch-up run after downtime |
| remark | text NOT NULL DEFAULT '' | ≤ 1,000 |
| next_run_at | timestamptz | NULL = nothing scheduled |
| last_run_at | timestamptz | start of the last finished attempt |
| last_status | text NOT NULL DEFAULT '' | final status of the last occurrence |
| last_message | text NOT NULL DEFAULT '' | ≤ 4 KiB |
| run_count | bigint NOT NULL DEFAULT 0 | finished occurrences (all triggers) |
| missed_count | bigint NOT NULL DEFAULT 0 | occurrences missed during downtime |
| created_by / updated_by | text NOT NULL DEFAULT '' | user id or `system` |
| created_at / updated_at | timestamptz NOT NULL | |

Constraints: `UNIQUE (tenant_id, lower(name))`; `UNIQUE (tenant_id, id)`;
CHECKs on kind/status/validity; `kind='periodic' ⇔ cron <> ''`.
Indexes: `(next_run_at) WHERE enabled AND status='active' AND next_run_at IS NOT NULL`
(the planner's scan), `(tenant_id, type_name)`, `(tenant_id, updated_at DESC)`.

State rules:

- `enabled=false` ⇒ stopped: never planned (start sets `enabled=true` and recomputes
  `next_run_at` from now).
- Periodic: `next_run_at = cron.Next(now, tz)` on create/start/restart/edit.
- One-shot (`delayed`, `wait_result`): `next_run_at = max(run_at, now)` until planned,
  then NULL; when the occurrence ends → `status='completed'`; cancel → `status='cancelled'`,
  `next_run_at=NULL`, queued attempts cancelled. Start-all never touches completed or
  cancelled tasks (FR-015); "run" re-runs explicitly (manual trigger).
- Validity ≠ ok ⇒ occurrences are planned as `skipped` with the validity message.

## scheduler_executions (RLS) — one row per attempt

| Column | Type | Notes |
|--------|------|-------|
| id | uuid PK | the `execution_id` sent to the module |
| tenant_id | uuid NOT NULL | copied from the task |
| task_id | uuid NOT NULL | no FK (history survives task deletion) |
| task_name | text NOT NULL | denormalised for history after delete |
| occurrence_id | uuid NOT NULL | shared by the attempts of one occurrence |
| type_name / module | text NOT NULL | |
| trigger | text NOT NULL | `schedule` \| `manual` \| `catch_up` |
| triggered_by | text NOT NULL DEFAULT '' | user id for manual runs |
| occurrence_at | timestamptz NOT NULL | intended scheduled time (sent as `scheduled_at`) |
| due_at | timestamptz NOT NULL | when this attempt may be dispatched |
| status | text NOT NULL | `queued` \| `running` \| `succeeded` \| `failed` \| `skipped` \| `timed_out` \| `cancelled` |
| attempt / max_attempts | int NOT NULL | 1-based |
| started_at / finished_at | timestamptz | |
| duration_ms | bigint NOT NULL DEFAULT 0 | |
| message | text NOT NULL DEFAULT '' | ≤ 4 KiB |
| result | bytea | ≤ 64 KiB |
| result_truncated | boolean NOT NULL DEFAULT false | |
| final | boolean NOT NULL DEFAULT false | last attempt of its occurrence (drives task last_* fields) |
| lease_owner | text NOT NULL DEFAULT '' | replica id while running |
| lease_until | timestamptz | crash recovery (D1.5) |
| created_at | timestamptz NOT NULL DEFAULT now() | retention key |

Indexes: `(status, due_at) WHERE status='queued'` (claim), `(lease_until) WHERE
status='running'` (recover), `(tenant_id, task_id, created_at DESC)` (history),
`(tenant_id, created_at DESC)`, `(created_at)` (retention), partial unique
`(task_id, occurrence_at, attempt) WHERE trigger <> 'manual'` (exactly once).

Transitions: `queued → running → succeeded|failed|timed_out`;
`queued → cancelled` (task cancelled/deleted); planning may insert `skipped` directly
(finished, final). A `failed|timed_out` attempt with attempts left and a retryable
cause inserts attempt+1 as `queued` with `due_at = now + backoff`; otherwise it is
`final`.

## scheduler_audit_events (hypertable on `at`, RLS)

`id uuid, tenant_id uuid, at timestamptz, actor_kind, actor_id, action, subject_kind
(task|tasktype|backup|system), subject_id, outcome (ok|refused|error), reason, detail
jsonb` — vocabulary in [contracts/audit-events.md](./contracts/audit-events.md).

## Derived views (API only)

- **Task**: the row + `type_display_name`, `type_available`, `scope`, `state`
  (`enabled`, `stopped`, `completed`, `cancelled`) and `schedule_text` (UI).
- **Overview**: counts by state and validity, runs/failures in the last 24 h,
  next 10 due, failing tasks (last_status in failed/timed_out).
- **Module registration**: `module → [types], last registered_at` from the type catalog.
