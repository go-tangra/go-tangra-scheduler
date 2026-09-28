package schedulerclient

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
)

type fakeScheduler struct {
	schedulerv1.UnimplementedRegistrationServer
	mu          sync.Mutex
	got         []*schedulerv1.RegisterTaskTypesRequest
	unregisters int
	failFirst   int32
	calls       atomic.Int32
}

func (f *fakeScheduler) RegisterTaskTypes(_ context.Context, req *schedulerv1.RegisterTaskTypesRequest) (*schedulerv1.RegisterTaskTypesResponse, error) {
	if f.calls.Add(1) <= f.failFirst {
		return nil, errors.New("scheduler starting")
	}
	f.mu.Lock()
	f.got = append(f.got, req)
	f.mu.Unlock()
	return &schedulerv1.RegisterTaskTypesResponse{RegisteredCount: int32(len(req.GetTaskTypes()))}, nil
}

func (f *fakeScheduler) UnregisterTaskTypes(context.Context, *schedulerv1.UnregisterTaskTypesRequest) (*schedulerv1.UnregisterTaskTypesResponse, error) {
	f.mu.Lock()
	f.unregisters++
	f.mu.Unlock()
	return &schedulerv1.UnregisterTaskTypesResponse{}, nil
}

func dial(t *testing.T, f *fakeScheduler) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	schedulerv1.RegisterRegistrationServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

var types = []Descriptor{
	{Type: "ipam:scan-network", DisplayName: "Scan network", DefaultCron: "0 3 * * *", DefaultMaxRetry: 2, PayloadSchema: `{"type":"object"}`},
	{Type: "ipam:purge", DisplayName: "Purge", Platform: true},
}

func TestDescriptorProto(t *testing.T) {
	p := types[0].Proto()
	if p.GetTaskType() != "ipam:scan-network" || p.GetDefaultCron() != "0 3 * * *" || p.GetDefaultMaxRetry() != 2 ||
		p.GetScope() != schedulerv1.TaskScope_TASK_SCOPE_TENANT || p.GetPayloadSchema() == "" {
		t.Fatalf("tenant descriptor: %+v", p)
	}
	if types[1].Proto().GetScope() != schedulerv1.TaskScope_TASK_SCOPE_PLATFORM {
		t.Fatal("platform scope")
	}
}

func TestRegisterUnregister(t *testing.T) {
	f := &fakeScheduler{}
	cc := dial(t, f)
	if _, err := Register(context.Background(), cc, nil); !errors.Is(err, ErrNoTypes) {
		t.Fatalf("empty: %v", err)
	}
	res, err := Register(context.Background(), cc, types)
	if err != nil || res.GetRegisteredCount() != 2 || len(f.got) != 1 || len(f.got[0].GetTaskTypes()) != 2 {
		t.Fatalf("register: %v %v", res, err)
	}
	if err := Unregister(context.Background(), cc); err != nil || f.unregisters != 1 {
		t.Fatalf("unregister: %v", err)
	}
}

func TestRegistrarRetriesThenRefreshes(t *testing.T) {
	f := &fakeScheduler{failFirst: 2}
	cc := dial(t, f)
	var dialErrs atomic.Int32
	var accepted atomic.Int32
	r := &Registrar{
		Dial: func(context.Context) (grpc.ClientConnInterface, error) {
			if dialErrs.Add(1) == 1 {
				return nil, errors.New("scheduler not discovered yet")
			}
			return cc, nil
		},
		Types: types, Retry: 5 * time.Millisecond, Refresh: 20 * time.Millisecond,
		Log:          slog.New(slog.DiscardHandler),
		OnRegistered: func(*schedulerv1.RegisterTaskTypesResponse) { accepted.Add(1) },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for accepted.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("accepted %d registrations", accepted.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRegistrarDefaultsAndNoDialer(t *testing.T) {
	r := &Registrar{Types: types}
	if err := r.Once(context.Background()); err == nil {
		t.Fatal("no dialer must fail")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Run(ctx) // returns immediately on a cancelled context
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	r.Run(ctx2) // default retry (5 s) is interrupted by the context
	if r.log() == nil {
		t.Fatal("log")
	}
}

func TestRegistrarStopsWhenCancelledDuringCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Registrar{Types: types, Dial: func(context.Context) (grpc.ClientConnInterface, error) {
		cancel()
		return nil, errors.New("cancelled while dialling")
	}}
	r.Run(ctx)
}
