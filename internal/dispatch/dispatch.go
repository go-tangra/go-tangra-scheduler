// Package dispatch sends a claimed attempt to its owning module over the
// Freya SPIFFE mesh (research D3): scheduler.v1.TaskExecutor/ExecuteTask on a
// lazily dialled per-module connection, bounded by the task timeout. The
// module's policy admits only svc/scheduler and it re-checks the peer.
//
// Classification (contracts/grpc-contract.md):
//
//	success=true                                    succeeded
//	success=false, permanent_failure=true           failed, no retry
//	success=false                                   failed, retried
//	Unavailable/Unknown/Internal/ResourceExhausted/
//	Aborted/dial failure                            failed "module unavailable", retried
//	deadline exceeded                               timed_out, retried
//	PermissionDenied/Unauthenticated/InvalidArgument/
//	Unimplemented/NotFound (and anything else)      failed "refused by module: <code>", no retry
//
// Messages are cut to 4 KiB and results to the configured size (flagged
// truncated). Nothing here logs payloads, messages or results.
package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/engine"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Bounds.
const (
	MaxMessageBytes       = 4 << 10
	DefaultMaxResultBytes = 64 << 10
	DefaultTimeout        = 300 * time.Second
	MsgUnavailable        = "module unavailable"
	MsgTimeout            = "timed out waiting for the module"
)

// Dialer returns a connection to a module by its logical (discovery) name.
type Dialer func(ctx context.Context, module string) (grpc.ClientConnInterface, error)

// Client dispatches attempts; connections are dialled once per module.
type Client struct {
	dial      Dialer
	maxResult int
	mu        sync.Mutex
	conns     map[string]grpc.ClientConnInterface
}

var _ engine.Dispatcher = (*Client)(nil)

// New builds a client; maxResult <= 0 uses 64 KiB.
func New(dial Dialer, maxResult int) *Client {
	if maxResult <= 0 {
		maxResult = DefaultMaxResultBytes
	}
	return &Client{dial: dial, maxResult: maxResult, conns: map[string]grpc.ClientConnInterface{}}
}

func (c *Client) conn(ctx context.Context, module string) (grpc.ClientConnInterface, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cc, ok := c.conns[module]; ok {
		return cc, nil
	}
	cc, err := c.dial(ctx, module)
	if err != nil {
		return nil, err
	}
	c.conns[module] = cc
	return cc, nil
}

// Request maps an attempt to the wire request: platform-scoped tasks carry no
// tenant, scheduled_at is the intended occurrence time.
func Request(a engine.Attempt) *schedulerv1.ExecuteTaskRequest {
	tenant := a.Exec.TenantID
	if tenant == store.PlatformScopeTenant {
		tenant = ""
	}
	return &schedulerv1.ExecuteTaskRequest{
		ExecutionId: a.Exec.ID, TaskType: a.Exec.TypeName, Payload: append([]byte(nil), a.Task.Payload...),
		Attempt: int32(a.Exec.Attempt), MaxAttempts: int32(a.Exec.MaxAttempts), // #nosec G115 -- attempts are bounded to 1..11
		ScheduledAt: timestamppb.New(a.Exec.OccurrenceAt), TenantId: tenant, TaskId: a.Exec.TaskID, OccurrenceId: a.Exec.OccurrenceID,
	}
}

// Dispatch implements engine.Dispatcher.
func (c *Client) Dispatch(ctx context.Context, a engine.Attempt) engine.Outcome {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cc, err := c.conn(ctx, a.Exec.Module)
	if err != nil {
		return engine.Outcome{Status: store.ExecFailed, Message: MsgUnavailable, Retryable: true}
	}
	res, err := schedulerv1.NewTaskExecutorClient(cc).ExecuteTask(ctx, Request(a))
	if err != nil {
		return Classify(err, ctx.Err())
	}
	out := engine.Outcome{Message: Cut(res.GetMessage(), MaxMessageBytes)}
	if data := res.GetResultData(); len(data) > 0 {
		if len(data) > c.maxResult {
			data, out.Truncated = data[:c.maxResult], true
		}
		out.Result = append([]byte(nil), data...)
	}
	switch {
	case res.GetSuccess():
		out.Status = store.ExecSucceeded
	case res.GetPermanentFailure():
		out.Status = store.ExecFailed
	default:
		out.Status, out.Retryable = store.ExecFailed, true
	}
	return out
}

// Classify maps a transport error (and the call context's error) to an
// outcome.
func Classify(err, ctxErr error) engine.Outcome {
	if errors.Is(ctxErr, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return engine.Outcome{Status: store.ExecTimedOut, Message: MsgTimeout, Retryable: true}
	}
	switch code := status.Code(err); code {
	case codes.Unavailable, codes.Unknown, codes.Internal, codes.ResourceExhausted, codes.Aborted, codes.Canceled:
		return engine.Outcome{Status: store.ExecFailed, Message: MsgUnavailable, Retryable: true}
	default:
		return engine.Outcome{Status: store.ExecFailed, Message: "refused by module: " + code.String()}
	}
}

// Cut bounds s to max bytes on a UTF-8 boundary.
func Cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}
