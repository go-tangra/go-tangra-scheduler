//go:build wireinject
// +build wireinject

//go:generate go run github.com/google/wire/cmd/wire

package main

import (
	"github.com/go-kratos/kratos/v2"
	"github.com/google/wire"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	dataProviders "github.com/go-tangra/go-tangra-scheduler/internal/data/providers"
	serverProviders "github.com/go-tangra/go-tangra-scheduler/internal/server/providers"
	serviceProviders "github.com/go-tangra/go-tangra-scheduler/internal/service/providers"
)

// initApp initializes the Wire provider entry for the kratos application
func initApp(*bootstrap.Context) (*kratos.App, func(), error) {
	panic(
		wire.Build(
			dataProviders.ProviderSet,
			serverProviders.ProviderSet,
			serviceProviders.ProviderSet,
			newApp,
		),
	)
}
