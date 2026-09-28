# Contract: Scheduler HTTP API (`/api/scheduler/v1`)

Browser API reached only through the gateway (platform token). The normative
document is `api/openapi/scheduler.yaml`; every operation declares
`x-freya-permission` (checked by the gateway **and** by the module) or
`x-freya-public`. Errors are `{"reason": "<stable reason>", "detail"?: {...}}`.
Foreign-tenant and (for non-platform-admins) platform-scoped rows answer
`404 not_found` (SR-003). JSON bodies ≤ 256 KiB unless stated.

| Method | Path | Permission | Purpose |
|--------|------|-----------|---------|
| GET | `/health` | public | component status |
| GET | `/task-types` | `scheduler:read` | catalog visible to the caller (`?module=`, `?available=`); platform-scoped types only for platform admins |
| POST | `/modules/{module}/unregister` | `tasks:manage` + platform-admin | retire a removed module's types (mark unavailable) |
| GET | `/cron/preview` | `scheduler:read` | `?expression=&timezone=&count=1..10` → `{next: [RFC3339…]}`; `422 invalid_cron` / `invalid_timezone` |
| GET | `/tasks` | `scheduler:read` | list: `kind, state (enabled\|stopped\|completed\|cancelled), type, validity, q, tenant_id (platform admin), page, page_size ≤ 100` → `{items, total}` |
| POST | `/tasks` | `tasks:manage` (+ platform-admin for platform types) | create → `201 Task` (+ `execution_id` for `wait_result`) |
| GET | `/tasks/{id}` | `scheduler:read` | one task |
| PUT | `/tasks/{id}` | `tasks:manage` | replace editable fields (name, payload, cron, timezone, run_at, enabled, remark, max_retries, timeout_seconds, catch_up); kind and type are immutable |
| DELETE | `/tasks/{id}` | `tasks:delete` | delete (queued attempts cancelled; a running attempt finishes and is recorded) |
| POST | `/tasks/{id}/start` | `tasks:control` | periodic only: enable + recompute next run |
| POST | `/tasks/{id}/stop` | `tasks:control` | periodic only: disable |
| POST | `/tasks/{id}/restart` | `tasks:control` | periodic only: stop + start |
| POST | `/tasks/{id}/run` | `tasks:control` | run now / run again → `202 {execution_id}`; `409 run_in_progress` |
| POST | `/tasks/{id}/cancel` | `tasks:control` | one-shot not yet finished → cancelled; `409 not_cancellable` |
| POST | `/tasks/bulk/{action}` | `tasks:control` | `start\|stop\|restart` periodic tasks in the caller's scope → `{affected}` |
| GET | `/executions` | `scheduler:read` | history: `task_id, status (repeatable), failed_only, trigger, from, to, page, page_size ≤ 100` → `{items, total, counts:{succeeded, failed, other}}` |
| GET | `/executions/{id}` | `scheduler:read` | one attempt with message and result |
| GET | `/overview` | `scheduler:read` | US6 figures for the caller's scope |
| GET | `/stream` | `scheduler:read` | SSE: `scheduler.execution` `{execution_id, task_id, status, attempt}` and `scheduler.task` `{task_id}` |
| POST | `/backup/export` | `backup:manage` | export of the caller's tenant (`{all:true}` platform-admin only) |
| POST | `/backup/import` | `backup:manage` | import into the caller's tenant (`x-freya-max-body-bytes` 32 MiB), existing ids skipped |

## Task (response)

```json
{
  "id": "uuid", "tenant_id": "uuid", "name": "Nightly scan", "type_name": "ipam:scan-network",
  "type_display_name": "Scan network", "module": "ipam", "scope": "tenant",
  "kind": "periodic", "payload": {"all": true}, "cron": "0 3 * * *", "timezone": "UTC",
  "run_at": null, "enabled": true, "status": "active", "state": "enabled",
  "validity": "ok", "validity_message": "", "max_retries": 2, "timeout_seconds": 300,
  "catch_up": false, "remark": "", "next_run_at": "…", "last_run_at": "…",
  "last_status": "succeeded", "last_message": "queued 3 scan(s), skipped 0",
  "run_count": 12, "missed_count": 0, "created_by": "uuid", "updated_by": "uuid",
  "created_at": "…", "updated_at": "…"
}
```

## Create/update request

`{name, type_name (create only), kind (create only), payload, cron, timezone, run_at,
delay_seconds (create only, one-shot), enabled, remark, max_retries, timeout_seconds,
catch_up}` — unknown fields refused. Validation reasons (422):

| reason | detail |
|--------|--------|
| `invalid_payload` | `{field: "/recipient", message}` (JSON Schema instance location) |
| `payload_too_large` / `payload_too_deep` | `{limit}` |
| `invalid_cron` | `{field: "cron", message}` |
| `cron_too_frequent` | `{min_interval_seconds}` |
| `invalid_timezone` | `{field: "timezone"}` |
| `invalid_options` | `{field: "max_retries" \| "timeout_seconds"}` |
| `type_unavailable` | `{type_name}` |
| `name_taken` (409) | `{field: "name"}` |

## Execution (response)

```json
{
  "id": "uuid", "task_id": "uuid", "task_name": "…", "occurrence_id": "uuid",
  "type_name": "…", "module": "…", "tenant_id": "uuid", "trigger": "schedule",
  "triggered_by": "", "scheduled_at": "…", "due_at": "…", "started_at": "…",
  "finished_at": "…", "duration_ms": 1520, "attempt": 2, "max_attempts": 3,
  "status": "succeeded", "message": "…", "result": {"any": "json"},
  "result_truncated": false
}
```

`result` is the module's JSON (or a string when it is not valid JSON); it is returned
only by `GET /executions/{id}` (lists omit it).

## Overview (response)

`{tasks: {enabled, stopped, completed, cancelled, type_unavailable, payload_invalid},
runs_24h, failures_24h, next_due: [{task_id, name, next_run_at}], failing: [{task_id,
name, last_status, last_message, last_run_at}]}`
