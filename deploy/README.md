# Scheduler service — deployment notes

The **scheduler** is the platform's central, UI-driven job scheduler (feature
026, go-tangra-scheduler v4). It decides **when** work runs and asks the module
that owns the work to run it: modules register task types over the mesh, users
create periodic / delayed / wait-for-result tasks with JSON-Schema-validated
payloads, and the engine calls the owning module's
`scheduler.v1.TaskExecutor/ExecuteTask` for every occurrence, with retries,
history and live status.

Module id `scheduler`; browser API under `/api/scheduler/v1` (gateway-proxied,
platform token); gRPC `scheduler.v1.Registration` on the SPIFFE mTLS mesh (not
proxied); the executor contract ships in the SDK
(`github.com/go-tangra/go-tangra-scheduler/sdk/v4`).

## Server operations

| Item | Value |
|---|---|
| Binary | `schedulersvc -config deploy/container.yaml` (applies migrations, then serves); `schedulersvc bootstrap -config …` migrates and exits; `schedulersvc version` |
| Listeners | gRPC `server.grpc_addr` (:9905), HTTP `server.http_addr` (:9906) — mesh, mTLS; admin `admin.addr` (127.0.0.1:9800, `/healthz`, `/readyz`, `/metrics`) |
| Store | TimescaleDB, database `scheduler`, app role `scheduler_app` (LOGIN, NOBYPASSRLS, created by init-db — migrations only grant to it); `db.migrate_dsn` for the migration role |
| Event bus | Valkey user `scheduler` (Streams `platform:events:<tenant>`) |
| Secrets | none — the scheduler stores no secrets (no KEK); payloads must never carry any |
| Mesh identity | enrols with lcm through the gateway edge (`mesh_enroll`), keeps its SVID in `/state` |
| Gateway | registers its manifest (routes, permissions, abilities, nav) on a lease; registers the module roles `administrator` / `operator` / `viewer` and the built-in grants with auth |
| Image | `ghcr.io/go-tangra/go-tangra-scheduler:<semver>` (alpine + `tzdata`; no `latest`) |

### Database

```sql
CREATE DATABASE scheduler;
CREATE ROLE scheduler_app LOGIN PASSWORD '…' NOBYPASSRLS;
-- in database scheduler, as the owner:
CREATE EXTENSION IF NOT EXISTS timescaledb;
```

`scheduler_tasks`, `scheduler_executions` and `scheduler_audit_events` carry
`tenant_id` under FORCE row-level security. Platform-scoped tasks use the nil
tenant `00000000-0000-0000-0000-000000000000`. The type catalog
`scheduler_task_types` is platform data (no RLS; the app role has no DELETE).

### Engine and replicas

Every replica runs the engine (`engine.*`). Due tasks are locked with
`FOR UPDATE SKIP LOCKED`, so an occurrence is planned by exactly one replica; a
partial unique index `(task_id, occurrence_at, attempt)` is the second guard.
Attempts are leased (`task timeout + engine.lease_grace_seconds`); a crashed
replica's attempts time out and are retried. History older than
`engine.retention_days` (default 90) is pruned every
`engine.retention_interval_minutes`. Give each replica a distinct
`engine.instance_id` or leave it empty (hostname + random suffix).

## Mesh policies

`deploy/policy.yaml` (this repository): the gateway forwards everything; only
`svc/ipam`, `svc/lcm` and `svc/notification` may call
`/scheduler.v1.Registration/{RegisterTaskTypes,UnregisterTaskTypes}` — extend
`modules-register` for every new executing module.

Each **executing module** allows only the scheduler to execute its tasks:

```yaml
  - id: scheduler-execute
    from: ["spiffe://example.org/svc/scheduler"]
    to: ["<module>"]
    operations: ["/scheduler.v1.TaskExecutor/ExecuteTask"]
    effect: allow
```

and lists the scheduler in `discovery.static` (`scheduler: ["scheduler:9905"]`)
for its registrar. The scheduler lists every executing module in its own
`discovery.static` (the module name is the task type's prefix).

Existing rules the scheduler relies on: auth `services-verify` /
`services-register` (token keys, revocations, `Authorization/Check`,
`Authorization/RegisterPermissions`), the gateway registry lease, lcm
`workloads-renew` (enrolment).

## Gateway route allow-list

```
-allow spiffe://example.org/svc/scheduler=/api/scheduler;scheduler
```

(stack `gateway-bootstrap`; the production allow-list). The UI remote is served
by the module at `/ui/` and relayed by the gateway under `/m/scheduler/`.

## Consumer checklist (executing modules)

1. Depend on `github.com/go-tangra/go-tangra-scheduler/sdk/v4`.
2. Register `pkg/taskexec.NewServer(handlers, taskexec.Options{Caller: …})` —
   the caller function returns the verified peer's service name; without it
   every call is refused.
3. Run `pkg/schedulerclient.Registrar` with the module's descriptors (retries
   every 5 s until accepted, re-registers every 5 min).
4. Add the `scheduler-execute` policy rule and the `scheduler` discovery entry.
5. Validate the payload again in the handler (`taskexec.DecodeStrict`) and
   scope every effect to the request's tenant.
