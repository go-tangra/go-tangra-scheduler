package executor

import (
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-common/grpcx"
	"github.com/go-tangra/go-tangra-scheduler/internal/data"
)

// ProvideModuleConnPool creates a ModuleConnPool from the ModuleDialer.
func ProvideModuleConnPool(ctx *bootstrap.Context, dialer *grpcx.ModuleDialer) *ModuleConnPool {
	if dialer == nil {
		return nil
	}
	return NewModuleConnPool(dialer, ctx.NewLoggerHelper("executor/conn-pool"))
}

// ProvideRemoteExecutor creates a RemoteExecutor with the CompositeExecutionRecorder.
func ProvideRemoteExecutor(ctx *bootstrap.Context, registry *TaskTypeRegistry, connPool *ModuleConnPool, recorder *data.CompositeExecutionRecorder) *RemoteExecutor {
	return NewRemoteExecutor(
		ctx.NewLoggerHelper("executor/remote"),
		registry,
		connPool,
		recorder,
	)
}
