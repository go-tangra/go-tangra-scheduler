package server

import (
	"io/fs"
	"net/http"

	kratosHttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/tx7do/kratos-bootstrap/bootstrap"

	"github.com/go-tangra/go-tangra-scheduler/cmd/server/assets"
)

// NewHTTPServer creates an HTTP server for serving embedded frontend assets.
func NewHTTPServer(ctx *bootstrap.Context) *kratosHttp.Server {
	cfg := ctx.GetConfig()
	l := ctx.NewLoggerHelper("scheduler/http")

	var opts []kratosHttp.ServerOption

	if cfg.Server != nil && cfg.Server.Rest != nil {
		if cfg.Server.Rest.Network != "" {
			opts = append(opts, kratosHttp.Network(cfg.Server.Rest.Network))
		}
		if cfg.Server.Rest.Addr != "" {
			opts = append(opts, kratosHttp.Address(cfg.Server.Rest.Addr))
		}
		if cfg.Server.Rest.Timeout != nil {
			opts = append(opts, kratosHttp.Timeout(cfg.Server.Rest.Timeout.AsDuration()))
		}
	}

	srv := kratosHttp.NewServer(opts...)
	route := srv.Route("/")

	// Health check
	route.GET("/health", func(ctx kratosHttp.Context) error {
		return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	route.GET("/openapi.yaml", func(ctx kratosHttp.Context) error {
		ctx.Response().Header().Set("Content-Type", "application/yaml")
		_, err := ctx.Response().Write(assets.OpenApiData)
		return err
	})

	route.GET("/proto-descriptor", func(ctx kratosHttp.Context) error {
		ctx.Response().Header().Set("Content-Type", "application/octet-stream")
		ctx.Response().Header().Set("Content-Disposition", "attachment; filename=descriptor.bin")
		_, err := ctx.Response().Write(assets.DescriptorData)
		return err
	})


	// Serve embedded frontend assets (Module Federation remote)
	fsys, err := fs.Sub(assets.FrontendDist, "frontend-dist")
	if err == nil {
		fileServer := http.FileServer(http.FS(fsys))
		srv.HandlePrefix("/", fileServer)
		l.Infof("Serving embedded frontend assets")
	} else {
		l.Warnf("No embedded frontend assets found: %v", err)
	}

	return srv
}
