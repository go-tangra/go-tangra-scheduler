---
title: Operate the scheduler
summary: Configuration keys and defaults, replicas, the service policies executing modules need, and fixes for common start-up and execution problems.
kind: reference
order: 90
tags: [scheduler, operations, configuration, policy, troubleshooting]
audience: operator
---
The scheduler runs as `schedulersvc` (image `ghcr.io/go-tangra/go-tangra-scheduler`).

```sh
schedulersvc -config /app/config.yaml             # serve; applies migrations
schedulersvc bootstrap -config /app/config.yaml   # apply migrations and exit
schedulersvc version
```

At start it enrolls for its SVID `spiffe://<trust domain>/svc/scheduler`,
migrates its TimescaleDB schema, serves the module API and the
`scheduler.v1.Registration` gRPC service, starts the engine, registers with
the gateway and registers its permissions and roles with auth. It stores no
secrets and needs no key-encryption key.

## Configuration

The file is YAML. Unknown keys are refused, so a typo stops the service at
start with `config: <file>: ...`. Besides the framework keys (`service_name`,
`trust_domain`, `env`, `identity`, `authz`, `server`, `admin`, `discovery`,
`limits`) the scheduler reads:

| Key | Default | Notes |
|---|---|---|
| `db.dsn` | — | required; in production `sslmode=verify-full` (or `verify-ca`) |
| `db.migrate_dsn` | `db.dsn` | owner connection used for migrations |
| `db.max_conns` | 16 | 0–256 |
| `valkey.addresses` | — | required; the platform event bus |
| `valkey.username`, `valkey.password`, `valkey.ca_file` | — | Valkey user `scheduler` over TLS |
| `valkey.allow_plaintext` | false | development only; refused in production |
| `events.enabled` | true | live task and execution events; off, the portal does not update live |
| `platform_tenant_id` | `00000000-0000-0000-0000-000000000001` | its admins and owners are platform administrators; events of platform-scoped tasks go to its stream |
| `gateway.service` | `gateway` | mesh name of the gateway; the only peer allowed to fetch the help pages |
| `gateway.issuer` | — | required, an `https://` origin: the platform token issuer |
| `mesh_enroll.*` | — | `enabled`, `enroll_url`, `lcm_grpc`, `tenant_id`, `token_file`, `state_file`, `insecure` (refused in production) |
| `engine.tick_ms` | 1000 | 100–60000, how often the engine looks for due work |
| `engine.workers` | 16 | 1–256 attempts dispatched at once per replica |
| `engine.batch` | 100 | 1–1000 due tasks planned per tick |
| `engine.misfire_grace_seconds` | 60 | 1–3600; a run later than this counts as missed |
| `engine.lease_grace_seconds` | 30 | 1–3600, added to the task timeout for an attempt's lease |
| `engine.retention_days` | 90 | 1–3650, history kept |
| `engine.retention_interval_minutes` | 60 | 1–1440, how often old history is pruned |
| `engine.instance_id` | empty | empty: host name plus a random suffix; give each replica its own |
| `limits_scheduler.max_payload_bytes` | 64 KiB | 1 KiB–1 MiB |
| `limits_scheduler.max_result_bytes` | 64 KiB | 1 KiB–1 MiB; longer results are cut |
| `limits_scheduler.max_timeout_seconds` | 3600 | 1–86400, the highest task timeout accepted |
| `limits_scheduler.min_interval_seconds` | 60 | 60–86400, shortest gap between two runs of a cron schedule |
| `limits_scheduler.max_page_size` | 200 | 1–200 |
| `limits_scheduler.max_backup_bytes` | 32 MiB | 1 KiB–256 MiB |
| `limits_scheduler.max_tasks_per_tenant` | 1000 | 1–100000 |

## Replicas

Every replica runs the engine. Due tasks are locked with `SKIP LOCKED`, so
each run is planned by one replica only, and a unique index on (task, run
time, attempt) guards against duplicates. An attempt is leased for the task
timeout plus `engine.lease_grace_seconds`; if its replica dies, the attempt
times out when the lease expires and is retried like any timeout.

## Executing modules

A module that runs tasks registers its types over the mesh and serves
`scheduler.v1.TaskExecutor/ExecuteTask`. Three things must be in place:

1. The scheduler's own policy lets the module register. Add its SPIFFE ID to
   the `modules-register` rule:

   ```yaml
   rules:
     - id: gateway-forwards
       from: ["spiffe://<trust domain>/svc/gateway"]
       to: ["scheduler"]
       operations: ["*"]
       effect: allow
     - id: modules-register
       from: ["spiffe://<trust domain>/svc/lcm", "spiffe://<trust domain>/svc/<module>"]
       to: ["scheduler"]
       operations: ["/scheduler.v1.Registration/RegisterTaskTypes", "/scheduler.v1.Registration/UnregisterTaskTypes"]
       effect: allow
   ```

   `gateway-forwards` also covers the help pages (`GET /docs/v1/bundle`).

2. The module's policy lets only the scheduler execute:

   ```yaml
   rules:
     - id: scheduler-execute
       from: ["spiffe://<trust domain>/svc/scheduler"]
       to: ["<module>"]
       operations: ["/scheduler.v1.TaskExecutor/ExecuteTask"]
       effect: allow
   ```

3. Both sides can find each other: the scheduler lists the module in
   `discovery.static` (the module name is the prefix of its type names), and
   the module lists `scheduler`.

A module may only register types named after itself (`<module>:...`); the
scheduler takes the name from the caller's SPIFFE identity. Types of a
module that has been removed for good can be marked unavailable by a
platform administrator with `POST /api/scheduler/v1/modules/{module}/unregister`;
their tasks are kept and show **Type unavailable**.

Other modules the scheduler calls: auth (token keys, revocations,
`Authorization/Check`, permission registration), the gateway (registration
lease) and lcm (enrolment).

## Metrics

The admin listener (`admin.addr`, default `127.0.0.1:9800`) serves
`/healthz`, `/readyz` and `/metrics`. The scheduler's own metrics are
`scheduler.runs`, `scheduler.run.duration`, `scheduler.retries`,
`scheduler.occurrences.skipped`, `scheduler.occurrences.missed` and
`scheduler.types.registered`. The engine never logs payload values, messages or results.

## Troubleshooting

**The service refuses to start with `config: ...`.** The message names the
key and its allowed range. Common ones: `db.dsn must use sslmode=verify-full`
in production, `gateway.issuer must be an https origin`,
`platform_tenant_id must be a uuid`.

**The service refuses to start with `help docs: ...`.** The help pages built
into the binary are invalid. This is caught by `tangra-docs lint` in CI; a
build that skips CI must be rebuilt.

**A module's task types do not appear.** The module's registration is
refused: check that the module is in `modules-register` and that its types
are named `<module>:...`. Registration is retried every few seconds.

**Every attempt of a module fails with `module unavailable`.** The scheduler
cannot reach the module: it is missing from `discovery.static`, it is down,
or its policy lacks the `scheduler-execute` rule.

**Attempts fail with `refused by module: PermissionDenied`.** The module
refused the scheduler's identity: its `scheduler-execute` rule is missing or
names another trust domain. These failures are not retried.

**The portal does not update live.** Check `events.enabled` and the Valkey
settings; the log warns at start when events are off.

**Tasks with a long timeout are refused with `invalid_options`.** The
timeout is above `limits_scheduler.max_timeout_seconds` (3600 by default).
