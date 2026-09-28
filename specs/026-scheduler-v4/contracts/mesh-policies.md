# Contract: Mesh policies

`example.org` is the dev trust domain (`prod-init.sh` rewrites it to `TRUST_DOMAIN`).

## scheduler (`deploy/policy.yaml` in go-tangra-scheduler)

```yaml
version: scheduler-dev-1
rules:
  - id: gateway-forwards
    from: ["spiffe://example.org/svc/gateway"]
    to: ["scheduler"]
    operations: ["*"]
    effect: allow
  - id: modules-register
    from: ["spiffe://example.org/svc/ipam", "spiffe://example.org/svc/lcm", "spiffe://example.org/svc/notification"]
    to: ["scheduler"]
    operations: ["/scheduler.v1.Registration/RegisterTaskTypes", "/scheduler.v1.Registration/UnregisterTaskTypes"]
    effect: allow
```

A new executing module is added to `modules-register` (the handler still limits it to
its own namespace).

## Executing modules (each module's `deploy/policy.yaml`)

```yaml
  - id: scheduler-execute
    from: ["spiffe://example.org/svc/scheduler"]
    to: ["<module>"]
    operations: ["/scheduler.v1.TaskExecutor/ExecuteTask"]
    effect: allow
```

in go-tangra-ipam, go-tangra-lcm and go-tangra-notification.

## Peers the scheduler calls (rules live in the callee's policy)

| Callee | Operations | Existing rule |
|--------|------------|---------------|
| auth | `Keys/List`, `Sessions/RevokedSince`, `Sessions/Watch`, `Authorization/Check`, `Authorization/RegisterPermissions`, `Sessions/Introspect` | `services-verify`, `services-register` (svc/*) |
| gateway | registry lease | gateway policy (svc/*) |
| lcm | `Enrollment/Enroll` (mesh enrolment) | `workloads-renew` (svc/*) |
| ipam / lcm / notification | `TaskExecutor/ExecuteTask` | `scheduler-execute` (new, above) |

## lcm → notification (Notify expiring certificates)

notification `modules-send` gains `spiffe://example.org/svc/lcm` (`Notifier/Send` with
`template_key = lcm.*` only — the key namespace is enforced by notification).

## Gateway route allow-list

`-allow spiffe://example.org/svc/scheduler=/api/scheduler;scheduler` (stack
`gateway-bootstrap`; production allow-list).
