// Package config loads and validates the scheduler service configuration: the
// Freya framework config plus the module's own sections. Every value is
// explicit; insecure opt-outs are named and surfaced at start (Constitution
// I/VII). The service refuses to start without a store, an event bus and a
// gateway issuer.
//
// The scheduler stores no secrets (payloads must not carry any), so there is no
// KEK section. The module's yaml keys never collide with the framework sections
// the embedded config already owns (server, admin, discovery, limits,
// identity, authz): the module's bounds live under "limits_scheduler".
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	fconfig "github.com/go-tangra/go-tangra/v4/config"
	"gopkg.in/yaml.v3"
)

// DefaultPlatformTenant is the tenant whose event stream receives the events of
// platform-scoped tasks (the platform tenant of the stack).
const DefaultPlatformTenant = "00000000-0000-0000-0000-000000000001"

// Config is the scheduler service configuration. The embedded framework config
// (inline) carries service_name, trust_domain, env, identity, authz, limits,
// admin, discovery and server (grpc_addr/http_addr).
type Config struct {
	fconfig.Config `yaml:",inline"`

	DB               DB         `yaml:"db"`
	Valkey           Valkey     `yaml:"valkey"`
	Gateway          Gateway    `yaml:"gateway"`
	MeshEnroll       MeshEnroll `yaml:"mesh_enroll"`
	Events           Events     `yaml:"events"`
	PlatformTenantID string     `yaml:"platform_tenant_id"`
	Engine           Engine     `yaml:"engine"`
	Limits           Limits     `yaml:"limits_scheduler"`
}

// DB configures the PostgreSQL/TimescaleDB store.
type DB struct {
	DSN        string `yaml:"dsn"`
	MigrateDSN string `yaml:"migrate_dsn"`
	MaxConns   int32  `yaml:"max_conns"`
}

// Valkey configures the platform event bus.
type Valkey struct {
	Addresses      []string `yaml:"addresses"`
	Username       string   `yaml:"username"`
	Password       string   `yaml:"password"`
	AllowPlaintext bool     `yaml:"allow_plaintext"`
	CAFile         string   `yaml:"ca_file"`
}

// Gateway names the application gateway and the platform token issuer.
type Gateway struct {
	Service string `yaml:"service"`
	Issuer  string `yaml:"issuer"`
}

// MeshEnroll configures how the scheduler obtains its own mesh SVID by
// enrolling with lcm over the network (identity.provider=provided).
type MeshEnroll struct {
	Enabled       bool   `yaml:"enabled"`
	EnrollURL     string `yaml:"enroll_url"`
	LCMGRPCTarget string `yaml:"lcm_grpc"`
	TenantID      string `yaml:"tenant_id"`
	TokenFile     string `yaml:"token_file"`
	StateFile     string `yaml:"state_file"`
	Insecure      bool   `yaml:"insecure"`
}

// Events toggles the realtime publisher.
type Events struct {
	Enabled bool `yaml:"enabled"`
}

// Engine tunes the planning/dispatch loop (research D1).
type Engine struct {
	TickMS                   int    `yaml:"tick_ms"`
	Workers                  int    `yaml:"workers"`
	Batch                    int    `yaml:"batch"`
	MisfireGraceSeconds      int    `yaml:"misfire_grace_seconds"`
	LeaseGraceSeconds        int    `yaml:"lease_grace_seconds"`
	RetentionDays            int    `yaml:"retention_days"`
	RetentionIntervalMinutes int    `yaml:"retention_interval_minutes"`
	InstanceID               string `yaml:"instance_id"` // "" = hostname + random suffix
}

// Limits bound the module's request and data shapes (SR-006).
type Limits struct {
	MaxPayloadBytes    int   `yaml:"max_payload_bytes"`
	MaxResultBytes     int   `yaml:"max_result_bytes"`
	MaxTimeoutSeconds  int   `yaml:"max_timeout_seconds"`
	MinIntervalSeconds int   `yaml:"min_interval_seconds"`
	MaxPageSize        int   `yaml:"max_page_size"`
	MaxBackupBytes     int64 `yaml:"max_backup_bytes"`
	MaxTasksPerTenant  int   `yaml:"max_tasks_per_tenant"`
}

// Default returns secure defaults on top of the Freya defaults.
func Default() Config {
	return Config{
		Config:           fconfig.Default(),
		DB:               DB{MaxConns: 16},
		Events:           Events{Enabled: true},
		Gateway:          Gateway{Service: "gateway"},
		PlatformTenantID: DefaultPlatformTenant,
		Engine: Engine{TickMS: 1000, Workers: 16, Batch: 100, MisfireGraceSeconds: 60, LeaseGraceSeconds: 30,
			RetentionDays: 90, RetentionIntervalMinutes: 60},
		Limits: Limits{MaxPayloadBytes: 64 << 10, MaxResultBytes: 64 << 10, MaxTimeoutSeconds: 3600, MinIntervalSeconds: 60,
			MaxPageSize: 100, MaxBackupBytes: 32 << 20, MaxTasksPerTenant: 1000},
	}
}

// Load reads YAML over Default(); unknown fields are rejected.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func within[T int | int64](v, lo, hi T) bool { return v >= lo && v <= hi }

// Validate checks the Freya config and every module section.
func (c Config) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	for _, check := range []func() error{c.validateInfra, c.validateEngine, c.validateLimits} {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateInfra() error {
	prod := c.IsProduction()
	if c.DB.DSN == "" {
		return errors.New("config: db.dsn is required")
	}
	if prod && !strings.Contains(c.DB.DSN, "sslmode=verify-full") && !strings.Contains(c.DB.DSN, "sslmode=verify-ca") {
		return errors.New("config: db.dsn must use sslmode=verify-full (or verify-ca) in production")
	}
	if c.DB.MaxConns < 0 || c.DB.MaxConns > 256 {
		return errors.New("config: db.max_conns must be within [0, 256]")
	}
	if len(c.Valkey.Addresses) == 0 {
		return errors.New("config: valkey.addresses is required")
	}
	if prod && c.Valkey.AllowPlaintext {
		return errors.New("config: valkey.allow_plaintext is not permitted in production")
	}
	if c.Gateway.Service == "" {
		return errors.New("config: gateway.service is required")
	}
	if iu, err := url.Parse(c.Gateway.Issuer); err != nil || iu.Scheme != "https" || iu.Host == "" {
		return errors.New("config: gateway.issuer must be an https origin")
	}
	if prod && c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		return errors.New("config: mesh_enroll.insecure is not permitted in production")
	}
	if !uuidRE.MatchString(c.PlatformTenantID) {
		return errors.New("config: platform_tenant_id must be a uuid")
	}
	return nil
}

func (c Config) validateEngine() error {
	e := c.Engine
	switch {
	case !within(e.TickMS, 100, 60000):
		return errors.New("config: engine.tick_ms must be within [100, 60000]")
	case !within(e.Workers, 1, 256):
		return errors.New("config: engine.workers must be within [1, 256]")
	case !within(e.Batch, 1, 1000):
		return errors.New("config: engine.batch must be within [1, 1000]")
	case !within(e.MisfireGraceSeconds, 1, 3600):
		return errors.New("config: engine.misfire_grace_seconds must be within [1, 3600]")
	case !within(e.LeaseGraceSeconds, 1, 3600):
		return errors.New("config: engine.lease_grace_seconds must be within [1, 3600]")
	case !within(e.RetentionDays, 1, 3650):
		return errors.New("config: engine.retention_days must be within [1, 3650]")
	case !within(e.RetentionIntervalMinutes, 1, 1440):
		return errors.New("config: engine.retention_interval_minutes must be within [1, 1440]")
	case len(e.InstanceID) > 128 || strings.ContainsAny(e.InstanceID, " \r\n\t"):
		return errors.New("config: engine.instance_id must be at most 128 characters without whitespace")
	}
	return nil
}

func (c Config) validateLimits() error {
	l := c.Limits
	switch {
	case !within(l.MaxPayloadBytes, 1<<10, 1<<20):
		return errors.New("config: limits_scheduler.max_payload_bytes must be within [1 KiB, 1 MiB]")
	case !within(l.MaxResultBytes, 1<<10, 1<<20):
		return errors.New("config: limits_scheduler.max_result_bytes must be within [1 KiB, 1 MiB]")
	case !within(l.MaxTimeoutSeconds, 1, 86400):
		return errors.New("config: limits_scheduler.max_timeout_seconds must be within [1, 86400]")
	case !within(l.MinIntervalSeconds, 60, 86400):
		return errors.New("config: limits_scheduler.min_interval_seconds must be within [60, 86400]")
	case !within(l.MaxPageSize, 1, 100):
		return errors.New("config: limits_scheduler.max_page_size must be within [1, 100]")
	case !within(l.MaxBackupBytes, 1<<10, 256<<20):
		return errors.New("config: limits_scheduler.max_backup_bytes must be within [1 KiB, 256 MiB]")
	case !within(l.MaxTasksPerTenant, 1, 100000):
		return errors.New("config: limits_scheduler.max_tasks_per_tenant must be within [1, 100000]")
	}
	return nil
}

// Warnings lists accepted insecure opt-outs (surfaced at start).
func (c Config) Warnings() []string {
	w := c.Config.Warnings()
	if c.Valkey.AllowPlaintext {
		w = append(w, "valkey.allow_plaintext: event-bus traffic without TLS (development only)")
	}
	if c.MeshEnroll.Enabled && c.MeshEnroll.Insecure {
		w = append(w, "mesh_enroll.insecure: SVID enrollment without TLS (development only)")
	}
	if !c.Events.Enabled {
		w = append(w, "events.enabled=false: the UI does not receive live execution updates")
	}
	return w
}

// Tick is the engine planning interval.
func (c Config) Tick() time.Duration { return time.Duration(c.Engine.TickMS) * time.Millisecond }

// MisfireGrace is how late an occurrence may start before it counts as missed.
func (c Config) MisfireGrace() time.Duration {
	return time.Duration(c.Engine.MisfireGraceSeconds) * time.Second
}

// LeaseGrace is added to the task timeout for an attempt's lease.
func (c Config) LeaseGrace() time.Duration {
	return time.Duration(c.Engine.LeaseGraceSeconds) * time.Second
}

// Retention is how long execution history is kept.
func (c Config) Retention() time.Duration {
	return time.Duration(c.Engine.RetentionDays) * 24 * time.Hour
}

// RetentionInterval is how often history is pruned.
func (c Config) RetentionInterval() time.Duration {
	return time.Duration(c.Engine.RetentionIntervalMinutes) * time.Minute
}
