// Package memstore is an in-memory repo.Store with the same semantics as the
// database implementation (repodb): scopes, case-insensitive unique names,
// fenced completion, SKIP-LOCKED-like planning (a mutex serialises every
// call) and the unique occurrence guard. It backs every unit test.
package memstore

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// MaxMessage bounds messages stored on tasks and attempts.
const MaxMessage = 4 << 10

// DefaultTimeout is the lease base of an attempt whose task is gone.
const DefaultTimeout = 300

// Mem implements repo.Store in memory.
type Mem struct {
	mu    sync.Mutex
	types map[string]store.TaskType
	tasks map[string]store.Task
	execs map[string]store.Execution
	audit []store.AuditRow
	// Err, when set, is returned by every call (failure injection).
	Err error
}

var _ repo.Store = (*Mem)(nil)

// New returns an empty store.
func New() *Mem {
	return &Mem{types: map[string]store.TaskType{}, tasks: map[string]store.Task{}, execs: map[string]store.Execution{}}
}

// SetErr injects err into every subsequent call (nil clears it).
func (m *Mem) SetErr(err error) { m.mu.Lock(); m.Err = err; m.mu.Unlock() }

// Audit returns the recorded audit rows.
func (m *Mem) Audit() []store.AuditRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]store.AuditRow(nil), m.audit...)
}

// Executions returns every attempt (tests), oldest first.
func (m *Mem) Executions() []store.Execution {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]store.Execution, 0, len(m.execs))
	for _, e := range m.execs {
		out = append(out, cloneExec(e))
	}
	sortExecsAsc(out)
	return out
}

// Task returns a task regardless of scope (tests).
func (m *Mem) Task(id string) (store.Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	return cloneTask(t), ok
}

// PutTask stores t as is (tests).
func (m *Mem) PutTask(t store.Task) { m.mu.Lock(); m.tasks[t.ID] = cloneTask(t); m.mu.Unlock() }

// PutExecution stores e as is (tests).
func (m *Mem) PutExecution(e store.Execution) {
	m.mu.Lock()
	m.execs[e.ID] = cloneExec(e)
	m.mu.Unlock()
}

// ------------------------------------------------------------------ helpers

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

func cloneTask(t store.Task) store.Task {
	t.Payload = cloneBytes(t.Payload)
	t.RunAt, t.NextRunAt, t.LastRunAt = cloneTime(t.RunAt), cloneTime(t.NextRunAt), cloneTime(t.LastRunAt)
	return t
}

func cloneExec(e store.Execution) store.Execution {
	e.Result = cloneBytes(e.Result)
	e.StartedAt, e.FinishedAt, e.LeaseUntil = cloneTime(e.StartedAt), cloneTime(e.FinishedAt), cloneTime(e.LeaseUntil)
	return e
}

func cloneType(t store.TaskType) store.TaskType {
	t.PayloadSchema = cloneBytes(t.PayloadSchema)
	t.UnregisteredAt = cloneTime(t.UnregisteredAt)
	return t
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

func sortExecsAsc(out []store.Execution) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
}

func (m *Mem) busy(taskID string) bool {
	for _, e := range m.execs {
		if e.TaskID == taskID && (e.Status == store.ExecQueued || e.Status == store.ExecRunning) {
			return true
		}
	}
	return false
}

func (m *Mem) nameTaken(tenantID, name, exceptID string) bool {
	ln := strings.ToLower(name)
	for _, t := range m.tasks {
		if t.TenantID == tenantID && t.ID != exceptID && strings.ToLower(t.Name) == ln {
			return true
		}
	}
	return false
}

func (m *Mem) occurrenceTaken(e store.Execution) bool {
	if e.Trigger == store.TriggerManual {
		return false
	}
	for _, x := range m.execs {
		if x.TaskID == e.TaskID && x.Trigger != store.TriggerManual && x.Attempt == e.Attempt && x.OccurrenceAt.Equal(e.OccurrenceAt) {
			return true
		}
	}
	return false
}

func (m *Mem) insertExec(e store.Execution, now time.Time) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	m.execs[e.ID] = cloneExec(e)
}

func (m *Mem) cancelQueued(taskID, msg string, now time.Time) int {
	n := 0
	for id, e := range m.execs {
		if e.TaskID == taskID && e.Status == store.ExecQueued {
			e.Status, e.Message, e.Final = store.ExecCancelled, Cut(msg, MaxMessage), true
			e.FinishedAt = cloneTime(&now)
			m.execs[id] = e
			n++
		}
	}
	return n
}

// finalise applies a final attempt to its task's derived fields.
func (m *Mem) finalise(e store.Execution) {
	t, ok := m.tasks[e.TaskID]
	if !ok {
		return
	}
	t.LastRunAt = cloneTime(e.StartedAt)
	if t.LastRunAt == nil {
		t.LastRunAt = cloneTime(e.FinishedAt)
	}
	t.LastStatus, t.LastMessage = e.Status, Cut(e.Message, MaxMessage)
	t.RunCount++
	if t.OneShot() && e.Trigger != store.TriggerManual && t.Status == store.TaskActive {
		t.Status, t.NextRunAt = store.TaskCompleted, nil
	}
	m.tasks[t.ID] = t
}

// ------------------------------------------------------------------ types

// GetTaskType implements repo.Types.
func (m *Mem) GetTaskType(_ context.Context, name string) (store.TaskType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return store.TaskType{}, m.Err
	}
	t, ok := m.types[name]
	if !ok {
		return store.TaskType{}, repo.ErrNotFound
	}
	return cloneType(t), nil
}

// ListTaskTypes implements repo.Types (sorted by name).
func (m *Mem) ListTaskTypes(_ context.Context, f store.TypeFilter) ([]store.TaskType, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := []store.TaskType{}
	for _, t := range m.types {
		if f.Module != "" && t.Module != f.Module {
			continue
		}
		if f.Available != nil && t.Available != *f.Available {
			continue
		}
		out = append(out, cloneType(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// UpsertTaskTypes implements repo.Types.
func (m *Mem) UpsertTaskTypes(_ context.Context, module string, types []store.TaskType) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	for _, t := range types {
		if old, ok := m.types[t.Name]; (ok && old.Module != module) || t.Module != module {
			return repo.ErrConflict
		}
	}
	for _, t := range types {
		t.Available, t.UnregisteredAt = true, nil
		m.types[t.Name] = cloneType(t)
	}
	return nil
}

// MarkModuleUnavailable implements repo.Types.
func (m *Mem) MarkModuleUnavailable(_ context.Context, module string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	names := map[string]bool{}
	for name, t := range m.types {
		if t.Module != module {
			continue
		}
		names[name] = true
		if t.Available {
			t.Available, t.UnregisteredAt = false, cloneTime(&at)
			m.types[name] = t
		}
	}
	for id, t := range m.tasks {
		if names[t.TypeName] {
			t.Validity, t.ValidityMessage = store.ValidityTypeUnavailable, "task type unavailable"
			m.tasks[id] = t
		}
	}
	return len(names), nil
}

// InsertTaskTypeIfAbsent implements repo.Types.
func (m *Mem) InsertTaskTypeIfAbsent(_ context.Context, t store.TaskType) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	if _, ok := m.types[t.Name]; ok {
		return false, nil
	}
	m.types[t.Name] = cloneType(t)
	return true, nil
}

// CountTypesByModule implements repo.Types.
func (m *Mem) CountTypesByModule(_ context.Context) (map[string]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := map[string]int64{}
	for _, t := range m.types {
		if t.Available {
			out[t.Module]++
		}
	}
	return out, nil
}

// UpdateTasksOfType implements repo.Types.
func (m *Mem) UpdateTasksOfType(_ context.Context, typeName string, fn func(t *store.Task) bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	n := 0
	for id, t := range m.tasks {
		if t.TypeName != typeName {
			continue
		}
		n++
		c := cloneTask(t)
		if fn(&c) {
			t.Validity, t.ValidityMessage = c.Validity, c.ValidityMessage
			m.tasks[id] = t
		}
	}
	return n, nil
}

// ------------------------------------------------------------------ tasks

func (m *Mem) countTenant(tenantID string) int {
	n := 0
	for _, t := range m.tasks {
		if t.TenantID == tenantID {
			n++
		}
	}
	return n
}

// CreateTask implements repo.Tasks.
func (m *Mem) CreateTask(_ context.Context, t store.Task, first *store.Execution, maxPerTenant int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if _, ok := m.tasks[t.ID]; ok {
		return repo.ErrConflict
	}
	if maxPerTenant > 0 && m.countTenant(t.TenantID) >= maxPerTenant {
		return repo.ErrLimit
	}
	if m.nameTaken(t.TenantID, t.Name, "") {
		return repo.ErrConflict
	}
	m.tasks[t.ID] = cloneTask(t)
	if first != nil {
		m.insertExec(*first, t.CreatedAt)
	}
	return nil
}

// GetTask implements repo.Tasks.
func (m *Mem) GetTask(_ context.Context, s repo.Scope, id string) (store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return store.Task{}, m.Err
	}
	t, ok := m.tasks[id]
	if !ok || !s.Visible(t.TenantID) {
		return store.Task{}, repo.ErrNotFound
	}
	return cloneTask(t), nil
}

func matchTask(t store.Task, s repo.Scope, f store.TaskFilter) bool {
	if !s.Visible(t.TenantID) {
		return false
	}
	if s.All && f.TenantID != "" && t.TenantID != f.TenantID {
		return false
	}
	if f.Periodic && t.Kind != store.KindPeriodic {
		return false
	}
	if (f.Kind != "" && t.Kind != f.Kind) || (f.Validity != "" && t.Validity != f.Validity) ||
		(f.Type != "" && t.TypeName != f.Type) || (f.State != "" && t.State() != f.State) {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" &&
		!strings.Contains(strings.ToLower(t.Name), q) && !strings.Contains(strings.ToLower(t.TypeName), q) {
		return false
	}
	return true
}

func (m *Mem) filterTasks(s repo.Scope, f store.TaskFilter) []store.Task {
	out := []store.Task{}
	for _, t := range m.tasks {
		if matchTask(t, s, f) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// taskKey is the value of a store.TaskList sort field (same semantics as SQL).
func taskKey(t store.Task, field string) any {
	switch field {
	case "type":
		return t.TypeName
	case "state":
		return t.State()
	case "next_run_at":
		if t.NextRunAt == nil {
			return nil
		}
		return *t.NextRunAt
	case "updated_at":
		return t.UpdatedAt
	default:
		return t.Name
	}
}

// ListTasks implements repo.Tasks (store.TaskList order).
func (m *Mem) ListTasks(_ context.Context, s repo.Scope, f store.TaskFilter) ([]store.Task, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, 0, m.Err
	}
	all := m.filterTasks(s, f)
	req := store.ListRequest(f.List, store.TaskList)
	listquery.SortSlice(all, req, taskKey, func(t store.Task) string { return t.ID })
	page, total, _ := listquery.Window(all, req)
	out := make([]store.Task, 0, len(page))
	for _, t := range page {
		out = append(out, cloneTask(t))
	}
	return out, int64(total), nil
}

// TaskIDs implements repo.Tasks.
func (m *Mem) TaskIDs(_ context.Context, s repo.Scope, f store.TaskFilter) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := []string{}
	for _, t := range m.filterTasks(s, f) {
		out = append(out, t.ID)
	}
	return out, nil
}

// MutateTask implements repo.Tasks.
func (m *Mem) MutateTask(_ context.Context, s repo.Scope, id string, fn repo.TaskMutation) (store.Task, repo.MutateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var res repo.MutateResult
	if m.Err != nil {
		return store.Task{}, res, m.Err
	}
	cur, ok := m.tasks[id]
	if !ok || !s.Visible(cur.TenantID) {
		return store.Task{}, res, repo.ErrNotFound
	}
	t := cloneTask(cur)
	eff, err := fn(&t, m.busy(id))
	if err != nil {
		return store.Task{}, res, err
	}
	t.ID, t.TenantID = cur.ID, cur.TenantID
	now := t.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !eff.Delete && m.nameTaken(t.TenantID, t.Name, t.ID) {
		return store.Task{}, res, repo.ErrConflict
	}
	if eff.CancelQueued {
		res.Cancelled = m.cancelQueued(id, eff.CancelMessage, now)
	}
	if eff.Delete {
		delete(m.tasks, id)
		return cloneTask(t), res, nil
	}
	m.tasks[id] = cloneTask(t)
	if eff.Insert != nil {
		m.insertExec(*eff.Insert, now)
	}
	return cloneTask(t), res, nil
}

// ------------------------------------------------------------------ executions

// GetExecution implements repo.Executions.
func (m *Mem) GetExecution(_ context.Context, s repo.Scope, id string) (store.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return store.Execution{}, m.Err
	}
	e, ok := m.execs[id]
	if !ok || !s.Visible(e.TenantID) {
		return store.Execution{}, repo.ErrNotFound
	}
	return cloneExec(e), nil
}

func matchExecBase(e store.Execution, s repo.Scope, f store.ExecFilter) bool {
	if !s.Visible(e.TenantID) || (f.TaskID != "" && e.TaskID != f.TaskID) || (f.Trigger != "" && e.Trigger != f.Trigger) {
		return false
	}
	if f.From != nil && e.CreatedAt.Before(*f.From) {
		return false
	}
	if f.To != nil && !e.CreatedAt.Before(*f.To) {
		return false
	}
	return true
}

func matchExecStatus(e store.Execution, f store.ExecFilter) bool {
	if f.FailedOnly && !store.Failure(e.Status) {
		return false
	}
	if len(f.Statuses) == 0 {
		return true
	}
	for _, st := range f.Statuses {
		if e.Status == st {
			return true
		}
	}
	return false
}

// ListExecutions implements repo.Executions (store.ExecutionList order, result omitted).
func (m *Mem) ListExecutions(_ context.Context, s repo.Scope, f store.ExecFilter) ([]store.Execution, int64, store.ExecCounts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var counts store.ExecCounts
	if m.Err != nil {
		return nil, 0, counts, m.Err
	}
	all := []store.Execution{}
	for _, e := range m.execs {
		if !matchExecBase(e, s, f) {
			continue
		}
		switch {
		case e.Status == store.ExecSucceeded:
			counts.Succeeded++
		case store.Failure(e.Status):
			counts.Failed++
		default:
			counts.Other++
		}
		if matchExecStatus(e, f) {
			all = append(all, e)
		}
	}
	req := store.ListRequest(f.List, store.ExecutionList)
	listquery.SortSlice(all, req, execKey, func(e store.Execution) string { return e.ID })
	page, total, _ := listquery.Window(all, req)
	out := make([]store.Execution, 0, len(page))
	for _, e := range page {
		e = cloneExec(e)
		e.Result = nil
		out = append(out, e)
	}
	return out, int64(total), counts, nil
}

// execKey is the value of a store.ExecutionList sort field.
func execKey(e store.Execution, field string) any {
	switch field {
	case "status":
		return e.Status
	case "duration":
		return e.DurationMS
	case "trigger":
		return e.Trigger
	default:
		return e.CreatedAt
	}
}

// Overview implements repo.Executions.
func (m *Mem) Overview(_ context.Context, s repo.Scope, now time.Time) (store.Overview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ov := store.Overview{NextDue: []store.NextDue{}, Failing: []store.Failing{}}
	if m.Err != nil {
		return ov, m.Err
	}
	tasks := m.filterTasks(s, store.TaskFilter{})
	for _, t := range tasks {
		switch t.State() {
		case store.StateEnabled:
			ov.Tasks.Enabled++
		case store.StateStopped:
			ov.Tasks.Stopped++
		case store.StateCompleted:
			ov.Tasks.Completed++
		case store.StateCancelled:
			ov.Tasks.Cancelled++
		}
		switch t.Validity {
		case store.ValidityTypeUnavailable:
			ov.Tasks.TypeUnavailable++
		case store.ValidityPayloadInvalid:
			ov.Tasks.PayloadInvalid++
		}
		if t.Enabled && t.Status == store.TaskActive && t.NextRunAt != nil {
			ov.NextDue = append(ov.NextDue, store.NextDue{TaskID: t.ID, Name: t.Name, NextRunAt: *t.NextRunAt})
		}
		if store.Failure(t.LastStatus) {
			ov.Failing = append(ov.Failing, store.Failing{TaskID: t.ID, Name: t.Name, LastStatus: t.LastStatus, LastMessage: t.LastMessage, LastRunAt: cloneTime(t.LastRunAt)})
		}
	}
	sort.SliceStable(ov.NextDue, func(i, j int) bool { return ov.NextDue[i].NextRunAt.Before(ov.NextDue[j].NextRunAt) })
	if len(ov.NextDue) > 10 {
		ov.NextDue = ov.NextDue[:10]
	}
	sort.SliceStable(ov.Failing, func(i, j int) bool {
		a, b := ov.Failing[i].LastRunAt, ov.Failing[j].LastRunAt
		return a != nil && (b == nil || a.After(*b))
	})
	if len(ov.Failing) > 10 {
		ov.Failing = ov.Failing[:10]
	}
	since := now.Add(-24 * time.Hour)
	for _, e := range m.execs {
		if !s.Visible(e.TenantID) || e.FinishedAt == nil || e.FinishedAt.Before(since) {
			continue
		}
		if e.Status == store.ExecSucceeded || store.Failure(e.Status) {
			ov.Runs24h++
			if store.Failure(e.Status) {
				ov.Failures24h++
			}
		}
	}
	return ov, nil
}

// ------------------------------------------------------------------ engine

// PlanDue implements repo.Engine.
func (m *Mem) PlanDue(_ context.Context, now time.Time, limit int, fn repo.PlanFunc) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	due := []store.Task{}
	for _, t := range m.tasks {
		if t.Enabled && t.Status == store.TaskActive && t.NextRunAt != nil && !t.NextRunAt.After(now) {
			due = append(due, t)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].NextRunAt.Equal(*due[j].NextRunAt) {
			return due[i].NextRunAt.Before(*due[j].NextRunAt)
		}
		return due[i].ID < due[j].ID
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	for _, cur := range due {
		t := cloneTask(cur)
		plan, err := fn(&t, m.busy(t.ID))
		if err != nil {
			return 0, err
		}
		t.ID, t.TenantID = cur.ID, cur.TenantID
		m.tasks[t.ID] = cloneTask(t)
		for _, e := range plan.Insert {
			if m.occurrenceTaken(e) {
				continue
			}
			m.insertExec(e, now)
		}
	}
	return len(due), nil
}

// ClaimQueued implements repo.Engine.
func (m *Mem) ClaimQueued(_ context.Context, now time.Time, owner string, limit int, leaseGrace time.Duration) ([]repo.Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	q := []store.Execution{}
	for _, e := range m.execs {
		if e.Status == store.ExecQueued && !e.DueAt.After(now) {
			q = append(q, e)
		}
	}
	sort.Slice(q, func(i, j int) bool {
		if !q[i].DueAt.Equal(q[j].DueAt) {
			return q[i].DueAt.Before(q[j].DueAt)
		}
		return q[i].ID < q[j].ID
	})
	if limit >= 0 && len(q) > limit {
		q = q[:limit]
	}
	out := make([]repo.Claim, 0, len(q))
	for _, e := range q {
		t, ok := m.tasks[e.TaskID]
		timeout := DefaultTimeout
		if ok {
			timeout = t.TimeoutSeconds
		} else {
			t = store.Task{}
		}
		until := now.Add(time.Duration(timeout)*time.Second + leaseGrace)
		e.Status, e.LeaseOwner = store.ExecRunning, owner
		e.StartedAt, e.LeaseUntil = cloneTime(&now), &until
		m.execs[e.ID] = e
		out = append(out, repo.Claim{Exec: cloneExec(e), Task: cloneTask(t)})
	}
	return out, nil
}

// CompleteAttempt implements repo.Engine.
func (m *Mem) CompleteAttempt(_ context.Context, c repo.Completion) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	e, ok := m.execs[c.ExecID]
	if !ok || e.Status != store.ExecRunning || e.LeaseOwner != c.Owner {
		return false, nil
	}
	e.Status, e.Message = c.Status, Cut(c.Message, MaxMessage)
	e.Result, e.ResultTruncated = cloneBytes(c.Result), c.ResultTruncated
	e.FinishedAt, e.DurationMS = cloneTime(&c.FinishedAt), c.DurationMS
	e.LeaseUntil, e.Final = nil, c.Retry == nil
	m.execs[e.ID] = e
	if c.Retry != nil {
		m.insertExec(*c.Retry, c.FinishedAt)
	} else {
		m.finalise(e)
	}
	if c.Validity != "" {
		if t, ok := m.tasks[e.TaskID]; ok {
			t.Validity, t.ValidityMessage = c.Validity, Cut(c.ValidityMessage, MaxMessage)
			m.tasks[t.ID] = t
		}
	}
	return true, nil
}

// LostMessage is recorded on attempts whose scheduler instance lost them.
const LostMessage = "scheduler instance lost the run"

// RecoverExpired implements repo.Engine.
func (m *Mem) RecoverExpired(_ context.Context, now time.Time, limit int, fn repo.Recovery) ([]store.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	exp := []store.Execution{}
	for _, e := range m.execs {
		if e.Status == store.ExecRunning && e.LeaseUntil != nil && e.LeaseUntil.Before(now) {
			exp = append(exp, e)
		}
	}
	sortExecsAsc(exp)
	if limit > 0 && len(exp) > limit {
		exp = exp[:limit]
	}
	out := []store.Execution{}
	for _, e := range exp {
		e.Status, e.Message = store.ExecTimedOut, LostMessage
		e.FinishedAt, e.LeaseUntil = cloneTime(&now), nil
		if e.StartedAt != nil {
			e.DurationMS = now.Sub(*e.StartedAt).Milliseconds()
		}
		retry := fn(cloneExec(e))
		e.Final = retry == nil
		m.execs[e.ID] = e
		if retry != nil {
			m.insertExec(*retry, now)
		} else {
			m.finalise(e)
		}
		out = append(out, cloneExec(e))
	}
	return out, nil
}

// PruneBefore implements repo.Engine.
func (m *Mem) PruneBefore(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	var n int64
	for id, e := range m.execs {
		if e.CreatedAt.Before(t) && e.Finished() {
			delete(m.execs, id)
			n++
		}
	}
	return n, nil
}

// ------------------------------------------------------------------ backup

// BackupTasks implements repo.Backup (oldest first).
func (m *Mem) BackupTasks(_ context.Context, s repo.Scope) ([]store.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := []store.Task{}
	for _, t := range m.tasks {
		if s.Visible(t.TenantID) {
			out = append(out, cloneTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// BackupExecutions implements repo.Backup (oldest first).
func (m *Mem) BackupExecutions(_ context.Context, s repo.Scope) ([]store.Execution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := []store.Execution{}
	for _, e := range m.execs {
		if s.Visible(e.TenantID) {
			out = append(out, cloneExec(e))
		}
	}
	sortExecsAsc(out)
	return out, nil
}

// ImportTask implements repo.Backup.
func (m *Mem) ImportTask(_ context.Context, t store.Task) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	if _, ok := m.tasks[t.ID]; ok {
		return false, nil
	}
	if m.nameTaken(t.TenantID, t.Name, "") {
		return false, repo.ErrConflict
	}
	m.tasks[t.ID] = cloneTask(t)
	return true, nil
}

// ImportExecution implements repo.Backup.
func (m *Mem) ImportExecution(_ context.Context, e store.Execution) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return false, m.Err
	}
	if _, ok := m.execs[e.ID]; ok {
		return false, nil
	}
	m.execs[e.ID] = cloneExec(e)
	return true, nil
}

// AppendAudit implements repo.Store.
func (m *Mem) AppendAudit(_ context.Context, row store.AuditRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.audit = append(m.audit, row)
	return nil
}
