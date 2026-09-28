# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub's
**"Report a vulnerability"** (Security → Advisories) on
`github.com/go-tangra/go-tangra-scheduler`. Do not open a public issue. Include
the affected version or commit, the impact and a reproduction. You will get an
acknowledgement within five working days.

## Threat model (summary)

The scheduler crosses four trust boundaries: browser → gateway → scheduler,
module → scheduler (type registration), scheduler → module (execution) and
scheduler → auth. The main threats and their mitigations (research STRIDE):

| Threat | Mitigation |
|---|---|
| A module registers or hijacks another module's task types | The owner is the verified SPIFFE peer (never a request field) and must belong to the scheduler's trust domain; names must start with `<owner>:`; a name owned by another module refuses the whole request; the mesh policy admits only executing modules |
| Forged or replayed execution requests to a module | Each module's policy admits only `svc/scheduler` for `ExecuteTask` and the SDK server re-checks the peer (fails closed); the request carries an occurrence id (idempotency key) |
| A tenant reads or runs another tenant's tasks | FORCE row-level security on every tenant row; the tenant scope comes from the platform token; the all-tenant scope is used only after the platform-admin check; foreign rows answer 404 |
| Escalation to platform-scoped task types | Platform types are invisible to (and refused for) everybody but platform administrators; backups never restore a platform task into a tenant or a type as available |
| Malicious payloads | JSON Schema validation (no remote references) at save time and before every run, size ≤ 64 KiB, nesting ≤ 32, object only; modules validate again |
| Secrets leaking through payloads, results, logs, metrics or audit | Payloads must not carry secrets (reference them, e.g. warden ids); payload and result values are never logged, never used as metric labels and never audited (detail redaction); events carry ids and statuses only |
| Denial of service by frequent crons or retry storms | Minimum interval 60 s, retries ≤ 10 with exponential backoff, bounded workers, bounded messages/results/descriptors, per-tenant task limit |

## Supported versions

Only the latest `v4.x` release receives security fixes.
