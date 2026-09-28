// Package contract exercises the scheduler browser API over the full HTTP
// chain (OpenAPI validation, platform token, per-route permission re-check,
// handlers, services, in-memory store) and validates every successful
// response against the OpenAPI document so the handlers and the contract
// cannot drift.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/events"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

const (
	tenantA  = "11111111-1111-7111-8111-111111111111"
	tenantB  = "22222222-2222-7222-8222-222222222222"
	adminA   = "33333333-3333-7333-8333-333333333333"
	viewerA  = "44444444-4444-7444-8444-444444444444"
	adminB   = "55555555-5555-7555-8555-555555555555"
	rootP    = "66666666-6666-7666-8666-666666666666"
	nobodyA  = "77777777-7777-7777-8777-777777777777"
	p        = "/api/scheduler/v1"
	scanType = "ipam:scan-network"
	mailType = "notification:send-test-email"
	sweep    = "ipam:platform-sweep"
	marker   = "SECRET-PAYLOAD-MARKER"
)

var all = []string{authz.SchedulerRead, authz.TasksManage, authz.TasksDelete, authz.TasksControl, authz.BackupManage}

type verifier map[string]authclient.Identity

func (v verifier) Verify(_ context.Context, tok string) (authclient.Identity, error) {
	if id, ok := v[tok]; ok {
		return id, nil
	}
	return authclient.Identity{}, errors.New("unauthenticated")
}

type rec struct {
	mu  sync.Mutex
	got []audit.Event
}

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

type harness struct {
	t    *testing.T
	s    *httpapi.Server
	doc  *openapi3.T
	st   *memstore.Mem
	reg  *registry.Registry
	svc  *tasks.Service
	rec  *rec
	pub  *events.Recorder
	logs *bytes.Buffer
	clk  *clock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	rt := testrt.New(t, testutil.MustCA("example.org"), "scheduler")
	v := verifier{
		"admin-a":  {UserID: adminA, TenantID: tenantA},
		"viewer-a": {UserID: viewerA, TenantID: tenantA},
		"admin-b":  {UserID: adminB, TenantID: tenantB},
		"root":     {UserID: rootP, TenantID: tenantA, Roles: []string{authz.RolePlatformAdmin}},
		"nobody-a": {UserID: nobodyA, TenantID: tenantA},
	}
	checker := authz.Static{adminA: all, adminB: all, rootP: all, viewerA: {authz.SchedulerRead}}
	h := &harness{t: t, st: memstore.New(), rec: &rec{}, pub: events.NewRecorder(), logs: &bytes.Buffer{},
		clk: &clock{t: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}}
	log := slog.New(slog.NewJSONHandler(h.logs, nil))
	s, err := httpapi.NewHandler(rt, httpapi.WithVerifier(v), httpapi.WithChecker(checker), httpapi.WithLogger(log))
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	if h.doc, err = httpapi.LoadDocument(); err != nil {
		t.Fatal(err)
	}
	h.reg = registry.New(registry.Deps{Store: h.st, Audit: h.rec, Now: h.clk.Now, MaxPayloadBytes: 1 << 16})
	for owner, ds := range map[string][]registry.Descriptor{
		"ipam": {
			{Type: scanType, DisplayName: "Scan network", DefaultCron: "0 3 * * *", DefaultMaxRetry: 2,
				PayloadSchema: `{"type":"object","properties":{"subnetId":{"type":"string"},"all":{"type":"boolean"}},"additionalProperties":false}`},
			{Type: sweep, DisplayName: "Platform sweep", Scope: registry.ScopePlatform},
		},
		"notification": {{Type: mailType, DisplayName: "Send test email", DefaultMaxRetry: 1,
			PayloadSchema: `{"type":"object","required":["recipient"],"properties":{"recipient":{"type":"string","format":"email"}}}`}},
	} {
		if _, err := h.reg.Register(context.Background(), owner, "spiffe://example.org/svc/"+owner, ds); err != nil {
			t.Fatal(err)
		}
	}
	h.svc = tasks.New(tasks.Deps{Store: h.st, Registry: h.reg, Audit: h.rec, Events: events.Emitter{Pub: h.pub, PlatformTenant: tenantA},
		Now: h.clk.Now, Limits: tasks.Limits{MaxPayloadBytes: 1 << 16, MaxTimeoutSeconds: 3600, MinIntervalSeconds: 60, MaxPageSize: 100, MaxTasksPerTenant: 100}})
	hub := stream.NewHub(stream.NewMemory(), stream.Config{}, nil)
	t.Cleanup(hub.Close)
	s.Register(httpapi.Deps{Hub: hub, Health: func() map[string]string { return map[string]string{"store": "ok"} }, Registry: h.reg, Tasks: h.svc,
		Backup: backup.New(h.st, h.rec, h.reg.Schemas(), h.clk.Now, 1<<16), MaxBackupBytes: 1 << 20})
	return h
}

func (h *harness) do(method, path, tok, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if method != http.MethodGet {
		r.Header.Set("X-CSRF-Token", "csrf")
	}
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	h.checkResponse(method, path, w)
	return w
}

// route resolves a request path to its declared template (literal segments
// win, earlier ones first — /tasks/bulk/{action} over /tasks/{id}/stop).
func (h *harness) route(method, path string) (*routers.Route, map[string]string) {
	best, bestLit := "", -1
	var params map[string]string
	clean, _, _ := strings.Cut(path, "?")
	for tmpl, item := range h.doc.Paths.Map() {
		if item.GetOperation(method) == nil {
			continue
		}
		ts, ps := strings.Split(tmpl, "/"), strings.Split(clean, "/")
		if len(ts) != len(ps) {
			continue
		}
		lit, pr, ok := 0, map[string]string{}, true
		for i, seg := range ts {
			if strings.HasPrefix(seg, "{") {
				pr[seg[1:len(seg)-1]] = ps[i]
				continue
			}
			if seg != ps[i] {
				ok = false
				break
			}
			lit += 1 << (len(ts) - i) // an earlier literal segment is more specific
		}
		if ok && lit > bestLit {
			best, bestLit, params = tmpl, lit, pr
		}
	}
	if best == "" {
		return nil, nil
	}
	item := h.doc.Paths.Value(best)
	return &routers.Route{Spec: h.doc, Path: best, PathItem: item, Method: method, Operation: item.GetOperation(method)}, params
}

// checkResponse validates a successful JSON response against the document.
func (h *harness) checkResponse(method, path string, w *httptest.ResponseRecorder) {
	h.t.Helper()
	if w.Code >= 300 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		return
	}
	route, params := h.route(method, path)
	if route == nil {
		h.t.Fatalf("%s %s: no route", method, path)
	}
	r := httptest.NewRequest(method, path, nil)
	in := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc}}
	res := &openapi3filter.ResponseValidationInput{RequestValidationInput: in, Status: w.Code, Header: w.Header(),
		Body: io.NopCloser(bytes.NewReader(w.Body.Bytes())), Options: &openapi3filter.Options{IncludeResponseStatus: true}}
	if err := openapi3filter.ValidateResponse(context.Background(), res); err != nil {
		h.t.Errorf("%s %s response violates the contract: %v\n%s", method, path, err, w.Body)
	}
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return v
}

type errJSON struct {
	Reason string         `json:"reason"`
	Detail map[string]any `json:"detail"`
}

type taskJSON struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenant_id"`
	Name            string          `json:"name"`
	TypeName        string          `json:"type_name"`
	TypeDisplayName string          `json:"type_display_name"`
	Module          string          `json:"module"`
	Scope           string          `json:"scope"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
	Cron            string          `json:"cron"`
	Enabled         bool            `json:"enabled"`
	Status          string          `json:"status"`
	State           string          `json:"state"`
	Validity        string          `json:"validity"`
	NextRunAt       *string         `json:"next_run_at"`
	MaxRetries      int             `json:"max_retries"`
	ExecutionID     string          `json:"execution_id"`
}

func (h *harness) create(tok, body string) taskJSON {
	h.t.Helper()
	w := h.do("POST", p+"/tasks", tok, body)
	if w.Code != http.StatusCreated {
		h.t.Fatalf("create %s = %d %s", body, w.Code, w.Body)
	}
	return decode[taskJSON](h.t, w)
}
