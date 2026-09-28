package taskexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	schedulerv1 "github.com/go-tangra/go-tangra-scheduler/sdk/v4/api/proto/scheduler/v1"
)

const tenant = "0190f0c2-7a3b-7c11-8000-000000000001"

func caller(name string, ok bool) func(context.Context) (string, bool) {
	return func(context.Context) (string, bool) { return name, ok }
}

func newServer(t *testing.T, h map[string]Handler, o Options) (*Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	if o.Log == nil {
		o.Log = slog.New(slog.NewJSONHandler(&buf, nil))
	}
	return NewServer(h, o), &buf
}

func code(err error) codes.Code { return status.Code(err) }

func TestCallerChecks(t *testing.T) {
	h := map[string]Handler{"m:a": func(context.Context, Request) Result { return OK("done") }}
	req := &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant}
	cases := []struct {
		name string
		o    Options
	}{
		{"nil caller fails closed", Options{}},
		{"no peer", Options{Caller: caller("", false)}},
		{"other service", Options{Caller: caller("ipam", true)}},
		{"custom scheduler name mismatch", Options{Caller: caller("scheduler", true), Scheduler: "sched2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newServer(t, h, tc.o)
			if _, err := s.ExecuteTask(context.Background(), req); code(err) != codes.PermissionDenied {
				t.Fatalf("got %v, want PermissionDenied", err)
			}
		})
	}
}

func TestTenantAndPayloadBounds(t *testing.T) {
	called := 0
	h := map[string]Handler{
		"m:a": func(context.Context, Request) Result { called++; return OK("ok") },
		"m:p": func(_ context.Context, r Request) Result { called++; return OK("platform " + r.TenantID) },
	}
	s, _ := newServer(t, h, Options{Caller: caller("scheduler", true), Platform: map[string]bool{"m:p": true}})
	cases := []struct {
		name string
		req  *schedulerv1.ExecuteTaskRequest
		want codes.Code
	}{
		{"missing tenant", &schedulerv1.ExecuteTaskRequest{TaskType: "m:a"}, codes.InvalidArgument},
		{"bad tenant", &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: "42"}, codes.InvalidArgument},
		{"bad tenant platform type", &schedulerv1.ExecuteTaskRequest{TaskType: "m:p", TenantId: "x"}, codes.InvalidArgument},
		{"payload too large", &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant, Payload: bytes.Repeat([]byte(" "), MaxPayloadBytes+1)}, codes.InvalidArgument},
		{"payload array", &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant, Payload: []byte(`[1]`)}, codes.InvalidArgument},
		{"payload invalid json", &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant, Payload: []byte(`{"a":`)}, codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.ExecuteTask(context.Background(), tc.req); code(err) != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if called != 0 {
		t.Fatalf("handler called %d times on refused requests", called)
	}
	res, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{TaskType: "m:p"})
	if err != nil || !res.GetSuccess() || res.GetMessage() != "platform " {
		t.Fatalf("platform type without tenant: %v %v", res, err)
	}
}

func TestRequestMappingAndResult(t *testing.T) {
	at := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	var got Request
	h := map[string]Handler{"m:a": func(_ context.Context, r Request) Result {
		got = r
		return Result{Success: true, Message: "queued 2", Data: map[string]int{"queued": 2}, Permanent: true}
	}}
	s, logs := newServer(t, h, Options{Caller: caller("scheduler", true)})
	res, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{
		ExecutionId: "e1", TaskId: "t1", OccurrenceId: "o1", TaskType: "m:a", TenantId: tenant,
		Payload: []byte(`  {"secretValue":"do-not-log"} `), Attempt: 2, MaxAttempts: 3, ScheduledAt: timestamppb.New(at),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionID != "e1" || got.TaskID != "t1" || got.OccurrenceID != "o1" || got.TenantID != tenant ||
		got.Attempt != 2 || got.MaxAttempts != 3 || !got.ScheduledAt.Equal(at) || string(got.Payload) != `{"secretValue":"do-not-log"}` {
		t.Fatalf("request mapping: %+v", got)
	}
	if !res.GetSuccess() || res.GetPermanentFailure() || string(res.GetResultData()) != `{"queued":2}` {
		t.Fatalf("response: %+v", res)
	}
	if strings.Contains(logs.String(), "do-not-log") {
		t.Fatal("payload value leaked into the log")
	}
	if !strings.Contains(logs.String(), `"execution_id":"e1"`) {
		t.Fatalf("expected an execution log line, got %s", logs.String())
	}
}

func TestEmptyPayloadAndNoScheduledAt(t *testing.T) {
	var got Request
	h := map[string]Handler{"m:a": func(_ context.Context, r Request) Result { got = r; return OK("") }}
	s := NewServer(h, Options{Caller: caller("scheduler", true)}) // no logger
	if _, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant}); err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != "{}" || !got.ScheduledAt.IsZero() {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestUnknownTypeIsPermanent(t *testing.T) {
	s, _ := newServer(t, nil, Options{Caller: caller("scheduler", true)})
	res, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{TaskType: strings.Repeat("x", 300), TenantId: tenant})
	if err != nil || res.GetSuccess() || !res.GetPermanentFailure() || len(res.GetMessage()) > 230 {
		t.Fatalf("unknown type: %+v %v", res, err)
	}
}

func TestPanicIsRetryableWithoutText(t *testing.T) {
	h := map[string]Handler{"m:a": func(context.Context, Request) Result { panic("secret-panic-detail") }}
	for _, withLog := range []bool{true, false} {
		o := Options{Caller: caller("scheduler", true)}
		var logs bytes.Buffer
		if withLog {
			o.Log = slog.New(slog.NewJSONHandler(&logs, nil))
		}
		s := NewServer(h, o)
		res, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant})
		if err != nil || res.GetSuccess() || res.GetPermanentFailure() || strings.Contains(res.GetMessage(), "secret") {
			t.Fatalf("panic: %+v %v", res, err)
		}
		if strings.Contains(logs.String(), "secret-panic-detail") {
			t.Fatal("panic text logged")
		}
	}
}

func TestResultBounds(t *testing.T) {
	long := strings.Repeat("é", MaxMessageBytes) // 2 bytes per rune: cut must stay valid UTF-8
	cases := []struct {
		name string
		r    Result
		msg  func(string) bool
		data bool
		perm bool
	}{
		{"message clipped on rune boundary", Retry(long), func(m string) bool { return len(m) <= MaxMessageBytes && json.Valid([]byte(`"` + m + `"`)) }, false, false},
		{"permanent", Permanent("nope"), func(m string) bool { return m == "nope" }, false, true},
		{"unserialisable data", Result{Success: true, Message: "x", Data: make(chan int)}, func(m string) bool { return strings.Contains(m, "not serialisable") }, false, false},
		{"oversized data", Result{Success: true, Message: "x", Data: strings.Repeat("a", MaxResultBytes)}, func(m string) bool { return strings.Contains(m, "too large") }, false, false},
		{"data kept", Result{Success: true, Data: []int{1}}, func(string) bool { return true }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := respond(tc.r)
			if !tc.msg(out.GetMessage()) || (len(out.GetResultData()) > 0) != tc.data || out.GetPermanentFailure() != tc.perm {
				t.Fatalf("respond: %+v", out)
			}
		})
	}
	if clip("abc", 10) != "abc" || clip("aé", 2) != "a" {
		t.Fatal("clip")
	}
}

func TestDecodeStrict(t *testing.T) {
	type cfg struct {
		Days int    `json:"days"`
		Name string `json:"name"`
	}
	cases := []struct {
		name, raw string
		ok        bool
		contains  string
	}{
		{"empty is object", "", true, ""},
		{"valid", `{"days":7,"name":"x"}`, true, ""},
		{"unknown field", `{"days":7,"other":1}`, false, "unknown field"},
		{"wrong type", `{"days":"seven"}`, false, `field "days"`},
		{"not object", `[1]`, false, "expected shape"},
		{"trailing", `{"days":1} {}`, false, "trailing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c cfg
			err := DecodeStrict(json.RawMessage(tc.raw), &c)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v", err)
			}
			if err != nil && (!errors.Is(err, ErrPayload) || !strings.Contains(err.Error(), tc.contains)) {
				t.Fatalf("err = %v, want %q", err, tc.contains)
			}
			if err != nil && strings.Contains(err.Error(), "seven") {
				t.Fatal("payload value echoed in the error")
			}
		})
	}
	if !ValidTenant(tenant) || ValidTenant("nope") {
		t.Fatal("ValidTenant")
	}
}

func TestHandlersCopied(t *testing.T) {
	h := map[string]Handler{"m:a": func(context.Context, Request) Result { return OK("a") }}
	s := NewServer(h, Options{Caller: caller("scheduler", true)})
	delete(h, "m:a")
	res, err := s.ExecuteTask(context.Background(), &schedulerv1.ExecuteTaskRequest{TaskType: "m:a", TenantId: tenant})
	if err != nil || res.GetMessage() != "a" {
		t.Fatalf("handlers map must be copied: %+v %v", res, err)
	}
}
