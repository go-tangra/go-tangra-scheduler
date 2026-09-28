package schedulermanifest

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-tangra/go-tangra-scheduler/v4/api/openapi"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
)

func TestOpenAPIParsesAndValidates(t *testing.T) {
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(openapi.Scheduler)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(loader.Context, openapi3.DisableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestManifestBuilds(t *testing.T) {
	m, err := Manifest()
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Module != "scheduler" || m.DisplayName != "Scheduler" || len(m.Prefixes) != 1 || m.Prefixes[0] != "/api/scheduler" {
		t.Fatalf("identity = %+v", m)
	}
	if len(m.Routes) != 21 {
		t.Fatalf("routes = %d", len(m.Routes))
	}
	public := 0
	for _, r := range m.Routes {
		if !strings.HasPrefix(r.Path, "/api/scheduler/v1/") {
			t.Fatalf("route outside prefix: %s", r.Path)
		}
		if r.Public {
			public++
			if r.Path != "/api/scheduler/v1/health" {
				t.Fatalf("unexpected public route %s", r.Path)
			}
		}
		if r.Path == "/api/scheduler/v1/backup/import" && r.MaxBodyBytes != 32<<20 {
			t.Fatalf("import body limit = %d", r.MaxBodyBytes)
		}
	}
	if public != 1 {
		t.Fatalf("public routes = %d", public)
	}
	titles := []string{}
	for _, n := range m.Nav {
		titles = append(titles, n.Title+"|"+n.Path+"|"+n.Icon+"|"+n.Requires)
	}
	if strings.Join(titles, ",") != "Scheduler|/scheduler|mdi-calendar-clock|scheduler:read,Dashboard|/scheduler/dashboard|mdi-view-dashboard-outline|scheduler:read" {
		t.Fatalf("nav = %v", titles)
	}
	if m.Nav[0].Order != 980 || m.Nav[1].Order != 985 {
		t.Fatal("nav order")
	}
	if strings.Join(m.Exposes, ",") != "./routes,./nav" || len(m.Methods) != 0 {
		t.Fatalf("exposes/methods = %v %v", m.Exposes, m.Methods)
	}
	if len(m.Abilities) != 5 || len(m.Permissions) != 5 {
		t.Fatalf("abilities/permissions = %d/%d", len(m.Abilities), len(m.Permissions))
	}
	want := map[string]string{"read:SchedulerTask": "scheduler:read", "read:SchedulerExecution": "scheduler:read", "read:SchedulerOverview": "scheduler:read",
		"read:SchedulerTaskType": "scheduler:read", "create:SchedulerTask": "tasks:manage", "update:SchedulerTask": "tasks:manage",
		"delete:SchedulerTask": "tasks:delete", "control:SchedulerTask": "tasks:control", "manage:SchedulerBackup": "backup:manage"}
	got := map[string]string{}
	for _, a := range m.Abilities {
		for _, act := range a.Action {
			for _, sub := range a.Subject {
				got[act+":"+sub] = a.Requires
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("abilities = %v", got)
	}
	for _, p := range m.Permissions {
		if p.Description == "" {
			t.Fatalf("permission %s:%s lacks a description", p.Resource, p.Action)
		}
	}
}

// The manifest's permission vocabulary is exactly the module's authz set.
func TestPermissionsMatchAuthz(t *testing.T) {
	got := PermissionRefs()
	want := append([]string(nil), authz.Permissions...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("manifest %v != authz %v", got, want)
	}
}

// TestRoles pins the module role set (feature 019).
func TestRoles(t *testing.T) {
	want := map[string][]string{
		"administrator": {"scheduler:read", "tasks:manage", "tasks:delete", "tasks:control", "backup:manage"},
		"operator":      {"scheduler:read", "tasks:manage", "tasks:control"},
		"viewer":        {"scheduler:read"},
	}
	names := map[string]string{"administrator": "Scheduler administrator", "operator": "Scheduler operator", "viewer": "Scheduler viewer"}
	if len(Roles) != len(want) {
		t.Fatalf("want %d roles, got %d", len(want), len(Roles))
	}
	own := map[string]bool{}
	for _, r := range PermissionRefs() {
		own[r] = true
	}
	slug := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	for _, r := range Roles {
		if !slug.MatchString(r.Slug) {
			t.Errorf("role slug %q", r.Slug)
		}
		if r.DisplayName != names[r.Slug] || r.Description == "" {
			t.Errorf("role %q: display name %q, description %q", r.Slug, r.DisplayName, r.Description)
		}
		if !reflect.DeepEqual(r.Permissions, want[r.Slug]) {
			t.Errorf("role %q: permissions %v, want %v", r.Slug, r.Permissions, want[r.Slug])
		}
		for _, p := range r.Permissions {
			if !own[p] {
				t.Errorf("role %q names %q, not a scheduler permission", r.Slug, p)
			}
		}
	}
}

// TestBuiltinGrants: owner/admin -> administrator set, operator -> operator
// set, member/auditor -> viewer set.
func TestBuiltinGrants(t *testing.T) {
	admin := []string{"scheduler:read", "tasks:manage", "tasks:delete", "tasks:control", "backup:manage"}
	op := []string{"scheduler:read", "tasks:manage", "tasks:control"}
	viewer := []string{"scheduler:read"}
	want := map[string][]string{"owner": admin, "admin": admin, "operator": op, "member": viewer, "auditor": viewer}
	if !reflect.DeepEqual(Grants, want) {
		t.Fatalf("built-in grants:\n got %v\nwant %v", Grants, want)
	}
	for _, slug := range BuiltinRoles {
		if len(Grants[slug]) == 0 {
			t.Fatalf("no grant for %s", slug)
		}
	}
	if len(BuiltinRoles) != len(Grants) {
		t.Fatalf("BuiltinRoles %v vs grants %v", BuiltinRoles, Grants)
	}
}

// TestRegistration: the auth registration carries the module identity, every
// permission, the complete role set and the built-in grants, and is valid.
func TestRegistration(t *testing.T) {
	reg := Registration()
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	req := reg.Request()
	if req.GetModule() != "scheduler" || req.GetModuleDisplayName() != "Scheduler" || !req.GetDeclaresRoles() {
		t.Fatalf("module %q display %q declares_roles %v", req.GetModule(), req.GetModuleDisplayName(), req.GetDeclaresRoles())
	}
	if len(req.GetPermissions()) != 5 || len(req.GetRoles()) != 3 {
		t.Fatalf("%d permissions, %d roles", len(req.GetPermissions()), len(req.GetRoles()))
	}
	got := map[string][]string{}
	for _, g := range req.GetBuiltinGrants() {
		got[g.GetRole()] = g.GetPermissions()
	}
	if !reflect.DeepEqual(got, Grants) {
		t.Fatalf("builtin grants %v", got)
	}
}

func docWith(ext map[string]any) *openapi3.T {
	paths := openapi3.NewPaths()
	paths.Set("/x", &openapi3.PathItem{Get: &openapi3.Operation{Extensions: ext}})
	return &openapi3.T{Paths: paths}
}

func TestRoutesValidationBranches(t *testing.T) {
	routes, err := Routes(docWith(map[string]any{PublicExtension: true}))
	if err != nil || !routes[0].Public {
		t.Fatalf("public: %v", err)
	}
	routes, err = Routes(docWith(map[string]any{PermissionExtension: "scheduler:read", BodyLimitExtension: float64(2048), TimeoutExtension: float64(30)}))
	if err != nil || routes[0].MaxBodyBytes != 2048 || routes[0].Timeout.Seconds() != 30 {
		t.Fatalf("valid: %v %+v", err, routes)
	}
	bad := []map[string]any{
		{},
		{PermissionExtension: "made:up"},
		{PermissionExtension: "scheduler:read", BodyLimitExtension: float64(0)},
		{PermissionExtension: "scheduler:read", BodyLimitExtension: "big"},
		{PermissionExtension: "scheduler:read", TimeoutExtension: float64(9999)},
		{PermissionExtension: "scheduler:read", TimeoutExtension: "slow"},
	}
	for i, ext := range bad {
		if _, err := Routes(docWith(ext)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
