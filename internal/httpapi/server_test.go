package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-tangra/go-tangra/v4/freyatest/testrt"
	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

func TestRemoteServing(t *testing.T) {
	dist := fstest.MapFS{
		"mf-manifest.json":   {Data: []byte(`{"name":"scheduler"}`)},
		"assets/remote-1.js": {Data: []byte("export {}")},
	}
	s, err := NewHandler(testrt.New(t, testutil.MustCA("example.org"), "scheduler"), WithRemote(dist))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	if w := get("/ui/mf-manifest.json"); w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest = %d %v", w.Code, w.Header())
	}
	if w := get("/ui/assets/remote-1.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %v", w.Code, w.Header())
	}
	for _, p := range []string{"/ui/", "/ui/assets/", "/ui/missing.js"} {
		if w := get(p); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
	if w := get("/api/scheduler/v1/overview"); w.Code != http.StatusUnauthorized || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("no verifier = %d %v", w.Code, w.Header())
	}
	if s.Document() == nil || s.Edge() != nil || s.Permission("GET", Prefix+"/overview") != authz.SchedulerRead || !s.IsPublic("GET", Prefix+"/health") {
		t.Fatal("accessors")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("MustHandle of an undeclared route must panic")
		}
	}()
	s.MustHandle("GET", "/nope", func(http.ResponseWriter, *http.Request) {})
}

func TestStatusMapping(t *testing.T) {
	cases := map[error]int{
		ErrConflict:                              409,
		&http.MaxBytesError{Limit: 1}:            413,
		authz.ErrForbidden:                       403,
		repo.ErrNotFound:                         404,
		repo.ErrConflict:                         409,
		repo.ErrLimit:                            409,
		fmt.Errorf("x: %w", backup.ErrInvalid):   422,
		&registry.Error{Reason: "invalid_owner"}: 422,
		errors.New("db down"):                    503,
	}
	for err, want := range cases {
		if got, _ := Status(err); got != want {
			t.Errorf("%v => %d, want %d", err, got, want)
		}
	}
	w := httptest.NewRecorder()
	Fail(w, httptest.NewRequest("GET", "/", nil), nil, &tasks.Error{Reason: "made_up"})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "made_up") {
		t.Fatalf("unknown domain reason = %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	WriteDetail(w, 409, "run_in_progress", nil)
	if w.Body.String() != "{\"reason\":\"run_in_progress\"}\n" {
		t.Fatalf("detail-less = %s", w.Body)
	}
	if ErrNotFound.Error() != "not_found" {
		t.Fatal("error text")
	}
}

func TestDecodeJSONAndLastID(t *testing.T) {
	var v map[string]any
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", 100)+`"}`))
	if err := DecodeJSON(r, &v, 10); err != ErrBodyTooLarge {
		t.Fatalf("too large: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}{}`))
	if err := DecodeJSON(r, &v, 0); err != ErrMalformed {
		t.Fatalf("trailing: %v", err)
	}
	r = httptest.NewRequest("GET", "/?last_id=5-0", nil)
	r.Header.Set("Last-Event-ID", "9-9")
	if lastID(r) != "5-0" {
		t.Fatal("query wins")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Last-Event-ID", "9-9")
	if lastID(r) != "9-9" {
		t.Fatal("header resume")
	}
	r.Header.Set("Last-Event-ID", strings.Repeat("9", 65))
	if lastID(r) != "" {
		t.Fatal("oversized header accepted")
	}
}
