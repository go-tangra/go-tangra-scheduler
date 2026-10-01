package contract

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// listPage is the list contract response (go-tangra specs/032-server-side-tables).
type listPage struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	Total    int    `json:"total"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Sort     string `json:"sort"`
	Order    string `json:"order"`
	Counts   *struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Other     int `json:"other"`
	} `json:"counts"`
}

// seedLists imports 13 tasks and 17 attempts into tenant A and 2 tasks into
// tenant B, with ties on every sortable value.
func seedLists(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	base := h.clk.Now()
	for i := 0; i < 15; i++ {
		tenant := tenantA
		if i >= 13 {
			tenant = tenantB
		}
		tk := store.Task{ID: store.NewID(), TenantID: tenant, Name: fmt.Sprintf("task %02d", i), TypeName: []string{scanType, mailType}[i%2],
			Module: "ipam", Kind: store.KindDelayed, Payload: []byte(`{}`), Enabled: i%3 != 0, Status: store.TaskActive, Validity: store.ValidityOK,
			TimeoutSeconds: 300, Timezone: "UTC", CreatedAt: base, UpdatedAt: base.Add(time.Duration(i%4) * time.Minute)}
		if i%2 == 0 {
			next := base.Add(time.Duration(i%3) * time.Hour)
			tk.NextRunAt = &next
		}
		if _, err := h.st.ImportTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 17; i++ {
		e := store.Execution{ID: store.NewID(), TenantID: tenantA, TaskID: store.NewID(), OccurrenceID: store.NewID(), TypeName: scanType, Module: "ipam",
			Trigger: []string{store.TriggerSchedule, store.TriggerManual}[i%2], OccurrenceAt: base, DueAt: base,
			Status: []string{store.ExecSucceeded, store.ExecFailed, store.ExecSkipped}[i%3], Attempt: 1, MaxAttempts: 1,
			DurationMS: int64(i%3) * 100, CreatedAt: base.Add(time.Duration(i/2) * time.Second)}
		if _, err := h.st.ImportExecution(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

func (h *harness) page(path string) listPage {
	h.t.Helper()
	w := h.do("GET", path, "viewer-a", "")
	if w.Code != http.StatusOK {
		h.t.Fatalf("%s = %d %s", path, w.Code, w.Body)
	}
	return decode[listPage](h.t, w)
}

func TestListsPageAndSort(t *testing.T) {
	h := newHarness(t)
	seedLists(t, h)
	for _, c := range []struct {
		path  string
		sorts []string
		total int
	}{
		{p + "/tasks", []string{"name", "type", "state", "next_run_at", "updated_at"}, 13},
		{p + "/executions", []string{"created_at", "status", "duration", "trigger"}, 17},
	} {
		for _, sort := range c.sorts {
			for _, order := range []string{"asc", "desc"} {
				seen := map[string]int{}
				for page := 1; page <= 4; page++ {
					pg := h.page(fmt.Sprintf("%s?page=%d&page_size=5&sort=%s&order=%s", c.path, page, sort, order))
					if pg.Total != c.total || pg.Sort != sort || pg.Order != order || pg.PageSize != 5 || pg.Page != min(page, (c.total+4)/5) {
						t.Fatalf("%s %s %s page %d: %+v", c.path, sort, order, page, pg)
					}
					if pg.Page != page {
						continue // clamped: the last page again
					}
					for _, it := range pg.Items {
						seen[it.ID]++
					}
				}
				if len(seen) != c.total {
					t.Fatalf("%s %s %s: %d distinct rows", c.path, sort, order, len(seen))
				}
				for id, n := range seen {
					if n != 1 {
						t.Fatalf("%s %s %s: %s seen %d times", c.path, sort, order, id, n)
					}
				}
			}
		}
	}
	// Defaults and field default directions.
	if pg := h.page(p + "/tasks"); pg.Page != 1 || pg.PageSize != 25 || pg.Sort != "name" || pg.Order != "asc" || pg.Counts != nil {
		t.Fatalf("task defaults = %+v", pg)
	}
	if pg := h.page(p + "/tasks?sort=updated_at"); pg.Order != "desc" {
		t.Fatalf("updated_at default dir = %s", pg.Order)
	}
	pg := h.page(p + "/executions?status=succeeded&page=9&page_size=2")
	if pg.Sort != "created_at" || pg.Order != "desc" || pg.Total != 6 || pg.Page != 3 || len(pg.Items) != 2 ||
		pg.Counts == nil || pg.Counts.Succeeded != 6 || pg.Counts.Failed != 6 || pg.Counts.Other != 5 {
		t.Fatalf("history page = %+v %+v", pg, pg.Counts)
	}
	if pg := h.page(p + "/executions?sort=duration"); pg.Order != "desc" {
		t.Fatalf("duration default dir = %s", pg.Order)
	}
	// Tenant B sees only its own tasks.
	w := h.do("GET", p+"/tasks", "admin-b", "")
	if decode[listPage](t, w).Total != 2 {
		t.Fatalf("tenant B total: %s", w.Body)
	}
}

func TestListsRejectInvalidParameters(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ query, param string }{
		{"sort=bogus", "sort"},
		{"sort=name%3Bdrop%20table%20x", "sort"},
		{"sort=created_at", "sort"}, // an executions field is not a task field
		{"order=up", "order"},
		{"page_size=201", "page_size"},
		{"page_size=0", "page_size"},
		{"page_size=abc", "page_size"},
		{"page=0", "page"},
		{"page=-3", "page"},
		{"cursor=abc&page=2", "cursor"}, // legacy and page styles mixed
	} {
		for _, path := range []string{p + "/tasks?", p + "/executions?"} {
			q := c.query
			if strings.HasSuffix(path, "/executions?") && c.query == "sort=created_at" {
				q = "sort=name"
			}
			w := h.do("GET", path+q, "viewer-a", "")
			body := w.Body.String()
			e := decode[errJSON](t, w)
			if w.Code != http.StatusUnprocessableEntity || e.Reason != "validation_failed" || e.Detail["param"] != c.param {
				t.Errorf("%s%s = %d %s", path, q, w.Code, body)
			}
			if strings.Contains(body, "drop") || strings.Contains(body, "bogus") || strings.Contains(body, "abc") {
				t.Errorf("%s%s echoed the value: %s", path, q, body)
			}
		}
	}
}
