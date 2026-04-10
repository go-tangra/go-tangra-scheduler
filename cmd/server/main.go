package main

import (
	"context"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/transport/grpc"
	kratosHttp "github.com/go-kratos/kratos/v2/transport/http"
	asynqServer "github.com/tx7do/kratos-transport/transport/asynq"

	conf "github.com/tx7do/kratos-bootstrap/api/gen/go/conf/v1"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-common/registration"
	"github.com/go-tangra/go-tangra-common/service"
	"github.com/go-tangra/go-tangra-scheduler/cmd/server/assets"
)

var (
	moduleID    = "scheduler"
	moduleName  = "Scheduler"
	version     = "1.0.0"
	description = "Distributed task scheduling with cron, delayed, and wait-result execution"
)

var globalRegHelper *registration.RegistrationHelper

func newApp(
	ctx *bootstrap.Context,
	gs *grpc.Server,
	hs *kratosHttp.Server,
	as *asynqServer.Server,
	regClient *registration.Client,
) *kratos.App {
	if regClient != nil {
		regClient.SetConfig(&registration.Config{
			ModuleID:         moduleID,
			ModuleName:       moduleName,
			Version:          version,
			Description:      description,
			GRPCEndpoint:     registration.GetGRPCAdvertiseAddr(ctx, "0.0.0.0:10500"),
			FrontendEntryUrl: registration.GetEnvOrDefault("FRONTEND_ENTRY_URL", ""),
			HttpEndpoint:     registration.GetEnvOrDefault("HTTP_ADVERTISE_ADDR", ""),
			OpenapiSpec:      assets.OpenApiData,
			ProtoDescriptor:  assets.DescriptorData,
			MenusYaml:        assets.MenusData,
		})
		globalRegHelper = registration.StartRegistrationWithClient(ctx.GetLogger(), regClient)
	}

	return bootstrap.NewApp(ctx, gs, hs, as)
}

func runApp() error {
	ctx := bootstrap.NewContext(
		context.Background(),
		&conf.AppInfo{
			Project: service.Project,
			AppId:   "scheduler.service",
			Version: version,
		},
	)

	defer func() {
		if globalRegHelper != nil {
			globalRegHelper.Stop()
		}
	}()

	return bootstrap.RunApp(ctx, initApp)
}

func main() {
	if err := runApp(); err != nil {
		panic(err)
	}
}
