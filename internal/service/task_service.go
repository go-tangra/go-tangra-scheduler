package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/hibiken/asynq"
	paginationV1 "github.com/tx7do/go-crud/api/gen/go/pagination/v1"
	"github.com/tx7do/go-utils/trans"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/go-tangra/go-tangra-common/grpcx"

	"github.com/go-tangra/go-tangra-scheduler/internal/data"
	"github.com/go-tangra/go-tangra-scheduler/internal/executor"

	schedulerV1 "github.com/go-tangra/go-tangra-scheduler/gen/go/scheduler/service/v1"
)

// TaskScheduler defines the interface for the underlying task execution engine.
type TaskScheduler interface {
	TaskTypeExists(taskType string) bool
	GetRegisteredTaskTypes() []string

	NewTask(typeName string, msg any, opts ...asynq.Option) error
	NewWaitResultTask(typeName string, msg any, opts ...asynq.Option) error
	NewPeriodicTask(cronSpec, typeName string, msg any, opts ...asynq.Option) (string, error)

	RemovePeriodicTask(id string) error
	RemoveAllPeriodicTask()
}

// TaskService implements the SchedulerTaskService gRPC interface.
type TaskService struct {
	schedulerV1.UnimplementedSchedulerTaskServiceServer

	log *log.Helper

	taskScheduler  TaskScheduler
	taskRepo       *data.TaskRepo
	executionRepo  *data.TaskExecutionRepo
	registry       *executor.TaskTypeRegistry
}

func NewTaskService(
	ctx *bootstrap.Context,
	taskRepo *data.TaskRepo,
	executionRepo *data.TaskExecutionRepo,
	registry *executor.TaskTypeRegistry,
) *TaskService {
	return &TaskService{
		log:           ctx.NewLoggerHelper("task/service/scheduler-service"),
		executionRepo: executionRepo,
		taskRepo: taskRepo,
		registry: registry,
	}
}

func (s *TaskService) RegisterTaskScheduler(taskScheduler TaskScheduler) {
	s.taskScheduler = taskScheduler
}

func (s *TaskService) ListTasks(ctx context.Context, req *schedulerV1.ListTasksRequest) (*schedulerV1.ListTasksResponse, error) {
	pagingReq := &paginationV1.PagingRequest{}
	if req != nil {
		if req.Page != nil {
			p := uint32(*req.Page)
			pagingReq.Page = &p
		}
		if req.PageSize != nil {
			ps := uint32(*req.PageSize)
			pagingReq.PageSize = &ps
		}
		if req.NoPaging != nil {
			pagingReq.NoPaging = req.NoPaging
		}
	}
	return s.taskRepo.List(ctx, pagingReq)
}

func (s *TaskService) GetTask(ctx context.Context, req *schedulerV1.GetTaskRequest) (*schedulerV1.Task, error) {
	return s.taskRepo.Get(ctx, req)
}

func (s *TaskService) ListTaskTypeNames(_ context.Context, _ *emptypb.Empty) (*schedulerV1.ListTaskTypeNamesResponse, error) {
	if s.taskScheduler == nil {
		return &schedulerV1.ListTaskTypeNamesResponse{}, nil
	}
	typeNames := s.taskScheduler.GetRegisteredTaskTypes()
	return &schedulerV1.ListTaskTypeNamesResponse{
		TypeNames: typeNames,
	}, nil
}

func (s *TaskService) CreateTask(ctx context.Context, req *schedulerV1.CreateTaskRequest) (*schedulerV1.Task, error) {
	if req.Data == nil {
		return nil, schedulerV1.ErrorBadRequest("invalid parameter")
	}

	userID := grpcx.GetUserIDAsUint32(ctx)
	req.Data.CreatedBy = userID

	// Auto-resolve module_id from task type registry
	if req.Data.ModuleId == nil || *req.Data.ModuleId == "" {
		if moduleID, ok := s.registry.ResolveModuleID(req.Data.GetTypeName()); ok {
			req.Data.ModuleId = &moduleID
		}
	}

	t, err := s.taskRepo.Create(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := s.startTask(t); err != nil {
		s.log.Errorf("failed to start task %s after creation: %v", t.GetTypeName(), err)
	}

	return t, nil
}

func (s *TaskService) UpdateTask(ctx context.Context, req *schedulerV1.UpdateTaskRequest) (*schedulerV1.Task, error) {
	if req.Data == nil {
		return nil, schedulerV1.ErrorBadRequest("invalid parameter")
	}

	userID := grpcx.GetUserIDAsUint32(ctx)
	req.Data.UpdatedBy = userID
	if req.UpdateMask != nil {
		req.UpdateMask.Paths = append(req.UpdateMask.Paths, "updated_by")
	}

	t, err := s.taskRepo.Update(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := s.startTask(t); err != nil {
		s.log.Errorf("failed to restart task %s after update: %v", t.GetTypeName(), err)
	}

	return t, nil
}

func (s *TaskService) DeleteTask(ctx context.Context, req *schedulerV1.DeleteTaskRequest) (*emptypb.Empty, error) {
	t, err := s.taskRepo.Get(ctx, &schedulerV1.GetTaskRequest{QueryBy: &schedulerV1.GetTaskRequest_Id{Id: req.GetId()}})
	if err != nil {
		s.log.Errorf("failed to get task before delete: %v", err)
	}

	if err := s.taskRepo.Delete(ctx, req); err != nil {
		return nil, err
	}

	if t != nil {
		if err := s.stopTask(t); err != nil {
			s.log.Warnf("failed to stop task %s on delete: %v", t.GetTypeName(), err)
		}
	}

	return &emptypb.Empty{}, nil
}

func (s *TaskService) ControlTask(ctx context.Context, req *schedulerV1.ControlTaskRequest) (*emptypb.Empty, error) {
	t, err := s.taskRepo.Get(ctx, &schedulerV1.GetTaskRequest{QueryBy: &schedulerV1.GetTaskRequest_TypeName{TypeName: req.GetTypeName()}})
	if err != nil {
		s.log.Errorf("failed to get task %s: %v", req.GetTypeName(), err)
		return nil, err
	}

	switch req.GetControlType() {
	case schedulerV1.ControlTaskRequest_RESTART:
		if err := s.stopTask(t); err != nil {
			return nil, err
		}
		if err := s.startTask(t); err != nil {
			return nil, err
		}

	case schedulerV1.ControlTaskRequest_STOP:
		if err := s.stopTask(t); err != nil {
			return nil, err
		}

	case schedulerV1.ControlTaskRequest_START:
		if err := s.startTask(t); err != nil {
			return nil, err
		}
	}

	return &emptypb.Empty{}, nil
}

func (s *TaskService) StopAllTasks(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.stopAllTasks()
	return &emptypb.Empty{}, nil
}

func (s *TaskService) StartAllTasks(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if _, err := s.startAllTasks(ctx); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *TaskService) RestartAllTasks(ctx context.Context, _ *emptypb.Empty) (*schedulerV1.RestartAllTasksResponse, error) {
	s.stopAllTasks()

	count, err := s.startAllTasks(ctx)

	return &schedulerV1.RestartAllTasksResponse{
		Count: count,
	}, err
}

func (s *TaskService) startAllTasks(ctx context.Context) (int32, error) {
	resp, err := s.ListTasks(ctx, &schedulerV1.ListTasksRequest{
		NoPaging: trans.Ptr(true),
	})
	if err != nil {
		s.log.Errorf("failed to list tasks: %v", err)
		return 0, err
	}

	s.log.Infof("starting all tasks, total: %d", resp.GetTotal())

	var count int32
	for _, t := range resp.GetItems() {
		if s.startTask(t) == nil {
			count++
		}
	}

	s.log.Infof("successfully started %d tasks", count)

	return count, nil
}

func (s *TaskService) stopAllTasks() {
	if s.taskScheduler == nil {
		return
	}
	s.log.Info("stopping all periodic tasks...")
	s.taskScheduler.RemoveAllPeriodicTask()
	s.log.Info("all periodic tasks stopped")
}

func (s *TaskService) stopTask(t *schedulerV1.Task) error {
	if t == nil {
		return errors.New("task is nil")
	}

	if !t.GetEnable() {
		return errors.New("task is not enabled")
	}

	if s.taskScheduler == nil {
		return errors.New("task scheduler not initialized")
	}

	switch t.GetType() {
	case schedulerV1.Task_PERIODIC:
		return s.taskScheduler.RemovePeriodicTask(t.GetTypeName())
	case schedulerV1.Task_DELAY, schedulerV1.Task_WAIT_RESULT:
		// fire-and-forget tasks cannot be stopped
	}

	return nil
}

func (s *TaskService) convertTaskOption(t *schedulerV1.Task) (opts []asynq.Option, payload any) {
	if t == nil {
		return
	}

	if len(t.GetTaskPayload()) > 0 {
		if err := json.Unmarshal([]byte(t.GetTaskPayload()), &payload); err != nil {
			s.log.Warnf("failed to unmarshal task payload for %s: %v", t.GetTypeName(), err)
		}
	}
	// Ensure payload is never nil (asynq requires non-nil message)
	if payload == nil {
		payload = map[string]any{}
	}

	if t.TaskOptions != nil {
		if t.GetTaskOptions().GetMaxRetry() > 0 {
			opts = append(opts, asynq.MaxRetry(int(t.GetTaskOptions().GetMaxRetry())))
		}
		if t.GetTaskOptions().Timeout != nil {
			opts = append(opts, asynq.Timeout(t.GetTaskOptions().GetTimeout().AsDuration()))
		}
		if t.GetTaskOptions().Deadline != nil {
			opts = append(opts, asynq.Deadline(t.GetTaskOptions().GetDeadline().AsTime()))
		}
		if t.GetTaskOptions().ProcessIn != nil {
			opts = append(opts, asynq.ProcessIn(t.GetTaskOptions().GetProcessIn().AsDuration()))
		}
		if t.GetTaskOptions().ProcessAt != nil {
			opts = append(opts, asynq.ProcessAt(t.GetTaskOptions().GetProcessAt().AsTime()))
		}
		if t.GetTaskOptions().UniqueTtl != nil {
			opts = append(opts, asynq.Unique(t.GetTaskOptions().GetUniqueTtl().AsDuration()))
		}
		if t.GetTaskOptions().Retention != nil {
			opts = append(opts, asynq.Retention(t.GetTaskOptions().GetRetention().AsDuration()))
		}
		if t.GetTaskOptions().Group != nil {
			opts = append(opts, asynq.Group(t.GetTaskOptions().GetGroup()))
		}
		if t.GetTaskOptions().TaskId != nil {
			opts = append(opts, asynq.TaskID(t.GetTaskOptions().GetTaskId()))
		}
	}

	return
}

func (s *TaskService) startTask(t *schedulerV1.Task) error {
	if t == nil {
		return errors.New("task is nil")
	}

	if !t.GetEnable() {
		return errors.New("task is not enabled")
	}

	if s.taskScheduler == nil {
		return errors.New("task scheduler not initialized")
	}

	opts, payload := s.convertTaskOption(t)

	switch t.GetType() {
	case schedulerV1.Task_PERIODIC:
		if _, err := s.taskScheduler.NewPeriodicTask(t.GetCronSpec(), t.GetTypeName(), payload, opts...); err != nil {
			s.log.Errorf("[%s] failed to create periodic task: %v", t.GetTypeName(), err)
			return err
		}

	case schedulerV1.Task_DELAY:
		if err := s.taskScheduler.NewTask(t.GetTypeName(), payload, opts...); err != nil {
			s.log.Errorf("[%s] failed to create delayed task: %v", t.GetTypeName(), err)
			return err
		}

	case schedulerV1.Task_WAIT_RESULT:
		if err := s.taskScheduler.NewWaitResultTask(t.GetTypeName(), payload, opts...); err != nil {
			s.log.Errorf("[%s] failed to create wait-result task: %v", t.GetTypeName(), err)
			return err
		}
	}

	return nil
}

func (s *TaskService) ListTaskExecutions(ctx context.Context, req *schedulerV1.ListTaskExecutionsRequest) (*schedulerV1.ListTaskExecutionsResponse, error) {
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	pageSize := int(req.GetPageSize())
	if pageSize < 1 {
		pageSize = 20
	}

	items, total, err := s.executionRepo.ListByTaskType(ctx, req.GetTaskType(), page, pageSize)
	if err != nil {
		return nil, err
	}

	result := make([]*schedulerV1.TaskExecution, 0, len(items))
	for _, item := range items {
		exec := &schedulerV1.TaskExecution{
			ExecutionId: item.ExecutionID,
			TaskType:    item.TaskType,
			ModuleId:    item.ModuleID,
			Status:      item.Status,
			Message:     item.Message,
			Attempt:     item.Attempt,
			DurationMs:  item.DurationMs,
			StartedAt:   timestamppb.New(item.StartedAt),
		}
		if item.FinishedAt != nil {
			exec.FinishedAt = timestamppb.New(*item.FinishedAt)
		}
		result = append(result, exec)
	}

	return &schedulerV1.ListTaskExecutionsResponse{
		Items: result,
		Total: int32(total),
	}, nil
}

