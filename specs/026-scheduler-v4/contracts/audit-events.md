# Contract: Scheduler audit events

Table `scheduler_audit_events` (append-only hypertable, RLS). Platform-scoped
actions use the nil tenant. Detail never carries payload values, results, e-mail
addresses, secrets or tokens (keys containing `payload`, `result`, `body`, `email`,
`recipient`, `secret`, `token`, `password`, `credential` are dropped; strings ≤ 256 B).

| action | subject_kind | actor | detail |
|--------|--------------|-------|--------|
| `task.create` | task | user | `type, kind, enabled, platform` |
| `task.update` | task | user | `fields: [..]` (names only) |
| `task.delete` | task | user | `type` |
| `task.enable` / `task.disable` | task | user | — |
| `task.start` / `task.stop` / `task.restart` | task | user | — |
| `task.run` | task | user | `execution_id, trigger` |
| `task.cancel` | task | user | `cancelled_attempts` |
| `tasks.bulk` | system | user | `action, affected` |
| `tasktype.register` | tasktype | service (SPIFFE id) | `module, types: [..], revalidated` |
| `tasktype.unregister` | tasktype | service or user (retire) | `module, count` |
| `backup.export` / `backup.import` | backup | user | `tasks, executions, types` |
| `access.refused` | task / tasktype | user / service | `reason` (e.g. `foreign_namespace`, `platform_type`) |

Outcomes: `ok`, `refused`, `error`. Actor kinds: `user`, `service`, `system`.
Executing modules audit their own side (e.g. ipam `scan_started` with
`detail.trigger = "scheduler"`, notification `notification_sent`).
