//go:build wireinject
// +build wireinject

//go:generate go run github.com/google/wire/cmd/wire

package providers

import (
	"github.com/google/wire"

	"github.com/go-tangra/go-tangra-scheduler/internal/cert"
	"github.com/go-tangra/go-tangra-scheduler/internal/server"
)

// ProviderSet is the Wire provider set for server layer
var ProviderSet = wire.NewSet(
	cert.NewCertManager,
	server.NewGRPCServer,
	server.NewHTTPServer,
	server.NewAsynqServer,
)
