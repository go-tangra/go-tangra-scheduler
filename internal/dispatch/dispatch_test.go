package dispatch

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/engine"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

const tenantA = "11111111-1111-7111-8111-111111111111"

// module is a fake TaskExecutor answering from a function.
type module struct {
	schedulerv1.UnimplementedTaskExecutorServer
	mu   sync.Mutex
	got  []*schedulerv1.ExecuteTaskRequest
	resp func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error)
}

func (m *module) ExecuteTask(ctx context.Context, req *schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
	m.mu.Lock()
	m.got = append(m.got, req)
	fn := m.resp
	m.mu.Unlock()
	return fn(req)
}

func (m *module) set(fn func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error)) {
	m.mu.Lock()
	m.resp = fn
	m.mu.Unlock()
}

func (m *module) last() *schedulerv1.ExecuteTaskRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.got[len(m.got)-1]
}

func serve(t *testing.T, m *module) Dialer {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	schedulerv1.RegisterTaskExecutorServer(gs, m)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	dials := 0
	return func(ctx context.Context, name string) (grpc.ClientConnInterface, error) {
		dials++
		if name != "ipam" {
			return nil, errors.New("unknown module")
		}
		if dials > 1 {
			t.Error("connection dialled twice for the same module")
		}
		return grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
}

func attempt(tenant string) engine.Attempt {
	occ := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	return engine.Attempt{
		Exec: store.Execution{ID: "e1", TenantID: tenant, TaskID: "t1", OccurrenceID: "o1", TypeName: "ipam:scan-network", Module: "ipam",
			Attempt: 2, MaxAttempts: 3, OccurrenceAt: occ},
		Task:    store.Task{Payload: []byte(`{"all":true}`)},
		Timeout: time.Second,
	}
}

func TestRequestMapping(t *testing.T) {
	r := Request(attempt(tenantA))
	if r.GetExecutionId() != "e1" || r.GetTaskId() != "t1" || r.GetOccurrenceId() != "o1" || r.GetTaskType() != "ipam:scan-network" ||
		string(r.GetPayload()) != `{"all":true}` || r.GetAttempt() != 2 || r.GetMaxAttempts() != 3 || r.GetTenantId() != tenantA ||
		!r.GetScheduledAt().AsTime().Equal(time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("request = %v", r)
	}
	if Request(attempt(store.PlatformScopeTenant)).GetTenantId() != "" {
		t.Fatal("platform tasks must carry no tenant")
	}
}

func TestDispatchOutcomes(t *testing.T) {
	m := &module{}
	c := New(serve(t, m), 16)
	cases := []struct {
		name      string
		resp      *schedulerv1.ExecuteTaskResponse
		err       error
		status    string
		retryable bool
		msg       string
	}{
		{"success", &schedulerv1.ExecuteTaskResponse{Success: true, Message: "queued 3 scan(s)", ResultData: []byte(`{"n":3}`)}, nil, store.ExecSucceeded, false, "queued 3 scan(s)"},
		{"permanent", &schedulerv1.ExecuteTaskResponse{Message: "invalid recipient", PermanentFailure: true}, nil, store.ExecFailed, false, "invalid recipient"},
		{"retryable", &schedulerv1.ExecuteTaskResponse{Message: "busy"}, nil, store.ExecFailed, true, "busy"},
		{"unavailable", nil, status.Error(codes.Unavailable, "down"), store.ExecFailed, true, MsgUnavailable},
		{"internal", nil, status.Error(codes.Internal, "boom"), store.ExecFailed, true, MsgUnavailable},
		{"resource", nil, status.Error(codes.ResourceExhausted, "x"), store.ExecFailed, true, MsgUnavailable},
		{"aborted", nil, status.Error(codes.Aborted, "x"), store.ExecFailed, true, MsgUnavailable},
		{"unknown", nil, errors.New("plain"), store.ExecFailed, true, MsgUnavailable},
		{"deadline", nil, status.Error(codes.DeadlineExceeded, "slow"), store.ExecTimedOut, true, MsgTimeout},
		{"denied", nil, status.Error(codes.PermissionDenied, "not the scheduler"), store.ExecFailed, false, "refused by module: PermissionDenied"},
		{"unauthenticated", nil, status.Error(codes.Unauthenticated, "x"), store.ExecFailed, false, "refused by module: Unauthenticated"},
		{"invalid", nil, status.Error(codes.InvalidArgument, "x"), store.ExecFailed, false, "refused by module: InvalidArgument"},
		{"unimplemented", nil, status.Error(codes.Unimplemented, "x"), store.ExecFailed, false, "refused by module: Unimplemented"},
		{"notfound", nil, status.Error(codes.NotFound, "x"), store.ExecFailed, false, "refused by module: NotFound"},
	}
	for _, cs := range cases {
		m.set(func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
			return cs.resp, cs.err
		})
		o := c.Dispatch(context.Background(), attempt(tenantA))
		if o.Status != cs.status || o.Retryable != cs.retryable || o.Message != cs.msg {
			t.Errorf("%s: %+v", cs.name, o)
		}
	}
	// result bounded and flagged; message cut to 4 KiB on a rune boundary
	m.set(func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
		return &schedulerv1.ExecuteTaskResponse{Success: true, Message: strings.Repeat("é", 3000), ResultData: []byte(strings.Repeat("x", 40))}, nil
	})
	o := c.Dispatch(context.Background(), attempt(tenantA))
	if !o.Truncated || len(o.Result) != 16 || len(o.Message) > MaxMessageBytes || !strings.HasSuffix(o.Message, "é") {
		t.Fatalf("bounds: truncated=%v result=%d message=%d", o.Truncated, len(o.Result), len(o.Message))
	}
	// the module receives the attempt's tenant
	if got := m.last(); got.GetTenantId() != tenantA {
		t.Fatalf("tenant = %q", got.GetTenantId())
	}
}

func TestTimeoutAndDialFailure(t *testing.T) {
	m := &module{resp: func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
		time.Sleep(200 * time.Millisecond)
		return &schedulerv1.ExecuteTaskResponse{Success: true}, nil
	}}
	c := New(serve(t, m), 0)
	a := attempt(tenantA)
	a.Timeout = 20 * time.Millisecond
	if o := c.Dispatch(context.Background(), a); o.Status != store.ExecTimedOut || !o.Retryable {
		t.Fatalf("timeout = %+v", o)
	}
	a.Exec.Module = "lcm" // the dialer refuses it
	if o := c.Dispatch(context.Background(), a); o.Status != store.ExecFailed || !o.Retryable || o.Message != MsgUnavailable {
		t.Fatalf("dial failure = %+v", o)
	}
	a = attempt(tenantA)
	a.Timeout = 0 // default timeout applies
	m.set(func(*schedulerv1.ExecuteTaskRequest) (*schedulerv1.ExecuteTaskResponse, error) {
		return &schedulerv1.ExecuteTaskResponse{Success: true}, nil
	})
	if o := c.Dispatch(context.Background(), a); o.Status != store.ExecSucceeded || o.Result != nil {
		t.Fatalf("default timeout = %+v", o)
	}
	if c.maxResult != DefaultMaxResultBytes {
		t.Fatal("default result bound")
	}
	if o := Classify(status.Error(codes.Canceled, "x"), context.DeadlineExceeded); o.Status != store.ExecTimedOut {
		t.Fatal("context deadline wins")
	}
	if Cut("abc", 10) != "abc" || Cut("aé", 2) != "a" {
		t.Fatal("cut")
	}
}
