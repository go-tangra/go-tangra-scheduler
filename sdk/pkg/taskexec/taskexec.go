// Package taskexec is the ready-made scheduler.v1.TaskExecutor server a module
// registers to execute its scheduled tasks (feature 026, contracts/grpc-contract.md).
//
// Security contract:
//   - Only the scheduler may call it. The mesh policy of the module admits
//     spiffe://<td>/svc/scheduler for /scheduler.v1.TaskExecutor/ExecuteTask and
//     the server re-checks the verified peer through Options.Caller. Without a
//     Caller function every call is refused (fail closed).
//   - The tenant is a UUID and every handler must scope its work to it; an empty
//     tenant is accepted only for the types the module declared platform-scoped.
//   - The payload is untrusted input even though the scheduler validated it
//     against the registered schema: it is bounded here and handlers decode it
//     strictly (DecodeStrict) and validate it again.
//   - Payloads and results are never logged by this package.
package taskexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
)

// Bounds of one execution.
const (
	MaxPayloadBytes = 64 << 10 // request payload
	MaxResultBytes  = 64 << 10 // result data (larger results are dropped with a note)
	MaxMessageBytes = 4 << 10  // message shown in the scheduler history
)

// DefaultScheduler is the service name of the scheduler's SPIFFE identity.
const DefaultScheduler = "scheduler"

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Request is one attempt of a task occurrence as the handler sees it.
type Request struct {
	ExecutionID  string
	TaskID       string
	OccurrenceID string
	Type         string
	TenantID     string // UUID; empty only for platform-scoped types
	Payload      json.RawMessage
	Attempt      int
	MaxAttempts  int
	ScheduledAt  time.Time
}

// Result is the handler's answer.
type Result struct {
	Success   bool
	Message   string
	Data      any  // optional; marshalled to JSON
	Permanent bool // failure that retrying cannot fix
}

// OK is a successful result.
func OK(msg string) Result { return Result{Success: true, Message: msg} }

// Retry is a failure the scheduler retries while attempts remain.
func Retry(msg string) Result { return Result{Message: msg} }

// Permanent is a failure the scheduler does not retry.
func Permanent(msg string) Result { return Result{Message: msg, Permanent: true} }

// Handler executes one task type.
type Handler func(ctx context.Context, req Request) Result

// Options configure the server.
type Options struct {
	// Caller returns the verified SPIFFE service name of the peer (for example
	// from go-tangra authn.FromContext: p.ID.ServiceName()). nil refuses every call.
	Caller func(ctx context.Context) (service string, ok bool)
	// Scheduler is the only service allowed to call (default "scheduler").
	Scheduler string
	// Platform lists the types that run without a tenant.
	Platform map[string]bool
	// Log receives one line per execution (type, attempt, outcome; never payloads).
	Log *slog.Logger
}

// Server implements schedulerv1.TaskExecutorServer.
type Server struct {
	schedulerv1.UnimplementedTaskExecutorServer
	handlers map[string]Handler
	o        Options
}

// NewServer builds the executor over handlers keyed by task type.
func NewServer(handlers map[string]Handler, o Options) *Server {
	if o.Scheduler == "" {
		o.Scheduler = DefaultScheduler
	}
	h := make(map[string]Handler, len(handlers))
	for k, v := range handlers {
		h[k] = v
	}
	return &Server{handlers: h, o: o}
}

var _ schedulerv1.TaskExecutorServer = (*Server)(nil)

// ExecuteTask implements schedulerv1.TaskExecutorServer.
func (s *Server) ExecuteTask(ctx context.Context, req *schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
	if s.o.Caller == nil {
		return nil, status.Error(codes.PermissionDenied, "caller_unverified")
	}
	svc, ok := s.o.Caller(ctx)
	if !ok || svc != s.o.Scheduler {
		return nil, status.Error(codes.PermissionDenied, "caller_not_scheduler")
	}
	tenant := req.GetTenantId()
	switch {
	case tenant == "" && !s.o.Platform[req.GetTaskType()]:
		return nil, status.Error(codes.InvalidArgument, "tenant_required")
	case tenant != "" && !uuidRE.MatchString(tenant):
		return nil, status.Error(codes.InvalidArgument, "tenant_invalid")
	}
	if len(req.GetPayload()) > MaxPayloadBytes {
		return nil, status.Error(codes.InvalidArgument, "payload_too_large")
	}
	payload := bytes.TrimSpace(req.GetPayload())
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if !isObject(payload) {
		return nil, status.Error(codes.InvalidArgument, "payload_not_object")
	}
	h := s.handlers[req.GetTaskType()]
	if h == nil {
		return respond(Permanent("unknown task type " + clip(req.GetTaskType(), 200))), nil
	}
	r := Request{
		ExecutionID: req.GetExecutionId(), TaskID: req.GetTaskId(), OccurrenceID: req.GetOccurrenceId(),
		Type: req.GetTaskType(), TenantID: tenant, Payload: json.RawMessage(payload),
		Attempt: int(req.GetAttempt()), MaxAttempts: int(req.GetMaxAttempts()),
	}
	if ts := req.GetScheduledAt(); ts != nil {
		r.ScheduledAt = ts.AsTime()
	}
	res := s.run(ctx, h, r)
	if s.o.Log != nil {
		s.o.Log.InfoContext(ctx, "scheduled task executed", "type", r.Type, "execution_id", r.ExecutionID,
			"attempt", r.Attempt, "success", res.Success, "permanent", res.Permanent)
	}
	return respond(res), nil
}

// run calls h and turns a panic into a retryable failure without its text.
func (s *Server) run(ctx context.Context, h Handler, r Request) (res Result) {
	defer func() {
		if p := recover(); p != nil {
			if s.o.Log != nil {
				s.o.Log.ErrorContext(ctx, "scheduled task handler panicked", "type", r.Type, "execution_id", r.ExecutionID)
			}
			res = Retry("internal error in the task handler")
		}
	}()
	return h(ctx, r)
}

func respond(r Result) *schedulerv1.ExecuteTaskResponse {
	out := &schedulerv1.ExecuteTaskResponse{Success: r.Success, Message: clip(r.Message, MaxMessageBytes)}
	if !r.Success {
		out.PermanentFailure = r.Permanent
	}
	if r.Data != nil {
		raw, err := json.Marshal(r.Data)
		switch {
		case err != nil:
			out.Message = clip(out.Message+" (result not serialisable)", MaxMessageBytes)
		case len(raw) > MaxResultBytes:
			out.Message = clip(out.Message+" (result too large, dropped)", MaxMessageBytes)
		default:
			out.ResultData = raw
		}
	}
	return out
}

// isObject reports whether raw is one JSON object.
func isObject(raw []byte) bool {
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	return json.Valid(raw)
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// ErrPayload wraps every DecodeStrict failure.
var ErrPayload = errors.New("taskexec: invalid payload")

// DecodeStrict decodes one JSON object into v, refusing unknown fields and
// trailing data. Errors name the problem but never echo payload values.
func DecodeStrict(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var ute *json.UnmarshalTypeError
		switch {
		case errors.As(err, &ute) && ute.Field != "":
			return fmt.Errorf("%w: field %q has the wrong type", ErrPayload, ute.Field)
		case bytes.Contains([]byte(err.Error()), []byte("unknown field")):
			return fmt.Errorf("%w: %s", ErrPayload, clip(err.Error(), 200))
		default:
			return fmt.Errorf("%w: not a JSON object of the expected shape", ErrPayload)
		}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", ErrPayload)
	}
	return nil
}

// ValidTenant reports whether id is a UUID (helper for handlers).
func ValidTenant(id string) bool { return uuidRE.MatchString(id) }
