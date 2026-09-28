package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/observe"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

const (
	tenantA  = "11111111-1111-7111-8111-111111111111"
	scanType = "ipam:scan-network"
	marker   = "PAYLOAD-MARKER-7f3a"
	schema   = `{"type":"object","properties":{"subnetId":{"type":"string"}},"additionalProperties":false}`
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 9, 28, 2, 59, 50, 0, time.UTC)
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// fakeDispatcher records attempts and answers from a script.
type fakeDispatcher struct {
	mu     sync.Mutex
	got    []Attempt
	script []Outcome
}

func (f *fakeDispatcher) Dispatch(_ context.Context, a Attempt) Outcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, a)
	if len(f.script) == 0 {
		return Outcome{Status: store.ExecSucceeded, Message: "ok"}
	}
	o := f.script[0]
	f.script = f.script[1:]
	return o
}

func (f *fakeDispatcher) attempts() []Attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Attempt(nil), f.got...)
}

type env struct {
	e    *Engine
	st   *memstore.Mem
	clk  *clock
	disp *fakeDispatcher
	pub  *events.Recorder
	logs *bytes.Buffer
	fm   *observe.Metrics
}

func newEnv(t *testing.T) *env {
	t.Helper()
	en := &env{st: memstore.New(), clk: &clock{t: t0}, disp: &fakeDispatcher{}, pub: events.NewRecorder(), logs: &bytes.Buffer{}}
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	en.fm = fm
	m, err := metrics.New(fm.Meter(metrics.Scope), nil)
	if err != nil {
		t.Fatal(err)
	}
	en.e = New(Config{InstanceID: "sched-1", MaxPayloadBytes: 1 << 16}, Deps{Store: en.st, Dispatcher: en.disp, Metrics: m,
		Events: events.Emitter{Pub: en.pub, PlatformTenant: tenantA}, Log: slog.New(slog.NewJSONHandler(en.logs, nil)), Now: en.clk.Now})
	if err := en.st.UpsertTaskTypes(ctx, "ipam", []store.TaskType{{Name: scanType, Module: "ipam", DisplayName: "Scan",
		PayloadSchema: json.RawMessage(schema), Scope: store.ScopeTenant, Available: true, SchemaHash: "h1"}}); err != nil {
		t.Fatal(err)
	}
	return en
}

func (en *env) task(t *testing.T, id, cron string, next time.Time) store.Task {
	t.Helper()
	tk := store.Task{ID: id, TenantID: tenantA, Name: id, TypeName: scanType, Module: "ipam", Kind: store.KindPeriodic,
		Payload: json.RawMessage(`{"subnetId":"` + marker + `"}`), Cron: cron, Timezone: "UTC", Enabled: true, Status: store.TaskActive,
		Validity: store.ValidityOK, MaxRetries: 2, TimeoutSeconds: 60, NextRunAt: &next}
	en.st.PutTask(tk)
	return tk
}

// cycle plans, recovers and dispatches, then waits for the workers.
func (en *env) cycle(t *testing.T) {
	t.Helper()
	en.e.Cycle(ctx)
	en.e.Wait()
}

func (en *env) execs(taskID string) []store.Execution {
	out := []store.Execution{}
	for _, x := range en.st.Executions() {
		if x.TaskID == taskID {
			out = append(out, x)
		}
	}
	return out
}

func (en *env) metrics(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	en.fm.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestBackoff(t *testing.T) {
	want := map[int]time.Duration{0: 30 * time.Second, 1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute,
		5: 8 * time.Minute, 6: 10 * time.Minute, 11: 10 * time.Minute}
	for n, d := range want {
		if Backoff(n) != d {
			t.Errorf("Backoff(%d) = %v, want %v", n, Backoff(n), d)
		}
	}
}

func TestDuePeriodicTaskRunsOnce(t *testing.T) {
	en := newEnv(t)
	occ := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	en.task(t, "t1", "0 3 * * *", occ)
	en.cycle(t) // not due yet
	if len(en.disp.attempts()) != 0 {
		t.Fatal("dispatched before due")
	}
	en.clk.Add(12 * time.Second) // 03:00:02
	en.cycle(t)
	en.cycle(t) // a second pass never re-plans the same occurrence
	got := en.disp.attempts()
	if len(got) != 1 {
		t.Fatalf("attempts = %d", len(got))
	}
	a := got[0]
	if a.Exec.TenantID != tenantA || !a.Exec.OccurrenceAt.Equal(occ) || a.Exec.Attempt != 1 || a.Exec.MaxAttempts != 3 ||
		a.Timeout != time.Minute || !strings.Contains(string(a.Task.Payload), marker) || a.Exec.Trigger != store.TriggerSchedule {
		t.Fatalf("attempt = %+v", a)
	}
	tk, _ := en.st.Task("t1")
	if want := occ.Add(24 * time.Hour); tk.NextRunAt == nil || !tk.NextRunAt.Equal(want) {
		t.Fatalf("next run = %v", tk.NextRunAt)
	}
	if tk.RunCount != 1 || tk.LastStatus != store.ExecSucceeded || tk.LastMessage != "ok" || tk.LastRunAt == nil {
		t.Fatalf("last run fields = %+v", tk)
	}
	x := en.execs("t1")
	if len(x) != 1 || x[0].Status != store.ExecSucceeded || !x[0].Final || x[0].FinishedAt == nil || x[0].LeaseOwner != "sched-1" {
		t.Fatalf("history = %+v", x)
	}
	types := []string{}
	for _, ev := range en.pub.Events() {
		types = append(types, ev.Type)
	}
	if !strings.Contains(strings.Join(types, ","), events.ExecutionChanged) {
		t.Fatalf("events = %v", types)
	}
	if !strings.Contains(en.metrics(t), `scheduler_runs_total{module="ipam",status="succeeded",type="ipam:scan-network"} 1`) {
		t.Fatal("run metric")
	}
	// SR-007: the payload value never reaches logs or metrics
	if strings.Contains(en.logs.String(), marker) || strings.Contains(en.metrics(t), marker) {
		t.Fatal("payload leaked")
	}
}

func TestOverlapAndValiditySkips(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	en.task(t, "busy", "*/5 * * * *", now)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-000000000001", TenantID: tenantA, TaskID: "busy", Status: store.ExecRunning,
		TypeName: scanType, Module: "ipam", Attempt: 1, MaxAttempts: 1, LeaseOwner: "other", LeaseUntil: ptrTime(now.Add(time.Hour))})
	un := en.task(t, "unavail", "*/5 * * * *", now)
	un.Validity = store.ValidityTypeUnavailable
	en.st.PutTask(un)
	bad := en.task(t, "badpayload", "*/5 * * * *", now)
	bad.Validity = store.ValidityPayloadInvalid
	en.st.PutTask(bad)
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for id, msg := range map[string]string{"busy": MsgOverlap, "unavail": MsgTypeUnavailable, "badpayload": MsgPayloadInvalid} {
		var skipped []store.Execution
		for _, x := range en.execs(id) {
			if x.Status == store.ExecSkipped {
				skipped = append(skipped, x)
			}
		}
		if len(skipped) != 1 || skipped[0].Message != msg || !skipped[0].Final {
			t.Errorf("%s skipped = %+v", id, skipped)
		}
		if tk, _ := en.st.Task(id); tk.NextRunAt == nil || !tk.NextRunAt.After(now) {
			t.Errorf("%s keeps its schedule: %v", id, tk.NextRunAt)
		}
	}
	m := en.metrics(t)
	for _, want := range []string{`reason="overlap"`, `reason="type_unavailable"`, `reason="payload_invalid"`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	if tk, _ := en.st.Task("unavail"); tk.LastStatus != store.ExecSkipped || tk.LastMessage != MsgTypeUnavailable {
		t.Fatalf("unavailable last status = %+v", tk)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestMissedOccurrencesAndCatchUp(t *testing.T) {
	en := newEnv(t)
	// scheduler was down for 3 hours: occurrences at 00:00, 01:00, 02:00 (next was 00:00)
	down := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	en.task(t, "nocatch", "0 * * * *", down)
	c := en.task(t, "catch", "0 * * * *", down)
	c.CatchUp = true
	en.st.PutTask(c)
	en.cycle(t)
	for _, id := range []string{"nocatch", "catch"} {
		tk, _ := en.st.Task(id)
		if tk.MissedCount != 3 || !tk.NextRunAt.Equal(time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)) {
			t.Fatalf("%s: missed %d next %v", id, tk.MissedCount, tk.NextRunAt)
		}
	}
	if n := len(en.execs("nocatch")); n != 0 {
		t.Fatalf("missed occurrences replayed: %d", n)
	}
	x := en.execs("catch")
	if len(x) != 1 || x[0].Trigger != store.TriggerCatchUp {
		t.Fatalf("catch-up = %+v", x)
	}
	if !strings.Contains(en.metrics(t), `scheduler_occurrences_missed_total{type="ipam:scan-network"} 6`) {
		t.Fatal("missed metric")
	}
}

func TestRetryPermanentAndTimeout(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	en.task(t, "retry", "0 4 * * *", now) // next occurrence after the retry window
	en.disp.script = []Outcome{
		{Status: store.ExecFailed, Message: "module unavailable", Retryable: true},
		{Status: store.ExecSucceeded, Message: "queued 3 scan(s), skipped 1", Result: []byte(`{"queued":3}`)},
	}
	en.cycle(t)
	x := en.execs("retry")
	if len(x) != 2 || x[0].Status != store.ExecFailed || x[0].Final || x[1].Status != store.ExecQueued || x[1].Attempt != 2 ||
		x[1].OccurrenceID != x[0].OccurrenceID || !x[1].DueAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("after attempt 1 = %+v", x)
	}
	if tk, _ := en.st.Task("retry"); tk.RunCount != 0 {
		t.Fatal("non-final attempt counted as a run")
	}
	en.cycle(t) // not due yet
	if len(en.disp.attempts()) != 1 {
		t.Fatal("retry dispatched early")
	}
	en.clk.Add(31 * time.Second)
	en.cycle(t)
	x = en.execs("retry")
	if len(x) != 2 || x[1].Status != store.ExecSucceeded || !x[1].Final || string(x[1].Result) != `{"queued":3}` {
		t.Fatalf("after attempt 2 = %+v", x)
	}
	tk, _ := en.st.Task("retry")
	if tk.RunCount != 1 || tk.LastStatus != store.ExecSucceeded || tk.LastMessage != "queued 3 scan(s), skipped 1" {
		t.Fatalf("task = %+v", tk)
	}
	if !strings.Contains(en.metrics(t), `scheduler_retries_total{module="ipam",type="ipam:scan-network"} 1`) {
		t.Fatal("retry metric")
	}

	// permanent failure: one attempt, no retry
	en.task(t, "perm", "0 3 * * *", en.clk.Now())
	en.disp.script = []Outcome{{Status: store.ExecFailed, Message: "invalid recipient"}}
	en.cycle(t)
	if x = en.execs("perm"); len(x) != 1 || !x[0].Final || x[0].Status != store.ExecFailed {
		t.Fatalf("permanent = %+v", x)
	}
	// timeout: retried until attempts are exhausted
	to := en.task(t, "timeout", "0 3 * * *", en.clk.Now())
	to.MaxRetries = 1
	en.st.PutTask(to)
	en.disp.script = []Outcome{{Status: store.ExecTimedOut, Retryable: true}, {Status: store.ExecTimedOut, Retryable: true}}
	en.cycle(t)
	en.clk.Add(time.Minute)
	en.cycle(t)
	x = en.execs("timeout")
	if len(x) != 2 || x[0].Status != store.ExecTimedOut || x[1].Status != store.ExecTimedOut || !x[1].Final {
		t.Fatalf("timeouts = %+v", x)
	}
	if tk, _ := en.st.Task("timeout"); tk.LastStatus != store.ExecTimedOut {
		t.Fatalf("task after timeouts = %+v", tk)
	}
}

func TestPayloadRevalidatedBeforeDispatch(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	tk := en.task(t, "p", "0 3 * * *", now)
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// the schema changed after planning: the payload no longer matches
	tk.Payload = json.RawMessage(`{"subnetId":5}`)
	en.st.PutTask(tk)
	en.cycle(t)
	if n := len(en.disp.attempts()); n != 0 {
		t.Fatalf("invalid payload dispatched %d times", n)
	}
	x := en.execs("p")
	if len(x) != 1 || x[0].Status != store.ExecFailed || !x[0].Final || !strings.HasPrefix(x[0].Message, MsgPayloadInvalid) {
		t.Fatalf("attempt = %+v", x)
	}
	if got, _ := en.st.Task("p"); got.Validity != store.ValidityPayloadInvalid || got.ValidityMessage == "" {
		t.Fatalf("validity = %+v", got)
	}
}

func TestDispatchPreconditions(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	// task deleted after its attempt was queued
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-00000000000a", TenantID: tenantA, TaskID: "gone", TypeName: scanType,
		Module: "ipam", Status: store.ExecQueued, DueAt: now, OccurrenceAt: now, Attempt: 1, MaxAttempts: 3, Trigger: store.TriggerSchedule})
	// type unavailable
	u := en.task(t, "unavailable-type", "0 3 * * *", now.Add(time.Hour))
	u.TypeName = "ipam:missing"
	en.st.PutTask(u)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-00000000000b", TenantID: tenantA, TaskID: u.ID, TypeName: "ipam:missing",
		Module: "ipam", Status: store.ExecQueued, DueAt: now, OccurrenceAt: now, Attempt: 1, MaxAttempts: 3, Trigger: store.TriggerManual})
	en.cycle(t)
	if len(en.disp.attempts()) != 0 {
		t.Fatal("dispatched without a task / type")
	}
	for id, msg := range map[string]string{"gone": MsgTaskDeleted, u.ID: MsgTypeUnavailable} {
		x := en.execs(id)
		if len(x) != 1 || x[0].Status != store.ExecFailed || x[0].Message != msg || !x[0].Final {
			t.Errorf("%s = %+v", id, x)
		}
	}
}

// storeDown fails the type lookup (retryable) or fences completion.
type storeDown struct {
	*memstore.Mem
	typeErr  error
	fence    bool
	claimErr error
}

func (s *storeDown) GetTaskType(ctx context.Context, name string) (store.TaskType, error) {
	if s.typeErr != nil {
		return store.TaskType{}, s.typeErr
	}
	return s.Mem.GetTaskType(ctx, name)
}

func (s *storeDown) CompleteAttempt(ctx context.Context, c repo.Completion) (bool, error) {
	if s.fence {
		return false, nil
	}
	return s.Mem.CompleteAttempt(ctx, c)
}

func (s *storeDown) ClaimQueued(ctx context.Context, now time.Time, owner string, limit int, grace time.Duration) ([]repo.Claim, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	return s.Mem.ClaimQueued(ctx, now, owner, limit, grace)
}

func TestStoreFailuresDuringDispatch(t *testing.T) {
	en := newEnv(t)
	sd := &storeDown{Mem: en.st, typeErr: errors.New("db down")}
	en.e.st = sd
	en.task(t, "a", "0 3 * * *", en.clk.Now())
	en.cycle(t)
	x := en.execs("a")
	if len(x) != 2 || x[0].Message != MsgStoreDown || x[1].Status != store.ExecQueued {
		t.Fatalf("store down during check = %+v", x)
	}
	// a fenced completion (lease lost) is not written and logged without payload
	sd.typeErr, sd.fence = nil, true
	en.clk.Add(time.Minute)
	en.cycle(t)
	if x = en.execs("a"); x[1].Status != store.ExecRunning {
		t.Fatalf("fenced attempt = %+v", x[1])
	}
	if !strings.Contains(en.logs.String(), "not recorded") || strings.Contains(en.logs.String(), marker) {
		t.Fatalf("logs = %s", en.logs.String())
	}
	sd.claimErr = errors.New("claim down")
	en.cycle(t)
	if !strings.Contains(en.logs.String(), "claim down") {
		t.Fatal("claim error not logged")
	}
}

func TestLeaseRecovery(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	en.task(t, "r", "0 3 * * *", now.Add(time.Hour))
	started := now.Add(-10 * time.Minute)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-00000000000c", TenantID: tenantA, TaskID: "r", TypeName: scanType, Module: "ipam",
		Status: store.ExecRunning, Attempt: 1, MaxAttempts: 2, Trigger: store.TriggerSchedule, OccurrenceAt: started, DueAt: started,
		StartedAt: &started, LeaseOwner: "crashed", LeaseUntil: ptrTime(now.Add(-time.Second)), OccurrenceID: "00000000-0000-7000-8000-0000000000cc"})
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-00000000000d", TenantID: tenantA, TaskID: "r", TypeName: scanType, Module: "ipam",
		Status: store.ExecRunning, Attempt: 2, MaxAttempts: 2, Trigger: store.TriggerSchedule, OccurrenceAt: started, DueAt: started,
		StartedAt: &started, LeaseOwner: "crashed", LeaseUntil: ptrTime(now.Add(-time.Second)), OccurrenceID: "00000000-0000-7000-8000-0000000000dd"})
	n, err := en.e.RecoverOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("recovered = %d %v", n, err)
	}
	var retried, final int
	for _, x := range en.execs("r") {
		switch {
		case x.Status == store.ExecTimedOut && x.Final:
			final++
		case x.Status == store.ExecTimedOut:
			retried++
		case x.Status == store.ExecQueued && x.Attempt == 2:
			if !x.DueAt.Equal(now.Add(30 * time.Second)) {
				t.Fatalf("retry due = %v", x.DueAt)
			}
		}
	}
	if retried != 1 || final != 1 {
		t.Fatalf("retried %d final %d", retried, final)
	}
	if tk, _ := en.st.Task("r"); tk.LastStatus != store.ExecTimedOut || tk.LastMessage != memstore.LostMessage {
		t.Fatalf("task = %+v", tk)
	}
}

func TestRetentionAndOneShot(t *testing.T) {
	en := newEnv(t)
	now := en.clk.Now()
	old := now.Add(-91 * 24 * time.Hour)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-0000000000e1", TenantID: tenantA, TaskID: "x", Status: store.ExecSucceeded, CreatedAt: old})
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-0000000000e2", TenantID: tenantA, TaskID: "x", Status: store.ExecQueued, CreatedAt: old, DueAt: now.Add(time.Hour)})
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-0000000000e3", TenantID: tenantA, TaskID: "x", Status: store.ExecFailed, CreatedAt: now})
	if n, err := en.e.PruneOnce(ctx); err != nil || n != 1 {
		t.Fatalf("pruned = %d %v", n, err)
	}
	// a delayed task runs once, completes and never fires again
	o := en.task(t, "once", "", now)
	o.Kind = store.KindDelayed
	en.st.PutTask(o)
	en.cycle(t)
	en.clk.Add(time.Hour)
	en.cycle(t)
	tk, _ := en.st.Task("once")
	if len(en.execs("once")) != 1 || tk.Status != store.TaskCompleted || tk.NextRunAt != nil || tk.RunCount != 1 {
		t.Fatalf("one-shot = %+v", tk)
	}
	// a restarted engine (fresh instance) never re-fires it either
	e2 := New(Config{}, Deps{Store: en.st, Dispatcher: en.disp, Now: en.clk.Now})
	e2.Cycle(ctx)
	e2.Wait()
	if len(en.execs("once")) != 1 || e2.InstanceID() == "" {
		t.Fatal("completed one-shot re-fired after restart")
	}
	// a one-shot whose previous (manual) run is still in progress is skipped and completed
	b := en.task(t, "busy-once", "", en.clk.Now())
	b.Kind = store.KindDelayed
	en.st.PutTask(b)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-0000000000e4", TenantID: tenantA, TaskID: "busy-once", Status: store.ExecRunning,
		LeaseUntil: ptrTime(en.clk.Now().Add(time.Hour)), Trigger: store.TriggerManual, TypeName: scanType})
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if tk, _ := en.st.Task("busy-once"); tk.Status != store.TaskCompleted || tk.LastMessage != MsgOverlap {
		t.Fatalf("busy one-shot = %+v", tk)
	}
	// a one-shot with an unavailable type completes as skipped
	u := en.task(t, "u-once", "", en.clk.Now())
	u.Kind, u.Validity = store.KindDelayed, store.ValidityTypeUnavailable
	en.st.PutTask(u)
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if tk, _ := en.st.Task("u-once"); tk.Status != store.TaskCompleted || tk.LastStatus != store.ExecSkipped {
		t.Fatalf("unavailable one-shot = %+v", tk)
	}
}

func TestInvalidStoredSchedule(t *testing.T) {
	en := newEnv(t)
	for id, tz := range map[string]string{"badcron": "UTC", "badtz": "Mars/Base"} {
		tk := en.task(t, id, "0 3 * * *", en.clk.Now())
		if id == "badcron" {
			tk.Cron = "not a cron"
		}
		tk.Timezone = tz
		en.st.PutTask(tk)
	}
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"badcron", "badtz"} {
		tk, _ := en.st.Task(id)
		x := en.execs(id)
		if tk.NextRunAt != nil || len(x) != 1 || x[0].Message != MsgInvalidSchedule {
			t.Fatalf("%s: %+v %+v", id, tk, x)
		}
	}
}

func TestRunLoopAndStoreErrors(t *testing.T) {
	en := newEnv(t)
	en.st.SetErr(errors.New("db down"))
	en.e.Cycle(ctx)
	for _, want := range []string{"scheduler: plan", "scheduler: recover", "scheduler: claim", "scheduler: retention"} {
		if !strings.Contains(en.logs.String(), want) {
			t.Errorf("logs lack %q", want)
		}
	}
	if _, err := en.e.PlanOnce(ctx); err == nil {
		t.Fatal("plan error hidden")
	}
	en.st.SetErr(nil)
	en.st.PutExecution(store.Execution{ID: "00000000-0000-7000-8000-0000000000f1", TenantID: tenantA, TaskID: "x", Status: store.ExecSucceeded,
		CreatedAt: en.clk.Now().Add(-100 * 24 * time.Hour)})
	en.e.lastPrune = time.Time{}
	en.e.Cycle(ctx)
	if !strings.Contains(en.logs.String(), "retention pruned") {
		t.Fatal("prune not logged")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	en.e.Cycle(cctx) // cancelled: nothing happens
	done := make(chan struct{})
	e := New(Config{Tick: time.Millisecond}, Deps{Store: memstore.New(), Dispatcher: en.disp})
	rctx, rcancel := context.WithCancel(ctx)
	go func() { e.Run(rctx); close(done) }()
	time.Sleep(10 * time.Millisecond)
	rcancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestShutdownLeavesAttemptForRecovery(t *testing.T) {
	en := newEnv(t)
	cctx, cancel := context.WithCancel(ctx)
	en.e.disp = DispatcherFunc(func(context.Context, Attempt) Outcome {
		cancel()
		return Outcome{Status: store.ExecFailed, Message: "canceled", Retryable: true}
	})
	en.task(t, "s", "0 3 * * *", en.clk.Now())
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := en.e.DispatchOnce(cctx); err != nil {
		t.Fatal(err)
	}
	en.e.Wait()
	if x := en.execs("s"); len(x) != 1 || x[0].Status != store.ExecRunning {
		t.Fatalf("attempt after shutdown = %+v", x)
	}
}

func TestWorkerBound(t *testing.T) {
	en := newEnv(t)
	release := make(chan struct{})
	var mu sync.Mutex
	inflight, peak := 0, 0
	en.e = New(Config{Workers: 2, InstanceID: "w"}, Deps{Store: en.st, Now: en.clk.Now, Dispatcher: DispatcherFunc(func(context.Context, Attempt) Outcome {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inflight--
		mu.Unlock()
		return Outcome{Status: store.ExecSucceeded}
	})})
	for _, id := range []string{"w1", "w2", "w3", "w4"} {
		en.task(t, id, "0 3 * * *", en.clk.Now())
	}
	if _, err := en.e.PlanOnce(ctx); err != nil {
		t.Fatal(err)
	}
	n, _ := en.e.DispatchOnce(ctx)
	m, _ := en.e.DispatchOnce(ctx) // no free worker
	if n != 2 || m != 0 {
		t.Fatalf("claimed %d then %d", n, m)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		p := peak
		mu.Unlock()
		if p == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	en.e.Wait()
	if peak != 2 {
		t.Fatalf("peak = %d", peak)
	}
}
