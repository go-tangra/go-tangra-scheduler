package contract

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
)

type typeList struct {
	Items []struct {
		Name      string `json:"name"`
		Module    string `json:"module"`
		Scope     string `json:"scope"`
		Available bool   `json:"available"`
	} `json:"items"`
}

func TestTaskTypesHidePlatformTypes(t *testing.T) {
	h := newHarness(t)
	names := func(tok, query string) string {
		w := h.do("GET", p+"/task-types"+query, tok, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list = %d %s", w.Code, w.Body)
		}
		var out []string
		for _, it := range decode[typeList](t, w).Items {
			out = append(out, it.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("viewer-a", ""); got != "ipam:scan-network,notification:send-test-email" {
		t.Fatalf("tenant view = %s", got)
	}
	if got := names("root", ""); got != "ipam:platform-sweep,ipam:scan-network,notification:send-test-email" {
		t.Fatalf("platform view = %s", got)
	}
	if got := names("viewer-a", "?module=notification&available=true"); got != mailType {
		t.Fatalf("filtered = %s", got)
	}
	// module retirement: platform admins only, tasks:manage
	if w := h.do("POST", p+"/modules/ipam/unregister", "admin-a", ""); w.Code != http.StatusForbidden {
		t.Fatalf("tenant admin retire = %d", w.Code)
	}
	if w := h.do("POST", p+"/modules/IPAM!/unregister", "root", ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad module = %d", w.Code)
	}
	w := h.do("POST", p+"/modules/ipam/unregister", "root", "")
	if w.Code != http.StatusOK || decode[map[string]int](t, w)["affected"] != 2 {
		t.Fatalf("retire = %d %s", w.Code, w.Body)
	}
	if got := names("viewer-a", "?available=false"); got != scanType {
		t.Fatalf("retired = %s", got)
	}
}

func TestCronPreview(t *testing.T) {
	h := newHarness(t)
	w := h.do("GET", p+"/cron/preview?expression=0%203%20*%20*%20*&count=5", "viewer-a", "")
	if w.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	if next := decode[map[string][]string](t, w)["next"]; len(next) != 5 || next[0] != "2026-09-29T03:00:00Z" {
		t.Fatalf("next = %v", next)
	}
	w = h.do("GET", p+"/cron/preview?expression=61%20*%20*%20*%20*", "viewer-a", "")
	if e := decode[errJSON](t, w); w.Code != 422 || e.Reason != "invalid_cron" || e.Detail["field"] != "cron" {
		t.Fatalf("invalid cron = %d %s", w.Code, w.Body)
	}
	w = h.do("GET", p+"/cron/preview?expression=0%203%20*%20*%20*&timezone=Mars/Base", "viewer-a", "")
	if e := decode[errJSON](t, w); w.Code != 422 || e.Reason != "invalid_timezone" {
		t.Fatalf("invalid tz = %d %s", w.Code, w.Body)
	}
	if w = h.do("GET", p+"/cron/preview?expression=0%203%20*%20*%20*&count=11", "viewer-a", ""); w.Code != 422 {
		t.Fatalf("count bound = %d", w.Code)
	}
}

func TestTaskCRUDOverHTTP(t *testing.T) {
	h := newHarness(t)
	tk := h.create("admin-a", `{"name":"Nightly scan","type_name":"ipam:scan-network","kind":"periodic","payload":{"all":true}}`)
	if tk.Cron != "0 3 * * *" || tk.MaxRetries != 2 || tk.State != "enabled" || tk.TypeDisplayName != "Scan network" || tk.Scope != "tenant" ||
		tk.NextRunAt == nil || string(tk.Payload) != `{"all":true}` {
		t.Fatalf("created = %+v", tk)
	}
	if w := h.do("GET", p+"/tasks/"+tk.ID, "viewer-a", ""); w.Code != 200 || decode[taskJSON](t, w).Name != "Nightly scan" {
		t.Fatalf("get = %d %s", w.Code, w.Body)
	}
	w := h.do("GET", p+"/tasks?kind=periodic&state=enabled&page=1&page_size=10", "viewer-a", "")
	page := decode[struct {
		Items []taskJSON `json:"items"`
		Total int        `json:"total"`
	}](t, w)
	if w.Code != 200 || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list = %d %s", w.Code, w.Body)
	}
	w = h.do("PUT", p+"/tasks/"+tk.ID, "admin-a", `{"name":"Hourly scan","cron":"0 * * * *","payload":{"subnetId":"s-1"}}`)
	if w.Code != 200 || decode[taskJSON](t, w).Cron != "0 * * * *" {
		t.Fatalf("update = %d %s", w.Code, w.Body)
	}
	// wait_result returns its execution id at once
	wr := h.create("admin-a", `{"name":"Test mail","type_name":"notification:send-test-email","kind":"wait_result","payload":{"recipient":"ops@example.org"}}`)
	if wr.ExecutionID == "" {
		t.Fatal("wait_result without execution id")
	}
	if w := h.do("GET", p+"/executions/"+wr.ExecutionID, "viewer-a", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"queued"`) {
		t.Fatalf("follow = %d %s", w.Code, w.Body)
	}
	// delayed with delay_seconds 0 means now
	d := h.create("admin-a", `{"name":"Now","type_name":"notification:send-test-email","kind":"delayed","payload":{"recipient":"ops@example.org"},"delay_seconds":0}`)
	if d.NextRunAt == nil || *d.NextRunAt != "2026-09-28T10:00:00Z" {
		t.Fatalf("delay 0 = %+v", d.NextRunAt)
	}
	// viewers never mutate
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/tasks", `{"name":"x","type_name":"ipam:scan-network","kind":"periodic"}`},
		{"PUT", "/tasks/" + tk.ID, `{"name":"x"}`},
		{"DELETE", "/tasks/" + tk.ID, ""},
		{"POST", "/tasks/" + tk.ID + "/run", ""},
		{"POST", "/tasks/bulk/stop", ""},
		{"POST", "/backup/export", ""},
	} {
		if w := h.do(c.method, p+c.path, "viewer-a", c.body); w.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d", c.method, c.path, w.Code)
		}
	}
	if w := h.do("DELETE", p+"/tasks/"+tk.ID, "admin-a", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", p+"/tasks/"+tk.ID, "admin-a", ""); w.Code != http.StatusNotFound {
		t.Fatalf("deleted = %d", w.Code)
	}
	var kinds []string
	for _, e := range h.rec.got {
		kinds = append(kinds, string(e.EventType))
	}
	if !strings.Contains(strings.Join(kinds, ","), "task.create,task.update,task.create,task.create,task.delete") {
		t.Fatalf("audit = %v", kinds)
	}
}

func TestValidationReasonsNameTheField(t *testing.T) {
	h := newHarness(t)
	h.create("admin-a", `{"name":"Taken","type_name":"ipam:scan-network","kind":"periodic"}`)
	cases := []struct {
		body, reason, field string
		status              int
	}{
		{`{"name":"a","type_name":"notification:send-test-email","kind":"delayed","payload":{"recipient":5}}`, "invalid_payload", "/recipient", 422},
		{`{"name":"a","type_name":"notification:send-test-email","kind":"delayed","payload":{}}`, "invalid_payload", "", 422},
		{`{"name":"a","type_name":"ipam:scan-network","kind":"periodic","cron":"61 * * * *"}`, "invalid_cron", "cron", 422},
		{`{"name":"a","type_name":"ipam:scan-network","kind":"periodic","timezone":"Nowhere/Land"}`, "invalid_timezone", "timezone", 422},
		{`{"name":"a","type_name":"ipam:scan-network","kind":"periodic","timeout_seconds":7200}`, "invalid_options", "timeout_seconds", 422},
		{`{"name":"a","type_name":"notification:send-test-email","kind":"delayed","payload":{"recipient":"a@b.example"},"run_at":"2026-09-28T11:00:00Z","delay_seconds":60}`, "invalid_run_at", "run_at", 422},
		{`{"name":"a","type_name":"ipam:nope","kind":"periodic"}`, "type_unavailable", "", 422},
		{`{"name":"a","type_name":"ipam:platform-sweep","kind":"delayed"}`, "type_unavailable", "", 422},
		{`{"name":"taken","type_name":"ipam:scan-network","kind":"periodic"}`, "name_taken", "name", 409},
		{`{"name":"a","type_name":"ipam:scan-network","kind":"periodic","bogus":1}`, "validation_failed", "", 422},
		{`{"name":"a","type_name":"ipam:scan-network","kind":"hourly"}`, "validation_failed", "", 422},
		{`not json`, "", "", 400},
	}
	for _, c := range cases {
		w := h.do("POST", p+"/tasks", "admin-a", c.body)
		if w.Code != c.status {
			t.Errorf("%s = %d %s", c.body, w.Code, w.Body)
			continue
		}
		if c.reason == "" {
			continue
		}
		e := decode[errJSON](t, w)
		if e.Reason != c.reason || (c.field != "" && e.Detail["field"] != c.field) {
			t.Errorf("%s = %+v", c.body, e)
		}
	}
	// every minute is exactly the 60 s platform minimum: accepted
	w := h.do("POST", p+"/tasks", "admin-a", `{"name":"a","type_name":"ipam:scan-network","kind":"periodic","payload":{"all":true},"cron":"* * * * *"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("every minute = %d %s", w.Code, w.Body)
	}
}

func TestCrossTenantAndPlatformIsolation(t *testing.T) {
	h := newHarness(t)
	a := h.create("admin-a", `{"name":"A","type_name":"ipam:scan-network","kind":"periodic"}`)
	plat := h.create("root", `{"name":"Sweep","type_name":"ipam:platform-sweep","kind":"delayed"}`)
	if plat.TenantID != "00000000-0000-0000-0000-000000000000" || plat.Scope != "platform" {
		t.Fatalf("platform task = %+v", plat)
	}
	for _, target := range []string{a.ID, plat.ID} {
		for _, c := range []struct{ method, path, body string }{
			{"GET", "/tasks/" + target, ""},
			{"PUT", "/tasks/" + target, `{"name":"stolen"}`},
			{"DELETE", "/tasks/" + target, ""},
			{"POST", "/tasks/" + target + "/run", ""},
			{"POST", "/tasks/" + target + "/stop", ""},
			{"POST", "/tasks/" + target + "/cancel", ""},
		} {
			if w := h.do(c.method, p+c.path, "admin-b", c.body); w.Code != http.StatusNotFound {
				t.Errorf("tenant B %s %s = %d", c.method, c.path, w.Code)
			}
		}
	}
	// tenant A does not see the platform task either
	if w := h.do("GET", p+"/tasks/"+plat.ID, "admin-a", ""); w.Code != http.StatusNotFound {
		t.Fatalf("tenant A sees platform task: %d", w.Code)
	}
	// B's bulk stop leaves A's task alone; the platform admin sees everything
	if w := h.do("POST", p+"/tasks/bulk/stop", "admin-b", ""); w.Code != 200 || decode[map[string]int](t, w)["affected"] != 0 {
		t.Fatalf("B bulk = %d %s", w.Code, w.Body)
	}
	w := h.do("GET", p+"/tasks", "root", "")
	if decode[struct {
		Total int `json:"total"`
	}](t, w).Total != 2 {
		t.Fatalf("root list = %s", w.Body)
	}
	if w := h.do("GET", p+"/tasks?tenant_id="+tenantB, "admin-a", ""); !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("tenant filter ignored for non-admins: %s", w.Body)
	}
	if w := h.do("GET", p+"/tasks/not-a-uuid", "admin-a", ""); w.Code != 422 {
		t.Fatalf("malformed id = %d", w.Code)
	}
}

func TestControlOverHTTP(t *testing.T) {
	h := newHarness(t)
	tk := h.create("admin-a", `{"name":"scan","type_name":"ipam:scan-network","kind":"periodic"}`)
	for _, action := range []string{"stop", "start", "restart"} {
		w := h.do("POST", p+"/tasks/"+tk.ID+"/"+action, "admin-a", "")
		if w.Code != 200 {
			t.Fatalf("%s = %d %s", action, w.Code, w.Body)
		}
	}
	w := h.do("POST", p+"/tasks/"+tk.ID+"/run", "admin-a", "")
	if w.Code != http.StatusAccepted || decode[map[string]string](t, w)["execution_id"] == "" {
		t.Fatalf("run = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tasks/"+tk.ID+"/run", "admin-a", ""); w.Code != 409 || decode[errJSON](t, w).Reason != "run_in_progress" {
		t.Fatalf("run twice = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tasks/"+tk.ID+"/cancel", "admin-a", ""); w.Code != 409 || decode[errJSON](t, w).Reason != "not_cancellable" {
		t.Fatalf("cancel periodic = %d %s", w.Code, w.Body)
	}
	o := h.create("admin-a", `{"name":"o","type_name":"notification:send-test-email","kind":"delayed","payload":{"recipient":"a@b.example"},"delay_seconds":60}`)
	if w := h.do("POST", p+"/tasks/"+o.ID+"/stop", "admin-a", ""); w.Code != 409 || decode[errJSON](t, w).Reason != "not_periodic" {
		t.Fatalf("stop one-shot = %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", p+"/tasks/"+o.ID+"/cancel", "admin-a", ""); w.Code != 200 || decode[taskJSON](t, w).State != "cancelled" {
		t.Fatalf("cancel = %d %s", w.Code, w.Body)
	}
	// the bulk route resolves to /tasks/bulk/{action}, never /tasks/{id}/start
	for _, action := range []string{"stop", "start", "restart"} {
		w := h.do("POST", p+"/tasks/bulk/"+action, "admin-a", "")
		if w.Code != 200 || decode[map[string]int](t, w)["affected"] != 1 {
			t.Fatalf("bulk %s = %d %s", action, w.Code, w.Body)
		}
	}
	if w := h.do("POST", p+"/tasks/bulk/explode", "admin-a", ""); w.Code != 422 && w.Code != 404 {
		t.Fatalf("bulk unknown = %d", w.Code)
	}
	found := false
	for _, e := range h.rec.got {
		if e.EventType == audit.TasksBulk {
			found = true
		}
	}
	if !found {
		t.Fatal("bulk not audited")
	}
}

// SC-008: payload values never reach logs, audit rows or events.
func TestPayloadNeverLeaks(t *testing.T) {
	h := newHarness(t)
	body := `{"name":"leak","type_name":"notification:send-test-email","kind":"wait_result","payload":{"recipient":"` + strings.ToLower(marker) + `@example.org"}}`
	tk := h.create("admin-a", body)
	h.do("PUT", p+"/tasks/"+tk.ID, "admin-a", `{"name":"leak2","payload":{"recipient":"x`+strings.ToLower(marker)+`@example.org"}}`)
	h.do("POST", p+"/tasks", "admin-a", `{"name":"bad","type_name":"notification:send-test-email","kind":"delayed","payload":{"recipient":"`+marker+`"}}`)
	if strings.Contains(strings.ToLower(h.logs.String()), strings.ToLower(marker)) {
		t.Fatal("payload in logs")
	}
	raw, _ := json.Marshal(h.rec.got)
	if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(marker)) {
		t.Fatal("payload in audit")
	}
	ev, _ := json.Marshal(h.pub.Events())
	if strings.Contains(strings.ToLower(string(ev)), strings.ToLower(marker)) {
		t.Fatal("payload in events")
	}
}
