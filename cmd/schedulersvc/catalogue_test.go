package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	fauthz "github.com/go-tangra/go-tangra/v4/authz"
	"github.com/go-tangra/go-tangra/v4/catalogue"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/config"
	"github.com/go-tangra/go-tangra-scheduler/v4/pkg/schedulermanifest"
)

var placeholderRE = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)

// joinValues are values of the shape the gateway fills in at join time: core
// values, generated passwords and the descriptor's host inputs.
var joinValues = map[string]string{
	"TRUST_DOMAIN": "infra.example.org", "GATEWAY_ISSUER": "https://portal.example.org:8443",
	"LCM_ENROLL_URL": "https://portal.example.org:8443/api/lcm/v1/enroll", "LCM_GRPC": "portal.example.org:9945",
	"GATEWAY_GRPC": "portal.example.org:9643", "AUTH_GRPC": "portal.example.org:9543", "MESH_TENANT_ID": "00000000-0000-0000-0000-000000000001",
	"MODULE_IMAGE": "ghcr.io/go-tangra/go-tangra-scheduler", "MODULE_VERSION": "4.1.0",
	"GEN_PASSWORD_1": strings.Repeat("a", 48), "GEN_PASSWORD_2": strings.Repeat("b", 48),
	"MODULE_ADVERTISE_HOST": "scheduler.example.org", "MODULE_BIND_IP": "10.0.0.5",
	"VALKEY_ADDRESS": "portal.example.org:6380", "VALKEY_PASSWORD": strings.Repeat("v", 32), "VALKEY_CA_FILE": "/srv/tangra/valkey-ca.pem",
}

func render(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "bundle", name))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	out := placeholderRE.ReplaceAllStringFunc(string(raw), func(m string) string {
		k := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := joinValues[k]
		if !ok {
			missing = append(missing, k)
		}
		return v
	})
	if len(missing) > 0 {
		t.Fatalf("%s uses placeholders the gateway does not fill: %v", name, missing)
	}
	return out
}

// The join bundle's templates, rendered with values of the shape the gateway
// fills in, are a valid production configuration and policy.
func TestJoinBundleRendersToValidProductionConfig(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"config.yaml", "policy.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(render(t, name)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rendered config invalid: %v", err)
	}
	if !cfg.IsProduction() || cfg.Gateway.Issuer != "https://portal.example.org:8443" || cfg.ServiceName != schedulermanifest.Module {
		t.Fatalf("%+v %q", cfg.Gateway, cfg.ServiceName)
	}
	// The only warning is the raised request limit backup imports need; no
	// insecure opt-out.
	for _, w := range cfg.Warnings() {
		if !strings.HasPrefix(w, "limits.max_request_bytes raised") {
			t.Fatalf("rendered config warns: %s", w)
		}
	}
	pf, err := os.Open(filepath.Join(dir, "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	if _, err := fauthz.Load(pf); err != nil {
		t.Fatalf("rendered policy invalid: %v", err)
	}
	pol, _ := os.ReadFile(filepath.Join(dir, "policy.yaml"))
	if !strings.Contains(string(pol), "spiffe://infra.example.org/svc/gateway") {
		t.Fatal(string(pol))
	}
	// compose.yaml is interpolated from the gateway's .env: every variable it
	// uses must be one the gateway writes there.
	render(t, "compose.yaml")
}

// tangra-module.yaml is a valid catalogue descriptor for this module, and
// deploy/bundle packs into a valid release entry.
func TestCatalogueDescriptor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tangra-module.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalogue.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d.Module != schedulermanifest.Module || !slices.Contains(d.Routes.Prefixes, schedulermanifest.APIPrefix) {
		t.Fatalf("%s %v", d.Module, d.Routes.Prefixes)
	}
	for _, in := range d.HostInputs {
		if _, ok := joinValues[in.Key]; !ok {
			t.Fatalf("host input %s is not covered by the render test", in.Key)
		}
		if !regexp.MustCompile(in.Pattern).MatchString(joinValues[in.Key]) {
			t.Fatalf("host input %s: test value does not match its pattern", in.Key)
		}
	}
	compose, _ := os.ReadFile(filepath.Join("..", "..", d.Bundle.Dir, "compose.yaml"))
	for _, h := range d.Bundle.TLSHosts {
		if !strings.Contains(string(compose), "/tls/"+h+".key") {
			t.Fatalf("tls host %s is not used by compose.yaml", h)
		}
	}
	bundle, err := catalogue.PackBundle(os.DirFS(filepath.Join("..", "..", d.Bundle.Dir)))
	if err != nil {
		t.Fatal(err)
	}
	files, err := catalogue.ReadBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range d.Bundle.Templates {
		if _, ok := files[tpl]; !ok {
			t.Fatalf("template %s is not in the bundle", tpl)
		}
	}
	e, err := catalogue.BuildEntry(d, "4.1.0", "go-tangra/go-tangra-scheduler", schedulermanifest.PermissionRefs(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.CheckBundle(bundle); err != nil {
		t.Fatal(err)
	}
}
