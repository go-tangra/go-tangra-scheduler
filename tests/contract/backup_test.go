package contract

import (
	"net/http"
	"strings"
	"testing"
)

func TestBackupOverHTTP(t *testing.T) {
	h := newHarness(t)
	h.create("admin-a", `{"name":"scan","type_name":"ipam:scan-network","kind":"periodic","payload":{"all":true}}`)
	w := h.do("POST", p+"/backup/export", "admin-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":1`) || !strings.Contains(w.Header().Get("Content-Disposition"), "scheduler-backup.json") {
		t.Fatalf("export = %d %s", w.Code, w.Body)
	}
	doc := w.Body.String()
	if w := h.do("POST", p+"/backup/export", "admin-a", `{"all":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("all by tenant admin = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/export", "root", `{"all":true}`); w.Code != 200 {
		t.Fatalf("all by platform admin = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/export", "admin-a", `{"bogus":true}`); w.Code != 422 {
		t.Fatalf("unknown field = %d", w.Code)
	}
	// importing into tenant B: existing ids are skipped (the id is global)
	w = h.do("POST", p+"/backup/import", "admin-b", doc)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"tasks":0`) {
		t.Fatalf("import = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/backup/import", "admin-b", `{"version":9}`); w.Code != 422 || !strings.Contains(w.Body.String(), "invalid_backup") {
		t.Fatalf("bad document = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/backup/import", "admin-b", `{"version":1,"tasks":[`+strings.Repeat(`{"name":"x"},`, 90000)+`{}]}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized import = %d", w.Code)
	}
	if w := h.do("POST", p+"/backup/import", "viewer-a", doc); w.Code != http.StatusForbidden {
		t.Fatalf("viewer import = %d", w.Code)
	}
}
