// Package events publishes scheduler state changes to the shared platform
// event bus (platform:events:<tenant>, research D7): the UI's SSE stream
// follows running executions and refreshes task lists. Payloads carry ids and
// statuses only — never payload values, messages or results (SR-007).
// Events of platform-scoped tasks go to the configured platform tenant's
// stream.
package events

import (
	"context"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream"
)

// Event types published to platform:events:<tenant>.
const (
	ExecutionChanged = "scheduler.execution"
	TaskChanged      = "scheduler.task"
)

// Types lists every event type the module publishes.
var Types = []string{ExecutionChanged, TaskChanged}

// ExecutionPayload is the content-safe body of scheduler.execution.
type ExecutionPayload struct {
	ExecutionID string `json:"execution_id"`
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
}

// TaskPayload is the body of scheduler.task.
type TaskPayload struct {
	TaskID string `json:"task_id"`
}

// Publisher emits a realtime event to all of a tenant's subscribers.
type Publisher interface {
	Publish(ctx context.Context, tenantID, eventType string, payload any)
}

// HubPublisher publishes through the stream hub (nil hub is a no-op).
type HubPublisher struct{ Hub *stream.Hub }

// Publish broadcasts eventType to every subscriber of tenantID; publishing is
// best effort and never fails the caller's operation.
func (p HubPublisher) Publish(ctx context.Context, tenantID, eventType string, payload any) {
	if p.Hub == nil {
		return
	}
	_, _ = p.Hub.PublishID(ctx, tenantID, nil, true, eventType, payload, true)
}

// Emitter maps scheduler rows to events and routes platform-scoped rows to the
// platform tenant's stream. The zero value (nil Pub) is a no-op.
type Emitter struct {
	Pub            Publisher
	PlatformTenant string
}

func (e Emitter) route(tenantID string) string {
	if tenantID == store.PlatformScopeTenant {
		return e.PlatformTenant
	}
	return tenantID
}

// Execution publishes an attempt's state.
func (e Emitter) Execution(ctx context.Context, x store.Execution) {
	if e.Pub == nil || e.route(x.TenantID) == "" {
		return
	}
	e.Pub.Publish(ctx, e.route(x.TenantID), ExecutionChanged,
		ExecutionPayload{ExecutionID: x.ID, TaskID: x.TaskID, Status: x.Status, Attempt: x.Attempt})
}

// Task publishes that a task changed.
func (e Emitter) Task(ctx context.Context, tenantID, taskID string) {
	if e.Pub == nil || e.route(tenantID) == "" {
		return
	}
	e.Pub.Publish(ctx, e.route(tenantID), TaskChanged, TaskPayload{TaskID: taskID})
}

// Recorded is one captured event (Recorder).
type Recorded struct {
	TenantID string
	Type     string
	Payload  any
}

// Recorder is an in-memory Publisher for tests (not safe for concurrent use
// without its lock).
type Recorder struct {
	mu     chan struct{}
	events []Recorded
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{mu: make(chan struct{}, 1)} }

// Publish implements Publisher.
func (r *Recorder) Publish(_ context.Context, tenantID, eventType string, p any) {
	r.mu <- struct{}{}
	r.events = append(r.events, Recorded{TenantID: tenantID, Type: eventType, Payload: p})
	<-r.mu
}

// Events returns a copy of the captured events.
func (r *Recorder) Events() []Recorded {
	r.mu <- struct{}{}
	defer func() { <-r.mu }()
	return append([]Recorded(nil), r.events...)
}

var (
	_ Publisher = HubPublisher{}
	_ Publisher = (*Recorder)(nil)
)
