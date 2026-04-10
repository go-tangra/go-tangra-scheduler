package data

import (
	"context"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent"
)

// CompositeExecutionRecorder records execution results to both
// the task table (last_run fields) and the execution history table.
type CompositeExecutionRecorder struct {
	taskRepo      *TaskRepo
	executionRepo *TaskExecutionRepo
}

func NewCompositeExecutionRecorder(taskRepo *TaskRepo, executionRepo *TaskExecutionRepo) *CompositeExecutionRecorder {
	return &CompositeExecutionRecorder{
		taskRepo:      taskRepo,
		executionRepo: executionRepo,
	}
}

func (r *CompositeExecutionRecorder) RecordExecution(ctx context.Context, typeName string, status string, message string) error {
	return r.taskRepo.UpdateExecution(ctx, typeName, status, message)
}

func (r *CompositeExecutionRecorder) RecordExecutionHistory(ctx context.Context, executionID, taskType, moduleID, status, message string, attempt int32, durationMs int64, tenantID uint32) error {
	now := time.Now()
	return r.executionRepo.Create(ctx, &ent.TaskExecution{
		ExecutionID: executionID,
		TaskType:    taskType,
		ModuleID:    moduleID,
		Status:      status,
		Message:     &message,
		Attempt:     attempt,
		DurationMs:  durationMs,
		StartedAt:   now.Add(-time.Duration(durationMs) * time.Millisecond),
		FinishedAt:  &now,
		TenantID:    tenantID,
	})
}
