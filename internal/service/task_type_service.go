package service

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
	"google.golang.org/protobuf/types/known/emptypb"

	schedulerV1 "github.com/go-tangra/go-tangra-scheduler/gen/go/scheduler/service/v1"
	"github.com/go-tangra/go-tangra-scheduler/internal/data"
	"github.com/go-tangra/go-tangra-scheduler/internal/executor"
)

// TaskTypeService implements the TaskTypeRegistrationService gRPC interface.
// Modules call this service to register their task types with the scheduler.
type TaskTypeService struct {
	schedulerV1.UnimplementedTaskTypeRegistrationServiceServer

	log          *log.Helper
	registry     *executor.TaskTypeRegistry
	taskTypeRepo *data.TaskTypeRepo
}

func NewTaskTypeService(
	ctx *bootstrap.Context,
	registry *executor.TaskTypeRegistry,
	taskTypeRepo *data.TaskTypeRepo,
) *TaskTypeService {
	return &TaskTypeService{
		log:          ctx.NewLoggerHelper("task-type/service/scheduler-service"),
		registry:     registry,
		taskTypeRepo: taskTypeRepo,
	}
}

// LoadFromDB loads persisted task types into the in-memory registry.
// Called on startup to restore state from previous registrations.
func (s *TaskTypeService) LoadFromDB(ctx context.Context) error {
	types, err := s.taskTypeRepo.ListAll(ctx)
	if err != nil {
		return err
	}

	for _, t := range types {
		s.registry.Register(executor.TaskTypeEntry{
			ModuleID:      t.ModuleID,
			TaskType:      t.TaskType,
			DisplayName:   ptrVal(t.DisplayName),
			Description:   ptrVal(t.Description),
			PayloadSchema: ptrVal(t.PayloadSchema),
			DefaultCron:   ptrVal(t.DefaultCron),
			DefaultRetry:  t.DefaultMaxRetry,
			RegisteredAt:  t.RegisteredAt,
		})
	}

	s.log.Infof("Loaded %d task types from database", len(types))
	return nil
}

func (s *TaskTypeService) RegisterTaskTypes(
	ctx context.Context,
	req *schedulerV1.RegisterTaskTypesRequest,
) (*schedulerV1.RegisterTaskTypesResponse, error) {
	moduleID := req.GetModuleId()
	s.log.Infof("Module %s registering %d task types", moduleID, len(req.GetTaskTypes()))

	for _, desc := range req.GetTaskTypes() {
		entry := executor.TaskTypeEntry{
			ModuleID:      moduleID,
			TaskType:      desc.GetTaskType(),
			DisplayName:   desc.GetDisplayName(),
			Description:   desc.GetDescription(),
			PayloadSchema: desc.GetPayloadSchema(),
			DefaultCron:   desc.GetDefaultCron(),
			DefaultRetry:  desc.GetDefaultMaxRetry(),
			RegisteredAt:  time.Now(),
		}

		// Persist to database
		if err := s.taskTypeRepo.UpsertTaskType(ctx, desc, moduleID); err != nil {
			s.log.Errorf("Failed to persist task type %s: %v", desc.GetTaskType(), err)
			return nil, err
		}

		// Update in-memory registry
		s.registry.Register(entry)

		s.log.Infof("Registered task type: %s (module=%s, display=%s)",
			desc.GetTaskType(), moduleID, desc.GetDisplayName())
	}

	return &schedulerV1.RegisterTaskTypesResponse{
		RegisteredCount: int32(len(req.GetTaskTypes())),
		Message:         "Task types registered successfully",
	}, nil
}

func (s *TaskTypeService) UnregisterTaskTypes(
	ctx context.Context,
	req *schedulerV1.UnregisterTaskTypesRequest,
) (*schedulerV1.UnregisterTaskTypesResponse, error) {
	moduleID := req.GetModuleId()

	count := s.registry.UnregisterModule(moduleID)

	if _, err := s.taskTypeRepo.DeleteByModule(ctx, moduleID); err != nil {
		s.log.Errorf("Failed to delete task types for module %s from DB: %v", moduleID, err)
	}

	s.log.Infof("Unregistered %d task types for module %s", count, moduleID)
	return &schedulerV1.UnregisterTaskTypesResponse{
		Message: fmt.Sprintf("Unregistered %d task types for module %s", count, moduleID),
	}, nil
}

func (s *TaskTypeService) ListRegisteredTaskTypes(
	_ context.Context,
	_ *emptypb.Empty,
) (*schedulerV1.ListRegisteredTaskTypesResponse, error) {
	entries := s.registry.ListAll()

	result := make([]*schedulerV1.RegisteredTaskType, 0, len(entries))
	for _, e := range entries {
		result = append(result, &schedulerV1.RegisteredTaskType{
			TaskType:        e.TaskType,
			ModuleId:        e.ModuleID,
			DisplayName:     e.DisplayName,
			Description:     e.Description,
			PayloadSchema:   e.PayloadSchema,
			DefaultCron:     e.DefaultCron,
			DefaultMaxRetry: e.DefaultRetry,
		})
	}

	return &schedulerV1.ListRegisteredTaskTypesResponse{
		TaskTypes: result,
	}, nil
}

func ptrVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
