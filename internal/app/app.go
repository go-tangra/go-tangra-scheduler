// Package app wires the scheduler service: configuration -> Freya runtime
// (mesh identity, admin listener) -> store/audit/events/metrics -> the type
// registry, the task service, backup and the engine -> the mesh HTTP surface
// (reached only through the gateway) and the scheduler.v1.Registration gRPC
// surface (module-to-module), plus gateway registration and permission
// seeding. It refuses to start without a store, an event bus and a gateway
// issuer (config.Validate).
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-lcm/sdk/v4/pkg/lcmidentity"
	"github.com/go-tangra/go-tangra/v4"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/config"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/dispatch"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/engine"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/grpcapi"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/metrics"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream/valkeykv"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
	"github.com/go-tangra/go-tangra-scheduler/v4/pkg/schedulermanifest"
)

// Options override infrastructure (tests) and attach optional parts.
type Options struct {
	Logger     slog.Handler
	Verifier   httpapi.Verifier
	Checker    authz.Checker     // API-permission checker override (default: auth Authorization/Check)
	Repo       repo.Store        // store override (tests: memstore); skips the DB
	Stream     stream.Client     // event-bus client override (tests: stream.NewMemory())
	Dispatcher engine.Dispatcher // executor override (default: the mesh dispatcher)
	Now        func() time.Time  // clock override (tests)
	Freya      []freya.Option
	Migrate    bool
	Remote     fs.FS // built federated UI remote (nil serves no remote)
}

// App is the wired service.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Freya    *freya.App
	Store    *store.Store
	Repo     repo.Store
	Audit    *audit.Writer
	Verifier httpapi.Verifier
	Checker  authz.Checker
	Hub      *stream.Hub
	Events   events.Emitter
	Metrics  *metrics.Metrics
	Registry *registry.Registry
	Tasks    *tasks.Service
	Backup   *backup.Service
	Engine   *engine.Engine
	HTTP     *httpapi.Server
	GRPC     *grpcapi.Server

	closers []func()
}

// InstanceID returns the configured engine instance id or hostname + a
// random suffix (unique per process, stable for its lifetime).
func InstanceID(configured string) string {
	if configured != "" {
		return configured
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "scheduler"
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return host + "-" + hex.EncodeToString(b[:])
}

// Build wires the service.
func Build(ctx context.Context, cfg config.Config, o Options) (a *App, err error) {
	authz.PlatformTenant = cfg.PlatformTenantID // its admins and owners act as platform administrators
	a = &App{Cfg: cfg}
	handler := o.Logger
	if handler == nil {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	a.Log = slog.New(handler)
	built := a
	defer func() { // release what was opened when wiring fails half-way
		if err != nil {
			built.Close()
		}
	}()
	now := o.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err = a.buildRuntime(ctx, cfg, handler, o.Freya); err != nil {
		return nil, err
	}
	if err = a.buildStorage(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildPeers(ctx, cfg, o); err != nil {
		return nil, err
	}
	if err = a.buildEvents(cfg, o.Stream); err != nil {
		return nil, err
	}
	if a.Metrics, err = metrics.New(a.Freya.Metrics().Meter(metrics.Scope), a.Repo.CountTypesByModule); err != nil {
		return nil, fmt.Errorf("metrics: %w", err)
	}

	// Domain services.
	schemas := registry.NewSchemas()
	a.Registry = registry.New(registry.Deps{Store: a.Repo, Audit: a.Audit, Now: now, MaxPayloadBytes: cfg.Limits.MaxPayloadBytes, Schemas: schemas})
	a.Tasks = tasks.New(tasks.Deps{Store: a.Repo, Registry: a.Registry, Audit: a.Audit, Events: a.Events, Now: now,
		Limits: tasks.Limits{MaxPayloadBytes: cfg.Limits.MaxPayloadBytes, MaxTimeoutSeconds: cfg.Limits.MaxTimeoutSeconds,
			MinIntervalSeconds: cfg.Limits.MinIntervalSeconds, MaxPageSize: cfg.Limits.MaxPageSize, MaxTasksPerTenant: cfg.Limits.MaxTasksPerTenant}})
	a.Backup = backup.New(a.Repo, a.Audit, schemas, now, cfg.Limits.MaxPayloadBytes)

	// Engine: plans and dispatches occurrences to the owning modules.
	disp := o.Dispatcher
	if disp == nil {
		disp = dispatch.New(func(ctx context.Context, module string) (grpc.ClientConnInterface, error) {
			return a.Freya.Client(ctx, module)
		}, cfg.Limits.MaxResultBytes)
	}
	a.Engine = engine.New(engine.Config{Tick: cfg.Tick(), Workers: cfg.Engine.Workers, Batch: cfg.Engine.Batch,
		MisfireGrace: cfg.MisfireGrace(), LeaseGrace: cfg.LeaseGrace(), Retention: cfg.Retention(), RetentionInterval: cfg.RetentionInterval(),
		InstanceID: InstanceID(cfg.Engine.InstanceID), MaxPayloadBytes: cfg.Limits.MaxPayloadBytes},
		engine.Deps{Store: a.Repo, Dispatcher: disp, Schemas: schemas, Metrics: a.Metrics, Events: a.Events, Log: a.Log, Now: now})

	// Mesh HTTP surface (reached only through the gateway).
	hopts := []httpapi.Option{httpapi.WithVerifier(a.Verifier), httpapi.WithChecker(a.Checker), httpapi.WithLogger(a.Log)}
	if o.Remote != nil {
		hopts = append(hopts, httpapi.WithRemote(o.Remote))
	}
	if a.HTTP, err = httpapi.NewHandler(a.Freya, hopts...); err != nil {
		return nil, err
	}
	a.HTTP.Register(httpapi.Deps{Hub: a.Hub, Health: a.health, Registry: a.Registry, Tasks: a.Tasks, Backup: a.Backup,
		MaxBackupBytes: cfg.Limits.MaxBackupBytes})
	a.Freya.HTTP().HandlePrefix("/", a.HTTP.Handler())

	// Service-to-service gRPC surface (scheduler.v1.Registration), SPIFFE mTLS.
	a.GRPC = grpcapi.New(a.Registry, cfg.Config.TrustDomain)
	grpcapi.Register(a.Freya.GRPC(), a.GRPC)
	return a, nil
}

func (a *App) buildRuntime(ctx context.Context, cfg config.Config, handler slog.Handler, extra []freya.Option) error {
	fopts := append([]freya.Option{freya.WithLogger(handler)}, extra...)
	if cfg.MeshEnroll.Enabled {
		raw, err := os.ReadFile(cfg.MeshEnroll.TokenFile)
		if err != nil {
			return fmt.Errorf("scheduler: mesh enroll token: %w", err)
		}
		prov, err := lcmidentity.NewNet(ctx, lcmidentity.NetConfig{
			EnrollURL: cfg.MeshEnroll.EnrollURL, LCMGRPCTarget: cfg.MeshEnroll.LCMGRPCTarget,
			TenantID: cfg.MeshEnroll.TenantID, TrustDomain: cfg.Config.TrustDomain, ServiceName: cfg.Config.ServiceName,
			EnrollmentToken: strings.TrimSpace(string(raw)), Insecure: cfg.MeshEnroll.Insecure, StateFile: cfg.MeshEnroll.StateFile,
		})
		if err != nil {
			return fmt.Errorf("scheduler: mesh enroll: %w", err)
		}
		a.closers = append(a.closers, func() { _ = prov.Close() })
		fopts = append(fopts, freya.WithIdentityProvider(prov))
	}
	f, err := freya.New(cfg.Config, fopts...)
	if err != nil {
		return err
	}
	a.Freya = f
	a.closers = append(a.closers, f.Close)
	return nil
}

func (a *App) buildStorage(ctx context.Context, cfg config.Config, o Options) (err error) {
	a.Repo = o.Repo
	if a.Repo == nil {
		if o.Migrate {
			mdsn := cfg.DB.MigrateDSN
			if mdsn == "" {
				mdsn = cfg.DB.DSN
			}
			if err = store.Migrate(ctx, mdsn); err != nil {
				return err
			}
		}
		if a.Store, err = store.Open(ctx, cfg.DB.DSN, cfg.DB.MaxConns); err != nil {
			return err
		}
		a.closers = append(a.closers, a.Store.Close)
		a.Repo = repodb.New(a.Store)
	}
	a.Audit = audit.NewWriter(a.Repo, func(err error) { a.Log.Error("audit write failed", "err", err) })
	a.closers = append(a.closers, a.Audit.Close)
	return nil
}

// buildPeers wires what the module asks auth: the platform-token verifier and
// the permission checker. The connection is dialled lazily by the framework,
// so the module starts while auth is down.
func (a *App) buildPeers(ctx context.Context, cfg config.Config, o Options) error {
	a.Verifier, a.Checker = o.Verifier, o.Checker
	if a.Verifier == nil || a.Checker == nil {
		conn, err := a.Freya.Client(ctx, "auth")
		if err != nil {
			return fmt.Errorf("auth client: %w", err)
		}
		if a.Verifier == nil {
			a.Verifier = authclient.New(authclient.Config{Issuer: cfg.Gateway.Issuer},
				authclient.GRPCKeys{Client: authv1.NewKeysClient(conn)},
				authclient.GRPCRevocations{Client: authv1.NewSessionsClient(conn)})
		}
		if a.Checker == nil {
			a.Checker = AuthPerms{Client: authv1.NewAuthorizationClient(conn)}
		}
	}
	return nil
}

func (a *App) buildEvents(cfg config.Config, client stream.Client) error {
	if client == nil {
		sc := valkeykv.Config{Addresses: cfg.Valkey.Addresses, Username: cfg.Valkey.Username, Password: cfg.Valkey.Password, AllowPlaintext: cfg.Valkey.AllowPlaintext}
		if cfg.Valkey.CAFile != "" {
			pem, err := os.ReadFile(cfg.Valkey.CAFile)
			if err != nil {
				return fmt.Errorf("valkey ca: %w", err)
			}
			sc.CAPEM = pem
		}
		c, err := valkeykv.New(sc)
		if err != nil {
			return fmt.Errorf("event bus: %w", err)
		}
		client = c
	}
	a.Hub = stream.NewHub(client, stream.Config{}, a.Log)
	a.closers = append(a.closers, a.Hub.Close)
	if cfg.Events.Enabled {
		a.Events = events.Emitter{Pub: events.HubPublisher{Hub: a.Hub}, PlatformTenant: cfg.PlatformTenantID}
	}
	return nil
}

// health reports component reachability for /health.
func (a *App) health() map[string]string {
	out := map[string]string{"store": "ok"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if a.Store != nil {
		if err := a.Store.Ping(ctx); err != nil {
			out["store"] = "unreachable"
		}
	}
	return out
}

// Run starts the verifier, gateway registration, permission seeding, the
// engine and the Freya runtime.
func (a *App) Run(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if v, ok := a.Verifier.(*authclient.Verifier); ok {
		go func() {
			for wctx.Err() == nil {
				if err := v.Start(wctx, func(err error) { a.Log.Warn("verifier", "err", err) }); err == nil {
					return
				}
				select {
				case <-wctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	go a.register(wctx)
	engineDone := make(chan struct{})
	go func() {
		defer close(engineDone)
		a.Log.Info("scheduler engine started", "instance", a.Engine.InstanceID())
		a.Engine.Run(wctx)
	}()
	go func() {
		for wctx.Err() == nil && !a.Freya.Ready() {
			time.Sleep(100 * time.Millisecond)
		}
		a.seedLoop(wctx)
	}()
	err := a.Freya.Run(ctx)
	cancel()
	<-engineDone
	return err
}

// Close releases resources (idempotent).
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// register keeps the gateway lease for the manifest.
func (a *App) register(ctx context.Context) {
	for ctx.Err() == nil && !a.Freya.Ready() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	man, err := schedulermanifest.Manifest()
	if err != nil {
		a.Log.Error("gateway manifest", "err", err)
		return
	}
	httpEP, err := a.Freya.HTTP().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: http endpoint", "err", err)
		return
	}
	grpcEP, err := a.Freya.GRPC().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: grpc endpoint", "err", err)
		return
	}
	var client *gatewayclient.Client
	for ctx.Err() == nil && client == nil {
		conn, cerr := a.Freya.Client(ctx, a.Cfg.Gateway.Service)
		if cerr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		client, err = gatewayclient.New(conn, gatewayclient.Options{Manifest: man, HTTPURL: "https://" + httpEP.Host, GRPCTarget: grpcEP.Host, Logger: a.Log,
			OnState: func(s gatewayclient.State) {
				a.Log.Info("gateway lease", "registered", s.Registered, "lease", s.LeaseID, "err", s.Err)
			}})
		if err != nil {
			a.Log.Error("gateway client", "err", err)
			return
		}
	}
	if client != nil {
		if err := client.Run(ctx); err != nil {
			a.Log.Error("gateway registration", "err", err)
		}
	}
}
