// Package backup exports and imports scheduler data (FR-024, research D10).
//
// Export: the caller's tenant — tasks, their execution history and the task
// types they reference — or, for a platform administrator asking for all,
// every tenant and the whole type catalog. The document is versioned JSON and
// carries no secrets (payloads must never hold any).
//
// Import restores into the CALLER's tenant only: every task and attempt is
// rewritten to that tenant, existing ids are skipped and clashing names are
// made unique. Rows of the platform scope are restored only for platform
// administrators; tasks of platform-scoped types are never restored into a
// tenant. Task types are restored only for platform administrators, only when
// absent, and always as unavailable — a module must register again to make
// them runnable, so a backup can never forge a module's type. Queued or
// running attempts are restored as cancelled; restored tasks are re-validated
// and get their next run recomputed.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/cron"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/payload"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// SchemaVersion is the document version this service reads and writes.
const SchemaVersion = 1

// MaxRows bounds any single collection of an import.
const MaxRows = 200000

// maxRows is MaxRows (lowered by tests).
var maxRows = MaxRows

// ErrInvalid is returned for a malformed or unsupported document.
var ErrInvalid = errors.New("backup: invalid backup document")

// Execution is an attempt as stored in a backup (all fields, result bytes
// base64-encoded by encoding/json).
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
	OccurrenceAt    time.Time  `json:"occurrence_at"`
	DueAt           time.Time  `json:"due_at"`
	Status          string     `json:"status"`
	Attempt         int        `json:"attempt"`
	MaxAttempts     int        `json:"max_attempts"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DurationMS      int64      `json:"duration_ms"`
	Message         string     `json:"message"`
	Result          []byte     `json:"result"`
	ResultTruncated bool       `json:"result_truncated"`
	Final           bool       `json:"final"`
	CreatedAt       time.Time  `json:"created_at"`
}

func fromStore(e store.Execution) Execution {
	return Execution{ID: e.ID, TenantID: e.TenantID, TaskID: e.TaskID, TaskName: e.TaskName, OccurrenceID: e.OccurrenceID,
		TypeName: e.TypeName, Module: e.Module, Trigger: e.Trigger, TriggeredBy: e.TriggeredBy, OccurrenceAt: e.OccurrenceAt,
		DueAt: e.DueAt, Status: e.Status, Attempt: e.Attempt, MaxAttempts: e.MaxAttempts, StartedAt: e.StartedAt,
		FinishedAt: e.FinishedAt, DurationMS: e.DurationMS, Message: e.Message, Result: e.Result, ResultTruncated: e.ResultTruncated,
		Final: e.Final, CreatedAt: e.CreatedAt}
}

func (e Execution) toStore() store.Execution {
	return store.Execution{ID: e.ID, TenantID: e.TenantID, TaskID: e.TaskID, TaskName: e.TaskName, OccurrenceID: e.OccurrenceID,
		TypeName: e.TypeName, Module: e.Module, Trigger: e.Trigger, TriggeredBy: e.TriggeredBy, OccurrenceAt: e.OccurrenceAt,
		DueAt: e.DueAt, Status: e.Status, Attempt: e.Attempt, MaxAttempts: e.MaxAttempts, StartedAt: e.StartedAt,
		FinishedAt: e.FinishedAt, DurationMS: e.DurationMS, Message: e.Message, Result: e.Result, ResultTruncated: e.ResultTruncated,
		Final: e.Final, CreatedAt: e.CreatedAt}
}

// Document is the backup format.
type Document struct {
	Version    int              `json:"version"`
	ExportedAt time.Time        `json:"exported_at"`
	TaskTypes  []store.TaskType `json:"task_types"`
	Tasks      []store.Task     `json:"tasks"`
	Executions []Execution      `json:"executions"`
}

// Result counts an import.
type Result struct {
	Tasks      int `json:"tasks"`
	Executions int `json:"executions"`
	Types      int `json:"types"`
	Skipped    int `json:"skipped"`
	Renamed    int `json:"renamed"`
}

// Service exports and imports.
type Service struct {
	st         repo.Store
	audit      audit.Recorder
	schemas    *registry.Schemas
	now        func() time.Time
	maxPayload int
}

// New builds the service.
func New(st repo.Store, rec audit.Recorder, schemas *registry.Schemas, now func() time.Time, maxPayload int) *Service {
	if schemas == nil {
		schemas = registry.NewSchemas()
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{st: st, audit: rec, schemas: schemas, now: now, maxPayload: maxPayload}
}

func (s *Service) record(ctx context.Context, subj authz.Subjects, typ audit.EventType, detail map[string]any) {
	tenant := subj.TenantID
	if tenant == "" {
		tenant = audit.NilTenant
	}
	audit.Emit(ctx, s.audit, audit.Event{TenantID: tenant, EventType: typ, ActorKind: audit.ActorOf(subj.ActorKind), ActorID: subj.ActorID(),
		SubjectKind: audit.SubjectBackup, Outcome: audit.OutcomeOK, Details: detail})
}

// Export builds the document of the caller's tenant, or of every tenant when
// all is set (platform administrators only).
func (s *Service) Export(ctx context.Context, subj authz.Subjects, all bool) (Document, error) {
	scope := repo.Tenant(subj.TenantID)
	if all {
		if err := authz.RequirePlatformAdmin(subj); err != nil {
			return Document{}, err
		}
		scope = repo.AllTenants()
	}
	tasks, err := s.st.BackupTasks(ctx, scope)
	if err != nil {
		return Document{}, err
	}
	execs, err := s.st.BackupExecutions(ctx, scope)
	if err != nil {
		return Document{}, err
	}
	types, err := s.st.ListTaskTypes(ctx, store.TypeFilter{})
	if err != nil {
		return Document{}, err
	}
	used := map[string]bool{}
	for _, t := range tasks {
		used[t.TypeName] = true
	}
	doc := Document{Version: SchemaVersion, ExportedAt: s.now(), Tasks: tasks, TaskTypes: []store.TaskType{}, Executions: make([]Execution, 0, len(execs))}
	for _, t := range types {
		if all || used[t.Name] {
			doc.TaskTypes = append(doc.TaskTypes, t)
		}
	}
	for _, e := range execs {
		doc.Executions = append(doc.Executions, fromStore(e))
	}
	s.record(ctx, subj, audit.BackupExport, map[string]any{"tasks": len(doc.Tasks), "executions": len(doc.Executions), "types": len(doc.TaskTypes), "all": all})
	return doc, nil
}

// Decode parses a document strictly (unknown fields and trailing data refused).
func Decode(raw []byte) (Document, error) {
	var doc Document
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Document{}, fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	if doc.Version != SchemaVersion {
		return Document{}, fmt.Errorf("%w: version %d", ErrInvalid, doc.Version)
	}
	if len(doc.TaskTypes) > maxRows || len(doc.Tasks) > maxRows || len(doc.Executions) > maxRows {
		return Document{}, fmt.Errorf("%w: too many rows", ErrInvalid)
	}
	return doc, nil
}

var (
	uuidRE     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	typeNameRE = regexp.MustCompile(`^([a-z0-9-]{1,63}):[a-z][a-z0-9-]{0,62}$`)
)

// validType checks a type row of the document.
func validType(t store.TaskType) bool {
	m := typeNameRE.FindStringSubmatch(t.Name)
	if m == nil || m[1] != t.Module || t.DisplayName == "" || utf8.RuneCountInString(t.DisplayName) > registry.MaxDisplayName ||
		utf8.RuneCountInString(t.Description) > registry.MaxDescription || t.DefaultMaxRetries < 0 || t.DefaultMaxRetries > registry.MaxRetries ||
		(t.Scope != store.ScopeTenant && t.Scope != store.ScopePlatform) || len(t.PayloadSchema) > payload.MaxSchemaBytes {
		return false
	}
	if t.DefaultCron != "" {
		if _, err := cron.Parse(t.DefaultCron); err != nil {
			return false
		}
	}
	if len(t.PayloadSchema) > 0 && string(t.PayloadSchema) != "null" {
		if _, err := payload.Compile(t.PayloadSchema); err != nil {
			return false
		}
	}
	return true
}

func validTask(t store.Task) bool {
	if !uuidRE.MatchString(t.ID) || strings.TrimSpace(t.Name) == "" || utf8.RuneCountInString(t.Name) > 200 ||
		utf8.RuneCountInString(t.Remark) > 1000 || t.MaxRetries < 0 || t.MaxRetries > registry.MaxRetries || t.TimeoutSeconds < 1 ||
		!typeNameRE.MatchString(t.TypeName) {
		return false
	}
	switch t.Status {
	case store.TaskActive, store.TaskCompleted, store.TaskCancelled:
	default:
		return false
	}
	switch t.Kind {
	case store.KindPeriodic:
		if _, err := cron.Parse(t.Cron); err != nil {
			return false
		}
	case store.KindDelayed, store.KindWaitResult:
		if t.Cron != "" {
			return false
		}
	default:
		return false
	}
	if _, err := cron.LoadLocation(t.Timezone); err != nil {
		return false
	}
	return true
}

func validExec(e Execution) bool {
	if !uuidRE.MatchString(e.ID) || !uuidRE.MatchString(e.TaskID) || !uuidRE.MatchString(e.OccurrenceID) || e.Attempt < 1 ||
		e.MaxAttempts < 1 || !typeNameRE.MatchString(e.TypeName) {
		return false
	}
	switch e.Trigger {
	case store.TriggerSchedule, store.TriggerManual, store.TriggerCatchUp:
	default:
		return false
	}
	for _, st := range store.ExecStatuses {
		if e.Status == st {
			return true
		}
	}
	return false
}

// Import restores doc into the caller's tenant.
func (s *Service) Import(ctx context.Context, subj authz.Subjects, doc Document) (Result, error) {
	if doc.Version != SchemaVersion {
		return Result{}, ErrInvalid
	}
	for _, t := range doc.TaskTypes {
		if !validType(t) {
			return Result{}, fmt.Errorf("%w: task type %q", ErrInvalid, t.Name)
		}
	}
	for _, t := range doc.Tasks {
		if !validTask(t) {
			return Result{}, fmt.Errorf("%w: task %q", ErrInvalid, t.ID)
		}
	}
	for _, e := range doc.Executions {
		if !validExec(e) {
			return Result{}, fmt.Errorf("%w: execution %q", ErrInvalid, e.ID)
		}
	}
	admin := subj.IsPlatformAdmin()
	now := s.now()
	var res Result
	if admin {
		for _, t := range doc.TaskTypes {
			t.Available, t.UnregisteredAt, t.RegisteredAt = false, &now, now
			if len(t.PayloadSchema) == 0 || string(t.PayloadSchema) == "null" {
				t.PayloadSchema = nil
			}
			t.SchemaHash = payload.Hash(t.PayloadSchema)
			ok, err := s.st.InsertTaskTypeIfAbsent(ctx, t)
			if err != nil {
				return res, err
			}
			if ok {
				res.Types++
			}
		}
	} else {
		res.Skipped += len(doc.TaskTypes)
	}
	target := func(tenant string) (string, bool) {
		if tenant == store.PlatformScopeTenant {
			return store.PlatformScopeTenant, admin
		}
		return subj.TenantID, subj.TenantID != ""
	}
	for _, t := range doc.Tasks {
		tenant, ok := target(t.TenantID)
		if !ok {
			res.Skipped++
			continue
		}
		t.TenantID = tenant
		imported, renamed, err := s.importTask(ctx, t, now)
		if err != nil {
			return res, err
		}
		switch {
		case imported:
			res.Tasks++
			if renamed {
				res.Renamed++
			}
		default:
			res.Skipped++
		}
	}
	for _, e := range doc.Executions {
		tenant, ok := target(e.TenantID)
		if !ok {
			res.Skipped++
			continue
		}
		x := e.toStore()
		x.TenantID = tenant
		x.LeaseOwner, x.LeaseUntil = "", nil
		if !x.Finished() {
			x.Status, x.Message, x.Final = store.ExecCancelled, "restored from backup", true
			x.FinishedAt = &now
		}
		ok, err := s.st.ImportExecution(ctx, x)
		if err != nil {
			return res, err
		}
		if ok {
			res.Executions++
		} else {
			res.Skipped++
		}
	}
	s.record(ctx, subj, audit.BackupImport, map[string]any{"tasks": res.Tasks, "executions": res.Executions, "types": res.Types, "skipped": res.Skipped})
	return res, nil
}

// importTask re-validates t against the current catalog, recomputes its next
// run and inserts it, renaming on a name clash. A task of a platform-scoped
// type is never restored into a tenant.
func (s *Service) importTask(ctx context.Context, t store.Task, now time.Time) (bool, bool, error) {
	t.Validity, t.ValidityMessage = store.ValidityTypeUnavailable, registry.UnavailableReason
	tt, err := s.st.GetTaskType(ctx, t.TypeName)
	switch {
	case errors.Is(err, repo.ErrNotFound):
	case err != nil:
		return false, false, err
	case tt.Platform() != (t.TenantID == store.PlatformScopeTenant):
		return false, false, nil // scope mismatch: never escalate or demote a task
	case tt.Available:
		t.Validity, t.ValidityMessage = store.ValidityOK, ""
		schema, cerr := s.schemas.Compiled(tt)
		if cerr == nil {
			cerr = payload.Validate(schema, t.Payload, s.maxPayload)
		}
		if cerr != nil {
			t.Validity, t.ValidityMessage = store.ValidityPayloadInvalid, cerr.Error()
		}
	}
	if len(t.Payload) == 0 || !json.Valid(t.Payload) {
		t.Payload = json.RawMessage("{}")
	}
	t.NextRunAt = nil
	if t.Status == store.TaskActive && t.Enabled {
		if t.Kind == store.KindPeriodic {
			sched, _ := cron.Parse(t.Cron)          // validated by validTask
			loc, _ := cron.LoadLocation(t.Timezone) // validated by validTask
			if next, ok := sched.Next(now, loc); ok {
				n := next.UTC()
				t.NextRunAt = &n
			}
		} else if t.LastRunAt == nil {
			at := now
			if t.RunAt != nil && t.RunAt.After(now) {
				at = *t.RunAt
			}
			t.NextRunAt = &at
		}
	}
	base := t.Name
	for i := 0; i < 20; i++ {
		if i > 0 {
			suffix := " (imported)"
			if i > 1 {
				suffix = fmt.Sprintf(" (imported %d)", i)
			}
			t.Name = cut(base, 200-len(suffix)) + suffix
		}
		ok, err := s.st.ImportTask(ctx, t)
		if errors.Is(err, repo.ErrConflict) {
			continue
		}
		return ok, i > 0 && ok, err
	}
	return false, false, nil
}

func cut(s string, runes int) string {
	r := []rune(s)
	if len(r) <= runes {
		return s
	}
	return string(r[:runes])
}
