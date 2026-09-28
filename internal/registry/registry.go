// Package registry holds the task-type registration rules (research D4,
// SR-001, FR-001–FR-004). The owner of a type is the calling module's
// verified SPIFFE service name — never a request field — and every type name
// must be "<owner>:<action>". A name owned by another module refuses the whole
// request. Descriptors are bounded (count, sizes, schema compiles without
// remote references, cron parses, retries 0..10). Registration is an
// idempotent upsert; unregistration marks the module's types unavailable and
// keeps their tasks (shown "type unavailable", occurrences skipped). When a
// type becomes available again or its schema changes, its tasks are
// re-validated (payload_invalid / ok).
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/audit"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/cron"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/payload"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Descriptor limits (research D4).
const (
	MaxTypes          = 50
	MaxDisplayName    = 200
	MaxDescription    = 2000
	MaxRetries        = 10
	UnavailableReason = "task type unavailable"
)

// Descriptor scopes as received (the gRPC layer maps the enum).
const (
	ScopeTenant   = store.ScopeTenant
	ScopePlatform = store.ScopePlatform
)

// Codes classify a refusal.
const (
	CodeInvalid = "invalid_argument"
	CodeDenied  = "permission_denied"
)

// Refusal reasons (stable, safe to return).
const (
	ReasonInvalidOwner     = "invalid_owner"
	ReasonNoTypes          = "no_types"
	ReasonTooManyTypes     = "too_many_types"
	ReasonInvalidName      = "invalid_name"
	ReasonDuplicateType    = "duplicate_type"
	ReasonForeignNamespace = "foreign_namespace"
	ReasonOwnedByOther     = "type_owned_by_other_module"
	ReasonDisplayName      = "invalid_display_name"
	ReasonDescription      = "invalid_description"
	ReasonSchemaTooLarge   = "schema_too_large"
	ReasonInvalidSchema    = "invalid_schema"
	ReasonInvalidCron      = "invalid_cron"
	ReasonInvalidRetries   = "invalid_retries"
	ReasonInvalidScope     = "invalid_scope"
	ReasonNotPlatformAdmin = "not_platform_admin"
)

// Error is a registration refusal.
type Error struct {
	Code   string // CodeInvalid | CodeDenied
	Reason string
	Type   string // the offending type name ("" for request-level refusals)
}

func (e *Error) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("registry: %s: %s", e.Reason, e.Type)
	}
	return "registry: " + e.Reason
}

func invalid(reason, typ string) *Error { return &Error{Code: CodeInvalid, Reason: reason, Type: typ} }
func denied(reason, typ string) *Error  { return &Error{Code: CodeDenied, Reason: reason, Type: typ} }

// Descriptor is one type as declared by a module.
type Descriptor struct {
	Type            string
	DisplayName     string
	Description     string
	PayloadSchema   string
	DefaultCron     string
	DefaultMaxRetry int32
	Scope           string // tenant | platform ("" = tenant)
}

// Result reports a registration.
type Result struct {
	Registered  int
	Revalidated int
}

var (
	moduleRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	nameRE   = regexp.MustCompile(`^([a-z0-9-]{1,63}):[a-z][a-z0-9-]{0,62}$`)
)

// ValidModule reports whether s is a well-formed module (service) name.
func ValidModule(s string) bool { return moduleRE.MatchString(s) }

// Registry applies the registration rules.
type Registry struct {
	st              repo.Store
	audit           audit.Recorder
	now             func() time.Time
	maxPayloadBytes int
	cache           *Schemas
}

// Deps wire the registry.
type Deps struct {
	Store           repo.Store
	Audit           audit.Recorder
	Now             func() time.Time
	MaxPayloadBytes int
	Schemas         *Schemas
}

// New builds the registry.
func New(d Deps) *Registry {
	r := &Registry{st: d.Store, audit: d.Audit, now: d.Now, maxPayloadBytes: d.MaxPayloadBytes, cache: d.Schemas}
	if r.now == nil {
		r.now = func() time.Time { return time.Now().UTC() }
	}
	if r.cache == nil {
		r.cache = NewSchemas()
	}
	return r
}

// Schemas returns the compiled-schema cache shared with the task service and
// the engine.
func (r *Registry) Schemas() *Schemas { return r.cache }

func cleanText(s string) string { return strings.TrimSpace(s) }

func printable(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

// validate checks one descriptor and returns the stored form.
func (r *Registry) validate(owner string, d Descriptor, at time.Time) (store.TaskType, *Error) {
	m := nameRE.FindStringSubmatch(d.Type)
	if m == nil {
		return store.TaskType{}, invalid(ReasonInvalidName, "")
	}
	if m[1] != owner {
		return store.TaskType{}, denied(ReasonForeignNamespace, d.Type)
	}
	name := cleanText(d.DisplayName)
	if name == "" || utf8.RuneCountInString(name) > MaxDisplayName || !printable(name) {
		return store.TaskType{}, invalid(ReasonDisplayName, d.Type)
	}
	desc := cleanText(d.Description)
	if utf8.RuneCountInString(desc) > MaxDescription || !utf8.ValidString(desc) {
		return store.TaskType{}, invalid(ReasonDescription, d.Type)
	}
	var schema json.RawMessage
	if raw := strings.TrimSpace(d.PayloadSchema); raw != "" {
		if len(raw) > payload.MaxSchemaBytes {
			return store.TaskType{}, invalid(ReasonSchemaTooLarge, d.Type)
		}
		canon, err := payload.Canonical([]byte(raw)) // a JSON object, or refused
		if err != nil {
			return store.TaskType{}, invalid(ReasonInvalidSchema, d.Type)
		}
		if _, err := payload.Compile(canon); err != nil {
			return store.TaskType{}, invalid(ReasonInvalidSchema, d.Type)
		}
		schema = canon
	}
	cr := strings.TrimSpace(d.DefaultCron)
	if cr != "" {
		s, err := cron.Parse(cr)
		if err != nil {
			return store.TaskType{}, invalid(ReasonInvalidCron, d.Type)
		}
		cr = s.String()
	}
	if d.DefaultMaxRetry < 0 || d.DefaultMaxRetry > MaxRetries {
		return store.TaskType{}, invalid(ReasonInvalidRetries, d.Type)
	}
	scope := d.Scope
	switch scope {
	case "", ScopeTenant:
		scope = ScopeTenant
	case ScopePlatform:
	default:
		return store.TaskType{}, invalid(ReasonInvalidScope, d.Type)
	}
	return store.TaskType{Name: d.Type, Module: owner, DisplayName: name, Description: desc, PayloadSchema: schema,
		DefaultCron: cr, DefaultMaxRetries: int(d.DefaultMaxRetry), Scope: scope, Available: true, RegisteredAt: at,
		SchemaHash: payload.Hash(schema)}, nil
}

func (r *Registry) refuse(ctx context.Context, actorKind, actorID, subject, reason string) {
	audit.Emit(ctx, r.audit, audit.Event{TenantID: audit.NilTenant, EventType: audit.AccessRefused, ActorKind: actorKind, ActorID: actorID,
		SubjectKind: audit.SubjectTaskType, SubjectID: subject, Outcome: audit.OutcomeRefused, Reason: reason,
		Details: map[string]any{"reason": reason}})
}

// Register upserts the owner's descriptors (all or nothing) and re-validates
// the tasks of types whose schema changed or that were unavailable.
func (r *Registry) Register(ctx context.Context, owner, actorID string, ds []Descriptor) (Result, error) {
	if !ValidModule(owner) {
		return Result{}, denied(ReasonInvalidOwner, "")
	}
	switch {
	case len(ds) == 0:
		return Result{}, invalid(ReasonNoTypes, "")
	case len(ds) > MaxTypes:
		return Result{}, invalid(ReasonTooManyTypes, "")
	}
	at := r.now()
	seen := map[string]bool{}
	types := make([]store.TaskType, 0, len(ds))
	for _, d := range ds {
		t, verr := r.validate(owner, d, at)
		if verr != nil {
			if verr.Code == CodeDenied {
				r.refuse(ctx, audit.ActorService, actorID, owner, verr.Reason)
			}
			return Result{}, verr
		}
		if seen[t.Name] {
			return Result{}, invalid(ReasonDuplicateType, t.Name)
		}
		seen[t.Name] = true
		types = append(types, t)
	}
	// A name owned by another module refuses the whole request; remember
	// which types need their tasks re-validated.
	recheck := map[string]bool{}
	for _, t := range types {
		prev, err := r.st.GetTaskType(ctx, t.Name)
		switch {
		case errors.Is(err, repo.ErrNotFound):
			recheck[t.Name] = true
		case err != nil:
			return Result{}, err
		case prev.Module != owner:
			r.refuse(ctx, audit.ActorService, actorID, owner, ReasonOwnedByOther)
			return Result{}, denied(ReasonOwnedByOther, t.Name)
		case !prev.Available || prev.SchemaHash != t.SchemaHash:
			recheck[t.Name] = true
		}
	}
	if err := r.st.UpsertTaskTypes(ctx, owner, types); err != nil {
		if errors.Is(err, repo.ErrConflict) {
			r.refuse(ctx, audit.ActorService, actorID, owner, ReasonOwnedByOther)
			return Result{}, denied(ReasonOwnedByOther, "")
		}
		return Result{}, err
	}
	res := Result{Registered: len(types)}
	names := make([]any, 0, len(types))
	for _, t := range types {
		names = append(names, t.Name)
		if !recheck[t.Name] {
			continue
		}
		n, err := r.Revalidate(ctx, t)
		if err != nil {
			return res, err
		}
		res.Revalidated += n
	}
	audit.Emit(ctx, r.audit, audit.Event{TenantID: audit.NilTenant, EventType: audit.TypeRegister, ActorKind: audit.ActorService, ActorID: actorID,
		SubjectKind: audit.SubjectTaskType, SubjectID: owner, Outcome: audit.OutcomeOK,
		Details: map[string]any{"module": owner, "types": names, "revalidated": res.Revalidated}})
	return res, nil
}

// Revalidate re-checks every task of the (available) type t against its
// current schema: ok, or payload_invalid with the validation message.
func (r *Registry) Revalidate(ctx context.Context, t store.TaskType) (int, error) {
	schema, err := r.cache.Compiled(t)
	if err != nil {
		return 0, err
	}
	return r.st.UpdateTasksOfType(ctx, t.Name, func(task *store.Task) bool {
		validity, msg := store.ValidityOK, ""
		if verr := payload.Validate(schema, task.Payload, r.maxPayloadBytes); verr != nil {
			validity, msg = store.ValidityPayloadInvalid, verr.Error()
		}
		changed := task.Validity != validity || task.ValidityMessage != msg
		task.Validity, task.ValidityMessage = validity, msg
		return changed
	})
}

// Unregister marks every type of the calling module unavailable.
func (r *Registry) Unregister(ctx context.Context, owner, actorID string) (int, error) {
	if !ValidModule(owner) {
		return 0, denied(ReasonInvalidOwner, "")
	}
	n, err := r.st.MarkModuleUnavailable(ctx, owner, r.now())
	if err != nil {
		return 0, err
	}
	audit.Emit(ctx, r.audit, audit.Event{TenantID: audit.NilTenant, EventType: audit.TypeUnregister, ActorKind: audit.ActorService, ActorID: actorID,
		SubjectKind: audit.SubjectTaskType, SubjectID: owner, Outcome: audit.OutcomeOK, Details: map[string]any{"module": owner, "count": n}})
	return n, nil
}

// Retire lets a platform administrator mark a removed module's types
// unavailable (POST /modules/{module}/unregister).
func (r *Registry) Retire(ctx context.Context, subj authz.Subjects, module string) (int, error) {
	if err := authz.RequirePlatformAdmin(subj); err != nil {
		r.refuse(ctx, audit.ActorOf(subj.ActorKind), subj.ActorID(), module, ReasonNotPlatformAdmin)
		return 0, err
	}
	if !ValidModule(module) {
		return 0, invalid(ReasonInvalidOwner, "")
	}
	n, err := r.st.MarkModuleUnavailable(ctx, module, r.now())
	if err != nil {
		return 0, err
	}
	audit.Emit(ctx, r.audit, audit.Event{TenantID: audit.NilTenant, EventType: audit.TypeUnregister, ActorKind: audit.ActorOf(subj.ActorKind),
		ActorID: subj.ActorID(), SubjectKind: audit.SubjectTaskType, SubjectID: module, Outcome: audit.OutcomeOK,
		Details: map[string]any{"module": module, "count": n}})
	return n, nil
}

// List returns the types visible to the caller: platform-scoped types only
// for platform administrators (SR-003).
func (r *Registry) List(ctx context.Context, subj authz.Subjects, f store.TypeFilter) ([]store.TaskType, error) {
	all, err := r.st.ListTaskTypes(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]store.TaskType, 0, len(all))
	for _, t := range all {
		if authz.CanUseType(subj, t.Platform()) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Visible returns one type when the caller may use it (repo.ErrNotFound
// otherwise, also for platform types and non-admins).
func (r *Registry) Visible(ctx context.Context, subj authz.Subjects, name string) (store.TaskType, error) {
	t, err := r.st.GetTaskType(ctx, name)
	if err != nil {
		return store.TaskType{}, err
	}
	if !authz.CanUseType(subj, t.Platform()) {
		return store.TaskType{}, repo.ErrNotFound
	}
	return t, nil
}

// Schemas caches compiled payload schemas by schema hash.
type Schemas struct {
	mu sync.Mutex
	m  map[string]*payload.Schema
}

// NewSchemas returns an empty cache.
func NewSchemas() *Schemas { return &Schemas{m: map[string]*payload.Schema{}} }

// Compiled returns the compiled schema of t (nil = any object).
func (c *Schemas) Compiled(t store.TaskType) (*payload.Schema, error) {
	if len(t.PayloadSchema) == 0 {
		return nil, nil
	}
	key := t.SchemaHash
	if key == "" {
		key = payload.Hash(t.PayloadSchema)
	}
	c.mu.Lock()
	s, ok := c.m[key]
	c.mu.Unlock()
	if ok {
		return s, nil
	}
	s, err := payload.Compile(t.PayloadSchema)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.m) >= 1024 { // bounded: types are few; drop everything on overflow
		c.m = map[string]*payload.Schema{}
	}
	c.m[key] = s
	c.mu.Unlock()
	return s, nil
}
