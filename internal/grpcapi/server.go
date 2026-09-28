// Package grpcapi serves scheduler.v1.Registration on the Freya SPIFFE mTLS
// channel (contracts/grpc-contract.md). The mesh policy admits only the
// modules that execute tasks; the owner of the registered types is the
// verified peer's service name — never a request field — and the peer must
// belong to the scheduler's own trust domain. Nothing here is proxied by the
// gateway.
package grpcapi

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
	"github.com/go-tangra/go-tangra/v4/authn"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
)

// Registrar is what the server needs from the registry.
type Registrar interface {
	Register(ctx context.Context, owner, actorID string, ds []registry.Descriptor) (registry.Result, error)
	Unregister(ctx context.Context, owner, actorID string) (int, error)
}

// Server implements scheduler.v1.Registration.
type Server struct {
	schedulerv1.UnimplementedRegistrationServer
	reg         Registrar
	trustDomain string
}

// New builds the server for the scheduler's trust domain.
func New(reg Registrar, trustDomain string) *Server {
	return &Server{reg: reg, trustDomain: trustDomain}
}

// Register registers the server on gs.
func Register(gs grpc.ServiceRegistrar, s *Server) { schedulerv1.RegisterRegistrationServer(gs, s) }

// Peer returns the verified caller's module name and SPIFFE id.
func (s *Server) Peer(ctx context.Context) (module, id string, err error) {
	p, ok := authn.FromContext(ctx)
	if !ok || p.ID.IsZero() {
		return "", "", status.Error(codes.Unauthenticated, "service identity required")
	}
	if p.ID.TrustDomain() != s.trustDomain {
		return "", "", status.Error(codes.PermissionDenied, "foreign_trust_domain")
	}
	return p.ID.ServiceName(), p.ID.String(), nil
}

// ScopeOf maps the wire scope to the registry's.
func ScopeOf(sc schedulerv1.TaskScope) string {
	switch sc {
	case schedulerv1.TaskScope_TASK_SCOPE_UNSPECIFIED, schedulerv1.TaskScope_TASK_SCOPE_TENANT:
		return registry.ScopeTenant
	case schedulerv1.TaskScope_TASK_SCOPE_PLATFORM:
		return registry.ScopePlatform
	}
	return fmt.Sprintf("unknown(%d)", int32(sc))
}

// RegisterTaskTypes implements schedulerv1.RegistrationServer.
func (s *Server) RegisterTaskTypes(ctx context.Context, req *schedulerv1.RegisterTaskTypesRequest) (*schedulerv1.RegisterTaskTypesResponse, error) {
	module, id, err := s.Peer(ctx)
	if err != nil {
		return nil, err
	}
	ds := make([]registry.Descriptor, 0, len(req.GetTaskTypes()))
	for _, d := range req.GetTaskTypes() {
		ds = append(ds, registry.Descriptor{Type: d.GetTaskType(), DisplayName: d.GetDisplayName(), Description: d.GetDescription(),
			PayloadSchema: d.GetPayloadSchema(), DefaultCron: d.GetDefaultCron(), DefaultMaxRetry: d.GetDefaultMaxRetry(), Scope: ScopeOf(d.GetScope())})
	}
	res, err := s.reg.Register(ctx, module, id, ds)
	if err != nil {
		return nil, GRPCError(err)
	}
	return &schedulerv1.RegisterTaskTypesResponse{RegisteredCount: int32(res.Registered), // #nosec G115 -- at most 50 descriptors
		RevalidatedTasks: int32(res.Revalidated), // #nosec G115 -- bounded by the tasks of 50 types
		Message:          fmt.Sprintf("registered %d task type(s)", res.Registered)}, nil
}

// UnregisterTaskTypes implements schedulerv1.RegistrationServer.
func (s *Server) UnregisterTaskTypes(ctx context.Context, _ *schedulerv1.UnregisterTaskTypesRequest) (*schedulerv1.UnregisterTaskTypesResponse, error) {
	module, id, err := s.Peer(ctx)
	if err != nil {
		return nil, err
	}
	n, err := s.reg.Unregister(ctx, module, id)
	if err != nil {
		return nil, GRPCError(err)
	}
	return &schedulerv1.UnregisterTaskTypesResponse{UnregisteredCount: int32(n), // #nosec G115 -- bounded by the module's types
		Message: fmt.Sprintf("unregistered %d task type(s)", n)}, nil
}

// GRPCError maps a registry error to a status carrying only the stable
// reason (and the offending type name, which the caller sent).
func GRPCError(err error) error {
	var re *registry.Error
	if errors.As(err, &re) {
		msg := re.Reason
		if re.Type != "" {
			msg += ": " + re.Type
		}
		if re.Code == registry.CodeDenied {
			return status.Error(codes.PermissionDenied, msg)
		}
		return status.Error(codes.InvalidArgument, msg)
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}
