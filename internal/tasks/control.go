package tasks

import (
	"context"
	"errors"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Control actions.
const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
)

// setEnabled starts (enabled=true, next run recomputed from now) or stops a
// periodic task inside a mutation.
func (s *Service) setEnabled(t *store.Task, enabled bool, by string) error {
	if t.Kind != store.KindPeriodic {
		return refuse(ReasonNotPeriodic, nil)
	}
	now := s.Now()
	t.Enabled, t.NextRunAt = enabled, nil
	if enabled {
		sc, err := s.ParseSchedule(t.Cron, t.Timezone)
		if err != nil {
			return err
		}
		t.NextRunAt = sc.Next(now)
	}
	t.UpdatedBy, t.UpdatedAt = by, now
	return nil
}

func (s *Service) control(ctx context.Context, subj authz.Subjects, id, action string) (View, error) {
	t, _, err := s.st.MutateTask(ctx, Scope(subj), id, func(t *store.Task, _ bool) (repo.Effects, error) {
		return repo.Effects{}, s.setEnabled(t, action != ActionStop, subj.ActorID())
	})
	if err != nil {
		return View{}, err
	}
	typ := map[string]audit.EventType{ActionStart: audit.TaskStart, ActionStop: audit.TaskStop, ActionRestart: audit.TaskRestart}[action]
	s.record(ctx, subj, typ, t.TenantID, t.ID, nil)
	s.events.Task(ctx, t.TenantID, t.ID)
	return s.viewOne(ctx, t), nil
}

// Start enables a periodic task and recomputes its next run from now.
func (s *Service) Start(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	return s.control(ctx, subj, id, ActionStart)
}

// Stop disables a periodic task: it no longer fires until started.
func (s *Service) Stop(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	return s.control(ctx, subj, id, ActionStop)
}

// Restart is stop + start of a periodic task.
func (s *Service) Restart(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	return s.control(ctx, subj, id, ActionRestart)
}

// Run queues one manual attempt now ("run now", or "run again" for a finished
// one-shot task) without changing the schedule; it returns the execution id.
func (s *Service) Run(ctx context.Context, subj authz.Subjects, id string) (string, error) {
	now := s.Now()
	var x store.Execution
	t, _, err := s.st.MutateTask(ctx, Scope(subj), id, func(t *store.Task, busy bool) (repo.Effects, error) {
		if busy {
			return repo.Effects{}, refuse(ReasonRunInProgress, nil)
		}
		switch t.Validity {
		case store.ValidityTypeUnavailable:
			return repo.Effects{}, refuse(ReasonTypeUnavailable, map[string]any{"type_name": t.TypeName})
		case store.ValidityPayloadInvalid:
			return repo.Effects{}, refuse(ReasonInvalidPayload, map[string]any{"field": "", "message": t.ValidityMessage})
		}
		x = NewAttempt(*t, store.TriggerManual, subj.ActorID(), now)
		return repo.Effects{Insert: &x}, nil
	})
	if err != nil {
		return "", err
	}
	s.record(ctx, subj, audit.TaskRun, t.TenantID, t.ID, map[string]any{"execution_id": x.ID, "trigger": store.TriggerManual})
	s.events.Execution(ctx, x)
	return x.ID, nil
}

// Cancel ends a one-shot task that has not finished: no further run, queued
// attempts cancelled (a running attempt finishes and is recorded).
func (s *Service) Cancel(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	now := s.Now()
	t, res, err := s.st.MutateTask(ctx, Scope(subj), id, func(t *store.Task, _ bool) (repo.Effects, error) {
		if !t.OneShot() || t.Status != store.TaskActive {
			return repo.Effects{}, refuse(ReasonNotCancellable, nil)
		}
		t.Status, t.NextRunAt = store.TaskCancelled, nil
		t.UpdatedBy, t.UpdatedAt = subj.ActorID(), now
		return repo.Effects{CancelQueued: true, CancelMessage: "task cancelled"}, nil
	})
	if err != nil {
		return View{}, err
	}
	s.record(ctx, subj, audit.TaskCancel, t.TenantID, t.ID, map[string]any{"cancelled_attempts": res.Cancelled})
	s.events.Task(ctx, t.TenantID, t.ID)
	return s.viewOne(ctx, t), nil
}

// Bulk starts, stops or restarts every periodic task in the caller's scope
// (their tenant, or every tenant for a platform administrator) and reports
// how many tasks changed. One-shot tasks are never touched (FR-015).
func (s *Service) Bulk(ctx context.Context, subj authz.Subjects, action string) (int, error) {
	if action != ActionStart && action != ActionStop && action != ActionRestart {
		return 0, field(ReasonValidation, "action", "")
	}
	scope := Scope(subj)
	ids, err := s.st.TaskIDs(ctx, scope, store.TaskFilter{Periodic: true})
	if err != nil {
		return 0, err
	}
	affected := 0
	for _, id := range ids {
		changed := false
		t, _, err := s.st.MutateTask(ctx, scope, id, func(t *store.Task, _ bool) (repo.Effects, error) {
			want := action != ActionStop
			if action != ActionRestart && t.Enabled == want {
				return repo.Effects{}, errUnchanged
			}
			changed = true
			return repo.Effects{}, s.setEnabled(t, want, subj.ActorID())
		})
		switch {
		case errors.Is(err, errUnchanged), errors.Is(err, repo.ErrNotFound):
			continue // unchanged, or deleted meanwhile
		case err != nil:
			var re *Error
			if errors.As(err, &re) {
				continue // a stored schedule that no longer validates stays as is
			}
			return affected, err
		}
		if changed {
			affected++
			s.events.Task(ctx, t.TenantID, t.ID)
		}
	}
	audit.Emit(ctx, s.audit, audit.Event{TenantID: tenantOf(subj), EventType: audit.TasksBulk, ActorKind: audit.ActorOf(subj.ActorKind),
		ActorID: subj.ActorID(), SubjectKind: audit.SubjectSystem, Outcome: audit.OutcomeOK,
		Details: map[string]any{"action": action, "affected": affected, "all_tenants": scope.All}})
	return affected, nil
}

var errUnchanged = errors.New("tasks: unchanged")

// tenantOf is the audit tenant of a caller (the nil tenant for the system).
func tenantOf(subj authz.Subjects) string {
	if subj.TenantID == "" {
		return audit.NilTenant
	}
	return subj.TenantID
}
