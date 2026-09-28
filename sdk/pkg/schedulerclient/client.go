// Package schedulerclient registers a module's task types with the scheduler
// over the Freya mesh (feature 026, contracts/grpc-contract.md). The scheduler
// takes the owner from the caller's SPIFFE identity, so a module can only
// register names in its own "<module>:" namespace.
//
// Modules run a Registrar for their whole lifetime: it retries until the
// scheduler accepts the registration, then re-registers periodically so a
// scheduler restart or database reset learns the types again. Modules do not
// unregister on shutdown (restarts must not orphan tasks); Unregister is for
// explicit retirement.
package schedulerclient

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
)

// Service is the discovery name of the scheduler module.
const Service = "scheduler"

// Default cadence of the Registrar.
const (
	DefaultRetry   = 5 * time.Second
	DefaultRefresh = 5 * time.Minute
	callTimeout    = 15 * time.Second
)

// Descriptor describes one task type the module executes.
type Descriptor struct {
	Type            string // "<module>:<action>"
	DisplayName     string
	Description     string
	PayloadSchema   string // JSON Schema of the payload object ("" = any object)
	DefaultCron     string // suggested 5-field cron ("" = none)
	DefaultMaxRetry int32
	Platform        bool // platform-scoped: platform administrators only, runs without a tenant
}

// Proto converts the descriptor to its wire form.
func (d Descriptor) Proto() *schedulerv1.TaskTypeDescriptor {
	scope := schedulerv1.TaskScope_TASK_SCOPE_TENANT
	if d.Platform {
		scope = schedulerv1.TaskScope_TASK_SCOPE_PLATFORM
	}
	return &schedulerv1.TaskTypeDescriptor{
		TaskType: d.Type, DisplayName: d.DisplayName, Description: d.Description,
		PayloadSchema: d.PayloadSchema, DefaultCron: d.DefaultCron, DefaultMaxRetry: d.DefaultMaxRetry, Scope: scope,
	}
}

// ErrNoTypes is returned when there is nothing to register.
var ErrNoTypes = errors.New("schedulerclient: no task types")

// Register declares the descriptors (idempotent).
func Register(ctx context.Context, cc grpc.ClientConnInterface, ds []Descriptor) (*schedulerv1.RegisterTaskTypesResponse, error) {
	if len(ds) == 0 {
		return nil, ErrNoTypes
	}
	req := &schedulerv1.RegisterTaskTypesRequest{}
	for _, d := range ds {
		req.TaskTypes = append(req.TaskTypes, d.Proto())
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return schedulerv1.NewRegistrationClient(cc).RegisterTaskTypes(ctx, req)
}

// Unregister marks every type of the calling module unavailable.
func Unregister(ctx context.Context, cc grpc.ClientConnInterface) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := schedulerv1.NewRegistrationClient(cc).UnregisterTaskTypes(ctx, &schedulerv1.UnregisterTaskTypesRequest{})
	return err
}

// Registrar keeps the module's types registered.
type Registrar struct {
	// Dial returns a mesh connection to the scheduler (freya App.Client(ctx, Service)).
	Dial  func(ctx context.Context) (grpc.ClientConnInterface, error)
	Types []Descriptor
	// Retry is the delay after a failed attempt (default 5 s); Refresh the
	// period of re-registration after success (default 5 min).
	Retry, Refresh time.Duration
	Log            *slog.Logger
	// OnRegistered is called after every accepted registration (tests, metrics).
	OnRegistered func(*schedulerv1.RegisterTaskTypesResponse)
}

func (r *Registrar) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Once performs one registration attempt.
func (r *Registrar) Once(ctx context.Context) error {
	if r.Dial == nil {
		return errors.New("schedulerclient: no dialer")
	}
	cc, err := r.Dial(ctx)
	if err != nil {
		return err
	}
	res, err := Register(ctx, cc, r.Types)
	if err != nil {
		return err
	}
	if r.OnRegistered != nil {
		r.OnRegistered(res)
	}
	return nil
}

// Run registers until ctx ends: every Retry until accepted, then every Refresh.
func (r *Registrar) Run(ctx context.Context) {
	retry, refresh := r.Retry, r.Refresh
	if retry <= 0 {
		retry = DefaultRetry
	}
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	registered := false
	for ctx.Err() == nil {
		wait := retry
		if err := r.Once(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.log().WarnContext(ctx, "scheduler registration failed; retrying", "err", err)
			registered = false
		} else {
			if !registered {
				r.log().InfoContext(ctx, "task types registered with the scheduler", "types", len(r.Types))
			}
			registered = true
			wait = refresh
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
