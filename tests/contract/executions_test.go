package contract

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/engine"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

type execPage struct {
	Items []struct {
		ID              string `json:"id"`
		Status          string `json:"status"`
		Attempt         int    `json:"attempt"`
		Trigger         string `json:"trigger"`
		Result          any    `json:"result"`
		ResultTruncated bool   `json:"result_truncated"`
	} `json:"items"`
	Total  int `json:"total"`
	Counts struct {
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Other     int `json:"other"`
	} `json:"counts"`
}

// history runs a task through the engine: attempt 1 fails retryably,
// attempt 2 succeeds with a JSON result; a second task ends with text.
func history(t *testing.T, h *harness) (string, string) {
	t.Helper()
	tk := h.create("admin-a", `{"name":"scan","type_name":"ipam:scan-network","kind":"periodic","payload":{"all":true}}`)
	txt := h.create("admin-a", `{"name":"mail","type_name":"notification:send-test-email","kind":"wait_result","payload":{"recipient":"a@b.example"}}`)
	script := map[string][]engine.Outcome{
		tk.ID:  {{Status: store.ExecFailed, Message: "module unavailable", Retryable: true}, {Status: store.ExecSucceeded, Message: "queued 3 scan(s)", Result: []byte(`{"queued":3}`)}},
		txt.ID: {{Status: store.ExecSucceeded, Message: "sent", Result: []byte("plain text"), Truncated: true}},
	}
	now := h.clk.Now()
	clock := func() time.Time { return now }
	var mu sync.Mutex
	e := engine.New(engine.Config{InstanceID: "it"}, engine.Deps{Store: h.st, Now: clock, Dispatcher: engine.DispatcherFunc(func(_ context.Context, a engine.Attempt) engine.Outcome {
		mu.Lock()
		defer mu.Unlock()
		o := script[a.Exec.TaskID][0]
		script[a.Exec.TaskID] = script[a.Exec.TaskID][1:]
		return o
	})})
	if w := h.do("POST", p+"/tasks/"+tk.ID+"/run", "admin-a", ""); w.Code != http.StatusAccepted {
		t.Fatalf("run = %d", w.Code)
	}
	e.Cycle(context.Background())
	e.Wait()
	now = now.Add(time.Minute) // past the 30 s backoff
	e.Cycle(context.Background())
	e.Wait()
	return tk.ID, txt.ID
}

func TestExecutionHistory(t *testing.T) {
	h := newHarness(t)
	scan, mail := history(t, h)
	w := h.do("GET", p+"/executions?task_id="+scan, "viewer-a", "")
	pg := decode[execPage](t, w)
	if w.Code != 200 || pg.Total != 2 || pg.Items[0].Attempt != 2 || pg.Items[0].Status != "succeeded" || pg.Items[1].Status != "failed" ||
		pg.Counts.Succeeded != 1 || pg.Counts.Failed != 1 || pg.Items[0].Trigger != "manual" {
		t.Fatalf("history = %d %s", w.Code, w.Body)
	}
	if pg.Items[0].Result != nil {
		t.Fatal("list carries results")
	}
	// failed only / status filter keep the counts of the whole history
	w = h.do("GET", p+"/executions?task_id="+scan+"&failed_only=true", "viewer-a", "")
	if pg = decode[execPage](t, w); pg.Total != 1 || pg.Items[0].Status != "failed" || pg.Counts.Succeeded != 1 {
		t.Fatalf("failed only = %s", w.Body)
	}
	w = h.do("GET", p+"/executions?status=succeeded&status=failed&trigger=manual&page_size=1&page=2", "viewer-a", "")
	if pg = decode[execPage](t, w); pg.Total != 2 || len(pg.Items) != 1 {
		t.Fatalf("paging = %s", w.Body)
	}
	from := url.QueryEscape(h.clk.Now().Add(time.Hour).Format(time.RFC3339))
	if w = h.do("GET", p+"/executions?from="+from, "viewer-a", ""); decode[execPage](t, w).Total != 0 {
		t.Fatalf("time range = %s", w.Body)
	}
	to := url.QueryEscape(h.clk.Now().Add(time.Hour).Format(time.RFC3339))
	if w = h.do("GET", p+"/executions?to="+to, "viewer-a", ""); decode[execPage](t, w).Total != 3 {
		t.Fatalf("to = %s", w.Body)
	}
	if w = h.do("GET", p+"/executions?status=exploded", "viewer-a", ""); w.Code != 422 {
		t.Fatalf("bad status = %d", w.Code)
	}
	// one attempt: JSON result as JSON, text as a string, truncated flag
	ids := map[string]string{}
	for _, tid := range []string{scan, mail} {
		w = h.do("GET", p+"/executions?task_id="+tid+"&status=succeeded", "viewer-a", "")
		ids[tid] = decode[execPage](t, w).Items[0].ID
	}
	w = h.do("GET", p+"/executions/"+ids[scan], "viewer-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"result":{"queued":3}`) || !strings.Contains(w.Body.String(), `"message":"queued 3 scan(s)"`) {
		t.Fatalf("json result = %s", w.Body)
	}
	w = h.do("GET", p+"/executions/"+ids[mail], "viewer-a", "")
	if !strings.Contains(w.Body.String(), `"result":"plain text"`) || !strings.Contains(w.Body.String(), `"result_truncated":true`) {
		t.Fatalf("text result = %s", w.Body)
	}
	// cross-tenant: 404 and empty lists
	if w = h.do("GET", p+"/executions/"+ids[scan], "admin-b", ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant execution = %d", w.Code)
	}
	if w = h.do("GET", p+"/executions?task_id="+scan, "admin-b", ""); decode[execPage](t, w).Total != 0 {
		t.Fatalf("cross-tenant history = %s", w.Body)
	}
	// the task shows its last run
	w = h.do("GET", p+"/tasks/"+scan, "viewer-a", "")
	if !strings.Contains(w.Body.String(), `"last_status":"succeeded"`) || !strings.Contains(w.Body.String(), `"run_count":1`) {
		t.Fatalf("task last run = %s", w.Body)
	}
}

func TestOverviewOverHTTP(t *testing.T) {
	h := newHarness(t)
	history(t, h)
	w := h.do("GET", p+"/overview", "viewer-a", "")
	ov := decode[struct {
		Tasks struct {
			Enabled   int `json:"enabled"`
			Completed int `json:"completed"`
		} `json:"tasks"`
		Runs24h     int              `json:"runs_24h"`
		Failures24h int              `json:"failures_24h"`
		NextDue     []map[string]any `json:"next_due"`
		Failing     []map[string]any `json:"failing"`
	}](t, w)
	if w.Code != 200 || ov.Tasks.Enabled != 1 || ov.Tasks.Completed != 1 || ov.Runs24h != 3 || ov.Failures24h != 1 || len(ov.NextDue) != 1 || len(ov.Failing) != 0 {
		t.Fatalf("overview = %d %s", w.Code, w.Body)
	}
	if w = h.do("GET", p+"/overview", "admin-b", ""); !strings.Contains(w.Body.String(), `"runs_24h":0`) {
		t.Fatalf("tenant B overview = %s", w.Body)
	}
}

func TestStreamRoute(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", p+"/stream", nil)
	r.Header.Set("Authorization", "Bearer viewer-a")
	r.Header.Set("Last-Event-ID", "0-0")
	w := &flushRecorder{header: http.Header{}}
	done := make(chan struct{})
	go func() { h.s.Handler().ServeHTTP(w, r); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if w.code != 200 || !strings.HasPrefix(w.header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream = %d %v", w.code, w.header)
	}
}

type flushRecorder struct {
	header http.Header
	code   int
	body   strings.Builder
}

func (f *flushRecorder) Header() http.Header { return f.header }
func (f *flushRecorder) Write(b []byte) (int, error) {
	if f.code == 0 {
		f.code = 200
	}
	return f.body.Write(b)
}
func (f *flushRecorder) WriteHeader(c int) { f.code = c }
func (f *flushRecorder) Flush()            {}
