// Package store holds the scheduler domain types, the embedded migrations and
// the pgx pool with tenant-scoped transactions (data-model.md).
package store

import (
	"encoding/json"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"
)

// PlatformScopeTenant is the tenant of platform-scoped tasks (the nil uuid,
// never a real tenant). Their execution requests carry an empty tenant.
const PlatformScopeTenant = "00000000-0000-0000-0000-000000000000"

// Task kinds.
const (
	KindPeriodic   = "periodic"
	KindDelayed    = "delayed"
	KindWaitResult = "wait_result"
)

// Task statuses (one-shot end states).
const (
	TaskActive    = "active"
	TaskCompleted = "completed"
	TaskCancelled = "cancelled"
)

// Task states (derived, API only).
const (
	StateEnabled   = "enabled"
	StateStopped   = "stopped"
	StateCompleted = "completed"
	StateCancelled = "cancelled"
)

// Task validity.
const (
	ValidityOK              = "ok"
	ValidityTypeUnavailable = "type_unavailable"
	ValidityPayloadInvalid  = "payload_invalid"
)

// Type scopes.
const (
	ScopeTenant   = "tenant"
	ScopePlatform = "platform"
)

// Execution statuses.
const (
	ExecQueued    = "queued"
	ExecRunning   = "running"
	ExecSucceeded = "succeeded"
	ExecFailed    = "failed"
	ExecSkipped   = "skipped"
	ExecTimedOut  = "timed_out"
	ExecCancelled = "cancelled"
)

// ExecStatuses lists every execution status.
var ExecStatuses = []string{ExecQueued, ExecRunning, ExecSucceeded, ExecFailed, ExecSkipped, ExecTimedOut, ExecCancelled}

// Triggers.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerCatchUp  = "catch_up"
)

// TaskType is one registered, executable unit of work.
type TaskType struct {
	Name              string          `json:"name"`
	Module            string          `json:"module"`
	DisplayName       string          `json:"display_name"`
	Description       string          `json:"description"`
	PayloadSchema     json.RawMessage `json:"payload_schema"` // nil = any JSON object
	DefaultCron       string          `json:"default_cron"`
	DefaultMaxRetries int             `json:"default_max_retries"`
	Scope             string          `json:"scope"`
	Available         bool            `json:"available"`
	RegisteredAt      time.Time       `json:"registered_at"`
	UnregisteredAt    *time.Time      `json:"unregistered_at"`
	SchemaHash        string          `json:"schema_hash,omitempty"`
}

// Platform reports whether the type is platform-scoped.
func (t TaskType) Platform() bool { return t.Scope == ScopePlatform }

// Task is a scheduled use of a task type.
type Task struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	Name            string          `json:"name"`
	TypeName        string          `json:"type_name"`
	Module          string          `json:"module"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
	Cron            string          `json:"cron"`
	Timezone        string          `json:"timezone"`
	RunAt           *time.Time      `json:"run_at"`
	Enabled         bool            `json:"enabled"`
	Status          string          `json:"status"`
	Validity        string          `json:"validity"`
	ValidityMessage string          `json:"validity_message"`
	MaxRetries      int             `json:"max_retries"`
	TimeoutSeconds  int             `json:"timeout_seconds"`
	CatchUp         bool            `json:"catch_up"`
	Remark          string          `json:"remark"`
	NextRunAt       *time.Time      `json:"next_run_at"`
	LastRunAt       *time.Time      `json:"last_run_at"`
	LastStatus      string          `json:"last_status"`
	LastMessage     string          `json:"last_message"`
	RunCount        int64           `json:"run_count"`
	MissedCount     int64           `json:"missed_count"`
	CreatedBy       string          `json:"created_by"`
	UpdatedBy       string          `json:"updated_by"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// OneShot reports whether the task runs once (delayed or wait_result).
func (t Task) OneShot() bool { return t.Kind != KindPeriodic }

// Platform reports whether the task belongs to the platform scope.
func (t Task) Platform() bool { return t.TenantID == PlatformScopeTenant }

// State derives the API state.
func (t Task) State() string {
	switch {
	case t.Status == TaskCompleted:
		return StateCompleted
	case t.Status == TaskCancelled:
		return StateCancelled
	case !t.Enabled:
		return StateStopped
	}
	return StateEnabled
}

// Execution is one attempt of one occurrence of a task.
type Execution struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	TaskID          string     `json:"task_id"`
	TaskName        string     `json:"task_name"`
	OccurrenceID    string     `json:"occurrence_id"`
	TypeName        string     `json:"type_name"`
	Module          string     `json:"module"`
	Trigger         string     `json:"trigger"`
	TriggeredBy     string     `json:"triggered_by"`
	OccurrenceAt    time.Time  `json:"scheduled_at"`
	DueAt           time.Time  `json:"due_at"`
	Status          string     `json:"status"`
	Attempt         int        `json:"attempt"`
	MaxAttempts     int        `json:"max_attempts"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DurationMS      int64      `json:"duration_ms"`
	Message         string     `json:"message"`
	Result          []byte     `json:"-"`
	ResultTruncated bool       `json:"result_truncated"`
	Final           bool       `json:"-"`
	LeaseOwner      string     `json:"-"`
	LeaseUntil      *time.Time `json:"-"`
	CreatedAt       time.Time  `json:"created_at"`
}

// Finished reports whether the attempt reached an end state.
func (e Execution) Finished() bool {
	return e.Status != ExecQueued && e.Status != ExecRunning
}

// Failure reports whether the status counts as a failure (history counts).
func Failure(status string) bool { return status == ExecFailed || status == ExecTimedOut }

// TypeFilter selects task types.
type TypeFilter struct {
	Module    string
	Available *bool
}

// TaskFilter selects tasks (the scope is a separate argument).
type TaskFilter struct {
	Kind     string
	State    string
	Validity string
	Type     string
	Query    string
	TenantID string // platform-admin (all-tenant) scope only
	Periodic bool   // only periodic tasks (bulk control)
	// List is the page and order (TaskList); TaskIDs ignores it.
	List listquery.Request
}

// ExecFilter selects executions.
type ExecFilter struct {
	TaskID     string
	Statuses   []string
	FailedOnly bool
	Trigger    string
	From, To   *time.Time
	// List is the page and order (ExecutionList).
	List listquery.Request
}

// ExecCounts are the history summary counts of a filter (status filters aside).
type ExecCounts struct {
	Succeeded int64 `json:"succeeded"`
	Failed    int64 `json:"failed"`
	Other     int64 `json:"other"`
}

// Overview is the US6 figures of a scope.
type Overview struct {
	Tasks       OverviewCounts `json:"tasks"`
	Runs24h     int64          `json:"runs_24h"`
	Failures24h int64          `json:"failures_24h"`
	NextDue     []NextDue      `json:"next_due"`
	Failing     []Failing      `json:"failing"`
}

// OverviewCounts are task counts by state and validity.
type OverviewCounts struct {
	Enabled         int64 `json:"enabled"`
	Stopped         int64 `json:"stopped"`
	Completed       int64 `json:"completed"`
	Cancelled       int64 `json:"cancelled"`
	TypeUnavailable int64 `json:"type_unavailable"`
	PayloadInvalid  int64 `json:"payload_invalid"`
}

// NextDue is one upcoming run.
type NextDue struct {
	TaskID    string    `json:"task_id"`
	Name      string    `json:"name"`
	NextRunAt time.Time `json:"next_run_at"`
}

// Failing is a task whose last run failed.
type Failing struct {
	TaskID      string     `json:"task_id"`
	Name        string     `json:"name"`
	LastStatus  string     `json:"last_status"`
	LastMessage string     `json:"last_message"`
	LastRunAt   *time.Time `json:"last_run_at"`
}

// AuditRow is an append-only audit event.
type AuditRow struct {
	ID          string
	TenantID    string
	At          time.Time
	ActorKind   string
	ActorID     string
	Action      string
	SubjectKind string
	SubjectID   string
	Outcome     string
	Reason      string
	Detail      map[string]any
}
