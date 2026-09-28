package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream"
)

const (
	tn       = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	platform = "00000000-0000-0000-0000-000000000001"
)

func TestEmitterRoutesAndIsContentSafe(t *testing.T) {
	rec := NewRecorder()
	em := Emitter{Pub: rec, PlatformTenant: platform}
	x := store.Execution{ID: "e1", TaskID: "t1", TenantID: tn, Status: store.ExecRunning, Attempt: 2,
		Message: "SECRET-MESSAGE", Result: []byte(`{"secret":"SECRET-RESULT"}`)}
	em.Execution(context.Background(), x)
	x.TenantID = store.PlatformScopeTenant
	em.Execution(context.Background(), x)
	em.Task(context.Background(), tn, "t1")
	em.Task(context.Background(), store.PlatformScopeTenant, "t2")
	got := rec.Events()
	if len(got) != 4 {
		t.Fatalf("events = %d", len(got))
	}
	if got[0].TenantID != tn || got[1].TenantID != platform || got[2].TenantID != tn || got[3].TenantID != platform {
		t.Fatalf("routing: %+v", got)
	}
	if got[0].Type != ExecutionChanged || got[2].Type != TaskChanged {
		t.Fatalf("types: %+v", got)
	}
	raw, _ := json.Marshal(got[0].Payload)
	if string(raw) != `{"execution_id":"e1","task_id":"t1","status":"running","attempt":2}` {
		t.Fatalf("payload = %s", raw)
	}
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("message/result in event")
	}
	// No platform tenant configured: platform rows are not published anywhere.
	rec2 := NewRecorder()
	Emitter{Pub: rec2}.Task(context.Background(), store.PlatformScopeTenant, "t")
	Emitter{Pub: rec2}.Execution(context.Background(), x)
	Emitter{}.Task(context.Background(), tn, "t")
	Emitter{}.Execution(context.Background(), x)
	if len(rec2.Events()) != 0 {
		t.Fatal("unrouted events published")
	}
	if len(Types) != 2 {
		t.Fatal("types")
	}
}

func TestHubPublisher(t *testing.T) {
	HubPublisher{}.Publish(context.Background(), tn, TaskChanged, TaskPayload{TaskID: "t"}) // nil hub: no-op
	mem := stream.NewMemory()
	hub := stream.NewHub(mem, stream.Config{}, nil)
	defer hub.Close()
	HubPublisher{Hub: hub}.Publish(context.Background(), tn, TaskChanged, TaskPayload{TaskID: "t"})
	deadline := time.Now().Add(time.Second)
	for mem.Len(stream.Key(tn)) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if mem.Len(stream.Key(tn)) != 1 {
		t.Fatal("event not on the tenant stream")
	}
}
