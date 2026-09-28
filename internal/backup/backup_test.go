package backup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

const (
	tenantA  = "11111111-1111-7111-8111-111111111111"
	tenantB  = "22222222-2222-7222-8222-222222222222"
	scanType = "ipam:scan-network"
	sweep    = "ipam:sweep"
)

var (
	ctx   = context.Background()
	now   = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	userA = authz.User(tenantA, "alice", nil)
	userB = authz.User(tenantB, "bob", nil)
	root  = authz.User(tenantA, "root", []string{authz.RolePlatformAdmin})
)

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.got = append(r.got, e)
	return nil
}

func id(n int) string {
	return "0190f7c2-6a3e-7c1a-9b2e-" + strings.Repeat("0", 11) + string(rune('0'+n))
}

func seed(t *testing.T) (*Service, *memstore.Mem, *rec) {
	t.Helper()
	st := memstore.New()
	r := &rec{}
	for _, tt := range []store.TaskType{
		{Name: scanType, Module: "ipam", DisplayName: "Scan", Scope: store.ScopeTenant, Available: true,
			PayloadSchema: json.RawMessage(`{"type":"object","properties":{"all":{"type":"boolean"}}}`), SchemaHash: "h"},
		{Name: sweep, Module: "ipam", DisplayName: "Sweep", Scope: store.ScopePlatform, Available: true},
		{Name: "lcm:other", Module: "lcm", DisplayName: "Other", Scope: store.ScopeTenant, Available: true},
	} {
		if _, err := st.InsertTaskTypeIfAbsent(ctx, tt); err != nil {
			t.Fatal(err)
		}
	}
	next := now.Add(time.Hour)
	st.PutTask(store.Task{ID: id(1), TenantID: tenantA, Name: "scan A", TypeName: scanType, Module: "ipam", Kind: store.KindPeriodic,
		Payload: json.RawMessage(`{"all":true}`), Cron: "0 3 * * *", Timezone: "UTC", Enabled: true, Status: store.TaskActive,
		Validity: store.ValidityOK, MaxRetries: 2, TimeoutSeconds: 300, NextRunAt: &next})
	st.PutTask(store.Task{ID: id(2), TenantID: tenantB, Name: "scan B", TypeName: scanType, Module: "ipam", Kind: store.KindPeriodic,
		Payload: json.RawMessage(`{}`), Cron: "0 3 * * *", Timezone: "UTC", Enabled: true, Status: store.TaskActive, Validity: store.ValidityOK,
		MaxRetries: 2, TimeoutSeconds: 300})
	st.PutTask(store.Task{ID: id(3), TenantID: store.PlatformScopeTenant, Name: "sweep", TypeName: sweep, Module: "ipam", Kind: store.KindDelayed,
		Payload: json.RawMessage(`{}`), Timezone: "UTC", Enabled: true, Status: store.TaskActive, Validity: store.ValidityOK, MaxRetries: 0, TimeoutSeconds: 300})
	st.PutExecution(store.Execution{ID: id(4), TenantID: tenantA, TaskID: id(1), OccurrenceID: id(5), TypeName: scanType, Module: "ipam",
		Trigger: store.TriggerSchedule, Status: store.ExecSucceeded, Attempt: 1, MaxAttempts: 3, Result: []byte(`{"n":1}`), Final: true, CreatedAt: now})
	st.PutExecution(store.Execution{ID: id(6), TenantID: tenantA, TaskID: id(1), OccurrenceID: id(7), TypeName: scanType, Module: "ipam",
		Trigger: store.TriggerSchedule, Status: store.ExecRunning, Attempt: 1, MaxAttempts: 3, LeaseOwner: "x", CreatedAt: now})
	return New(st, r, nil, func() time.Time { return now }, 1<<16), st, r
}

func TestExportScope(t *testing.T) {
	s, _, r := seed(t)
	doc, err := s.Export(ctx, userA, false)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != SchemaVersion || len(doc.Tasks) != 1 || doc.Tasks[0].ID != id(1) || len(doc.Executions) != 2 || len(doc.TaskTypes) != 1 ||
		doc.TaskTypes[0].Name != scanType || !doc.ExportedAt.Equal(now) {
		t.Fatalf("tenant export = %+v", doc)
	}
	if _, err := s.Export(ctx, userA, true); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("all by tenant admin: %v", err)
	}
	all, err := s.Export(ctx, root, true)
	if err != nil || len(all.Tasks) != 3 || len(all.TaskTypes) != 3 {
		t.Fatalf("platform export = %d tasks %d types %v", len(all.Tasks), len(all.TaskTypes), err)
	}
	if e := r.got[len(r.got)-1]; e.EventType != audit.BackupExport || e.Details["tasks"] != 3 {
		t.Fatalf("audit = %+v", e)
	}
	// the system subject exports everything and is audited under the nil tenant
	if _, err := s.Export(ctx, authz.System(), true); err != nil || r.got[len(r.got)-1].TenantID != audit.NilTenant {
		t.Fatalf("system export: %v", err)
	}
}

func roundTrip(t *testing.T, doc Document) Document {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestImportIntoCallerTenantOnly(t *testing.T) {
	s, _, r := seed(t)
	all, _ := s.Export(ctx, root, true)
	doc := roundTrip(t, all)
	// tenant B imports everything: ids exist → skipped; nothing lands outside B
	res, err := s.Import(ctx, userB, doc)
	if err != nil || res.Tasks != 0 || res.Types != 0 {
		t.Fatalf("re-import = %+v %v", res, err)
	}
	// into a fresh store: rows are rewritten to the caller's tenant
	fresh := memstore.New()
	for _, tt := range all.TaskTypes {
		_, _ = fresh.InsertTaskTypeIfAbsent(ctx, tt)
	}
	s2 := New(fresh, r, nil, func() time.Time { return now }, 1<<16)
	res, err = s2.Import(ctx, userB, doc)
	if err != nil {
		t.Fatal(err)
	}
	// tasks A and B → tenant B (one renamed? no: different names); the platform task is skipped for a tenant admin
	if res.Tasks != 2 || res.Types != 0 || res.Executions != 2 || res.Skipped != 3+1 {
		t.Fatalf("import = %+v", res)
	}
	for _, tk := range []string{id(1), id(2)} {
		got, ok := fresh.Task(tk)
		if !ok || got.TenantID != tenantB {
			t.Fatalf("task %s = %+v", tk, got)
		}
	}
	if _, ok := fresh.Task(id(3)); ok {
		t.Fatal("platform task imported by a tenant admin")
	}
	x, _ := fresh.GetExecution(ctx, repo.Tenant(tenantB), id(6))
	if x.Status != store.ExecCancelled || x.LeaseOwner != "" || !x.Final {
		t.Fatalf("running attempt restored as %+v", x)
	}
	if got, _ := fresh.Task(id(1)); got.NextRunAt == nil || got.Validity != store.ValidityOK {
		t.Fatalf("recomputed = %+v", got)
	}
	if e := r.got[len(r.got)-1]; e.EventType != audit.BackupImport || e.TenantID != tenantB {
		t.Fatalf("audit = %+v", e)
	}
}

func TestImportTypesOnlyForPlatformAdmins(t *testing.T) {
	s, _, _ := seed(t)
	doc := Document{Version: SchemaVersion, TaskTypes: []store.TaskType{
		{Name: "forge:evil", Module: "forge", DisplayName: "Evil", Scope: store.ScopeTenant, Available: true, DefaultCron: "0 3 * * *",
			PayloadSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: scanType, Module: "ipam", DisplayName: "Changed", Scope: store.ScopeTenant, Available: true},
		{Name: "forge:null", Module: "forge", DisplayName: "Null schema", Scope: store.ScopeTenant, PayloadSchema: json.RawMessage(`null`)},
	}}
	res, err := s.Import(ctx, userA, doc)
	if err != nil || res.Types != 0 || res.Skipped != 3 {
		t.Fatalf("tenant admin types = %+v %v", res, err)
	}
	res, err = s.Import(ctx, root, doc)
	if err != nil || res.Types != 2 {
		t.Fatalf("admin types = %+v %v", res, err)
	}
	got, _ := s.st.GetTaskType(ctx, "forge:evil")
	if got.Available || got.UnregisteredAt == nil || got.SchemaHash == "" {
		t.Fatalf("imported type must be unavailable: %+v", got)
	}
	if orig, _ := s.st.GetTaskType(ctx, scanType); orig.DisplayName != "Scan" {
		t.Fatal("existing type overwritten")
	}
	if n, _ := s.st.GetTaskType(ctx, "forge:null"); n.PayloadSchema != nil {
		t.Fatal("null schema kept")
	}
}

func TestImportRenamesAndRevalidates(t *testing.T) {
	s, st, _ := seed(t)
	doc := Document{Version: SchemaVersion, Tasks: []store.Task{
		{ID: id(8), TenantID: tenantB, Name: "scan A", TypeName: scanType, Kind: store.KindPeriodic, Cron: "0 3 * * *", Timezone: "UTC",
			Status: store.TaskActive, Enabled: true, MaxRetries: 1, TimeoutSeconds: 60, Payload: json.RawMessage(`{"all":"no"}`)},
		{ID: id(9), TenantID: tenantB, Name: "scan A", TypeName: "gone:type", Kind: store.KindDelayed, Timezone: "UTC",
			Status: store.TaskActive, Enabled: true, MaxRetries: 1, TimeoutSeconds: 60, RunAt: ptr(now.Add(time.Hour))},
		{ID: "0190f7c2-6a3e-7c1a-9b2e-0000000000aa", TenantID: tenantB, Name: "sweep", TypeName: sweep, Kind: store.KindDelayed, Timezone: "UTC",
			Status: store.TaskActive, Enabled: true, TimeoutSeconds: 60},
	}}
	res, err := s.Import(ctx, userA, doc)
	if err != nil || res.Tasks != 2 || res.Renamed != 2 || res.Skipped != 1 {
		t.Fatalf("import = %+v %v", res, err)
	}
	a, _ := st.Task(id(8))
	b, _ := st.Task(id(9))
	if a.Name != "scan A (imported)" || a.Validity != store.ValidityPayloadInvalid || a.TenantID != tenantA {
		t.Fatalf("renamed/revalidated = %+v", a)
	}
	if b.Name != "scan A (imported 2)" || b.Validity != store.ValidityTypeUnavailable || b.NextRunAt == nil || !b.NextRunAt.Equal(now.Add(time.Hour)) ||
		string(b.Payload) != "{}" {
		t.Fatalf("second = %+v", b)
	}
}

func ptr[T any](v T) *T { return &v }

func TestDecodeAndValidation(t *testing.T) {
	for _, raw := range []string{`{`, `{"version":2}`, `{"version":1,"bogus":1}`, `{"version":1} {}`} {
		if _, err := Decode([]byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted", raw)
		}
	}
	maxRows = 3
	defer func() { maxRows = MaxRows }()
	raw, _ := json.Marshal(Document{Version: 1, Tasks: make([]store.Task, 4)})
	if _, err := Decode(raw); !errors.Is(err, ErrInvalid) {
		t.Fatal("row cap")
	}
	s, _, _ := seed(t)
	if _, err := s.Import(ctx, userA, Document{Version: 2}); !errors.Is(err, ErrInvalid) {
		t.Fatal("version")
	}
	good := store.Task{ID: id(1), Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Cron: "0 3 * * *", Timezone: "UTC", Status: store.TaskActive, TimeoutSeconds: 1}
	badTasks := []func(*store.Task){
		func(t *store.Task) { t.ID = "x" },
		func(t *store.Task) { t.Name = " " },
		func(t *store.Task) { t.MaxRetries = 11 },
		func(t *store.Task) { t.TimeoutSeconds = 0 },
		func(t *store.Task) { t.TypeName = "nope" },
		func(t *store.Task) { t.Status = "zombie" },
		func(t *store.Task) { t.Cron = "bad" },
		func(t *store.Task) { t.Kind = store.KindDelayed },
		func(t *store.Task) { t.Kind = "hourly" },
		func(t *store.Task) { t.Timezone = "Mars/Base" },
	}
	for i, mut := range badTasks {
		tk := good
		mut(&tk)
		if _, err := s.Import(ctx, userA, Document{Version: 1, Tasks: []store.Task{tk}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad task %d accepted", i)
		}
	}
	goodType := store.TaskType{Name: "x:y", Module: "x", DisplayName: "Y", Scope: store.ScopeTenant}
	badTypes := []func(*store.TaskType){
		func(t *store.TaskType) { t.Module = "z" },
		func(t *store.TaskType) { t.DisplayName = "" },
		func(t *store.TaskType) { t.DefaultMaxRetries = 11 },
		func(t *store.TaskType) { t.Scope = "galaxy" },
		func(t *store.TaskType) { t.DefaultCron = "bad" },
		func(t *store.TaskType) { t.PayloadSchema = json.RawMessage(`{"type":5}`) },
	}
	for i, mut := range badTypes {
		tt := goodType
		mut(&tt)
		if _, err := s.Import(ctx, root, Document{Version: 1, TaskTypes: []store.TaskType{tt}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad type %d accepted", i)
		}
	}
	goodExec := Execution{ID: id(1), TaskID: id(2), OccurrenceID: id(3), Attempt: 1, MaxAttempts: 1, TypeName: scanType, Trigger: store.TriggerManual, Status: store.ExecFailed}
	badExecs := []func(*Execution){
		func(e *Execution) { e.ID = "x" },
		func(e *Execution) { e.Attempt = 0 },
		func(e *Execution) { e.Trigger = "cosmic" },
		func(e *Execution) { e.Status = "exploded" },
	}
	for i, mut := range badExecs {
		e := goodExec
		mut(&e)
		if _, err := s.Import(ctx, userA, Document{Version: 1, Executions: []Execution{e}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("bad execution %d accepted", i)
		}
	}
}

func TestStoreFailures(t *testing.T) {
	s, st, _ := seed(t)
	doc, _ := s.Export(ctx, root, true)
	st.SetErr(errors.New("db down"))
	if _, err := s.Export(ctx, userA, false); err == nil {
		t.Fatal("export")
	}
	if _, err := s.Import(ctx, root, doc); err == nil {
		t.Fatal("import types")
	}
	if _, err := s.Import(ctx, userA, Document{Version: 1, Tasks: doc.Tasks[:1]}); err == nil {
		t.Fatal("import tasks")
	}
	if _, err := s.Import(ctx, userA, Document{Version: 1, Executions: doc.Executions[:1]}); err == nil {
		t.Fatal("import executions")
	}
	st.SetErr(nil)
	for name, fs := range map[string]*failing{"tasks": {Mem: st, failBackupTasks: true}, "execs": {Mem: st, failBackupExecs: true}} {
		if _, err := New(fs, nil, nil, nil, 0).Export(ctx, userA, false); err == nil {
			t.Errorf("%s failure hidden", name)
		}
	}
	if New(st, nil, nil, nil, 0).now().IsZero() {
		t.Fatal("default clock")
	}
	fs := &failing{Mem: st, failGetType: true}
	if _, err := New(fs, nil, nil, nil, 0).Import(ctx, userA, Document{Version: 1, Tasks: doc.Tasks[:1]}); err == nil {
		t.Fatal("type lookup failure hidden")
	}
}

type failing struct {
	*memstore.Mem
	failBackupTasks, failBackupExecs, failGetType bool
}

func (f *failing) BackupTasks(ctx context.Context, s repo.Scope) ([]store.Task, error) {
	if f.failBackupTasks {
		return nil, errors.New("down")
	}
	return f.Mem.BackupTasks(ctx, s)
}

func (f *failing) BackupExecutions(ctx context.Context, s repo.Scope) ([]store.Execution, error) {
	if f.failBackupExecs {
		return nil, errors.New("down")
	}
	return f.Mem.BackupExecutions(ctx, s)
}

func (f *failing) GetTaskType(ctx context.Context, name string) (store.TaskType, error) {
	if f.failGetType {
		return store.TaskType{}, errors.New("down")
	}
	return f.Mem.GetTaskType(ctx, name)
}

func TestNameClashExhaustion(t *testing.T) {
	st := &allConflict{Mem: memstore.New()}
	s := New(st, nil, nil, func() time.Time { return now }, 0)
	res, err := s.Import(ctx, userA, Document{Version: 1, Tasks: []store.Task{{ID: id(1), Name: strings.Repeat("n", 200), TypeName: scanType,
		Kind: store.KindDelayed, Timezone: "UTC", Status: store.TaskCompleted, TimeoutSeconds: 1}}})
	if err != nil || res.Tasks != 0 || res.Skipped != 1 || st.calls != 20 || len([]rune(st.lastName)) > 200 {
		t.Fatalf("exhaustion = %+v %v calls %d", res, err, st.calls)
	}
}

type allConflict struct {
	*memstore.Mem
	calls    int
	lastName string
}

func (a *allConflict) ImportTask(_ context.Context, t store.Task) (bool, error) {
	a.calls++
	a.lastName = t.Name
	return false, repo.ErrConflict
}
