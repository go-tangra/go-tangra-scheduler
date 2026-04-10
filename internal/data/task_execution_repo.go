package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	entCrud "github.com/tx7do/go-crud/entgo"

	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent/taskexecution"
)

// TaskExecutionRepo handles persistence for task execution history.
type TaskExecutionRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewTaskExecutionRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *TaskExecutionRepo {
	return &TaskExecutionRepo{
		log:       ctx.NewLoggerHelper("task-execution/repo/scheduler-service"),
		entClient: entClient,
	}
}

// Create records a new task execution.
func (r *TaskExecutionRepo) Create(ctx context.Context, exec *ent.TaskExecution) error {
	_, err := r.entClient.Client().TaskExecution.Create().
		SetExecutionID(exec.ExecutionID).
		SetTaskType(exec.TaskType).
		SetModuleID(exec.ModuleID).
		SetStatus(exec.Status).
		SetNillableMessage(exec.Message).
		SetAttempt(exec.Attempt).
		SetDurationMs(exec.DurationMs).
		SetStartedAt(exec.StartedAt).
		SetNillableFinishedAt(exec.FinishedAt).
		SetTenantID(exec.TenantID).
		Save(ctx)
	if err != nil {
		r.log.Errorf("insert task execution failed: %v", err)
	}
	return err
}

// UpdateFinished marks an execution as complete.
func (r *TaskExecutionRepo) UpdateFinished(ctx context.Context, executionID string, status string, message string, durationMs int64) error {
	now := time.Now()
	_, err := r.entClient.Client().TaskExecution.Update().
		Where(taskexecution.ExecutionIDEQ(executionID)).
		SetStatus(status).
		SetMessage(message).
		SetDurationMs(durationMs).
		SetFinishedAt(now).
		Save(ctx)
	return err
}

// ListByTaskType returns execution history for a task type, newest first.
func (r *TaskExecutionRepo) ListByTaskType(ctx context.Context, taskType string, page, pageSize int) ([]*ent.TaskExecution, int, error) {
	query := r.entClient.Client().TaskExecution.Query().
		Where(taskexecution.TaskTypeEQ(taskType))

	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}

	items, err := query.
		Order(ent.Desc(taskexecution.FieldStartedAt)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}
