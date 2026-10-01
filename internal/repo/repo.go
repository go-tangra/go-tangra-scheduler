// Package repo defines the storage contract of the scheduler. Tenant-facing
// calls take a Scope: one tenant (RLS tenant scope) or, for a platform
// administrator who has already been authorised, every tenant (system scope).
// The engine operations and the type catalog run under the system scope. The
// engine operations are transactional: planning, claiming and completing an
// attempt each happen in one transaction with row locks (research D1).
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Sentinel errors every implementation maps its failures to.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict") // unique name / foreign type owner
	ErrLimit    = errors.New("limit reached")
)

// Scope selects the rows a tenant-facing call may see.
type Scope struct {
	TenantID string // the caller's tenant (All=false)
	All      bool   // every tenant: platform administrators only, after the check
}

// Tenant is the scope of one tenant.
func Tenant(id string) Scope { return Scope{TenantID: id} }

// AllTenants is the platform-administrator scope.
func AllTenants() Scope { return Scope{All: true} }

// Visible reports whether a row of tenantID is inside the scope.
func (s Scope) Visible(tenantID string) bool {
	return s.All || (s.TenantID != "" && s.TenantID == tenantID)
}

// Effects are the side effects a task mutation asks for, applied in the same
// transaction as the task write.
type Effects struct {
	Delete        bool             // remove the task
	CancelQueued  bool             // queued attempts of the task become cancelled
	CancelMessage string           // message recorded on the cancelled attempts
	Insert        *store.Execution // an attempt to insert (run now)
}

// MutateResult reports what the side effects did.
type MutateResult struct {
	Cancelled int
}

// TaskMutation edits t in place. busy reports whether the task has a queued or
// running attempt. Returning an error aborts without writing.
type TaskMutation func(t *store.Task, busy bool) (Effects, error)

// Plan is the planning decision for one due task: the task is written back as
// mutated and Insert is added (duplicates of a scheduled occurrence are
// ignored — the unique occurrence index is the second guard).
type Plan struct {
	Insert []store.Execution
}

// PlanFunc decides one due task; t is mutated in place.
type PlanFunc func(t *store.Task, busy bool) (Plan, error)

// Claim is a claimed attempt with the task it belongs to (Task.ID is empty
// when the task has been deleted).
type Claim struct {
	Exec store.Execution
	Task store.Task
}

// Completion finishes a running attempt. It is fenced: it applies only while
// the attempt is still running under Owner.
type Completion struct {
	ExecID          string
	Owner           string
	Status          string // succeeded | failed | timed_out
	Message         string
	Result          []byte
	ResultTruncated bool
	FinishedAt      time.Time
	DurationMS      int64
	Retry           *store.Execution // the next attempt; nil makes this attempt final
	Validity        string           // when set, the task's validity is updated (payload re-validation)
	ValidityMessage string
}

// Recovery decides what happens to an attempt whose lease expired: the
// returned attempt (or nil) is inserted as its retry.
type Recovery func(e store.Execution) *store.Execution

// ImportResult counts what an import inserted.
type ImportResult struct {
	Tasks, Executions, Types int
}

// Types is the task-type catalog (platform data, system scope).
type Types interface {
	GetTaskType(ctx context.Context, name string) (store.TaskType, error)
	ListTaskTypes(ctx context.Context, f store.TypeFilter) ([]store.TaskType, error)
	// UpsertTaskTypes inserts or updates the types of one module (available,
	// registered_at). ErrConflict (nothing written) when a name is owned by
	// another module.
	UpsertTaskTypes(ctx context.Context, module string, types []store.TaskType) error
	// MarkModuleUnavailable marks every type of module unavailable and flags
	// the tasks of those types type_unavailable; it returns the type count.
	MarkModuleUnavailable(ctx context.Context, module string, at time.Time) (int, error)
	// InsertTaskTypeIfAbsent inserts t only when no type of that name exists.
	InsertTaskTypeIfAbsent(ctx context.Context, t store.TaskType) (bool, error)
	// CountTypesByModule counts the available types of each module.
	CountTypesByModule(ctx context.Context) (map[string]int64, error)
	// UpdateTasksOfType applies fn to every task of the type (all tenants) and
	// writes back the validity of those fn reports changed; it returns the
	// number of tasks visited.
	UpdateTasksOfType(ctx context.Context, typeName string, fn func(t *store.Task) bool) (int, error)
}

// Tasks is the task surface.
type Tasks interface {
	// CreateTask inserts t (and first, the first attempt of a wait_result task,
	// in the same transaction). ErrConflict on a duplicate name in the tenant,
	// ErrLimit when the tenant already holds maxPerTenant tasks (0 = no limit).
	CreateTask(ctx context.Context, t store.Task, first *store.Execution, maxPerTenant int) error
	GetTask(ctx context.Context, s Scope, id string) (store.Task, error)
	// ListTasks returns one page (f.List, store.TaskList order; a page beyond
	// the end answers the last page) and the total of the filter.
	ListTasks(ctx context.Context, s Scope, f store.TaskFilter) ([]store.Task, int64, error)
	// MutateTask locks the task, applies fn and writes the task and its
	// effects in one transaction. ErrConflict on a duplicate name.
	MutateTask(ctx context.Context, s Scope, id string, fn TaskMutation) (store.Task, MutateResult, error)
	// TaskIDs lists the ids of the tasks matching f in the scope (no paging).
	TaskIDs(ctx context.Context, s Scope, f store.TaskFilter) ([]string, error)
}

// Executions is the history surface.
type Executions interface {
	GetExecution(ctx context.Context, s Scope, id string) (store.Execution, error)
	// ListExecutions returns one page (f.List, store.ExecutionList order; result omitted), the total
	// and the summary counts of the filter without its status filters.
	ListExecutions(ctx context.Context, s Scope, f store.ExecFilter) ([]store.Execution, int64, store.ExecCounts, error)
	Overview(ctx context.Context, s Scope, now time.Time) (store.Overview, error)
}

// Engine is the scheduling-engine surface (system scope, row locks).
type Engine interface {
	// PlanDue locks up to limit due tasks (SKIP LOCKED), lets fn decide each
	// and writes the task and its planned attempts in the same transaction.
	PlanDue(ctx context.Context, now time.Time, limit int, fn PlanFunc) (int, error)
	// ClaimQueued moves up to limit due queued attempts to running under owner
	// with lease_until = now + task timeout + leaseGrace.
	ClaimQueued(ctx context.Context, now time.Time, owner string, limit int, leaseGrace time.Duration) ([]Claim, error)
	// CompleteAttempt finishes a running attempt (fenced by id, status and
	// owner) and, when final, updates the task's last-run fields, run count and
	// one-shot completion. false when the fence did not match.
	CompleteAttempt(ctx context.Context, c Completion) (bool, error)
	// RecoverExpired times out running attempts whose lease passed.
	RecoverExpired(ctx context.Context, now time.Time, limit int, fn Recovery) ([]store.Execution, error)
	// PruneBefore deletes finished attempts created before t.
	PruneBefore(ctx context.Context, t time.Time) (int64, error)
}

// Backup is the export/import surface.
type Backup interface {
	BackupTasks(ctx context.Context, s Scope) ([]store.Task, error)
	BackupExecutions(ctx context.Context, s Scope) ([]store.Execution, error)
	// ImportTask inserts t unless its id exists (false); ErrConflict on a
	// duplicate name in the tenant.
	ImportTask(ctx context.Context, t store.Task) (bool, error)
	// ImportExecution inserts e unless its id exists.
	ImportExecution(ctx context.Context, e store.Execution) (bool, error)
}

// Store is the full scheduler persistence contract.
type Store interface {
	Types
	Tasks
	Executions
	Engine
	Backup
	// AppendAudit persists an audit row (system scope).
	AppendAudit(ctx context.Context, row store.AuditRow) error
}
