# go-tangra-scheduler (v4)

Central, UI-driven job scheduler of the go-tangra v4 platform (feature 026).
It decides **when** work runs and asks the module that owns the work to run
it over the SPIFFE mTLS mesh.

- **Task types** are registered by modules (`scheduler.v1.Registration`). The
  owner is the caller's verified SPIFFE identity; every name is
  `<module>:<action>`. Types carry a JSON Schema for their payload, a suggested
  cron expression, a default retry count and a scope (tenant or platform).
- **Tasks** belong to a tenant (platform-scoped types: platform administrators
  only). Kinds: `periodic` (5-field cron with an IANA time zone, DST-safe),
  `delayed` (once, at a time or after a delay) and `wait_result` (once, now;
  the UI follows the run live). Payloads are validated against the type's
  schema at save time and again before every run.
- **Execution** calls `scheduler.v1.TaskExecutor/ExecuteTask` on the owning
  module with the task timeout. Retryable failures back off 30 s × 2^(n-1)
  (capped at 10 min); permanent failures stop. Every attempt is recorded with
  status, message and result; tasks show their last run and next run.
- **Exactly once across replicas**: PostgreSQL row locks (`SKIP LOCKED`),
  leases with fencing and a unique occurrence index. Missed occurrences during
  downtime are counted, with at most one catch-up run when the task allows it.
- **Control**: start/stop/restart periodic tasks, run now / run again, cancel
  one-shot tasks, bulk actions scoped to the caller.
- **Overview, metrics, audit, backup**: US6 overview; OTel metrics on the
  admin listener; closed-vocabulary audit that never records payload values;
  export/import of tasks, history and types.

## Layout

```
cmd/schedulersvc          binary (serve | bootstrap | version)
api/openapi               browser API contract (OpenAPI 3.1)
sdk/                      nested module …/sdk/v4: scheduler.v1 proto, taskexec, schedulerclient
internal/                 config, authz, audit, cron, payload, registry, tasks, engine,
                          dispatch, backup, metrics, events, stream, store, repo, httpapi, grpcapi, app
pkg/schedulermanifest     gateway manifest, permissions, module roles, abilities, nav
ui/                       federated UI remote (Vue 3, @go-tangra/ui)
deploy/                   policy.yaml, container.yaml (stack-shaped example), README.md
specs/026-scheduler-v4    specification, plan, research, data model, contracts, tasks
```

## Development

```bash
unset GOROOT; export GOWORK=off
make test                 # unit + contract tests (race)
make cover                # ≥ 80 % overall; 100 % authz, cron, payload, registry
make lint vuln            # vet, staticcheck, gosec; govulncheck (service + sdk)
sg docker -c 'make test-integration'   # TimescaleDB via testcontainers
(cd sdk && buf lint && go test -race ./...)
make build                # bin/schedulersvc (no UI); make build-ui embeds the remote
```

Permissions: `scheduler:read`, `tasks:manage`, `tasks:delete`,
`tasks:control`, `backup:manage`; module roles Scheduler administrator /
operator / viewer. Deployment: see [deploy/README.md](deploy/README.md).
Security: see [SECURITY.md](SECURITY.md).
