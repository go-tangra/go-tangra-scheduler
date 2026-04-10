package data

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	entCrud "github.com/tx7do/go-crud/entgo"

	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent"
	"github.com/go-tangra/go-tangra-scheduler/internal/data/ent/tasktype"

	schedulerV1 "github.com/go-tangra/go-tangra-scheduler/gen/go/scheduler/service/v1"
)

// TaskTypeRepo handles persistence for registered task types.
type TaskTypeRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	log       *log.Helper
}

func NewTaskTypeRepo(ctx *bootstrap.Context, entClient *entCrud.EntClient[*ent.Client]) *TaskTypeRepo {
	return &TaskTypeRepo{
		log:       ctx.NewLoggerHelper("task-type/repo/scheduler-service"),
		entClient: entClient,
	}
}

// UpsertTaskType creates or updates a task type registration.
func (r *TaskTypeRepo) UpsertTaskType(ctx context.Context, desc *schedulerV1.TaskTypeDescriptor, moduleID string) error {
	err := r.entClient.Client().TaskType.Create().
		SetTaskType(desc.GetTaskType()).
		SetModuleID(moduleID).
		SetNillableDisplayName(strPtr(desc.GetDisplayName())).
		SetNillableDescription(strPtr(desc.GetDescription())).
		SetNillablePayloadSchema(strPtr(desc.GetPayloadSchema())).
		SetNillableDefaultCron(strPtr(desc.GetDefaultCron())).
		SetDefaultMaxRetry(desc.GetDefaultMaxRetry()).
		SetRegisteredAt(time.Now()).
		OnConflictColumns(tasktype.FieldTaskType).
		UpdateModuleID().
		UpdateDisplayName().
		UpdateDescription().
		UpdatePayloadSchema().
		UpdateDefaultCron().
		UpdateDefaultMaxRetry().
		UpdateRegisteredAt().
		Exec(ctx)
	if err != nil {
		r.log.Errorf("upsert task type %s failed: %v", desc.GetTaskType(), err)
		return err
	}
	return nil
}

// DeleteByModule removes all task types for a module.
func (r *TaskTypeRepo) DeleteByModule(ctx context.Context, moduleID string) (int, error) {
	count, err := r.entClient.Client().TaskType.Delete().
		Where(tasktype.ModuleIDEQ(moduleID)).
		Exec(ctx)
	if err != nil {
		r.log.Errorf("delete task types for module %s failed: %v", moduleID, err)
		return 0, err
	}
	return count, nil
}

// ListAll returns all registered task types.
func (r *TaskTypeRepo) ListAll(ctx context.Context) ([]*ent.TaskType, error) {
	return r.entClient.Client().TaskType.Query().All(ctx)
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
