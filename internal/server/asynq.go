package server

import (
	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/tx7do/kratos-bootstrap/bootstrap"
	bootstrapAsynq "github.com/tx7do/kratos-bootstrap/transport/asynq"
	asynqServer "github.com/tx7do/kratos-transport/transport/asynq"

	"github.com/go-tangra/go-tangra-common/viewer"
	"github.com/go-tangra/go-tangra-scheduler/internal/executor"
	"github.com/go-tangra/go-tangra-scheduler/internal/service"
)

// NewAsynqServer creates a new asynq server for distributed task execution.
// Uses the RemoteExecutor as the generic handler — all task types are
// dispatched to their owning module via gRPC TaskExecutorService.
func NewAsynqServer(
	ctx *bootstrap.Context,
	taskService *service.TaskService,
	taskTypeService *service.TaskTypeService,
	remoteExecutor *executor.RemoteExecutor,
	registry *executor.TaskTypeRegistry,
) (*asynqServer.Server, error) {
	cfg := ctx.GetConfig()

	if cfg == nil || cfg.Server == nil || cfg.Server.Asynq == nil {
		return nil, nil
	}

	srv := bootstrapAsynq.NewAsynqServer(cfg.Server.Asynq)

	// Allow TaskTypeService to register asynq handlers dynamically
	// when modules register task types after startup
	taskTypeService.SetAsynqServer(srv)

	taskService.RegisterTaskScheduler(srv)

	// Load persisted task types from DB and register handlers
	systemCtx := viewer.NewSystemViewerContext(ctx.Context())
	if err := taskTypeService.LoadFromDB(systemCtx); err != nil {
		log.Warnf("Failed to load task types from DB: %v", err)
	}

	// Register a remote executor handler for each persisted task type
	for _, entry := range registry.ListAll() {
		if err := asynqServer.RegisterSubscriber(srv, entry.TaskType, remoteExecutor.Handle); err != nil {
			log.Warnf("Failed to register handler for task type %s: %v", entry.TaskType, err)
		}
	}

	// Start all enabled tasks
	if _, err := taskService.StartAllTasks(systemCtx, &emptypb.Empty{}); err != nil {
		log.Warn(err)
	}

	return srv, nil
}
