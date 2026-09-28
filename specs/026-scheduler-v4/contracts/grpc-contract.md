# Contract: Registration + TaskExecutor (scheduler SDK)

Normative proto: `sdk/api/proto/scheduler/v1/scheduler.proto`
(module `github.com/go-tangra/go-tangra-scheduler/sdk/v4`, package `scheduler.v1`).

## Registration (served by the scheduler)

| RPC | Caller | Semantics |
|-----|--------|-----------|
| `/scheduler.v1.Registration/RegisterTaskTypes` | modules listed in the scheduler policy | owner = SPIFFE peer service name; upsert; names `^<owner>:[a-z][a-z0-9-]{0,62}$`; foreign names refused (`PERMISSION_DENIED type_owned_by_other_module` / `foreign_namespace`); malformed → `INVALID_ARGUMENT <reason>`; schema change re-validates the type's tasks |
| `/scheduler.v1.Registration/UnregisterTaskTypes` | same | marks the caller's types unavailable; tasks kept |

Descriptor limits: ≤ 50 per request; display name 1–200; description ≤ 2,000;
`payload_schema` ≤ 64 KiB, a JSON object schema that compiles with remote references
disabled; `default_cron` empty or valid; `default_max_retry` 0–10;
`scope` TENANT (default) or PLATFORM.

## TaskExecutor (served by each executing module)

| RPC | Caller | Semantics |
|-----|--------|-----------|
| `/scheduler.v1.TaskExecutor/ExecuteTask` | `spiffe://<td>/svc/scheduler` only | run one attempt for `tenant_id`; answer promptly |

Request: `execution_id`, `task_id`, `occurrence_id`, `task_type`, `payload` (JSON
object), `attempt`, `max_attempts`, `tenant_id` (UUID; empty only for platform types),
`scheduled_at`. Response: `success`, `message` (≤ 4 KiB), `result_data` (≤ 64 KiB JSON),
`permanent_failure`.

Scheduler interpretation:

| Module answer | Attempt status | Retry |
|---------------|----------------|-------|
| `success=true` | succeeded | — |
| `success=false, permanent_failure=true` | failed | no |
| `success=false` | failed | yes, while attempts remain |
| gRPC `Unavailable`, `Unknown`, `Internal`, `ResourceExhausted`, `Aborted`, dial failure | failed ("module unavailable") | yes |
| deadline exceeded (task timeout) | timed_out | yes |
| gRPC `PermissionDenied`, `Unauthenticated`, `InvalidArgument`, `Unimplemented`, `NotFound` | failed ("refused by module: <code>") | no |

## SDK helpers

### `pkg/schedulerclient`

```go
type Descriptor struct { Type, DisplayName, Description, PayloadSchema, DefaultCron string; DefaultMaxRetry int32; Platform bool }
func Register(ctx context.Context, cc grpc.ClientConnInterface, ds []Descriptor) (*schedulerv1.RegisterTaskTypesResponse, error)
func Unregister(ctx context.Context, cc grpc.ClientConnInterface) error
type Registrar struct { Dial func(context.Context) (grpc.ClientConnInterface, error); Types []Descriptor; Retry, Refresh time.Duration; Log *slog.Logger }
func (r *Registrar) Run(ctx context.Context) // retry until accepted, then refresh periodically
```

### `pkg/taskexec`

```go
type Request struct { ExecutionID, TaskID, OccurrenceID, Type, TenantID string; Payload json.RawMessage; Attempt, MaxAttempts int; ScheduledAt time.Time }
type Result struct { Success bool; Message string; Data any; Permanent bool }
func OK(msg string) Result; func Retry(msg string) Result; func Permanent(msg string) Result
type Handler func(ctx context.Context, req Request) Result
type Options struct {
    Caller     func(ctx context.Context) (service string, ok bool) // nil ⇒ every call refused
    Scheduler  string                                               // allowed caller service name, default "scheduler"
    Platform   map[string]bool                                      // types that run without a tenant
    Log        *slog.Logger
}
func NewServer(handlers map[string]Handler, o Options) *Server // implements schedulerv1.TaskExecutorServer
func DecodeStrict(raw json.RawMessage, v any) error // unknown fields refused
```

Server guarantees: caller must be the scheduler (`PermissionDenied`); tenant must be a
UUID, or empty only for `Platform` types (`InvalidArgument`); payload ≤ 64 KiB JSON
object; unknown type → `permanent_failure`; handler panic → retryable failure without
the panic text; message cut to 4 KiB; result marshalled and cut to 64 KiB. Handlers
receive the payload as untrusted input.
