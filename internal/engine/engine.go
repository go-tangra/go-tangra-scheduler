// Package engine is the scheduler's in-process planning and dispatch loop
// (research D1, D2, D6). Every replica runs one; PostgreSQL row locks make
// each step safe across replicas:
//
//  1. Plan: due tasks are locked (SKIP LOCKED); each occurrence is decided —
//     run, skip (overlap, type unavailable, payload invalid) or missed (with at
//     most one catch-up run) — its first attempt is inserted and the task's
//     next run advanced, all in one transaction.
//  2. Claim: due queued attempts become running under this instance's lease.
//  3. Dispatch: the payload is re-validated against the type's CURRENT schema
//     and the attempt is sent to the owning module with the task timeout.
//  4. Complete: the outcome is written only while this instance still holds
//     the lease (fencing); a retryable failure with attempts left queues the
//     next attempt after a growing backoff; the final attempt updates the
//     task's last-run fields and completes one-shot tasks.
//  5. Recover: attempts whose lease expired (a crashed replica) time out and
//     are retried like a timeout — never lost silently.
//  6. Retention: finished history older than the retention period is pruned.
//
// The engine never logs payload values, messages or results (SR-007).
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/cron"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/payload"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Messages recorded by the engine (never payload-derived).
const (
	MsgOverlap         = "previous run still in progress"
	MsgTypeUnavailable = "task type unavailable"
	MsgInvalidSchedule = "the stored schedule is no longer valid"
	MsgTaskDeleted     = "task deleted"
	MsgStoreDown       = "scheduler store unavailable"
	MsgPayloadInvalid  = "payload no longer matches the task type's schema"
)

// Backoff bounds (research D6).
const (
	BackoffBase = 30 * time.Second
	BackoffCap  = 10 * time.Minute
	// MaxMissedCount bounds the missed-occurrence walk after a long downtime.
	MaxMissedCount = 100000
)

// Backoff is the delay before the attempt after a failed attempt n (1-based):
// 30 s × 2^(n-1), capped at 10 minutes.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := BackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= BackoffCap {
			return BackoffCap
		}
	}
	return d
}

// Attempt is one claimed attempt handed to the dispatcher.
type Attempt struct {
	Exec    store.Execution
	Task    store.Task
	Timeout time.Duration
}

// Outcome is the dispatcher's classification of one attempt.
type Outcome struct {
	Status    string // succeeded | failed | timed_out
	Message   string
	Result    []byte
	Truncated bool
	Retryable bool
}

// Dispatcher sends an attempt to the owning module.
type Dispatcher interface {
	Dispatch(ctx context.Context, a Attempt) Outcome
}

// DispatcherFunc adapts a function to Dispatcher.
type DispatcherFunc func(ctx context.Context, a Attempt) Outcome

// Dispatch implements Dispatcher.
func (f DispatcherFunc) Dispatch(ctx context.Context, a Attempt) Outcome { return f(ctx, a) }

// Config tunes the engine.
type Config struct {
	Tick              time.Duration
	Workers           int
	Batch             int
	MisfireGrace      time.Duration
	LeaseGrace        time.Duration
	Retention         time.Duration
	RetentionInterval time.Duration
	InstanceID        string
	MaxPayloadBytes   int
}

// Deps wire the engine.
type Deps struct {
	Store      repo.Store
	Dispatcher Dispatcher
	Schemas    *registry.Schemas
	Metrics    *metrics.Metrics
	Events     events.Emitter
	Log        *slog.Logger
	Now        func() time.Time
}

// Engine plans and dispatches task occurrences.
type Engine struct {
	cfg       Config
	st        repo.Store
	disp      Dispatcher
	schemas   *registry.Schemas
	metrics   *metrics.Metrics
	events    events.Emitter
	log       *slog.Logger
	now       func() time.Time
	sem       chan struct{}
	wg        sync.WaitGroup
	lastPrune time.Time
}

// New builds an engine with defaults for unset values.
func New(cfg Config, d Deps) *Engine {
	if cfg.Tick <= 0 {
		cfg.Tick = time.Second
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 16
	}
	if cfg.Batch <= 0 {
		cfg.Batch = 100
	}
	if cfg.MisfireGrace <= 0 {
		cfg.MisfireGrace = time.Minute
	}
	if cfg.LeaseGrace <= 0 {
		cfg.LeaseGrace = 30 * time.Second
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 90 * 24 * time.Hour
	}
	if cfg.RetentionInterval <= 0 {
		cfg.RetentionInterval = time.Hour
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = "scheduler-" + store.NewID()
	}
	e := &Engine{cfg: cfg, st: d.Store, disp: d.Dispatcher, schemas: d.Schemas, metrics: d.Metrics, events: d.Events, log: d.Log, now: d.Now}
	if e.schemas == nil {
		e.schemas = registry.NewSchemas()
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	if e.now == nil {
		e.now = time.Now
	}
	e.sem = make(chan struct{}, cfg.Workers)
	return e
}

// InstanceID is the lease owner name of this replica.
func (e *Engine) InstanceID() string { return e.cfg.InstanceID }

func (e *Engine) clock() time.Time { return e.now().UTC() }

// Run loops until ctx ends, then waits for in-flight attempts.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.cfg.Tick)
	defer t.Stop()
	for {
		e.Cycle(ctx)
		select {
		case <-ctx.Done():
			e.Wait()
			return
		case <-t.C:
		}
	}
}

// Cycle runs one plan / recover / dispatch pass and, when due, retention.
func (e *Engine) Cycle(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if _, err := e.PlanOnce(ctx); err != nil {
		e.log.Warn("scheduler: plan", "err", err)
	}
	if _, err := e.RecoverOnce(ctx); err != nil {
		e.log.Warn("scheduler: recover", "err", err)
	}
	if _, err := e.DispatchOnce(ctx); err != nil {
		e.log.Warn("scheduler: claim", "err", err)
	}
	if now := e.clock(); now.Sub(e.lastPrune) >= e.cfg.RetentionInterval {
		e.lastPrune = now
		if n, err := e.PruneOnce(ctx); err != nil {
			e.log.Warn("scheduler: retention", "err", err)
		} else if n > 0 {
			e.log.Info("scheduler: retention pruned history", "attempts", n)
		}
	}
}

// Wait blocks until every in-flight attempt has completed.
func (e *Engine) Wait() { e.wg.Wait() }

// ------------------------------------------------------------------ plan

func skipReason(validity string) (string, string) {
	if validity == store.ValidityTypeUnavailable {
		return metrics.SkipTypeUnavailable, MsgTypeUnavailable
	}
	return metrics.SkipPayloadInvalid, MsgPayloadInvalid
}

func (e *Engine) skipped(t *store.Task, occ time.Time, trigger, msg string, now time.Time) store.Execution {
	return store.Execution{ID: store.NewID(), TenantID: t.TenantID, TaskID: t.ID, TaskName: t.Name, OccurrenceID: store.NewID(),
		TypeName: t.TypeName, Module: t.Module, Trigger: trigger, OccurrenceAt: occ, DueAt: now, Status: store.ExecSkipped,
		Attempt: 1, MaxAttempts: t.MaxRetries + 1, FinishedAt: &now, Message: msg, Final: true, CreatedAt: now}
}

func (e *Engine) queued(t *store.Task, occ time.Time, trigger string, now time.Time) store.Execution {
	return store.Execution{ID: store.NewID(), TenantID: t.TenantID, TaskID: t.ID, TaskName: t.Name, OccurrenceID: store.NewID(),
		TypeName: t.TypeName, Module: t.Module, Trigger: trigger, OccurrenceAt: occ, DueAt: now, Status: store.ExecQueued,
		Attempt: 1, MaxAttempts: t.MaxRetries + 1, CreatedAt: now}
}

// decide plans one due task at now (t is mutated in place).
func (e *Engine) decide(t *store.Task, busy bool, now time.Time) repo.Plan {
	occ := *t.NextRunAt
	trigger := store.TriggerSchedule
	run := true
	if t.Kind == store.KindPeriodic {
		sched, err := cron.Parse(t.Cron)
		var loc *time.Location
		if err == nil {
			loc, err = cron.LoadLocation(t.Timezone)
		}
		if err != nil {
			t.NextRunAt = nil
			e.metrics.Skipped(t.TypeName, metrics.SkipInvalidSchedule)
			t.LastStatus, t.LastMessage = store.ExecSkipped, MsgInvalidSchedule
			return repo.Plan{Insert: []store.Execution{e.skipped(t, occ, trigger, MsgInvalidSchedule, now)}}
		}
		if now.Sub(occ) > e.cfg.MisfireGrace {
			missed := sched.Count(occ.Add(-time.Nanosecond), now, loc, MaxMissedCount)
			t.MissedCount += int64(missed)
			e.metrics.Missed(t.TypeName, int64(missed))
			trigger, run = store.TriggerCatchUp, t.CatchUp
		}
		t.NextRunAt = nil
		if next, ok := sched.Next(now, loc); ok {
			n := next.UTC()
			t.NextRunAt = &n
		}
	} else {
		t.NextRunAt = nil // a one-shot occurrence is planned exactly once
	}
	if !run {
		return repo.Plan{}
	}
	switch {
	case t.Validity != store.ValidityOK:
		reason, msg := skipReason(t.Validity)
		e.metrics.Skipped(t.TypeName, reason)
		t.LastStatus, t.LastMessage = store.ExecSkipped, msg
		if t.OneShot() {
			t.Status = store.TaskCompleted
		}
		return repo.Plan{Insert: []store.Execution{e.skipped(t, occ, trigger, msg, now)}}
	case busy:
		e.metrics.Skipped(t.TypeName, metrics.SkipOverlap)
		if t.OneShot() {
			t.Status, t.LastStatus, t.LastMessage = store.TaskCompleted, store.ExecSkipped, MsgOverlap
		}
		return repo.Plan{Insert: []store.Execution{e.skipped(t, occ, trigger, MsgOverlap, now)}}
	}
	return repo.Plan{Insert: []store.Execution{e.queued(t, occ, trigger, now)}}
}

// PlanOnce plans every due task (up to the batch size).
func (e *Engine) PlanOnce(ctx context.Context) (int, error) {
	now := e.clock()
	var planned []store.Execution
	var tasks []store.Task
	n, err := e.st.PlanDue(ctx, now, e.cfg.Batch, func(t *store.Task, busy bool) (repo.Plan, error) {
		p := e.decide(t, busy, now)
		planned = append(planned, p.Insert...)
		tasks = append(tasks, *t)
		return p, nil
	})
	if err != nil {
		return 0, err
	}
	for _, x := range planned {
		e.events.Execution(ctx, x)
	}
	for _, t := range tasks {
		e.events.Task(ctx, t.TenantID, t.ID)
	}
	return n, nil
}

// ------------------------------------------------------------------ dispatch

// DispatchOnce claims as many due attempts as there are free workers and runs
// them in the background (Wait blocks until they finish).
func (e *Engine) DispatchOnce(ctx context.Context) (int, error) {
	free := cap(e.sem) - len(e.sem)
	if free > e.cfg.Batch {
		free = e.cfg.Batch
	}
	if free <= 0 {
		return 0, nil
	}
	claims, err := e.st.ClaimQueued(ctx, e.clock(), e.cfg.InstanceID, free, e.cfg.LeaseGrace)
	if err != nil {
		return 0, err
	}
	for _, c := range claims {
		e.sem <- struct{}{}
		e.wg.Add(1)
		go func(c repo.Claim) {
			defer func() { <-e.sem; e.wg.Done() }()
			e.attempt(ctx, c)
		}(c)
	}
	return len(claims), nil
}

// retryOf builds the next attempt of the same occurrence.
func retryOf(x store.Execution, now time.Time) *store.Execution {
	if x.Attempt >= x.MaxAttempts {
		return nil
	}
	r := store.Execution{ID: store.NewID(), TenantID: x.TenantID, TaskID: x.TaskID, TaskName: x.TaskName, OccurrenceID: x.OccurrenceID,
		TypeName: x.TypeName, Module: x.Module, Trigger: x.Trigger, TriggeredBy: x.TriggeredBy, OccurrenceAt: x.OccurrenceAt,
		DueAt: now.Add(Backoff(x.Attempt)), Status: store.ExecQueued, Attempt: x.Attempt + 1, MaxAttempts: x.MaxAttempts, CreatedAt: now}
	return &r
}

// check re-validates a claimed attempt before dispatch: the task must exist,
// its type must be available and the payload must match the CURRENT schema.
func (e *Engine) check(ctx context.Context, c repo.Claim) (*Outcome, string, string) {
	if c.Task.ID == "" {
		return &Outcome{Status: store.ExecFailed, Message: MsgTaskDeleted}, "", ""
	}
	tt, err := e.st.GetTaskType(ctx, c.Task.TypeName)
	switch {
	case errors.Is(err, repo.ErrNotFound) || (err == nil && !tt.Available):
		return &Outcome{Status: store.ExecFailed, Message: MsgTypeUnavailable}, "", ""
	case err != nil:
		return &Outcome{Status: store.ExecFailed, Message: MsgStoreDown, Retryable: true}, "", ""
	}
	schema, err := e.schemas.Compiled(tt)
	if err == nil {
		err = payload.Validate(schema, c.Task.Payload, e.cfg.MaxPayloadBytes)
	}
	if err != nil {
		msg := MsgPayloadInvalid + ": " + err.Error()
		return &Outcome{Status: store.ExecFailed, Message: msg}, store.ValidityPayloadInvalid, err.Error()
	}
	return nil, "", ""
}

// attempt runs one claimed attempt to completion.
func (e *Engine) attempt(ctx context.Context, c repo.Claim) {
	e.events.Execution(ctx, c.Exec)
	started := time.Now()
	out, validity, vmsg := e.check(ctx, c)
	if out == nil {
		timeout := time.Duration(c.Task.TimeoutSeconds) * time.Second
		o := e.disp.Dispatch(ctx, Attempt{Exec: c.Exec, Task: c.Task, Timeout: timeout})
		out = &o
	}
	if ctx.Err() != nil {
		return // shutting down: the lease expires and recovery retries the attempt
	}
	elapsed := time.Since(started)
	now := e.clock()
	var retry *store.Execution
	if out.Status != store.ExecSucceeded && out.Retryable {
		retry = retryOf(c.Exec, now)
	}
	comp := repo.Completion{ExecID: c.Exec.ID, Owner: e.cfg.InstanceID, Status: out.Status, Message: out.Message, Result: out.Result,
		ResultTruncated: out.Truncated, FinishedAt: now, DurationMS: elapsed.Milliseconds(), Retry: retry,
		Validity: validity, ValidityMessage: vmsg}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	ok, err := e.st.CompleteAttempt(wctx, comp)
	if err != nil || !ok {
		// Not written: the lease was lost (another replica recovered the
		// attempt) or the store is down (recovery retries it later).
		e.log.Warn("scheduler: attempt outcome not recorded", "execution_id", c.Exec.ID, "type", c.Exec.TypeName, "fenced", !ok, "err", err)
		return
	}
	e.metrics.Run(c.Exec.TypeName, c.Exec.Module, out.Status, elapsed)
	done := c.Exec
	done.Status = out.Status
	e.events.Execution(ctx, done)
	if retry != nil {
		e.metrics.Retry(c.Exec.TypeName, c.Exec.Module)
		e.events.Execution(ctx, *retry)
		return
	}
	e.events.Task(ctx, c.Exec.TenantID, c.Exec.TaskID)
}

// ------------------------------------------------------------------ recover, retention

// RecoverOnce times out attempts whose lease expired and retries them while
// attempts remain.
func (e *Engine) RecoverOnce(ctx context.Context) (int, error) {
	now := e.clock()
	var retries []store.Execution
	lost, err := e.st.RecoverExpired(ctx, now, e.cfg.Batch, func(x store.Execution) *store.Execution {
		r := retryOf(x, now)
		if r != nil {
			retries = append(retries, *r)
		}
		return r
	})
	if err != nil {
		return 0, err
	}
	for _, x := range lost {
		e.log.Warn("scheduler: attempt lease expired", "execution_id", x.ID, "type", x.TypeName, "owner", x.LeaseOwner)
		e.metrics.Run(x.TypeName, x.Module, store.ExecTimedOut, time.Duration(x.DurationMS)*time.Millisecond)
		e.events.Execution(ctx, x)
	}
	for _, r := range retries {
		e.metrics.Retry(r.TypeName, r.Module)
		e.events.Execution(ctx, r)
	}
	return len(lost), nil
}

// PruneOnce deletes finished history older than the retention period.
func (e *Engine) PruneOnce(ctx context.Context) (int64, error) {
	return e.st.PruneBefore(ctx, e.clock().Add(-e.cfg.Retention))
}
