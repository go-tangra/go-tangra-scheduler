package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

func TestStartStopRestart(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "scan", `{}`)
	s, err := f.svc.Stop(ctx, userA, v.ID)
	if err != nil || s.Enabled || s.NextRunAt != nil || s.State != store.StateStopped {
		t.Fatalf("stop: %+v %v", s, err)
	}
	f.clk.Add(48 * time.Hour)
	s, err = f.svc.Start(ctx, userA, v.ID)
	want := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC) // recomputed from now
	if err != nil || !s.Enabled || s.NextRunAt == nil || !s.NextRunAt.Equal(want) {
		t.Fatalf("start: %+v %v", s.NextRunAt, err)
	}
	if s, err = f.svc.Restart(ctx, userA, v.ID); err != nil || !s.Enabled {
		t.Fatalf("restart: %v", err)
	}
	got := f.rec.types()
	if got[len(got)-3] != string(audit.TaskStop) || got[len(got)-2] != string(audit.TaskStart) || got[len(got)-1] != string(audit.TaskRestart) {
		t.Fatalf("audit = %v", got)
	}
	// one-shot tasks cannot be started/stopped
	o, _ := f.svc.Create(ctx, userA, CreateInput{Name: "o", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`)})
	for _, fn := range []func() (View, error){
		func() (View, error) { return f.svc.Start(ctx, userA, o.ID) },
		func() (View, error) { return f.svc.Stop(ctx, userA, o.ID) },
		func() (View, error) { return f.svc.Restart(ctx, userA, o.ID) },
	} {
		if _, err := fn(); reason(err) != ReasonNotPeriodic {
			t.Fatalf("one-shot control: %v", err)
		}
	}
	if _, err := f.svc.Start(ctx, userB, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("cross-tenant start")
	}
	// a stored schedule that no longer parses refuses start
	tk, _ := f.st.Task(v.ID)
	tk.Cron = "garbage"
	f.st.PutTask(tk)
	if _, err := f.svc.Start(ctx, userA, v.ID); reason(err) != ReasonInvalidCron {
		t.Fatalf("broken schedule: %v", err)
	}
}

func TestRunNow(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "scan", `{}`)
	before, _ := f.st.Task(v.ID)
	id, err := f.svc.Run(ctx, userA, v.ID)
	if err != nil || id == "" {
		t.Fatalf("run: %v", err)
	}
	x, _ := f.st.GetExecution(ctx, repo.Tenant(tenantA), id)
	if x.Trigger != store.TriggerManual || x.TriggeredBy != "alice" || x.Status != store.ExecQueued || x.MaxAttempts != 3 || !x.OccurrenceAt.Equal(start) {
		t.Fatalf("manual attempt = %+v", x)
	}
	after, _ := f.st.Task(v.ID)
	if !after.NextRunAt.Equal(*before.NextRunAt) {
		t.Fatal("run now changed the schedule")
	}
	if e := f.rec.last(); e.EventType != audit.TaskRun || e.Details["execution_id"] != id || e.Details["trigger"] != "manual" {
		t.Fatalf("audit = %+v", e)
	}
	if _, err := f.svc.Run(ctx, userA, v.ID); reason(err) != ReasonRunInProgress {
		t.Fatalf("overlap: %v", err)
	}
	// validity blocks a manual run
	w := f.periodic(t, userA, "w", `{}`)
	tk, _ := f.st.Task(w.ID)
	tk.Validity = store.ValidityTypeUnavailable
	f.st.PutTask(tk)
	if _, err := f.svc.Run(ctx, userA, w.ID); reason(err) != ReasonTypeUnavailable {
		t.Fatalf("unavailable: %v", err)
	}
	tk.Validity, tk.ValidityMessage = store.ValidityPayloadInvalid, "/x: bad"
	f.st.PutTask(tk)
	if _, err := f.svc.Run(ctx, userA, w.ID); reason(err) != ReasonInvalidPayload {
		t.Fatalf("payload invalid: %v", err)
	}
	// a completed one-shot can run again (manual trigger)
	o, _ := f.svc.Create(ctx, userA, CreateInput{Name: "o", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`)})
	tk, _ = f.st.Task(o.ID)
	tk.Status, tk.NextRunAt = store.TaskCompleted, nil
	f.st.PutTask(tk)
	if _, err := f.svc.Run(ctx, userA, o.ID); err != nil {
		t.Fatalf("run again: %v", err)
	}
	if tk, _ = f.st.Task(o.ID); tk.Status != store.TaskCompleted {
		t.Fatal("run again changed the state")
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	mail := raw(`{"recipient":"a@b.example"}`)
	o, _ := f.svc.Create(ctx, userA, CreateInput{Name: "o", TypeName: mailType, Kind: store.KindDelayed, Payload: mail, DelaySeconds: ptr(600)})
	c, err := f.svc.Cancel(ctx, userA, o.ID)
	if err != nil || c.Status != store.TaskCancelled || c.State != store.StateCancelled || c.NextRunAt != nil {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if _, err := f.svc.Cancel(ctx, userA, o.ID); reason(err) != ReasonNotCancellable {
		t.Fatalf("cancel twice: %v", err)
	}
	p := f.periodic(t, userA, "p", `{}`)
	if _, err := f.svc.Cancel(ctx, userA, p.ID); reason(err) != ReasonNotCancellable {
		t.Fatalf("cancel periodic: %v", err)
	}
	// cancelling a queued wait_result cancels its attempt
	w, _ := f.svc.Create(ctx, userA, CreateInput{Name: "w", TypeName: mailType, Kind: store.KindWaitResult, Payload: mail})
	if _, err := f.svc.Cancel(ctx, userA, w.ID); err != nil {
		t.Fatal(err)
	}
	if x, _ := f.st.GetExecution(ctx, repo.Tenant(tenantA), w.ExecutionID); x.Status != store.ExecCancelled {
		t.Fatalf("attempt = %s", x.Status)
	}
	if e := f.rec.last(); e.EventType != audit.TaskCancel || e.Details["cancelled_attempts"] != 1 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestBulkIsScoped(t *testing.T) {
	f := newFixture(t)
	a1 := f.periodic(t, userA, "a1", `{}`)
	f.periodic(t, userA, "a2", `{}`)
	b1 := f.periodic(t, userB, "b1", `{}`)
	o, _ := f.svc.Create(ctx, userA, CreateInput{Name: "o", TypeName: mailType, Kind: store.KindDelayed, Payload: raw(`{"recipient":"a@b.example"}`)})
	n, err := f.svc.Bulk(ctx, userA, ActionStop)
	if err != nil || n != 2 {
		t.Fatalf("stop all = %d %v", n, err)
	}
	if tk, _ := f.st.Task(b1.ID); !tk.Enabled {
		t.Fatal("tenant B task stopped by tenant A")
	}
	if tk, _ := f.st.Task(o.ID); !tk.Enabled || tk.NextRunAt == nil {
		t.Fatal("one-shot touched by bulk")
	}
	if n, _ = f.svc.Bulk(ctx, userA, ActionStop); n != 0 {
		t.Fatalf("second stop affected %d", n)
	}
	if n, _ = f.svc.Bulk(ctx, userA, ActionStart); n != 2 {
		t.Fatalf("start all = %d", n)
	}
	if n, _ = f.svc.Bulk(ctx, userA, ActionRestart); n != 2 {
		t.Fatalf("restart all = %d", n)
	}
	e := f.rec.last()
	if e.EventType != audit.TasksBulk || e.Details["action"] != ActionRestart || e.Details["affected"] != 2 || e.TenantID != tenantA {
		t.Fatalf("audit = %+v", e)
	}
	// platform admin: every tenant
	if n, _ = f.svc.Bulk(ctx, root, ActionStop); n != 3 {
		t.Fatalf("admin stop all = %d", n)
	}
	if _, err := f.svc.Bulk(ctx, userA, "explode"); reason(err) != ReasonValidation {
		t.Fatalf("bad action: %v", err)
	}
	// a task whose stored schedule is broken is skipped, not fatal
	tk, _ := f.st.Task(a1.ID)
	tk.Cron = "garbage"
	f.st.PutTask(tk)
	if n, err = f.svc.Bulk(ctx, userA, ActionStart); err != nil || n != 1 {
		t.Fatalf("broken schedule skipped: %d %v", n, err)
	}
	f.st.SetErr(errors.New("db down"))
	if _, err := f.svc.Bulk(ctx, userA, ActionStart); err == nil {
		t.Fatal("store failure hidden")
	}
	if tenantOf(authz.System()) != audit.NilTenant {
		t.Fatal("system audit tenant")
	}
}

func TestBulkStoreFailureMidway(t *testing.T) {
	f := newFixture(t)
	f.periodic(t, userA, "a1", `{}`)
	f.svc.st = &mutateDown{Mem: f.st}
	if _, err := f.svc.Bulk(ctx, userA, ActionStop); err == nil {
		t.Fatal("mutation failure hidden")
	}
}

type mutateDown struct{ *memstore.Mem }

func (mutateDown) MutateTask(context.Context, repo.Scope, string, repo.TaskMutation) (store.Task, repo.MutateResult, error) {
	return store.Task{}, repo.MutateResult{}, errors.New("db down")
}

func TestControlStoreFailures(t *testing.T) {
	f := newFixture(t)
	v := f.periodic(t, userA, "scan", `{}`)
	f.st.SetErr(errors.New("db down"))
	if _, err := f.svc.Stop(ctx, userA, v.ID); err == nil {
		t.Fatal("stop")
	}
	if _, err := f.svc.Run(ctx, userA, v.ID); err == nil {
		t.Fatal("run")
	}
	if _, err := f.svc.Cancel(ctx, userA, v.ID); err == nil {
		t.Fatal("cancel")
	}
	if err := f.svc.Delete(ctx, userA, v.ID); err == nil {
		t.Fatal("delete")
	}
	if _, err := f.svc.Execution(ctx, userA, "x"); err == nil {
		t.Fatal("execution")
	}
	if _, err := f.svc.Executions(ctx, userA, store.ExecFilter{}); err == nil {
		t.Fatal("executions")
	}
	if _, err := f.svc.Overview(ctx, userA); err == nil {
		t.Fatal("overview")
	}
}
