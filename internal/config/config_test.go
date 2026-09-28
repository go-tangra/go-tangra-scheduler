package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// valid returns a Config that passes both the framework and module Validate.
func valid() Config {
	c := Default()
	c.ServiceName = "scheduler"
	c.TrustDomain = "example.org"
	c.Authz.Path = "/etc/scheduler/policy.yaml"
	c.DB.DSN = "postgres://localhost/scheduler"
	c.Valkey.Addresses = []string{"valkey:6379"}
	c.Gateway.Issuer = "https://gw.example.org"
	return c
}

func TestDefaultSecure(t *testing.T) {
	d := Default()
	if d.Valkey.AllowPlaintext || !d.Events.Enabled || d.MeshEnroll.Insecure {
		t.Fatalf("insecure defaults: %+v %+v", d.Valkey, d.Events)
	}
	if d.PlatformTenantID != DefaultPlatformTenant || d.Gateway.Service != "gateway" {
		t.Fatalf("defaults: %q %q", d.PlatformTenantID, d.Gateway.Service)
	}
	e := d.Engine
	if e.TickMS != 1000 || e.Workers != 16 || e.Batch != 100 || e.MisfireGraceSeconds != 60 || e.LeaseGraceSeconds != 30 ||
		e.RetentionDays != 90 || e.RetentionIntervalMinutes != 60 || e.InstanceID != "" {
		t.Fatalf("engine defaults: %+v", e)
	}
	l := d.Limits
	if l.MaxPayloadBytes != 65536 || l.MaxResultBytes != 65536 || l.MaxTimeoutSeconds != 3600 || l.MinIntervalSeconds != 60 ||
		l.MaxPageSize != 100 || l.MaxBackupBytes != 33554432 || l.MaxTasksPerTenant != 1000 {
		t.Fatalf("limit defaults: %+v", l)
	}
	if err := Default().Validate(); err == nil {
		t.Fatal("Default() must not validate without required fields")
	}
}

func TestValidateOK(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	c := valid()
	c.Env = "production"
	c.DB.DSN = "postgres://db/scheduler?sslmode=verify-full"
	c.MeshEnroll = MeshEnroll{Enabled: true}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := map[string]func(*Config){
		"framework":    func(c *Config) { c.ServiceName = "" },
		"no dsn":       func(c *Config) { c.DB.DSN = "" },
		"prod sslmode": func(c *Config) { c.Env = "production" },
		"max conns":    func(c *Config) { c.DB.MaxConns = 1000 },
		"no valkey":    func(c *Config) { c.Valkey.Addresses = nil },
		"prod plaintext": func(c *Config) {
			c.Env = "production"
			c.DB.DSN += "?sslmode=verify-ca"
			c.Valkey.AllowPlaintext = true
		},
		"no gateway":  func(c *Config) { c.Gateway.Service = "" },
		"http issuer": func(c *Config) { c.Gateway.Issuer = "http://gw" },
		"bad issuer":  func(c *Config) { c.Gateway.Issuer = "://" },
		"prod insecure mesh": func(c *Config) {
			c.Env = "production"
			c.DB.DSN += "?sslmode=verify-full"
			c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
		},
		"platform tenant":    func(c *Config) { c.PlatformTenantID = "platform" },
		"tick":               func(c *Config) { c.Engine.TickMS = 10 },
		"workers":            func(c *Config) { c.Engine.Workers = 0 },
		"batch":              func(c *Config) { c.Engine.Batch = 5000 },
		"misfire":            func(c *Config) { c.Engine.MisfireGraceSeconds = 0 },
		"lease":              func(c *Config) { c.Engine.LeaseGraceSeconds = 0 },
		"retention":          func(c *Config) { c.Engine.RetentionDays = 0 },
		"retention interval": func(c *Config) { c.Engine.RetentionIntervalMinutes = 0 },
		"instance id":        func(c *Config) { c.Engine.InstanceID = "a b" },
		"payload":            func(c *Config) { c.Limits.MaxPayloadBytes = 10 },
		"result":             func(c *Config) { c.Limits.MaxResultBytes = 2 << 20 },
		"timeout":            func(c *Config) { c.Limits.MaxTimeoutSeconds = 0 },
		"min interval":       func(c *Config) { c.Limits.MinIntervalSeconds = 30 },
		"page size":          func(c *Config) { c.Limits.MaxPageSize = 1000 },
		"backup":             func(c *Config) { c.Limits.MaxBackupBytes = 1 },
		"tasks per tenant":   func(c *Config) { c.Limits.MaxTasksPerTenant = 0 },
	}
	for name, mut := range cases {
		c := valid()
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "scheduler.yaml")
	body := `service_name: scheduler
trust_domain: example.org
authz: {path: /etc/scheduler/policy.yaml}
db: {dsn: "postgres://localhost/scheduler"}
valkey: {addresses: ["valkey:6379"], allow_plaintext: true}
gateway: {issuer: "https://gw.example.org"}
engine: {tick_ms: 500, instance_id: "sched-1"}
limits_scheduler: {max_tasks_per_tenant: 10}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Tick() != 500*time.Millisecond || c.Engine.InstanceID != "sched-1" || c.Limits.MaxTasksPerTenant != 10 || c.Engine.Workers != 16 {
		t.Fatalf("loaded: %+v", c.Engine)
	}
	if err := os.WriteFile(p, []byte(body+"bogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown key accepted: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestWarningsAndDurations(t *testing.T) {
	c := valid()
	c.Valkey.AllowPlaintext = true
	c.MeshEnroll = MeshEnroll{Enabled: true, Insecure: true}
	c.Events.Enabled = false
	w := strings.Join(c.Warnings(), "\n")
	for _, want := range []string{"valkey.allow_plaintext", "mesh_enroll.insecure", "events.enabled=false"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %s: %s", want, w)
		}
	}
	if len(valid().Warnings()) != len(valid().Config.Warnings()) {
		t.Fatal("secure config warns")
	}
	if c.MisfireGrace() != time.Minute || c.LeaseGrace() != 30*time.Second || c.Retention() != 90*24*time.Hour || c.RetentionInterval() != time.Hour {
		t.Fatal("durations")
	}
}
