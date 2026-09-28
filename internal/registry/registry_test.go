package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

const (
	tn       = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	ipamID   = "spiffe://example.org/svc/ipam"
	scanType = "ipam:scan-network"
	schemaV1 = `{"type":"object","properties":{"subnetId":{"type":"string"},"all":{"type":"boolean"}},"additionalProperties":false}`
	schemaV2 = `{"type":"object","required":["subnetId"],"properties":{"subnetId":{"type":"string"}}}`
)

var (
	ctx = context.Background()
	at  = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
)

type rec struct{ got []audit.Event }

func (r *rec) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.got = append(r.got, e)
	return nil
}

func (r *rec) last() audit.Event { return r.got[len(r.got)-1] }

// faulty wraps the memstore with per-method failures.
type faulty struct {
	*memstore.Mem
	getErr, upsertErr, updateErr, markErr, listErr error
}

func (f *faulty) GetTaskType(ctx context.Context, name string) (store.TaskType, error) {
	if f.getErr != nil {
		return store.TaskType{}, f.getErr
	}
	return f.Mem.GetTaskType(ctx, name)
}

func (f *faulty) UpsertTaskTypes(ctx context.Context, module string, ts []store.TaskType) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	return f.Mem.UpsertTaskTypes(ctx, module, ts)
}

func (f *faulty) UpdateTasksOfType(ctx context.Context, name string, fn func(*store.Task) bool) (int, error) {
	if f.updateErr != nil {
		return 0, f.updateErr
	}
	return f.Mem.UpdateTasksOfType(ctx, name, fn)
}

func (f *faulty) MarkModuleUnavailable(ctx context.Context, module string, at time.Time) (int, error) {
	if f.markErr != nil {
		return 0, f.markErr
	}
	return f.Mem.MarkModuleUnavailable(ctx, module, at)
}

func (f *faulty) ListTaskTypes(ctx context.Context, tf store.TypeFilter) ([]store.TaskType, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.Mem.ListTaskTypes(ctx, tf)
}

func setup(t *testing.T) (*Registry, *faulty, *rec) {
	t.Helper()
	st := &faulty{Mem: memstore.New()}
	r := &rec{}
	return New(Deps{Store: st, Audit: r, Now: func() time.Time { return at }, MaxPayloadBytes: 1 << 16}), st, r
}

func scan() Descriptor {
	return Descriptor{Type: scanType, DisplayName: " Scan network ", Description: "Scan subnets", PayloadSchema: schemaV1,
		DefaultCron: "0 3 * * *", DefaultMaxRetry: 2}
}

func task(id, payload string) store.Task {
	return store.Task{ID: id, TenantID: tn, Name: id, TypeName: scanType, Module: "ipam", Kind: store.KindPeriodic, Cron: "0 3 * * *",
		Payload: json.RawMessage(payload), Validity: store.ValidityOK, Status: store.TaskActive, Enabled: true}
}

func TestRegisterStoresOwnerFromPeer(t *testing.T) {
	r, st, a := setup(t)
	res, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan(), {Type: "ipam:sync", DisplayName: "Sync", Scope: ScopePlatform}})
	if err != nil || res.Registered != 2 || res.Revalidated != 0 {
		t.Fatalf("register = %+v %v", res, err)
	}
	tt, err := st.GetTaskType(ctx, scanType)
	if err != nil {
		t.Fatal(err)
	}
	if tt.Module != "ipam" || tt.DisplayName != "Scan network" || tt.DefaultCron != "0 3 * * *" || tt.DefaultMaxRetries != 2 ||
		tt.Scope != store.ScopeTenant || !tt.Available || !tt.RegisteredAt.Equal(at) || tt.SchemaHash == "" || len(tt.PayloadSchema) == 0 {
		t.Fatalf("stored = %+v", tt)
	}
	if p, _ := st.GetTaskType(ctx, "ipam:sync"); p.Scope != store.ScopePlatform || p.PayloadSchema != nil || p.SchemaHash != "" {
		t.Fatalf("platform type = %+v", p)
	}
	e := a.last()
	if e.EventType != audit.TypeRegister || e.ActorKind != audit.ActorService || e.ActorID != ipamID || e.SubjectID != "ipam" || e.TenantID != audit.NilTenant {
		t.Fatalf("audit = %+v", e)
	}
	if types := e.Details["types"].([]any); len(types) != 2 {
		t.Fatalf("audit types = %v", types)
	}
	// idempotent
	if res, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); err != nil || res.Registered != 1 {
		t.Fatalf("re-register = %+v %v", res, err)
	}
}

func TestRegisterRefusals(t *testing.T) {
	r, st, a := setup(t)
	if _, err := r.Register(ctx, "lcm", "spiffe://example.org/svc/lcm", []Descriptor{{Type: "lcm:check", DisplayName: "Check"}}); err != nil {
		t.Fatal(err)
	}
	big := `{"type":"object","description":"` + strings.Repeat("x", 70000) + `"}`
	cases := []struct {
		name   string
		owner  string
		ds     []Descriptor
		code   string
		reason string
	}{
		{"bad owner", "IPAM!", []Descriptor{scan()}, CodeDenied, ReasonInvalidOwner},
		{"no types", "ipam", nil, CodeInvalid, ReasonNoTypes},
		{"too many", "ipam", make([]Descriptor, 51), CodeInvalid, ReasonTooManyTypes},
		{"bad name", "ipam", []Descriptor{{Type: "ipam", DisplayName: "x"}}, CodeInvalid, ReasonInvalidName},
		{"upper action", "ipam", []Descriptor{{Type: "ipam:Scan", DisplayName: "x"}}, CodeInvalid, ReasonInvalidName},
		{"foreign namespace", "ipam", []Descriptor{{Type: "lcm:steal", DisplayName: "x"}}, CodeDenied, ReasonForeignNamespace},
		{"owned by other", "lcm2", []Descriptor{{Type: "lcm:check", DisplayName: "x"}}, CodeDenied, ReasonForeignNamespace},
		{"empty display", "ipam", []Descriptor{{Type: scanType, DisplayName: "  "}}, CodeInvalid, ReasonDisplayName},
		{"long display", "ipam", []Descriptor{{Type: scanType, DisplayName: strings.Repeat("d", 201)}}, CodeInvalid, ReasonDisplayName},
		{"control display", "ipam", []Descriptor{{Type: scanType, DisplayName: "a\nb"}}, CodeInvalid, ReasonDisplayName},
		{"long description", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", Description: strings.Repeat("d", 2001)}}, CodeInvalid, ReasonDescription},
		{"bad utf8 description", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", Description: "\xff"}}, CodeInvalid, ReasonDescription},
		{"big schema", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", PayloadSchema: big}}, CodeInvalid, ReasonSchemaTooLarge},
		{"non-object schema", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", PayloadSchema: `[1]`}}, CodeInvalid, ReasonInvalidSchema},
		{"broken schema", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", PayloadSchema: `{"type":5}`}}, CodeInvalid, ReasonInvalidSchema},
		{"remote ref", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", PayloadSchema: `{"$ref":"https://evil.example/s.json"}`}}, CodeInvalid, ReasonInvalidSchema},
		{"bad cron", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", DefaultCron: "61 * * * *"}}, CodeInvalid, ReasonInvalidCron},
		{"negative retries", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", DefaultMaxRetry: -1}}, CodeInvalid, ReasonInvalidRetries},
		{"many retries", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", DefaultMaxRetry: 11}}, CodeInvalid, ReasonInvalidRetries},
		{"bad scope", "ipam", []Descriptor{{Type: scanType, DisplayName: "x", Scope: "galaxy"}}, CodeInvalid, ReasonInvalidScope},
		{"duplicate", "ipam", []Descriptor{scan(), scan()}, CodeInvalid, ReasonDuplicateType},
	}
	for _, c := range cases {
		_, err := r.Register(ctx, c.owner, ipamID, c.ds)
		var re *Error
		if !errors.As(err, &re) || re.Code != c.code || re.Reason != c.reason {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if (&Error{Reason: "x", Type: "a:b"}).Error() != "registry: x: a:b" || (&Error{Reason: "x"}).Error() != "registry: x" {
		t.Fatal("error text")
	}
	// A module registering another module's existing type (same prefix spoof is
	// impossible: the prefix must equal the owner) — simulate a foreign owner row.
	if _, err := st.InsertTaskTypeIfAbsent(ctx, store.TaskType{Name: "ipam:hijacked", Module: "lcm", DisplayName: "x"}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan(), {Type: "ipam:hijacked", DisplayName: "x"}})
	var re *Error
	if !errors.As(err, &re) || re.Reason != ReasonOwnedByOther || re.Code != CodeDenied {
		t.Fatalf("owned by other: %v", err)
	}
	if _, err := st.GetTaskType(ctx, scanType); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("refused request partially written")
	}
	refused := 0
	for _, e := range a.got {
		if e.EventType == audit.AccessRefused {
			refused++
			if e.Outcome != audit.OutcomeRefused || e.ActorKind != audit.ActorService {
				t.Fatalf("refusal audit = %+v", e)
			}
		}
	}
	if refused != 3 { // foreign namespace ×2, owned by another module ×1 (invalid owner is not audited)
		t.Fatalf("refusals audited = %d", refused)
	}
}

func TestRegisterStoreFailures(t *testing.T) {
	r, st, a := setup(t)
	boom := errors.New("db down")
	st.getErr = boom
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); !errors.Is(err, boom) {
		t.Fatalf("get: %v", err)
	}
	st.getErr, st.upsertErr = nil, boom
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); !errors.Is(err, boom) {
		t.Fatalf("upsert: %v", err)
	}
	st.upsertErr = repo.ErrConflict // concurrent registration by another module
	var re *Error
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); !errors.As(err, &re) || re.Reason != ReasonOwnedByOther {
		t.Fatalf("upsert conflict: %v", err)
	}
	if a.last().EventType != audit.AccessRefused {
		t.Fatal("race refusal not audited")
	}
	st.upsertErr, st.updateErr = nil, boom
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); !errors.Is(err, boom) {
		t.Fatalf("revalidate: %v", err)
	}
}

func TestSchemaChangeRevalidatesTasks(t *testing.T) {
	r, st, _ := setup(t)
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); err != nil {
		t.Fatal(err)
	}
	st.PutTask(task("a", `{"subnetId":"s1"}`))
	st.PutTask(task("b", `{"all":true}`))
	other := task("c", `{}`)
	other.TypeName = "ipam:other"
	st.PutTask(other)
	// same schema again: nothing re-checked
	if res, _ := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); res.Revalidated != 0 {
		t.Fatalf("unchanged schema re-validated %d", res.Revalidated)
	}
	d := scan()
	d.PayloadSchema = schemaV2
	res, err := r.Register(ctx, "ipam", ipamID, []Descriptor{d})
	if err != nil || res.Revalidated != 2 {
		t.Fatalf("revalidated = %+v %v", res, err)
	}
	a, _ := st.Task("a")
	b, _ := st.Task("b")
	if a.Validity != store.ValidityOK || b.Validity != store.ValidityPayloadInvalid || !strings.Contains(b.ValidityMessage, "subnetId") {
		t.Fatalf("validity a=%s b=%s %q", a.Validity, b.Validity, b.ValidityMessage)
	}
	// fixing the schema again clears the flag
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); err != nil {
		t.Fatal(err)
	}
	if b, _ = st.Task("b"); b.Validity != store.ValidityOK || b.ValidityMessage != "" {
		t.Fatalf("cleared = %+v", b)
	}
}

func TestUnregisterAndReturn(t *testing.T) {
	r, st, a := setup(t)
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); err != nil {
		t.Fatal(err)
	}
	st.PutTask(task("a", `{"subnetId":"s1"}`))
	n, err := r.Unregister(ctx, "ipam", ipamID)
	if err != nil || n != 1 {
		t.Fatalf("unregister = %d %v", n, err)
	}
	tt, _ := st.GetTaskType(ctx, scanType)
	tk, _ := st.Task("a")
	if tt.Available || tt.UnregisteredAt == nil || tk.Validity != store.ValidityTypeUnavailable {
		t.Fatalf("after unregister: %+v %+v", tt, tk)
	}
	if e := a.last(); e.EventType != audit.TypeUnregister || e.Details["count"] != 1 {
		t.Fatalf("audit = %+v", e)
	}
	// registering again makes the type available and re-validates its tasks
	res, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()})
	if err != nil || res.Revalidated != 1 {
		t.Fatalf("return = %+v %v", res, err)
	}
	if tk, _ = st.Task("a"); tk.Validity != store.ValidityOK {
		t.Fatalf("task after return: %+v", tk)
	}
	if _, err := r.Unregister(ctx, "Bad Owner", ipamID); err == nil {
		t.Fatal("bad owner unregistered")
	}
	st.markErr = errors.New("db down")
	if _, err := r.Unregister(ctx, "ipam", ipamID); err == nil {
		t.Fatal("store failure hidden")
	}
}

func TestRetire(t *testing.T) {
	r, st, a := setup(t)
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan()}); err != nil {
		t.Fatal(err)
	}
	member := authz.User(tn, "u1", nil)
	if _, err := r.Retire(ctx, member, "ipam"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("member retire: %v", err)
	}
	if e := a.last(); e.EventType != audit.AccessRefused || e.ActorKind != audit.ActorUser {
		t.Fatalf("refusal audit = %+v", e)
	}
	admin := authz.User(tn, "root", []string{authz.RolePlatformAdmin})
	if _, err := r.Retire(ctx, admin, "no such"); err == nil {
		t.Fatal("bad module accepted")
	}
	n, err := r.Retire(ctx, admin, "ipam")
	if err != nil || n != 1 {
		t.Fatalf("retire = %d %v", n, err)
	}
	if e := a.last(); e.EventType != audit.TypeUnregister || e.ActorKind != audit.ActorUser || e.ActorID != "root" {
		t.Fatalf("retire audit = %+v", e)
	}
	st.markErr = errors.New("db down")
	if _, err := r.Retire(ctx, admin, "ipam"); err == nil {
		t.Fatal("store failure hidden")
	}
}

func TestListAndVisibleHidePlatformTypes(t *testing.T) {
	r, st, _ := setup(t)
	if _, err := r.Register(ctx, "ipam", ipamID, []Descriptor{scan(), {Type: "ipam:platform-sweep", DisplayName: "Sweep", Scope: ScopePlatform}}); err != nil {
		t.Fatal(err)
	}
	member := authz.User(tn, "u1", nil)
	admin := authz.User(tn, "root", []string{authz.RolePlatformAdmin})
	got, err := r.List(ctx, member, store.TypeFilter{})
	if err != nil || len(got) != 1 || got[0].Name != scanType {
		t.Fatalf("member list = %v %v", got, err)
	}
	if got, _ = r.List(ctx, admin, store.TypeFilter{}); len(got) != 2 {
		t.Fatalf("admin list = %v", got)
	}
	if _, err := r.Visible(ctx, member, "ipam:platform-sweep"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("member sees platform type: %v", err)
	}
	if tt, err := r.Visible(ctx, admin, "ipam:platform-sweep"); err != nil || !tt.Platform() {
		t.Fatalf("admin visible: %v", err)
	}
	if _, err := r.Visible(ctx, admin, "ipam:nope"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatal("missing type visible")
	}
	st.listErr = errors.New("db down")
	if _, err := r.List(ctx, admin, store.TypeFilter{}); err == nil {
		t.Fatal("list failure hidden")
	}
}

func TestSchemas(t *testing.T) {
	c := NewSchemas()
	if s, err := c.Compiled(store.TaskType{}); s != nil || err != nil {
		t.Fatal("no schema = any object")
	}
	tt := store.TaskType{PayloadSchema: json.RawMessage(schemaV1)} // no hash: computed
	s1, err := c.Compiled(tt)
	if err != nil || s1 == nil {
		t.Fatal(err)
	}
	if s2, _ := c.Compiled(tt); s2 != s1 {
		t.Fatal("cache miss")
	}
	if _, err := c.Compiled(store.TaskType{PayloadSchema: json.RawMessage(`{"type":5}`), SchemaHash: "bad"}); err == nil {
		t.Fatal("broken schema compiled")
	}
	for i := 0; i < 1100; i++ { // overflow resets the bounded cache
		if _, err := c.Compiled(store.TaskType{PayloadSchema: json.RawMessage(schemaV1), SchemaHash: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	// A broken stored schema surfaces from Revalidate.
	r, _, _ := setup(t)
	if r.Schemas() == nil {
		t.Fatal("schemas")
	}
	if _, err := r.Revalidate(ctx, store.TaskType{Name: scanType, PayloadSchema: json.RawMessage(`{"type":5}`), SchemaHash: "x"}); err == nil {
		t.Fatal("revalidate with a broken schema")
	}
	if New(Deps{Store: memstore.New()}).now().IsZero() {
		t.Fatal("default clock")
	}
}
