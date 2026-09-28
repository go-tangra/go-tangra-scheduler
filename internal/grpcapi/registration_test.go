package grpcapi

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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
	"github.com/go-tangra/go-tangra/v4/authn"
	"github.com/go-tangra/go-tangra/v4/identity"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

type rec struct {
	mu  sync.Mutex
	got []audit.Event
}

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	return nil
}

// stampPeer plays the Freya authn middleware: the test names the verified
// SPIFFE identity in metadata ("" = no peer).
func stampPeer(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-test-peer"); len(v) == 1 && v[0] != "" {
		id, err := identity.ParseSPIFFEID(v[0])
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		ctx = authn.WithPeer(ctx, authn.PeerIdentity{ID: id, ServiceName: id.ServiceName(), VerifiedAt: time.Now()})
	}
	return h(ctx, req)
}

type harness struct {
	client schedulerv1.RegistrationClient
	st     *memstore.Mem
	rec    *rec
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{st: memstore.New(), rec: &rec{}}
	reg := registry.New(registry.Deps{Store: h.st, Audit: h.rec, MaxPayloadBytes: 1 << 16})
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(stampPeer))
	Register(gs, New(reg, "example.org"))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	h.client = schedulerv1.NewRegistrationClient(cc)
	return h
}

func as(peer string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-test-peer", peer)
}

func scanDescriptor() *schedulerv1.TaskTypeDescriptor {
	return &schedulerv1.TaskTypeDescriptor{TaskType: "ipam:scan-network", DisplayName: "Scan network", DefaultCron: "0 3 * * *", DefaultMaxRetry: 2,
		PayloadSchema: `{"type":"object","properties":{"all":{"type":"boolean"}}}`}
}

func TestRegisterAndUnregister(t *testing.T) {
	h := setup(t)
	res, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/ipam"), &schedulerv1.RegisterTaskTypesRequest{TaskTypes: []*schedulerv1.TaskTypeDescriptor{
		scanDescriptor(), {TaskType: "ipam:sweep", DisplayName: "Sweep", Scope: schedulerv1.TaskScope_TASK_SCOPE_PLATFORM},
	}})
	if err != nil || res.GetRegisteredCount() != 2 || !strings.Contains(res.GetMessage(), "2") {
		t.Fatalf("register = %v %v", res, err)
	}
	tt, err := h.st.GetTaskType(context.Background(), "ipam:scan-network")
	if err != nil || tt.Module != "ipam" || tt.Scope != store.ScopeTenant {
		t.Fatalf("stored = %+v %v", tt, err)
	}
	if p, _ := h.st.GetTaskType(context.Background(), "ipam:sweep"); p.Scope != store.ScopePlatform {
		t.Fatalf("platform scope = %+v", p)
	}
	un, err := h.client.UnregisterTaskTypes(as("spiffe://example.org/svc/ipam"), &schedulerv1.UnregisterTaskTypesRequest{})
	if err != nil || un.GetUnregisteredCount() != 2 {
		t.Fatalf("unregister = %v %v", un, err)
	}
	if tt, _ = h.st.GetTaskType(context.Background(), "ipam:scan-network"); tt.Available {
		t.Fatal("still available")
	}
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	if len(h.rec.got) != 2 || h.rec.got[0].EventType != audit.TypeRegister || h.rec.got[1].EventType != audit.TypeUnregister ||
		h.rec.got[0].ActorID != "spiffe://example.org/svc/ipam" {
		t.Fatalf("audit = %+v", h.rec.got)
	}
}

// SR-001 / SC-006: the owner is the verified peer, never a request field.
func TestForeignAndForgedRegistrations(t *testing.T) {
	h := setup(t)
	req := &schedulerv1.RegisterTaskTypesRequest{TaskTypes: []*schedulerv1.TaskTypeDescriptor{scanDescriptor()}}
	// no verified peer
	if _, err := h.client.RegisterTaskTypes(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no peer: %v", err)
	}
	if _, err := h.client.UnregisterTaskTypes(context.Background(), &schedulerv1.UnregisterTaskTypesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no peer unregister: %v", err)
	}
	// lcm registering ipam's namespace
	_, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/lcm"), req)
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(status.Convert(err).Message(), registry.ReasonForeignNamespace) {
		t.Fatalf("foreign namespace: %v", err)
	}
	// a peer of another trust domain named ipam
	if _, err := h.client.RegisterTaskTypes(as("spiffe://evil.example/svc/ipam"), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign trust domain: %v", err)
	}
	if _, err := h.client.UnregisterTaskTypes(as("spiffe://evil.example/svc/ipam"), &schedulerv1.UnregisterTaskTypesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign trust domain unregister: %v", err)
	}
	// malformed descriptor
	bad := &schedulerv1.RegisterTaskTypesRequest{TaskTypes: []*schedulerv1.TaskTypeDescriptor{{TaskType: "ipam:x", DisplayName: "X", DefaultMaxRetry: 99}}}
	if _, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/ipam"), bad); status.Code(err) != codes.InvalidArgument ||
		!strings.Contains(status.Convert(err).Message(), "invalid_retries: ipam:x") {
		t.Fatalf("invalid descriptor: %v", err)
	}
	// unknown scope enum value
	odd := &schedulerv1.RegisterTaskTypesRequest{TaskTypes: []*schedulerv1.TaskTypeDescriptor{{TaskType: "ipam:x", DisplayName: "X", Scope: 7}}}
	if _, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/ipam"), odd); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown scope: %v", err)
	}
	if _, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/ipam"), &schedulerv1.RegisterTaskTypesRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty request: %v", err)
	}
	// nothing was written by any refused call
	if types, _ := h.st.ListTaskTypes(context.Background(), store.TypeFilter{}); len(types) != 0 {
		t.Fatalf("refused registrations wrote %v", types)
	}
	refused := 0
	h.rec.mu.Lock()
	for _, e := range h.rec.got {
		if e.EventType == audit.AccessRefused {
			refused++
		}
	}
	h.rec.mu.Unlock()
	if refused != 1 {
		t.Fatalf("refusals audited = %d", refused)
	}
}

func TestStoreFailureMapsToUnavailable(t *testing.T) {
	h := setup(t)
	h.st.SetErr(errors.New("db down"))
	_, err := h.client.RegisterTaskTypes(as("spiffe://example.org/svc/ipam"), &schedulerv1.RegisterTaskTypesRequest{TaskTypes: []*schedulerv1.TaskTypeDescriptor{scanDescriptor()}})
	if status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), "db down") {
		t.Fatalf("store failure: %v", err)
	}
	if _, err := h.client.UnregisterTaskTypes(as("spiffe://example.org/svc/ipam"), &schedulerv1.UnregisterTaskTypesRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unregister store failure: %v", err)
	}
}

func TestScopeOf(t *testing.T) {
	if ScopeOf(schedulerv1.TaskScope_TASK_SCOPE_UNSPECIFIED) != registry.ScopeTenant || ScopeOf(schedulerv1.TaskScope_TASK_SCOPE_TENANT) != registry.ScopeTenant ||
		ScopeOf(schedulerv1.TaskScope_TASK_SCOPE_PLATFORM) != registry.ScopePlatform || ScopeOf(9) != "unknown(9)" {
		t.Fatal("scope mapping")
	}
	if status.Code(GRPCError(&registry.Error{Code: registry.CodeDenied, Reason: "x"})) != codes.PermissionDenied {
		t.Fatal("denied mapping")
	}
}
