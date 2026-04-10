package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonV1 "github.com/go-tangra/go-tangra-common/gen/go/common/service/v1"
	"github.com/go-tangra/go-tangra-common/viewer"
)

// RemoteTaskData is the payload structure for remote task execution.
// The asynq handler deserializes this from the task message.
type RemoteTaskData struct {
	Payload     []byte `json:"payload"`
	TenantID    uint32 `json:"tenantId"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	ScheduledAt time.Time `json:"scheduledAt"`
}

// ExecutionRecorder saves task execution results.
type ExecutionRecorder interface {
	RecordExecution(ctx context.Context, typeName string, status string, message string) error
	RecordExecutionHistory(ctx context.Context, executionID, taskType, moduleID, status, message string, attempt int32, durationMs int64, tenantID uint32) error
}

// RemoteExecutor handles asynq task callbacks by forwarding them
// to the owning module via gRPC TaskExecutorService.
type RemoteExecutor struct {
	log      *log.Helper
	registry *TaskTypeRegistry
	connPool *ModuleConnPool
	recorder ExecutionRecorder
}

func NewRemoteExecutor(
	log *log.Helper,
	registry *TaskTypeRegistry,
	connPool *ModuleConnPool,
	recorder ExecutionRecorder,
) *RemoteExecutor {
	return &RemoteExecutor{
		log:      log,
		registry: registry,
		connPool: connPool,
		recorder: recorder,
	}
}

// Handle is the generic asynq handler for all remote task types.
// When asynq fires a task, this method resolves the owning module
// and calls TaskExecutorService.ExecuteTask via gRPC.
func (e *RemoteExecutor) Handle(taskType string, taskData *RemoteTaskData) error {
	moduleID, ok := e.registry.ResolveModuleID(taskType)
	if !ok {
		e.log.Errorf("unknown task type with no module prefix: %s", taskType)
		return fmt.Errorf("unknown task type: %s", taskType)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, err := e.connPool.Get(ctx, moduleID)
	if err != nil {
		e.log.Errorf("failed to dial module %s for task %s: %v", moduleID, taskType, err)
		e.recordResult(taskType, moduleID, "failed", fmt.Sprintf("gRPC error: %v", err), "", int32(taskData.Attempt), 0, taskData.TenantID)
		return fmt.Errorf("dial module %s: %w", moduleID, err)
	}

	client := commonV1.NewTaskExecutorServiceClient(conn)

	executionID := uuid.New().String()
	e.log.Infof("Executing task %s on module %s (execution=%s, attempt=%d)",
		taskType, moduleID, executionID, taskData.Attempt)

	startTime := time.Now()
	resp, err := client.ExecuteTask(ctx, &commonV1.ExecuteTaskRequest{
		ExecutionId: executionID,
		TaskType:    taskType,
		Payload:     taskData.Payload,
		Attempt:     int32(taskData.Attempt),
		MaxAttempts: int32(taskData.MaxAttempts),
		TenantId:    taskData.TenantID,
		ScheduledAt: timestamppb.New(taskData.ScheduledAt),
	})
	durationMs := time.Since(startTime).Milliseconds()

	if err != nil {
		e.log.Errorf("gRPC call to module %s for task %s failed: %v", moduleID, taskType, err)
		e.recordResult(taskType, moduleID, "failed", fmt.Sprintf("gRPC error: %v", err), executionID, int32(taskData.Attempt), durationMs, taskData.TenantID)
		return fmt.Errorf("execute task %s on module %s: %w", taskType, moduleID, err)
	}

	if resp.GetPermanentFailure() {
		e.log.Warnf("task %s permanent failure on module %s: %s", taskType, moduleID, resp.GetMessage())
		e.recordResult(taskType, moduleID, "failed", resp.GetMessage(), executionID, int32(taskData.Attempt), durationMs, taskData.TenantID)
		return nil
	}

	if !resp.GetSuccess() {
		e.log.Warnf("task %s failed on module %s: %s", taskType, moduleID, resp.GetMessage())
		e.recordResult(taskType, moduleID, "failed", resp.GetMessage(), executionID, int32(taskData.Attempt), durationMs, taskData.TenantID)
		return fmt.Errorf("task %s failed: %s", taskType, resp.GetMessage())
	}

	e.log.Infof("task %s completed successfully on module %s: %s", taskType, moduleID, resp.GetMessage())
	e.recordResult(taskType, moduleID, "success", resp.GetMessage(), executionID, int32(taskData.Attempt), durationMs, taskData.TenantID)
	return nil
}

func (e *RemoteExecutor) recordResult(taskType, moduleID, status, message, executionID string, attempt int32, durationMs int64, tenantID uint32) {
	if e.recorder == nil {
		return
	}
	ctx, cancel := context.WithTimeout(viewer.NewSystemViewerContext(context.Background()), 5*time.Second)
	defer cancel()

	// Update the task's last_run fields
	if err := e.recorder.RecordExecution(ctx, taskType, status, message); err != nil {
		e.log.Warnf("failed to update task %s: %v", taskType, err)
	}

	// Record execution history entry
	if executionID != "" {
		if err := e.recorder.RecordExecutionHistory(ctx, executionID, taskType, moduleID, status, message, attempt, durationMs, tenantID); err != nil {
			e.log.Warnf("failed to record execution history for %s: %v", taskType, err)
		}
	}
}
