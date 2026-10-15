---
title: Reference
summary: Task, task type and execution fields, task states and validity, execution statuses and triggers, permissions, roles, API routes, errors and limits.
kind: reference
order: 60
tags: [scheduler, reference, tasks, states, permissions, api, limits]
---
## Task fields

| Field | Meaning |
|---|---|
| `id` | task ID |
| `name` | 1–200 characters, unique in the tenant (case-insensitive) |
| `type_name`, `module` | the task type and the module that runs it; fixed after creation |
| `kind` | `periodic`, `delayed` or `wait_result`; fixed after creation |
| `payload` | the settings for the module, checked against the type's fields |
| `cron`, `timezone` | periodic tasks: five-field cron expression and IANA time zone (default `UTC`) |
| `run_at` | one-shot tasks: when the task runs |
| `enabled` | periodic tasks: `false` means stopped |
| `status` | `active`, `completed` or `cancelled` |
| `state` | see [task states](#task-states) |
| `validity`, `validity_message` | see [validity](#validity) |
| `max_retries` | 0–10 extra attempts; default from the task type |
| `timeout_seconds` | one attempt; default 300, at most `limits_scheduler.max_timeout_seconds` (3600) |
| `catch_up` | periodic tasks: one catch-up run after missed runs |
| `remark` | up to 1000 characters |
| `next_run_at`, `last_run_at` | next planned run, last finished run |
| `last_status`, `last_message` | outcome of the last run |
| `run_count`, `missed_count` | runs made, runs missed |
| `created_by`, `updated_by`, `created_at`, `updated_at` | audit fields |

To create a one-shot task, send either `run_at` or `delay_seconds` (0 to
31 536 000, one year), not both; with neither it runs now. A `wait_result`
task always runs now; a time in the future is refused. Its create answer
carries `execution_id`, the first attempt.

## Task states

| State | Meaning |
|---|---|
| `enabled` | active and runs on its schedule or at its time |
| `stopped` | a periodic task that was stopped (or created disabled) |
| `completed` | a one-shot task whose run finished, whatever the result |
| `cancelled` | a one-shot task that was cancelled |

## Validity

| Validity | Meaning | Runs |
|---|---|---|
| `ok` | the type is available and the payload matches it | yes |
| `type_unavailable` | the module unregistered the type | skipped |
| `payload_invalid` | the module changed the type's fields and the payload no longer matches | skipped |

## Task type fields

| Field | Meaning |
|---|---|
| `name` | `<module>:<action>` |
| `module`, `display_name`, `description` | as registered by the module |
| `payload_schema` | JSON Schema of the payload (`null`: any object) |
| `default_cron`, `default_max_retries` | suggestions used when you create a task |
| `scope` | `tenant`, or `platform`: only platform administrators see and use it |
| `available` | `false` after the module unregistered it |

## Execution fields

| Field | Meaning |
|---|---|
| `id`, `task_id`, `task_name`, `occurrence_id` | the attempt, its task and the run it belongs to |
| `type_name`, `module` | what ran where |
| `trigger`, `triggered_by` | see [triggers](#triggers); the user for manual runs |
| `scheduled_at`, `due_at` | when the run was planned, when this attempt was due |
| `started_at`, `finished_at`, `duration_ms` | timing |
| `attempt`, `max_attempts` | this attempt's number, and `max_retries` + 1 |
| `status` | see [execution statuses](#execution-statuses) |
| `message` | short text from the module or the scheduler (at most 4 KiB) |
| `result`, `result_truncated` | the module's result, cut at `limits_scheduler.max_result_bytes` (64 KiB) |

## Execution statuses

| Status | Meaning | Final |
|---|---|:-:|
| `queued` | waiting to start | |
| `running` | sent to the module | |
| `succeeded` | the module reported success | yes |
| `failed` | failed; retried if the failure is temporary and retries are left | yes |
| `timed_out` | no answer within the timeout; retried if retries are left | yes |
| `skipped` | not sent: overlap, type unavailable, payload invalid or invalid schedule | yes |
| `cancelled` | cancelled before it started | yes |

Retries wait 30 s × 2^(n−1) after attempt n, at most 10 minutes.

## Triggers

| `trigger` | Set when |
|---|---|
| `schedule` | the task's schedule or time |
| `manual` | **Run now** or **Run again** |
| `catch_up` | the one catch-up run after missed runs |

## Permissions and roles

| Permission | Allows |
|---|---|
| `scheduler:read` | list task types, tasks, history and the dashboard; preview cron schedules; live updates |
| `tasks:manage` | create and edit tasks; platform administrators: retire a removed module's types |
| `tasks:delete` | delete tasks |
| `tasks:control` | start, stop, restart, run now, run again, cancel; bulk actions |
| `backup:manage` | export and import tasks and history |

| Module role | Permissions |
|---|---|
| Scheduler administrator | all five |
| Scheduler operator | `scheduler:read`, `tasks:manage`, `tasks:control` |
| Scheduler viewer | `scheduler:read` |

The platform's built-in roles also receive scheduler permissions: `owner`
and `admin` all; `operator` those of the Scheduler operator; `member` and
`auditor` `scheduler:read`.

**Platform administrators** (admins and owners of the platform tenant) see
the tasks of every tenant, are the only ones who can use platform-scoped
task types, and their bulk actions cover every tenant.

## API

The browser API is under `/api/scheduler/v1`, through the gateway. Changing
requests need the `X-CSRF-Token` header.

| Route | Permission |
|---|---|
| `GET /task-types` (`module`, `available`) | `scheduler:read` |
| `POST /modules/{module}/unregister` | `tasks:manage`, platform administrators only |
| `GET /cron/preview` (`expression`, `timezone`, `count` 1–10) | `scheduler:read` |
| `GET /tasks`, `GET /tasks/{id}` | `scheduler:read` |
| `POST /tasks`, `PUT /tasks/{id}` | `tasks:manage` |
| `DELETE /tasks/{id}` | `tasks:delete` |
| `POST /tasks/{id}/start`, `/stop`, `/restart`, `/run`, `/cancel` | `tasks:control` |
| `POST /tasks/bulk/{start,stop,restart}` | `tasks:control` |
| `GET /executions`, `GET /executions/{id}` | `scheduler:read` |
| `GET /overview` | `scheduler:read` |
| `GET /stream` (server-sent events) | `scheduler:read` |
| `POST /backup/export`, `POST /backup/import` | `backup:manage` |

`GET /tasks` filters: `kind`, `state`, `validity`, `type`, `q` (name or
type), and `tenant_id` for platform administrators; sort by `name`, `type`,
`state`, `next_run_at` or `updated_at`. `GET /executions` filters:
`task_id`, `status` (repeatable), `failed_only`, `trigger`, `from`, `to`;
sort by `created_at` (default, newest first), `status`, `duration` or
`trigger`. `POST /tasks/{id}/run` answers `202` with `execution_id`.

`POST /backup/export` exports your tenant's tasks, history and the task types
they use; `{"all": true}` exports every tenant (platform administrators
only). `POST /backup/import` loads a backup into your tenant and skips IDs
that already exist; task types are restored only for platform
administrators, only when missing, and always as unavailable.

### Errors

Refusals answer `{"reason": "...", "detail": {...}}`; `detail.field` names
the field when there is one.

| Status | `reason` | Typical cause |
|---|---|---|
| 400 | `malformed_body` | invalid JSON or an unknown field |
| 403 | `forbidden` | the permission is missing |
| 404 | `not_found` | no such task or execution in your tenant |
| 409 | `name_taken` | another task in the tenant has the name |
| 409 | `not_periodic` | start, stop or restart on a one-shot task |
| 409 | `run_in_progress` | run now while an attempt is queued or running |
| 409 | `not_cancellable` | cancel on a periodic or finished task |
| 409 | `conflict` | the tenant has reached its task limit |
| 422 | `invalid_payload`, `payload_too_large`, `payload_too_deep` | the payload does not match the type, is over 64 KiB or nested deeper than 32 levels |
| 422 | `invalid_cron`, `cron_too_frequent`, `invalid_timezone` | the schedule is wrong or fires more than once a minute |
| 422 | `invalid_run_at`, `invalid_options` | wrong time, retries or timeout |
| 422 | `type_unavailable` | the type does not exist, is unavailable or is platform-scoped |
| 422 | `validation_failed` | another field is invalid |

## Limits

| Limit | Default |
|---|---|
| tasks per tenant | 1000 (`limits_scheduler.max_tasks_per_tenant`) |
| payload size | 64 KiB (`limits_scheduler.max_payload_bytes`) |
| payload nesting | 32 levels |
| time between two runs | 60 s (`limits_scheduler.min_interval_seconds`) |
| attempt timeout | 3600 s (`limits_scheduler.max_timeout_seconds`) |
| retries per run | 10 |
| list page size | 200 |
| history retention | 90 days (`engine.retention_days`) |
| backup import | 32 MiB |
