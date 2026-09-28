package contract

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-scheduler/v4/pkg/schedulermanifest"
)

// contractRoutes are the routes of contracts/scheduler-api.md.
var contractRoutes = []string{
	"GET /api/scheduler/v1/health", "GET /api/scheduler/v1/task-types", "POST /api/scheduler/v1/modules/{module}/unregister",
	"GET /api/scheduler/v1/cron/preview",
	"GET /api/scheduler/v1/tasks", "POST /api/scheduler/v1/tasks", "GET /api/scheduler/v1/tasks/{id}", "PUT /api/scheduler/v1/tasks/{id}",
	"DELETE /api/scheduler/v1/tasks/{id}", "POST /api/scheduler/v1/tasks/{id}/start", "POST /api/scheduler/v1/tasks/{id}/stop",
	"POST /api/scheduler/v1/tasks/{id}/restart", "POST /api/scheduler/v1/tasks/{id}/run", "POST /api/scheduler/v1/tasks/{id}/cancel",
	"POST /api/scheduler/v1/tasks/bulk/{action}",
	"GET /api/scheduler/v1/executions", "GET /api/scheduler/v1/executions/{id}", "GET /api/scheduler/v1/overview",
	"GET /api/scheduler/v1/stream", "POST /api/scheduler/v1/backup/export", "POST /api/scheduler/v1/backup/import",
}

func TestDocumentMatchesContract(t *testing.T) {
	doc, err := httpapi.LoadDocument()
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	declared := map[string]bool{}
	for _, r := range httpapi.DeclaredRoutes(doc) {
		declared[r.String()] = true
	}
	for _, want := range contractRoutes {
		if !declared[want] {
			t.Errorf("contract route %s is not declared", want)
		}
	}
	if len(declared) != len(contractRoutes) {
		t.Errorf("declared %d routes, contract lists %d", len(declared), len(contractRoutes))
	}
	routes, err := schedulermanifest.Routes(doc)
	if err != nil {
		t.Fatalf("manifest routes: %v", err)
	}
	public := httpapi.PublicRoutes(doc)
	if len(public) != 1 || !public[httpapi.Route{Method: "GET", Path: p + "/health"}] {
		t.Fatalf("exactly /health is public: %v", public)
	}
	for _, r := range routes {
		if r.Public {
			continue
		}
		if !authz.Known(r.Permission) {
			t.Errorf("%s %s: unknown permission %q", r.Method, r.Path, r.Permission)
		}
		if r.Path == p+"/stream" && r.Timeout != 5*time.Minute {
			t.Errorf("/stream must declare the 300 s route maximum, got %v", r.Timeout)
		}
	}
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			if m == "GET" {
				continue
			}
			found := false
			for _, prm := range op.Parameters {
				if prm.Value != nil && prm.Value.In == "header" && prm.Value.Name == "X-CSRF-Token" && prm.Value.Required {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s does not require X-CSRF-Token", m, p)
			}
		}
	}
}

// Every declared route has a handler (no 501 left), nothing undeclared is
// mounted, and an unknown path answers 404.
func TestEveryRouteImplemented(t *testing.T) {
	h := newHarness(t)
	if missing := h.s.Missing(); len(missing) != 0 {
		t.Fatalf("routes without a handler: %v", missing)
	}
	got := []string{}
	for _, r := range h.s.Implemented() {
		got = append(got, r.String())
	}
	want := append([]string(nil), contractRoutes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("implemented %v", got)
	}
	if err := h.s.HandleFunc("GET", p+"/undeclared", nil); err == nil {
		t.Fatal("an undeclared route was mounted")
	}
	if w := h.do("GET", p+"/nope", "admin-a", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d", w.Code)
	}
	if w := h.do("GET", p+"/health", "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("health = %d %s", w.Code, w.Body)
	}
	// a bare handler (nothing registered) answers 501 on every protected route
	bare, err := httpapi.NewHandler(testrt.New(t, testutil.MustCA("example.org"), "scheduler"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Missing()) != len(contractRoutes) || len(bare.Declared()) != len(contractRoutes) {
		t.Fatalf("bare handler: missing %d", len(bare.Missing()))
	}
}

// Every protected route refuses an anonymous and an unauthorised caller.
func TestAuthenticationAndPermission(t *testing.T) {
	h := newHarness(t)
	for _, rt := range contractRoutes {
		method, path, _ := strings.Cut(rt, " ")
		if path == p+"/health" {
			continue
		}
		path = strings.NewReplacer("{id}", "018f3a2b-0000-7000-8000-000000000001", "{module}", "ipam", "{action}", "start").Replace(path)
		if w := h.do(method, path, "", `{}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d", rt, w.Code)
		}
		if w := h.do(method, path, "bad-token", `{}`); w.Code != http.StatusUnauthorized {
			t.Errorf("%s bad token = %d", rt, w.Code)
		}
		if w := h.do(method, path, "nobody-a", `{}`); w.Code != http.StatusForbidden {
			t.Errorf("%s without permission = %d %s", rt, w.Code, w.Body)
		}
	}
}
