// Package tasks is the scheduler's task service (FR-005–FR-010, FR-019,
// FR-025–FR-027): create, read, edit and delete tasks with validated
// payloads and schedules, control them (start, stop, restart, run now, cancel,
// bulk) and read their history and the overview. Every call is scoped: a user
// sees their own tenant; a platform administrator sees every tenant (the
// system scope is used only after that check) and is the only caller who may
// use platform-scoped task types. Foreign and platform rows answer "not
// found" to everybody else (SR-003). Payload values are never logged, audited
// or published (SR-005, SR-007).
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/cron"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/payload"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Refusal reasons (contracts/scheduler-api.md).
const (
	ReasonInvalidPayload  = payload.ReasonInvalid
	ReasonPayloadTooLarge = payload.ReasonTooLarge
	ReasonPayloadTooDeep  = payload.ReasonTooDeep
	ReasonInvalidCron     = "invalid_cron"
	ReasonCronTooFrequent = "cron_too_frequent"
	ReasonInvalidTimezone = "invalid_timezone"
	ReasonInvalidOptions  = "invalid_options"
	ReasonInvalidRunAt    = "invalid_run_at"
	ReasonTypeUnavailable = "type_unavailable"
	ReasonNameTaken       = "name_taken"
	ReasonNotPeriodic     = "not_periodic"
	ReasonRunInProgress   = "run_in_progress"
	ReasonNotCancellable  = "not_cancellable"
	ReasonValidation      = "validation_failed"
	ReasonTaskLimit       = "conflict"
)

// Defaults and bounds.
const (
	DefaultTimeoutSeconds = 300
	MaxRetries            = 10
	MaxName               = 200
	MaxRemark             = 1000
	PreviewCount          = 10
)

// ErrNotFound is returned for missing, foreign and hidden rows.
var ErrNotFound = repo.ErrNotFound

// Error is a refusal with a stable reason and a value-free detail.
type Error struct {
	Reason string
	Detail map[string]any
}

func (e *Error) Error() string { return "tasks: " + e.Reason }

func refuse(reason string, detail map[string]any) *Error {
	return &Error{Reason: reason, Detail: detail}
}

func field(reason, name, msg string) *Error {
	d := map[string]any{"field": name}
	if msg != "" {
		d["message"] = msg
	}
	return refuse(reason, d)
}

// Limits bound task shapes (config limits_scheduler).
type Limits struct {
	MaxPayloadBytes    int
	MaxTimeoutSeconds  int
	MinIntervalSeconds int
	MaxPageSize        int
	MaxTasksPerTenant  int
}

// Deps wire the service.
type Deps struct {
	Store    repo.Store
	Registry *registry.Registry
	Audit    audit.Recorder
	Events   events.Emitter
	Now      func() time.Time
	Limits   Limits
}

// Service implements the task operations.
type Service struct {
	st     repo.Store
	reg    *registry.Registry
	audit  audit.Recorder
	events events.Emitter
	now    func() time.Time
	lim    Limits
}

// New builds the service with defaults for unset limits.
func New(d Deps) *Service {
	s := &Service{st: d.Store, reg: d.Registry, audit: d.Audit, events: d.Events, now: d.Now, lim: d.Limits}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	if s.reg == nil {
		s.reg = registry.New(registry.Deps{Store: d.Store, Audit: d.Audit, MaxPayloadBytes: d.Limits.MaxPayloadBytes})
	}
	if s.lim.MaxPayloadBytes <= 0 {
		s.lim.MaxPayloadBytes = payload.DefaultMaxBytes
	}
	if s.lim.MaxTimeoutSeconds <= 0 {
		s.lim.MaxTimeoutSeconds = 3600
	}
	if s.lim.MinIntervalSeconds <= 0 {
		s.lim.MinIntervalSeconds = 60
	}
	if s.lim.MaxPageSize <= 0 || s.lim.MaxPageSize > listquery.MaxPageSize {
		s.lim.MaxPageSize = listquery.MaxPageSize
	}
	return s
}

// Now returns the service clock (UTC).
func (s *Service) Now() time.Time { return s.now().UTC() }

// Scope is the rows a caller may see: every tenant for platform
// administrators, the caller's tenant otherwise.
func Scope(subj authz.Subjects) repo.Scope {
	if subj.IsPlatformAdmin() {
		return repo.AllTenants()
	}
	return repo.Tenant(subj.TenantID)
}

// View is a task as the API returns it (contracts: Task).
type View struct {
	store.Task
	TypeDisplayName string `json:"type_display_name"`
	Scope           string `json:"scope"`
	State           string `json:"state"`
	ExecutionID     string `json:"execution_id,omitempty"`
}

func (s *Service) view(t store.Task, types map[string]store.TaskType) View {
	v := View{Task: t, State: t.State(), Scope: store.ScopeTenant}
	if len(v.Payload) == 0 {
		v.Payload = json.RawMessage("{}")
	}
	if tt, ok := types[t.TypeName]; ok {
		v.TypeDisplayName = tt.DisplayName
		v.Scope = tt.Scope
	} else if t.Platform() {
		v.Scope = store.ScopePlatform
	}
	return v
}

func (s *Service) typeMap(ctx context.Context) map[string]store.TaskType {
	out := map[string]store.TaskType{}
	all, err := s.st.ListTaskTypes(ctx, store.TypeFilter{})
	if err != nil {
		return out // display names are cosmetic; the task data is authoritative
	}
	for _, t := range all {
		out[t.Name] = t
	}
	return out
}

func (s *Service) viewOne(ctx context.Context, t store.Task) View {
	return s.view(t, s.typeMap(ctx))
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, typ audit.EventType, tenantID, taskID string, detail map[string]any) {
	audit.Emit(ctx, s.audit, audit.Event{TenantID: tenantID, EventType: typ, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectTask, SubjectID: taskID, Outcome: audit.OutcomeOK, Details: detail})
}

// ------------------------------------------------------------------ validation

// Schedule is a validated schedule.
type Schedule struct {
	Cron     string
	Timezone string
	sched    *cron.Schedule
	loc      *time.Location
}

// Next returns the first occurrence after t.
func (sc Schedule) Next(t time.Time) *time.Time {
	n, ok := sc.sched.Next(t, sc.loc)
	if !ok {
		return nil
	}
	n = n.UTC()
	return &n
}

// ValidateTimezone resolves an IANA name ("" = UTC).
func ValidateTimezone(name string) (string, *time.Location, error) {
	loc, err := cron.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return "", nil, field(ReasonInvalidTimezone, "timezone", "")
	}
	if strings.TrimSpace(name) == "" {
		return "UTC", loc, nil
	}
	return loc.String(), loc, nil
}

// ParseSchedule validates a cron expression in a time zone, enforcing the
// minimum interval between runs (SR-006).
func (s *Service) ParseSchedule(expr, tz string) (Schedule, error) {
	name, loc, err := ValidateTimezone(tz)
	if err != nil {
		return Schedule{}, err
	}
	sched, perr := cron.Parse(strings.TrimSpace(expr))
	if perr != nil {
		var pe *cron.ParseError
		if errors.As(perr, &pe) {
			return Schedule{}, field(ReasonInvalidCron, "cron", pe.Field+": "+pe.Msg)
		}
		return Schedule{}, field(ReasonInvalidCron, "cron", "the expression never fires")
	}
	if gap := sched.MinGap(s.Now(), loc, PreviewCount); gap > 0 && gap < time.Duration(s.lim.MinIntervalSeconds)*time.Second {
		return Schedule{}, refuse(ReasonCronTooFrequent, map[string]any{"field": "cron", "min_interval_seconds": s.lim.MinIntervalSeconds})
	}
	return Schedule{Cron: sched.String(), Timezone: name, sched: sched, loc: loc}, nil
}

// Preview returns the next count run times of expr in tz (1..10).
func (s *Service) Preview(expr, tz string, count int) ([]time.Time, error) {
	if count < 1 || count > PreviewCount {
		count = 5
	}
	sc, err := s.ParseSchedule(expr, tz)
	if err != nil {
		return nil, err
	}
	out := []time.Time{}
	for _, t := range sc.sched.Preview(s.Now(), sc.loc, count) {
		out = append(out, t.UTC())
	}
	return out, nil
}

// validatePayload checks raw against the type's schema and returns its
// canonical form ("{}" when absent).
func (s *Service) validatePayload(tt store.TaskType, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	schema, err := s.reg.Schemas().Compiled(tt)
	if err != nil {
		return nil, refuse(ReasonTypeUnavailable, map[string]any{"type_name": tt.Name})
	}
	if err := payload.Validate(schema, raw, s.lim.MaxPayloadBytes); err != nil {
		var fe *payload.FieldError
		if !errors.As(err, &fe) {
			return nil, field(ReasonInvalidPayload, "", "")
		}
		d := map[string]any{"field": fe.Field, "message": fe.Message}
		switch fe.Reason {
		case payload.ReasonTooLarge:
			d["limit"] = s.lim.MaxPayloadBytes
		case payload.ReasonTooDeep:
			d["limit"] = payload.MaxDepth
		}
		return nil, refuse(fe.Reason, d)
	}
	canon, err := payload.Canonical(raw)
	if err != nil {
		return nil, field(ReasonInvalidPayload, "", "")
	}
	return canon, nil
}

func (s *Service) validateOptions(retries, timeout int) error {
	if retries < 0 || retries > MaxRetries {
		return field(ReasonInvalidOptions, "max_retries", "")
	}
	if timeout < 1 || timeout > s.lim.MaxTimeoutSeconds {
		return refuse(ReasonInvalidOptions, map[string]any{"field": "timeout_seconds", "max": s.lim.MaxTimeoutSeconds})
	}
	return nil
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxName {
		return "", field(ReasonValidation, "name", "")
	}
	return name, nil
}

func validRemark(r string) (string, error) {
	r = strings.TrimSpace(r)
	if utf8.RuneCountInString(r) > MaxRemark {
		return "", field(ReasonValidation, "remark", "")
	}
	return r, nil
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ------------------------------------------------------------------ create

// CreateInput is the create request body.
type CreateInput struct {
	Name           string          `json:"name"`
	TypeName       string          `json:"type_name"`
	Kind           string          `json:"kind"`
	Payload        json.RawMessage `json:"payload"`
	Cron           string          `json:"cron"`
	Timezone       string          `json:"timezone"`
	RunAt          *time.Time      `json:"run_at"`
	DelaySeconds   *int            `json:"delay_seconds"`
	Enabled        *bool           `json:"enabled"`
	Remark         string          `json:"remark"`
	MaxRetries     *int            `json:"max_retries"`
	TimeoutSeconds *int            `json:"timeout_seconds"`
	CatchUp        bool            `json:"catch_up"`
}

// usableType returns the type when the caller may create tasks of it.
func (s *Service) usableType(ctx context.Context, subj authz.Subjects, name string) (store.TaskType, error) {
	tt, err := s.reg.Visible(ctx, subj, name)
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return tt, refuse(ReasonTypeUnavailable, map[string]any{"type_name": name})
	case err != nil:
		return tt, err
	case !tt.Available:
		return tt, refuse(ReasonTypeUnavailable, map[string]any{"type_name": name})
	}
	return tt, nil
}

// oneShotRunAt resolves run_at / delay_seconds (both refused together).
func oneShotRunAt(kind string, runAt *time.Time, delay *int, now time.Time) (*time.Time, error) {
	if runAt != nil && delay != nil && *delay > 0 {
		return nil, field(ReasonInvalidRunAt, "run_at", "run_at and delay_seconds are exclusive")
	}
	at := now
	switch {
	case delay != nil && *delay > 0:
		at = now.Add(time.Duration(*delay) * time.Second)
	case runAt != nil:
		at = runAt.UTC()
	}
	if kind == store.KindWaitResult && at.After(now) {
		return nil, field(ReasonInvalidRunAt, "run_at", "wait_result tasks run immediately")
	}
	return &at, nil
}

// Create validates and stores a task. A wait_result task gets its first
// attempt in the same transaction; View.ExecutionID carries its id.
func (s *Service) Create(ctx context.Context, subj authz.Subjects, in CreateInput) (View, error) {
	now := s.Now()
	name, err := validName(in.Name)
	if err != nil {
		return View{}, err
	}
	remark, err := validRemark(in.Remark)
	if err != nil {
		return View{}, err
	}
	tt, err := s.usableType(ctx, subj, strings.TrimSpace(in.TypeName))
	if err != nil {
		return View{}, err
	}
	switch in.Kind {
	case store.KindPeriodic, store.KindDelayed, store.KindWaitResult:
	default:
		return View{}, field(ReasonValidation, "kind", "")
	}
	body, err := s.validatePayload(tt, in.Payload)
	if err != nil {
		return View{}, err
	}
	t := store.Task{ID: store.NewID(), TenantID: subj.TenantID, Name: name, TypeName: tt.Name, Module: tt.Module, Kind: in.Kind,
		Payload: body, Enabled: in.Enabled == nil || *in.Enabled, Status: store.TaskActive, Validity: store.ValidityOK,
		MaxRetries: tt.DefaultMaxRetries, TimeoutSeconds: DefaultTimeoutSeconds, CatchUp: in.CatchUp, Remark: remark,
		CreatedBy: subj.ActorID(), UpdatedBy: subj.ActorID(), CreatedAt: now, UpdatedAt: now}
	if tt.Platform() {
		t.TenantID = store.PlatformScopeTenant
	}
	if in.MaxRetries != nil {
		t.MaxRetries = *in.MaxRetries
	}
	if in.TimeoutSeconds != nil {
		t.TimeoutSeconds = *in.TimeoutSeconds
	}
	if err := s.validateOptions(t.MaxRetries, t.TimeoutSeconds); err != nil {
		return View{}, err
	}
	var first *store.Execution
	if in.Kind == store.KindPeriodic {
		expr := strings.TrimSpace(in.Cron)
		if expr == "" {
			expr = tt.DefaultCron
		}
		if expr == "" {
			return View{}, field(ReasonInvalidCron, "cron", "required for periodic tasks")
		}
		sc, err := s.ParseSchedule(expr, in.Timezone)
		if err != nil {
			return View{}, err
		}
		t.Cron, t.Timezone = sc.Cron, sc.Timezone
		if t.Enabled {
			t.NextRunAt = sc.Next(now)
		}
	} else {
		if strings.TrimSpace(in.Cron) != "" {
			return View{}, field(ReasonInvalidCron, "cron", "only periodic tasks have a cron expression")
		}
		tz, _, err := ValidateTimezone(in.Timezone)
		if err != nil {
			return View{}, err
		}
		t.Timezone = tz
		runAt, err := oneShotRunAt(in.Kind, in.RunAt, in.DelaySeconds, now)
		if err != nil {
			return View{}, err
		}
		t.RunAt = runAt
		next := later(*runAt, now)
		t.NextRunAt = &next
		if in.Kind == store.KindWaitResult && t.Enabled {
			x := NewAttempt(t, store.TriggerSchedule, subj.ActorID(), now)
			first, t.NextRunAt = &x, nil
		}
	}
	if err := s.st.CreateTask(ctx, t, first, s.lim.MaxTasksPerTenant); err != nil {
		switch {
		case errors.Is(err, repo.ErrConflict):
			return View{}, field(ReasonNameTaken, "name", "")
		case errors.Is(err, repo.ErrLimit):
			return View{}, refuse(ReasonTaskLimit, map[string]any{"limit": s.lim.MaxTasksPerTenant})
		}
		return View{}, err
	}
	s.record(ctx, subj, audit.TaskCreate, t.TenantID, t.ID, map[string]any{"type": t.TypeName, "kind": t.Kind, "enabled": t.Enabled, "platform": t.Platform()})
	s.events.Task(ctx, t.TenantID, t.ID)
	v := s.view(t, map[string]store.TaskType{tt.Name: tt})
	if first != nil {
		v.ExecutionID = first.ID
		s.events.Execution(ctx, *first)
	}
	return v, nil
}

// NewAttempt builds the first (queued) attempt of a new occurrence of t.
func NewAttempt(t store.Task, trigger, by string, at time.Time) store.Execution {
	return store.Execution{ID: store.NewID(), TenantID: t.TenantID, TaskID: t.ID, TaskName: t.Name, OccurrenceID: store.NewID(),
		TypeName: t.TypeName, Module: t.Module, Trigger: trigger, TriggeredBy: by, OccurrenceAt: at, DueAt: at,
		Status: store.ExecQueued, Attempt: 1, MaxAttempts: t.MaxRetries + 1, CreatedAt: at}
}

// ------------------------------------------------------------------ read

// Get returns one visible task.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	t, err := s.st.GetTask(ctx, Scope(subj), id)
	if err != nil {
		return View{}, err
	}
	return s.viewOne(ctx, t), nil
}

// pageRequest completes a list request with the Spec's defaults and caps its
// size at the configured limit (limits_scheduler.max_page_size).
func (s *Service) pageRequest(r listquery.Request, spec listquery.Spec) listquery.Request {
	r = store.ListRequest(r, spec)
	if r.PageSize > s.lim.MaxPageSize {
		r.PageSize = s.lim.MaxPageSize
	}
	return r
}

// List returns one page of visible tasks (store.TaskList order). The page in
// the result is the one actually returned (clamped to the last page).
func (s *Service) List(ctx context.Context, subj authz.Subjects, f store.TaskFilter) (listquery.Page[View], error) {
	f.List = s.pageRequest(f.List, store.TaskList)
	if !subj.IsPlatformAdmin() {
		f.TenantID = ""
	}
	items, total, err := s.st.ListTasks(ctx, Scope(subj), f)
	if err != nil {
		return listquery.Page[View]{}, err
	}
	types := s.typeMap(ctx)
	out := make([]View, 0, len(items))
	for _, t := range items {
		out = append(out, s.view(t, types))
	}
	return listquery.NewPage(out, int(total), f.List.Clamp(int(total))), nil
}

// ------------------------------------------------------------------ update

// OptTime distinguishes an absent JSON field from an explicit null.
type OptTime struct {
	Set  bool
	Time *time.Time
}

// UnmarshalJSON implements json.Unmarshaler (called only when present).
func (o *OptTime) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Time = nil
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(b, &t); err != nil {
		return err
	}
	o.Time = &t
	return nil
}

// UpdateInput is the edit request body; absent fields keep their value. Kind
// and type are immutable.
type UpdateInput struct {
	Name           string          `json:"name"`
	Payload        json.RawMessage `json:"payload"`
	Cron           *string         `json:"cron"`
	Timezone       *string         `json:"timezone"`
	RunAt          OptTime         `json:"run_at"`
	Enabled        *bool           `json:"enabled"`
	Remark         *string         `json:"remark"`
	MaxRetries     *int            `json:"max_retries"`
	TimeoutSeconds *int            `json:"timeout_seconds"`
	CatchUp        *bool           `json:"catch_up"`
}

// unfired reports whether a one-shot task has not been planned yet.
func unfired(t store.Task) bool { return t.Status == store.TaskActive && t.NextRunAt != nil }

// Update edits a task; the change applies from the next occurrence.
func (s *Service) Update(ctx context.Context, subj authz.Subjects, id string, in UpdateInput) (View, error) {
	now := s.Now()
	name, err := validName(in.Name)
	if err != nil {
		return View{}, err
	}
	// The type is read before the task is locked (task edits never change the
	// catalog; a concurrent schema change re-validates the task afterwards).
	cur, err := s.st.GetTask(ctx, Scope(subj), id)
	if err != nil {
		return View{}, err
	}
	tt, terr := s.st.GetTaskType(ctx, cur.TypeName)
	if terr != nil && !errors.Is(terr, repo.ErrNotFound) {
		return View{}, terr
	}
	var fields []any
	var before store.Task
	t, _, err := s.st.MutateTask(ctx, Scope(subj), id, func(t *store.Task, _ bool) (repo.Effects, error) {
		before = *t
		var uerr error
		if fields, uerr = s.applyUpdate(t, tt, terr == nil, name, in, now); uerr != nil {
			return repo.Effects{}, uerr
		}
		t.UpdatedBy, t.UpdatedAt = subj.ActorID(), now
		return repo.Effects{}, nil
	})
	if err != nil {
		if errors.Is(err, repo.ErrConflict) {
			return View{}, field(ReasonNameTaken, "name", "")
		}
		return View{}, err
	}
	s.record(ctx, subj, audit.TaskUpdate, t.TenantID, t.ID, map[string]any{"fields": fields})
	if before.Enabled != t.Enabled {
		typ := audit.TaskDisable
		if t.Enabled {
			typ = audit.TaskEnable
		}
		s.record(ctx, subj, typ, t.TenantID, t.ID, nil)
	}
	s.events.Task(ctx, t.TenantID, t.ID)
	return s.view(t, map[string]store.TaskType{tt.Name: tt}), nil
}

// applyUpdate applies in to t and returns the names of the provided fields;
// the first validation failure aborts.
func (s *Service) applyUpdate(t *store.Task, tt store.TaskType, typeKnown bool, name string, in UpdateInput, now time.Time) ([]any, error) {
	fields := []any{"name"}
	fail := func(err error) ([]any, error) { return nil, err }
	wasUnfired := unfired(*t)
	t.Name = name
	if in.Payload != nil {
		fields = append(fields, "payload")
		if typeKnown {
			body, err := s.validatePayload(tt, in.Payload)
			if err != nil {
				return fail(err)
			}
			t.Payload = body
			if tt.Available {
				t.Validity, t.ValidityMessage = store.ValidityOK, ""
			}
		}
	}
	if in.Remark != nil {
		r, err := validRemark(*in.Remark)
		if err != nil {
			return fail(err)
		}
		t.Remark = r
		fields = append(fields, "remark")
	}
	if in.MaxRetries != nil {
		t.MaxRetries = *in.MaxRetries
		fields = append(fields, "max_retries")
	}
	if in.TimeoutSeconds != nil {
		t.TimeoutSeconds = *in.TimeoutSeconds
		fields = append(fields, "timeout_seconds")
	}
	if err := s.validateOptions(t.MaxRetries, t.TimeoutSeconds); err != nil {
		return fail(err)
	}
	if in.CatchUp != nil {
		t.CatchUp = *in.CatchUp
		fields = append(fields, "catch_up")
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
		fields = append(fields, "enabled")
	}
	if in.Timezone != nil {
		fields = append(fields, "timezone")
	}
	if t.Kind == store.KindPeriodic {
		expr, tz := t.Cron, t.Timezone
		if in.Cron != nil {
			expr = *in.Cron
			fields = append(fields, "cron")
		}
		if in.Timezone != nil {
			tz = *in.Timezone
		}
		if strings.TrimSpace(expr) == "" {
			return fail(field(ReasonInvalidCron, "cron", "required for periodic tasks"))
		}
		sc, err := s.ParseSchedule(expr, tz)
		if err != nil {
			return fail(err)
		}
		t.Cron, t.Timezone, t.NextRunAt = sc.Cron, sc.Timezone, nil
		if t.Enabled {
			t.NextRunAt = sc.Next(now)
		}
		return fields, nil
	}
	if in.Cron != nil && strings.TrimSpace(*in.Cron) != "" {
		return fail(field(ReasonInvalidCron, "cron", "only periodic tasks have a cron expression"))
	}
	if in.Timezone != nil {
		tz, _, err := ValidateTimezone(*in.Timezone)
		if err != nil {
			return fail(err)
		}
		t.Timezone = tz
	}
	if in.RunAt.Set {
		fields = append(fields, "run_at")
		runAt, err := oneShotRunAt(t.Kind, in.RunAt.Time, nil, now)
		if err != nil {
			return fail(err)
		}
		t.RunAt = runAt
	}
	if wasUnfired {
		at := now
		if t.RunAt != nil {
			at = later(*t.RunAt, now)
		}
		t.NextRunAt = &at
	}
	return fields, nil
}

// Delete removes a task; queued attempts are cancelled, a running attempt
// finishes and is recorded.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	t, res, err := s.st.MutateTask(ctx, Scope(subj), id, func(*store.Task, bool) (repo.Effects, error) {
		return repo.Effects{Delete: true, CancelQueued: true, CancelMessage: "task deleted"}, nil
	})
	if err != nil {
		return err
	}
	s.record(ctx, subj, audit.TaskDelete, t.TenantID, t.ID, map[string]any{"type": t.TypeName, "cancelled_attempts": res.Cancelled})
	s.events.Task(ctx, t.TenantID, t.ID)
	return nil
}
