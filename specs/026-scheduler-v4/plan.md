# Implementation Plan: Scheduler Module for v4

**Branch**: `026-scheduler-v4` (spec, v3 repo) → code on orphan branch `v4` of
go-tangra-scheduler (worktree `go-tangra-scheduler-v4`) | **Date**: 2026-09-28 |
**Spec**: [spec.md](./spec.md)

## Summary

A central, UI-driven scheduler module for the v4 platform that decides **when** work
runs and asks the owning module to run it — v3 parity (task types registered by modules,
periodic / delayed / wait-for-result tasks, retries, history, control, backup) with v4
foundations and the v3 shortcomings fixed: tenant-owned tasks under RLS and enforced
permissions, owner taken from the SPIFFE identity, schema-validated payloads, several
tasks per type, exactly-once occurrences across replicas, one-shot tasks that never
re-fire, recorded results and attempts, real metrics, non-blocking wait-for-result with
live follow. The engine is a PostgreSQL work table with row locks and leases (research
D1); the executor/registration contract ships in the scheduler SDK (D3). ipam, lcm and
notification gain the three ported v3 task types (D11).

## Technical Context

**Language/Version**: Go 1.26 (toolchain 1.26.8); UI TypeScript + Vue 3.

**Primary Dependencies**: go-tangra framework `github.com/go-tangra/go-tangra/v4`
(SPIFFE mTLS gRPC, HTTP edge, policy, observe), auth SDK (token verifier, permission
checks, module-role registration), portal SDK (gateway client), `pgx/v5`, `goose/v3`,
`kin-openapi`, `valkey-go` (event stream), `santhosh-tekuri/jsonschema/v6` (payload
schemas), OpenTelemetry metric API; in-repo cron parser (D2). UI: `@go-tangra/ui` 4.2.x
federated remote.

**Storage**: TimescaleDB database `scheduler`, role `scheduler_app` (NOBYPASSRLS),
tables per [data-model.md](./data-model.md), audit hypertable.

**Testing**: Go `testing`; `memstore` fake repository; fake clock + fake executor
dialer for the engine; bufconn gRPC tests with stamped SPIFFE peers; fuzz tests for the
cron parser and payload validation; contract test OpenAPI ↔ mounted routes; integration
suite (testcontainers TimescaleDB + Valkey, `//go:build integration`) covering RLS,
SKIP LOCKED exactly-once with two engines, lease recovery, retention; UI vitest + lint;
coverage gate ≥ 80 % overall, 100 % on `internal/authz`, `internal/cron`,
`internal/payload`, `internal/registry` (trust decisions over untrusted input) and the
SDK `pkg/taskexec`.

**Target Platform**: Linux container (alpine + tzdata) in `go-tangra/deploy/stack` and
the go-tangra-docker production compose; behind the gateway; mesh identity by
enrolment with lcm.

**Project Type**: Web service (Go, gRPC + OpenAPI HTTP) + nested SDK module +
Module-Federation UI; consumer changes in three module repositories.

**Performance Goals**: occurrence start ≤ 5 s after the scheduled time for 99 % under
1,000 enabled tasks (SC-002; 1 s planning tick); task history page < 2 s at 10,000 runs
(SC-004; `(tenant_id, task_id, created_at)` index + server paging).

**Constraints**: RLS on every tenant row; payload/result never logged or in metrics;
payload ≤ 64 KiB, depth ≤ 32; result ≤ 64 KiB; retries ≤ 10; 1-minute minimum
interval; no catch-up by default; executor calls only from `svc/scheduler`.

**Scale/Scope**: ≤ 1,000 enabled tasks, ≤ 100 task types; six user stories; ports gRPC
9905, HTTP 9906, admin 9800.

## Constitution Check

*GATE: passed before Phase 0 and re-checked after Phase 1 (no violations).*

- **I. Secure by Default** — refuses to start without db, valkey, gateway issuer;
  production refuses plaintext DB/Valkey and insecure enrolment; every opt-out is a named
  config flag listed in `Warnings()`. Executor servers in the SDK fail closed without a
  caller function.
- **II. Zero Trust Service Communication** — all module traffic is SPIFFE mTLS with
  per-module policy files: only listed modules may register; only `svc/scheduler` may
  call `ExecuteTask`; the owner is the verified peer (never a request field); modules
  re-check the peer.
- **III. Boundary Validation & Defense in Depth** — OpenAPI validation at the edge,
  permission re-checked in the module, RLS in the database, JSON Schema validation of
  payloads at save and before each run, descriptor limits, modules validate the payload
  again (SR-002), bounded bodies/results/messages.
- **IV. Test-First** — tasks.md orders tests before implementation in every phase;
  negative tests for foreign registration, forged executor calls, cross-tenant access,
  platform-type escalation, oversized/deep payloads; fuzz for cron and payload parsing;
  100 % on security/pure packages.
- **V. Observability & Auditability** — closed-vocabulary audit for every mutation,
  control action, manual run and (un)registration (payload never recorded); OTel metrics
  (runs, duration, retries, skipped, missed, registered types) on the admin listener;
  JSON logs via the framework; correlation ids from the gateway.
- **VI. Supply Chain** — no dependency that another v4 module does not already use; cron
  in-repo; SDK depends on grpc/protobuf only; `govulncheck` gate.
- **VII. Simplicity & Explicit Configuration** — one binary, one database, a polling
  engine without leader election, typed YAML config with `KnownFields`, no reflection
  wiring.

## Project Structure

### Documentation (this feature)

```text
specs/026-scheduler-v4/
├── spec.md, checklists/requirements.md
├── plan.md, research.md, data-model.md, quickstart.md, tasks.md
└── contracts/{scheduler-api.md, grpc-contract.md, audit-events.md, mesh-policies.md}
```

### Source Code

```text
go-tangra-scheduler (branch v4, worktree go-tangra-scheduler-v4)
├── cmd/schedulersvc/{main.go,version.go}
├── api/openapi/{scheduler.yaml,embed.go}
├── sdk/                                   # module github.com/go-tangra/go-tangra-scheduler/sdk/v4
│   ├── api/proto/scheduler/v1/scheduler.proto (+ generated)
│   ├── pkg/schedulerclient/               # Register/Unregister + Registrar loop
│   └── pkg/taskexec/                      # executor server helper
├── internal/
│   ├── app/          # wiring, permissions registration, gateway lease
│   ├── config/       # typed config
│   ├── authz/        # subjects, permissions, platform scope
│   ├── audit/        # closed vocabulary + redaction + writer
│   ├── cron/         # 5-field parser, Next with tz/DST, preview
│   ├── payload/      # JSON Schema compile/validate, size/depth bounds
│   ├── registry/     # registration rules (owner, names, limits) + re-validation
│   ├── tasks/        # task service: CRUD, control, bulk, run now, overview
│   ├── engine/       # plan/claim/dispatch/retry/recover/retention loop
│   ├── dispatch/     # mesh executor client (dial by module, error classification)
│   ├── backup/       # export/import
│   ├── metrics/      # OTel instruments
│   ├── events/       # stream publisher (execution/task changes)
│   ├── stream/       # platform stream hub + SSE (copied from ticket)
│   ├── store/        # pool, scopes, migrations (goose), models, ids
│   ├── repo/         # storage contract; repodb/ (pgx), memstore/ (fake)
│   ├── httpapi/      # OpenAPI-validated browser API
│   └── grpcapi/      # scheduler.v1.Registration server
├── pkg/schedulermanifest/                 # gateway manifest, permissions, roles, abilities, nav
├── ui/                                    # federated remote (Vue 3, @go-tangra/ui 4.2.x)
├── deploy/{policy.yaml,container.yaml,README.md}
├── Dockerfile, Makefile, scripts/{coverage-gate.sh,vulncheck.sh}, .github/workflows/ci.yaml
└── README.md, SECURITY.md

go-tangra-ipam-v4 (branch 026-scheduler-tasks)   internal/taskexec/ (scan-network), app wiring, config scheduler section, policy rule
go-tangra-lcm-v4  (branch 026-scheduler-tasks)   repo ExpiringCertificates, internal/taskexec/ (check-expiring), notification client, config, policy rule
go-tangra-notification-v4 (branch 026-scheduler-tasks) notify.SendCustom, internal/taskexec/ (send-test-email), system template lcm.certificates_expiring, policy rules
go-tangra/deploy/stack (branch 026-scheduler)    compose scheduler service/token, configs/scheduler.yaml, init-db, valkey user, gateway allow, consumer configs
go-tangra-docker (branch v4, local commit)       compose example, configs/scheduler.yaml, policies/*.yaml, init-db, .env.example, prod-init
```

**Structure Decision**: mirror go-tangra-ticket-v4 / go-tangra-ipam-v4 (same package
names and middleware chain); the scheduler SDK follows ipam's nested `sdk/` module with a
local `replace`. Consumers depend on the unreleased SDK through a TEMP local `replace`
committed last on their branch.

## Rollout

1. Release the scheduler SDK (`sdk/v4.0.0`) and the scheduler (`v4.0.0`) from the new
   `v4` → `main` of go-tangra-scheduler (v3 moves to branch `v3`).
2. Consumers drop the TEMP replace, require `sdk/v4.0.0`, release minors (ipam, lcm,
   notification). notification first (template + lcm policy), then lcm, ipam.
3. Stack/prod: create database + role, register the gateway allow-list entry, add the
   scheduler service and its config/policy, update consumer policies/configs, bump pins.
4. The scheduler starts empty (no v3 data migration); modules register their types
   within 5 s of both being up and re-register every 5 min.

## Complexity Tracking

| Item | Why needed | Simpler alternative rejected because |
|------|------------|--------------------------------------|
| In-repo cron parser | exact DST/"never fires" semantics required by the spec, fuzzable | robfig/cron: different DST behaviour, unmaintained, another dependency |
| Lease + fencing on executions | replica crash must not lose or duplicate attempts (SC-003) | fire-and-forget goroutines lose attempts on crash |
| Nil-UUID tenant for platform tasks | keeps `tenant_id NOT NULL` + one RLS policy for all rows | NULL tenant needs a second policy and nullable joins |
