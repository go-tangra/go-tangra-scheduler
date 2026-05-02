package service

import (
	"context"

	commonV1 "github.com/go-tangra/go-tangra-common/gen/go/common/service/v1"
	schedulerV1 "github.com/go-tangra/go-tangra-scheduler/gen/go/scheduler/service/v1"
)

// TaskTypeCommonAdapter adapts the scheduler's TaskTypeService to the
// common.service.v1.TaskTypeRegistrationService interface that modules call.
// This allows modules to import only go-tangra-common, not go-tangra-scheduler.
type TaskTypeCommonAdapter struct {
	commonV1.UnimplementedTaskTypeRegistrationServiceServer

	inner *TaskTypeService
}

func NewTaskTypeCommonAdapter(inner *TaskTypeService) *TaskTypeCommonAdapter {
	return &TaskTypeCommonAdapter{inner: inner}
}

func (a *TaskTypeCommonAdapter) RegisterTaskTypes(
	ctx context.Context,
	req *commonV1.RegisterTaskTypesRequest,
) (*commonV1.RegisterTaskTypesResponse, error) {
	descs := make([]*schedulerV1.TaskTypeDescriptor, 0, len(req.GetTaskTypes()))
	for _, d := range req.GetTaskTypes() {
		descs = append(descs, &schedulerV1.TaskTypeDescriptor{
			TaskType:        d.GetTaskType(),
			DisplayName:     d.GetDisplayName(),
			Description:     d.GetDescription(),
			PayloadSchema:   d.GetPayloadSchema(),
			DefaultCron:     d.GetDefaultCron(),
			DefaultMaxRetry: d.GetDefaultMaxRetry(),
		})
	}

	resp, err := a.inner.RegisterTaskTypes(ctx, &schedulerV1.RegisterTaskTypesRequest{
		ModuleId:  req.GetModuleId(),
		TaskTypes: descs,
	})
	if err != nil {
		return nil, err
	}

	return &commonV1.RegisterTaskTypesResponse{
		RegisteredCount: resp.GetRegisteredCount(),
		Message:         resp.GetMessage(),
	}, nil
}

func (a *TaskTypeCommonAdapter) UnregisterTaskTypes(
	ctx context.Context,
	req *commonV1.UnregisterTaskTypesRequest,
) (*commonV1.UnregisterTaskTypesResponse, error) {
	resp, err := a.inner.UnregisterTaskTypes(ctx, &schedulerV1.UnregisterTaskTypesRequest{
		ModuleId: req.GetModuleId(),
	})
	if err != nil {
		return nil, err
	}
	return &commonV1.UnregisterTaskTypesResponse{
		Message: resp.GetMessage(),
	}, nil
}
