//go:build wireinject
// +build wireinject

//go:generate go run github.com/google/wire/cmd/wire

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-scheduler/internal/data"
	"github.com/go-tangra/go-tangra-scheduler/internal/executor"
	"github.com/go-tangra/go-tangra-scheduler/internal/metrics"
	"github.com/go-tangra/go-tangra-scheduler/internal/service"
)

// ProviderSet is the Wire provider set for service layer
var ProviderSet = wire.NewSet(
	service.NewTaskService,
	service.NewTaskTypeService,
	service.NewBackupService,
	data.NewCompositeExecutionRecorder,
	executor.NewTaskTypeRegistry,
	executor.ProvideRemoteExecutor,
	executor.ProvideModuleConnPool,
	metrics.NewCollector,
)
