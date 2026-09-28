package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

const (
	tenantA  = "11111111-1111-7111-8111-111111111111"
	tenantB  = "22222222-2222-7222-8222-222222222222"
	scanType = "ipam:scan-network"
	sweep    = "ipam:platform-sweep"
	mailType = "notification:send-test-email"
	schema   = `{"type":"object","properties":{"subnetId":{"type":"string"},"all":{"type":"boolean"},"depth":{"type":"integer","minimum":1,"maximum":5}},"additionalProperties":false}`
	mailSch  = `{"type":"object","required":["recipient"],"properties":{"recipient":{"type":"string","format":"email"}}}`
)

var (
	ctx   = context.Background()
	start = time.Date(2026, 9, 28, 10, 0, 30, 0, time.UTC)
	userA = authz.User(tenantA, "alice", nil)
	userB = authz.User(tenantB, "bob", nil)
	root  = authz.User(tenantA, "root", []string{authz.RolePlatformAdmin})
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func (c *clock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func ptr[T any](v T) *T              { return &v }
func raw(s string) json.RawMessage   { return json.RawMessage(s) }
func reason(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}
func detail(err error) map[string]any {
	var e *Error
	if errors.As(err, &e) {
		return e.Detail
	}
	return nil
}

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

func (r *rec) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for _, e := range r.got {
		out = append(out, string(e.EventType))
	}
	return out
}

func (r *rec) last() audit.Event { r.mu.Lock(); defer r.mu.Unlock(); return r.got[len(r.got)-1] }

type fixture struct {
	svc *Service
	st  *memstore.Mem
	reg *registry.Registry
	rec *rec
	pub *events.Recorder
	clk *clock
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{st: memstore.New(), rec: &rec{}, pub: events.NewRecorder(), clk: &clock{t: start}}
	f.reg = registry.New(registry.Deps{Store: f.st, Audit: f.rec, Now: f.clk.Now, MaxPayloadBytes: 1024})
	f.svc = New(Deps{Store: f.st, Registry: f.reg, Audit: f.rec, Events: events.Emitter{Pub: f.pub, PlatformTenant: tenantA}, Now: f.clk.Now,
		Limits: Limits{MaxPayloadBytes: 1024, MaxTimeoutSeconds: 3600, MinIntervalSeconds: 60, MaxPageSize: 100, MaxTasksPerTenant: 50}})
	if _, err := f.reg.Register(ctx, "ipam", "spiffe://example.org/svc/ipam", []registry.Descriptor{
		{Type: scanType, DisplayName: "Scan network", PayloadSchema: schema, DefaultCron: "0 3 * * *", DefaultMaxRetry: 2},
		{Type: sweep, DisplayName: "Platform sweep", Scope: registry.ScopePlatform, DefaultMaxRetry: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.Register(ctx, "notification", "spiffe://example.org/svc/notification", []registry.Descriptor{
		{Type: mailType, DisplayName: "Send test email", PayloadSchema: mailSch, DefaultMaxRetry: 1},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) periodic(t *testing.T, subj authz.Subjects, name, payload string) View {
	t.Helper()
	v, err := f.svc.Create(ctx, subj, CreateInput{Name: name, TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(payload)})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return v
}

func TestCreatePeriodicUsesTypeDefaults(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "Nightly scan A", `{"subnetId":"a", "all":false}`)
	if v.TenantID != tenantA || v.Module != "ipam" || v.Cron != "0 3 * * *" || v.Timezone != "UTC" || v.MaxRetries != 2 ||
		v.TimeoutSeconds != DefaultTimeoutSeconds || !v.Enabled || v.State != store.StateEnabled || v.Validity != store.ValidityOK ||
		v.TypeDisplayName != "Scan network" || v.Scope != store.ScopeTenant || v.CreatedBy != "alice" || v.ExecutionID != "" {
		t.Fatalf("view = %+v", v)
	}
	if string(v.Payload) != `{"all":false,"subnetId":"a"}` {
		t.Fatalf("canonical payload = %s", v.Payload)
	}
	want := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	if v.NextRunAt == nil || !v.NextRunAt.Equal(want) {
		t.Fatalf("next run = %v", v.NextRunAt)
	}
	// several tasks of the same type with different payloads coexist (FR-009)
	w, err := f.svc.Create(ctx, userA, CreateInput{Name: "Hourly scan B", TypeName: scanType, Kind: store.KindPeriodic,
		Payload: raw(`{"subnetId":"b"}`), Cron: "0 * * * *", Timezone: "Europe/Sofia", MaxRetries: ptr(0), TimeoutSeconds: ptr(60),
		Remark: " note ", CatchUp: true, Enabled: ptr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if w.Timezone != "Europe/Sofia" || w.MaxRetries != 0 || w.TimeoutSeconds != 60 || w.Remark != "note" || !w.CatchUp ||
		w.State != store.StateStopped || w.NextRunAt != nil {
		t.Fatalf("custom = %+v", w)
	}
	if e := f.rec.last(); e.EventType != audit.TaskCreate || e.Details["type"] != scanType || e.Details["platform"] != false {
		t.Fatalf("audit = %+v", e)
	}
	for _, e := range f.rec.got {
		for k := range e.Details {
			if strings.Contains(k, "payload") {
				t.Fatal("payload in audit")
			}
		}
	}
	if evs := f.pub.Events(); len(evs) != 2 || evs[0].Type != events.TaskChanged || evs[0].TenantID != tenantA {
		t.Fatalf("events = %+v", evs)
	}
}

func TestCreateValidationReasons(t *testing.T) {
	f := newFixture(t)
	big := `{"subnetId":"` + strings.Repeat("x", 2000) + `"}`
	deep := strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40)
	cases := []struct {
		name   string
		in     CreateInput
		reason string
		field  string
	}{
		{"blank name", CreateInput{Name: " ", TypeName: scanType, Kind: store.KindPeriodic}, ReasonValidation, "name"},
		{"long remark", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Remark: strings.Repeat("r", 1001)}, ReasonValidation, "remark"},
		{"unknown type", CreateInput{Name: "x", TypeName: "ipam:nope", Kind: store.KindPeriodic}, ReasonTypeUnavailable, ""},
		{"bad kind", CreateInput{Name: "x", TypeName: scanType, Kind: "hourly"}, ReasonValidation, "kind"},
		{"wrong type in payload", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(`{"subnetId":5}`)}, ReasonInvalidPayload, "/subnetId"},
		{"out of range", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(`{"depth":9}`)}, ReasonInvalidPayload, "/depth"},
		{"unknown field", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(`{"evil":1}`)}, ReasonInvalidPayload, ""},
		{"missing required", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{}`)}, ReasonInvalidPayload, ""},
		{"bad email", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"nope"}`)}, ReasonInvalidPayload, "/recipient"},
		{"non-object", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(`[1]`)}, ReasonInvalidPayload, ""},
		{"too large", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(big)}, ReasonPayloadTooLarge, ""},
		{"too deep", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(deep)}, ReasonPayloadTooDeep, ""},
		{"retries", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, MaxRetries: ptr(11)}, ReasonInvalidOptions, "max_retries"},
		{"timeout", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, TimeoutSeconds: ptr(3601)}, ReasonInvalidOptions, "timeout_seconds"},
		{"bad cron", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Cron: "61 * * * *"}, ReasonInvalidCron, "cron"},
		{"never fires", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Cron: "0 0 30 2 *"}, ReasonInvalidCron, "cron"},
		{"too frequent", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Cron: "* * * * *"}, "", ""},
		{"no cron", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindPeriodic, Payload: raw(`{"recipient":"a@b.example"}`)}, ReasonInvalidCron, "cron"},
		{"cron on one-shot", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`), Cron: "0 3 * * *"}, ReasonInvalidCron, "cron"},
		{"bad tz", CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Timezone: "Mars/Olympus"}, ReasonInvalidTimezone, "timezone"},
		{"bad tz one-shot", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`), Timezone: "Local"}, ReasonInvalidTimezone, "timezone"},
		{"run_at and delay", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`), RunAt: ptr(start), DelaySeconds: ptr(5)}, ReasonInvalidRunAt, "run_at"},
		{"future wait_result", CreateInput{Name: "x", TypeName: mailType, Kind: store.KindWaitResult, Payload: raw(`{"recipient":"a@b.example"}`), DelaySeconds: ptr(60)}, ReasonInvalidRunAt, "run_at"},
		{"platform type for tenant admin", CreateInput{Name: "x", TypeName: sweep, Kind: store.KindDelayed}, ReasonTypeUnavailable, ""},
	}
	for _, c := range cases {
		_, err := f.svc.Create(ctx, userA, c.in)
		if c.name == "too frequent" {
			// every minute is exactly the 60 s minimum: accepted
			if err != nil {
				t.Errorf("every minute refused: %v", err)
			}
			continue
		}
		if reason(err) != c.reason {
			t.Errorf("%s: reason %q (%v)", c.name, reason(err), err)
			continue
		}
		if c.field != "" && detail(err)["field"] != c.field {
			t.Errorf("%s: detail %v", c.name, detail(err))
		}
	}
	// the refusal never echoes the payload value
	_, err := f.svc.Create(ctx, userA, CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"SECRET-MARKER"}`)})
	if raw, _ := json.Marshal(detail(err)); strings.Contains(string(raw), "SECRET-MARKER") {
		t.Fatalf("payload value echoed: %s", raw)
	}
	if d := detail(func() error {
		_, e := f.svc.Create(ctx, userA, CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Payload: raw(big)})
		return e
	}()); d["limit"] != 1024 {
		t.Fatalf("too large detail = %v", d)
	}
	if d := detail(func() error {
		_, e := f.svc.Create(ctx, userA, CreateInput{Name: "x", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(deep)})
		return e
	}()); d["limit"] != 32 {
		t.Fatalf("too deep detail = %v", d)
	}
	if (&Error{Reason: "x"}).Error() != "tasks: x" {
		t.Fatal("error text")
	}
}

func TestMinimumIntervalAndUnavailableType(t *testing.T) {
	f := newFixture(t)
	f.svc.lim.MinIntervalSeconds = 3600
	_, err := f.svc.Create(ctx, userA, CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic, Cron: "*/5 * * * *"})
	if reason(err) != ReasonCronTooFrequent || detail(err)["min_interval_seconds"] != 3600 {
		t.Fatalf("too frequent: %v", err)
	}
	if _, err := f.reg.Unregister(ctx, "ipam", "spiffe://example.org/svc/ipam"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, userA, CreateInput{Name: "x", TypeName: scanType, Kind: store.KindPeriodic}); reason(err) != ReasonTypeUnavailable {
		t.Fatalf("unavailable type: %v", err)
	}
}

func TestNameUniquenessAndLimit(t *testing.T) {
	f := newFixture(t)
	f.periodic(t, userA, "Scan", `{}`)
	_, err := f.svc.Create(ctx, userA, CreateInput{Name: "SCAN", TypeName: scanType, Kind: store.KindPeriodic})
	if reason(err) != ReasonNameTaken || detail(err)["field"] != "name" {
		t.Fatalf("duplicate name: %v", err)
	}
	// the same name in another tenant is fine
	f.periodic(t, userB, "Scan", `{}`)
	f.svc.lim.MaxTasksPerTenant = 1
	_, err = f.svc.Create(ctx, userA, CreateInput{Name: "Other", TypeName: scanType, Kind: store.KindPeriodic})
	if reason(err) != ReasonTaskLimit || detail(err)["limit"] != 1 {
		t.Fatalf("limit: %v", err)
	}
	f.st.SetErr(errors.New("db down"))
	if _, err := f.svc.Create(ctx, userA, CreateInput{Name: "Z", TypeName: scanType, Kind: store.KindPeriodic}); err == nil || reason(err) != "" {
		t.Fatalf("store failure: %v", err)
	}
}

func TestOneShotCreate(t *testing.T) {
	f := newFixture(t)
	mail := raw(`{"recipient":"ops@example.org"}`)
	// delay
	v, err := f.svc.Create(ctx, userA, CreateInput{Name: "in 10 min", TypeName: mailType, Kind: store.KindDelayed, Payload: mail, DelaySeconds: ptr(600)})
	if err != nil || v.NextRunAt == nil || !v.NextRunAt.Equal(start.Add(10*time.Minute)) || v.Cron != "" || v.MaxRetries != 1 {
		t.Fatalf("delay: %+v %v", v, err)
	}
	// absolute time
	at := time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)
	v, err = f.svc.Create(ctx, userA, CreateInput{Name: "at 22", TypeName: mailType, Kind: store.KindDelayed, Payload: mail, RunAt: &at})
	if err != nil || !v.NextRunAt.Equal(at) || !v.RunAt.Equal(at) {
		t.Fatalf("absolute: %+v %v", v, err)
	}
	// past / empty / zero delay mean now
	past := start.Add(-time.Hour)
	for i, in := range []CreateInput{{RunAt: &past}, {}, {DelaySeconds: ptr(0)}} {
		in.Name, in.TypeName, in.Kind, in.Payload = "now "+string(rune('a'+i)), mailType, store.KindDelayed, mail
		v, err = f.svc.Create(ctx, userA, in)
		if err != nil || !v.NextRunAt.Equal(start) {
			t.Fatalf("now %d: %+v %v", i, v.NextRunAt, err)
		}
	}
	// wait_result: first attempt inserted with the task, id returned at once
	w, err := f.svc.Create(ctx, userA, CreateInput{Name: "follow", TypeName: mailType, Kind: store.KindWaitResult, Payload: mail})
	if err != nil || w.ExecutionID == "" || w.NextRunAt != nil {
		t.Fatalf("wait_result: %+v %v", w, err)
	}
	x, err := f.st.GetExecution(ctx, repo.Tenant(tenantA), w.ExecutionID)
	if err != nil || x.Status != store.ExecQueued || x.Attempt != 1 || x.MaxAttempts != 2 || x.Trigger != store.TriggerSchedule || x.TriggeredBy != "alice" {
		t.Fatalf("first attempt = %+v %v", x, err)
	}
	// disabled wait_result: no attempt yet, planned once enabled
	d, err := f.svc.Create(ctx, userA, CreateInput{Name: "later", TypeName: mailType, Kind: store.KindWaitResult, Payload: mail, Enabled: ptr(false)})
	if err != nil || d.ExecutionID != "" || d.NextRunAt == nil {
		t.Fatalf("disabled wait_result: %+v %v", d, err)
	}
}

func TestPlatformScope(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Create(ctx, root, CreateInput{Name: "sweep", TypeName: sweep, Kind: store.KindDelayed})
	if err != nil || p.TenantID != store.PlatformScopeTenant || p.Scope != store.ScopePlatform {
		t.Fatalf("platform task: %+v %v", p, err)
	}
	if e := f.rec.last(); e.TenantID != store.PlatformScopeTenant || e.Details["platform"] != true {
		t.Fatalf("audit = %+v", e)
	}
	a := f.periodic(t, userA, "A", `{}`)
	b := f.periodic(t, userB, "B", `{}`)
	// tenant users never see other tenants or platform rows
	for _, id := range []string{p.ID, b.ID} {
		if _, err := f.svc.Get(ctx, userA, id); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("user A sees %s: %v", id, err)
		}
		if _, err := f.svc.Update(ctx, userA, id, UpdateInput{Name: "x"}); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("user A edits %s: %v", id, err)
		}
		if err := f.svc.Delete(ctx, userA, id); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("user A deletes %s: %v", id, err)
		}
		if _, err := f.svc.Run(ctx, userA, id); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("user A runs %s: %v", id, err)
		}
	}
	items, total, err := f.svc.List(ctx, userA, store.TaskFilter{TenantID: tenantB})
	if err != nil || total != 1 || items[0].ID != a.ID {
		t.Fatalf("user A list = %v %d %v", items, total, err)
	}
	// platform admins see every tenant and may narrow to one
	if _, total, _ := f.svc.List(ctx, root, store.TaskFilter{}); total != 3 {
		t.Fatalf("admin list total = %d", total)
	}
	if items, _, _ := f.svc.List(ctx, root, store.TaskFilter{TenantID: tenantB}); len(items) != 1 || items[0].ID != b.ID {
		t.Fatalf("admin narrowed = %v", items)
	}
	if v, err := f.svc.Get(ctx, root, b.ID); err != nil || v.ID != b.ID {
		t.Fatalf("admin get: %v", err)
	}
	if v, err := f.svc.Get(ctx, root, p.ID); err != nil || v.Scope != store.ScopePlatform {
		t.Fatalf("admin get platform: %v", err)
	}
}

func TestListFiltersAndTypeNames(t *testing.T) {
	f := newFixture(t)
	f.periodic(t, userA, "alpha", `{}`)
	f.periodic(t, userA, "beta", `{}`)
	if _, err := f.svc.Create(ctx, userA, CreateInput{Name: "gamma", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`)}); err != nil {
		t.Fatal(err)
	}
	items, total, err := f.svc.List(ctx, userA, store.TaskFilter{Kind: store.KindPeriodic, Page: 2, PageSize: 1})
	if err != nil || total != 2 || len(items) != 1 || items[0].Name != "beta" || items[0].TypeDisplayName != "Scan network" {
		t.Fatalf("page 2 = %+v %d %v", items, total, err)
	}
	if items, _, _ := f.svc.List(ctx, userA, store.TaskFilter{Query: "GAM"}); len(items) != 1 || items[0].Name != "gamma" {
		t.Fatalf("query = %+v", items)
	}
	f.st.SetErr(errors.New("db down"))
	if _, _, err := f.svc.List(ctx, userA, store.TaskFilter{}); err == nil {
		t.Fatal("list failure hidden")
	}
	if _, err := f.svc.Get(ctx, userA, "x"); err == nil {
		t.Fatal("get failure hidden")
	}
	// type names are cosmetic: a failing catalog still renders tasks
	f.st.SetErr(nil)
	st := &typesDown{Mem: f.st}
	f.svc.st = st
	if items, _, err := f.svc.List(ctx, userA, store.TaskFilter{}); err != nil || items[0].TypeDisplayName != "" {
		t.Fatalf("catalog down: %v", err)
	}
}

type typesDown struct{ *memstore.Mem }

func (typesDown) ListTaskTypes(context.Context, store.TypeFilter) ([]store.TaskType, error) {
	return nil, errors.New("down")
}

func TestUpdate(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "scan", `{"subnetId":"a"}`)
	f.clk.Add(time.Hour)
	u, err := f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "scan hourly", Cron: ptr("30 * * * *"), Timezone: ptr("Europe/Sofia"),
		Payload: raw(`{"all":true}`), Remark: ptr("r"), MaxRetries: ptr(4), TimeoutSeconds: ptr(30), CatchUp: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "scan hourly" || u.Cron != "30 * * * *" || u.Timezone != "Europe/Sofia" || string(u.Payload) != `{"all":true}` ||
		u.MaxRetries != 4 || u.TimeoutSeconds != 30 || !u.CatchUp || u.Remark != "r" || u.UpdatedBy != "alice" {
		t.Fatalf("updated = %+v", u)
	}
	if want := time.Date(2026, 9, 28, 11, 30, 0, 0, time.UTC); !u.NextRunAt.Equal(want) {
		t.Fatalf("next run recomputed = %v", u.NextRunAt)
	}
	fields := f.rec.last().Details["fields"].([]any)
	if len(fields) != 8 {
		t.Fatalf("audited fields = %v", fields)
	}
	// disable then enable: audited, next run cleared and recomputed
	u, err = f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "scan hourly", Enabled: ptr(false)})
	if err != nil || u.NextRunAt != nil || u.State != store.StateStopped {
		t.Fatalf("disable: %+v %v", u, err)
	}
	if f.rec.last().EventType != audit.TaskDisable {
		t.Fatal("disable not audited")
	}
	if u, err = f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "scan hourly", Enabled: ptr(true)}); err != nil || u.NextRunAt == nil {
		t.Fatalf("enable: %v", err)
	}
	if f.rec.last().EventType != audit.TaskEnable {
		t.Fatal("enable not audited")
	}
	// refusals
	bad := []struct {
		in     UpdateInput
		reason string
	}{
		{UpdateInput{Name: ""}, ReasonValidation},
		{UpdateInput{Name: "x", Payload: raw(`{"subnetId":1}`)}, ReasonInvalidPayload},
		{UpdateInput{Name: "x", Remark: ptr(strings.Repeat("r", 1001))}, ReasonValidation},
		{UpdateInput{Name: "x", MaxRetries: ptr(-1)}, ReasonInvalidOptions},
		{UpdateInput{Name: "x", Cron: ptr("bad")}, ReasonInvalidCron},
		{UpdateInput{Name: "x", Cron: ptr(" ")}, ReasonInvalidCron},
		{UpdateInput{Name: "x", Timezone: ptr("Nowhere/Land")}, ReasonInvalidTimezone},
	}
	for i, c := range bad {
		if _, err := f.svc.Update(ctx, userA, v.ID, c.in); reason(err) != c.reason {
			t.Errorf("case %d: %v", i, err)
		}
	}
	f.periodic(t, userA, "other", `{}`)
	if _, err := f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "OTHER"}); reason(err) != ReasonNameTaken {
		t.Fatalf("rename clash: %v", err)
	}
}

func TestUpdateOneShotAndValidity(t *testing.T) {
	f := newFixture(t)
	mail := raw(`{"recipient":"a@b.example"}`)
	v, err := f.svc.Create(ctx, userA, CreateInput{Name: "later", TypeName: mailType, Kind: store.KindDelayed, Payload: mail, DelaySeconds: ptr(600)})
	if err != nil {
		t.Fatal(err)
	}
	at := start.Add(2 * time.Hour)
	u, err := f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "later", RunAt: OptTime{Set: true, Time: &at}, Timezone: ptr("Europe/Sofia")})
	if err != nil || !u.NextRunAt.Equal(at) || u.Timezone != "Europe/Sofia" {
		t.Fatalf("reschedule: %+v %v", u, err)
	}
	if u, err = f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "later", RunAt: OptTime{Set: true}}); err != nil || !u.NextRunAt.Equal(start) {
		t.Fatalf("run_at null = now: %+v %v", u.NextRunAt, err)
	}
	for i, in := range []UpdateInput{
		{Name: "later", Cron: ptr("0 3 * * *")},
		{Name: "later", Timezone: ptr("Local")},
	} {
		if _, err := f.svc.Update(ctx, userA, v.ID, in); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	// a fired one-shot (planned, next_run_at cleared) is not re-armed by an edit
	tk, _ := f.st.Task(v.ID)
	tk.NextRunAt = nil
	f.st.PutTask(tk)
	if u, err = f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "renamed"}); err != nil || u.NextRunAt != nil {
		t.Fatalf("fired one-shot re-armed: %+v %v", u.NextRunAt, err)
	}
	// fixing the payload clears payload_invalid
	tk, _ = f.st.Task(v.ID)
	tk.Validity, tk.ValidityMessage = store.ValidityPayloadInvalid, "bad"
	f.st.PutTask(tk)
	if u, err = f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "renamed", Payload: mail}); err != nil || u.Validity != store.ValidityOK {
		t.Fatalf("validity not cleared: %+v %v", u, err)
	}
	// wait_result tasks never accept a future run_at
	w, _ := f.svc.Create(ctx, userA, CreateInput{Name: "w", TypeName: mailType, Kind: store.KindWaitResult, Payload: mail, Enabled: ptr(false)})
	if _, err := f.svc.Update(ctx, userA, w.ID, UpdateInput{Name: "w", RunAt: OptTime{Set: true, Time: &at}}); reason(err) != ReasonInvalidRunAt {
		t.Fatalf("future wait_result: %v", err)
	}
	// a task whose type disappeared from the catalog can still be edited
	tk, _ = f.st.Task(v.ID)
	tk.TypeName = "gone:type"
	f.st.PutTask(tk)
	if _, err := f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "orphan", Payload: raw(`{"anything":1}`)}); err != nil {
		t.Fatalf("orphan edit: %v", err)
	}
}

func TestUpdateTypeLookupFailure(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "scan", `{}`)
	f.svc.st = &typeGetDown{Mem: f.st}
	if _, err := f.svc.Update(ctx, userA, v.ID, UpdateInput{Name: "x"}); err == nil || reason(err) != "" {
		t.Fatalf("type lookup failure: %v", err)
	}
}

type typeGetDown struct{ *memstore.Mem }

func (typeGetDown) GetTaskType(context.Context, string) (store.TaskType, error) {
	return store.TaskType{}, errors.New("down")
}

func TestDeleteCancelsQueuedAttempts(t *testing.T) {
	f := newFixture(t)
	w, err := f.svc.Create(ctx, userA, CreateInput{Name: "w", TypeName: mailType, Kind: store.KindWaitResult, Payload: raw(`{"recipient":"a@b.example"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, userA, w.ID); err != nil {
		t.Fatal(err)
	}
	x, _ := f.st.GetExecution(ctx, repo.Tenant(tenantA), w.ExecutionID)
	if x.Status != store.ExecCancelled || !x.Final {
		t.Fatalf("queued attempt = %+v", x)
	}
	if e := f.rec.last(); e.EventType != audit.TaskDelete || e.Details["cancelled_attempts"] != 1 {
		t.Fatalf("audit = %+v", e)
	}
	if _, err := f.svc.Get(ctx, userA, w.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("deleted task visible")
	}
}

func TestPreview(t *testing.T) {
	f := newFixture(t)
	next, err := f.svc.Preview("0 3 * * *", "", 3)
	if err != nil || len(next) != 3 || !next[0].Equal(time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("preview = %v %v", next, err)
	}
	if next, _ := f.svc.Preview("0 3 * * *", "Europe/Sofia", 0); len(next) != 5 || next[0].Hour() != 0 {
		t.Fatalf("default count / tz = %v", next)
	}
	if _, err := f.svc.Preview("0 3 * *", "", 3); reason(err) != ReasonInvalidCron {
		t.Fatalf("bad expression: %v", err)
	}
	if _, err := f.svc.Preview("0 3 * * *", "Mars/Base", 3); reason(err) != ReasonInvalidTimezone {
		t.Fatalf("bad tz: %v", err)
	}
	if f.svc.Now() != start {
		t.Fatal("clock")
	}
}

func TestOptTimeAndResultValue(t *testing.T) {
	var in UpdateInput
	if err := json.Unmarshal([]byte(`{"name":"x"}`), &in); err != nil || in.RunAt.Set {
		t.Fatal("absent run_at")
	}
	if err := json.Unmarshal([]byte(`{"name":"x","run_at":null}`), &in); err != nil || !in.RunAt.Set || in.RunAt.Time != nil {
		t.Fatal("null run_at")
	}
	if err := json.Unmarshal([]byte(`{"name":"x","run_at":"2026-09-28T10:00:00Z"}`), &in); err != nil || in.RunAt.Time == nil {
		t.Fatal("run_at")
	}
	if err := json.Unmarshal([]byte(`{"name":"x","run_at":5}`), &in); err == nil {
		t.Fatal("bad run_at accepted")
	}
	if ResultValue(nil) != nil {
		t.Fatal("empty result")
	}
	if v, ok := ResultValue([]byte(`{"a":1}`)).(json.RawMessage); !ok || string(v) != `{"a":1}` {
		t.Fatal("json result")
	}
	if ResultValue([]byte(`queued 3`)) != "queued 3" {
		t.Fatal("text result")
	}
	if s, ok := ResultValue([]byte{0xff, 'a'}).(string); !ok || !strings.HasSuffix(s, "a") {
		t.Fatal("binary result")
	}
}

func TestDefaults(t *testing.T) {
	s := New(Deps{Store: memstore.New()})
	if s.lim.MaxPayloadBytes != 64<<10 || s.lim.MaxTimeoutSeconds != 3600 || s.lim.MinIntervalSeconds != 60 || s.lim.MaxPageSize != 100 || s.reg == nil {
		t.Fatalf("defaults = %+v", s.lim)
	}
	if s.Now().IsZero() {
		t.Fatal("default clock")
	}
	if Scope(userA) != repo.Tenant(tenantA) || !Scope(root).All {
		t.Fatal("scope")
	}
}

// A stored schema that no longer compiles makes the type unusable for saves.
func TestBrokenSchemaRefusesSave(t *testing.T) {
	f := newFixture(t)
	if _, err := f.st.InsertTaskTypeIfAbsent(ctx, store.TaskType{Name: "x:broken", Module: "x", DisplayName: "B", Scope: store.ScopeTenant,
		Available: true, PayloadSchema: raw(`{"type":5}`), SchemaHash: "h"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(ctx, userA, CreateInput{Name: "b", TypeName: "x:broken", Kind: store.KindDelayed}); reason(err) != ReasonTypeUnavailable {
		t.Fatalf("broken schema: %v", err)
	}
}
