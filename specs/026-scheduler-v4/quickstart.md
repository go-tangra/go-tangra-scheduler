# Quickstart: Scheduler Module for v4

## Local development

```bash
cd go-tangra-scheduler-v4
unset GOROOT; export GOWORK=off
make test                 # unit + contract (race)
make cover                # ≥ 80 % overall, 100 % authz/cron/payload/registry + sdk taskexec
sg docker -c 'unset GOROOT; cd /home/jadmin/projects/go-tangra/go-tangra-scheduler-v4 && make test-integration'
make vuln lint
(cd sdk && go test -race ./... && buf lint)
export NODE_AUTH_TOKEN=$(gh auth token); (cd ui && npm ci && npm run lint && npm run test:unit && npm run build)
```

## Scenario 1 — types registered, periodic task (US1)

1. Stack up with scheduler + ipam/lcm/notification on the `026-*` images.
2. `GET /api/scheduler/v1/task-types` (as a tenant admin) lists `ipam:scan-network`,
   `lcm:check-expiring-certificates`, `notification:send-test-email` with module owner.
3. Scheduler → Tasks → New task → "Scan network (ipam)": the form shows subnet/CIDR/all/
   SNMP/DNS fields; schedule pre-filled `0 3 * * *` with its next 5 runs; save.
4. Invalid payload (`{"subnetId": 5}`) → 422 `invalid_payload` naming `/subnetId`;
   `cron=61 * * * *` → 422 `invalid_cron`.

## Scenario 2 — history and retries (US2)

Stop ipam, "Run now" the scan task: attempt 1 `failed` "module unavailable", attempt 2
queued ~30 s later; start ipam → attempt 2 `succeeded` with "queued N scan(s), skipped M".
Filter "failed only" shows attempt 1.

## Scenario 3 — one-shot and wait-for-result (US3)

Create "Send test email" `wait_result` with `{"recipient":"ops@example.org"}`: the
response carries `execution_id`; the UI follows queued → running → succeeded; Mailpit
(http://localhost:8025) shows the message. Restart the scheduler: the completed task does
not fire again.

## Scenario 4 — control (US4)

Stop the periodic task → `enabled=false`, no occurrence; start → `next_run_at` recomputed;
bulk stop/start report `affected`.

## Scenario 5 — ported types (US5)

- `lcm:check-expiring-certificates` with `{"daysBeforeExpiry":30,"recipients":["pki@example.org"]}`
  → Mailpit receives the digest (or "No certificates expiring within 30 days").
- `ipam:scan-network` `{"all":true}` → scans appear under IPAM → IP scans.

## Scenario 6 — overview/metrics (US6)

`GET /api/scheduler/v1/overview`; admin listener
`docker compose -p freya-stack exec scheduler wget -qO- http://127.0.0.1:9800/metrics | grep scheduler_`.

## Negative checks (SC-006, SC-008)

- A module registering `lcm:*` from svc/ipam → `PERMISSION_DENIED foreign_namespace`.
- Calling ipam `ExecuteTask` from any identity but svc/scheduler → `PermissionDenied`.
- Tenant B `GET /tasks/{tenant-A-task}` → 404.
- `grep` logs/metrics/audit for a payload marker value → no hits.
