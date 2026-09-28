// Package authz is the scheduler's access model (research D5). Every browser
// route declares one API permission (x-freya-permission); the module enforces
// it itself (defence in depth behind the gateway) through a Checker (the auth
// service's Authorization/Check). A caller acts within its own tenant.
//
// Platform administrators (the platform-admin role of the platform token)
// additionally see and act on every tenant's tasks — the read path switches to
// the system scope only after the permission check — and are the only callers
// who may use platform-scoped task types. Everybody else gets "not found" for
// foreign and platform rows (SR-003).
//
// Non-human actors: mesh services register task types (authorised by the
// SPIFFE identity and the mesh policy, never by these permissions) and the
// engine acts as the system subject.
package authz

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrForbidden is returned when a caller lacks the required permission or scope.
var ErrForbidden = errors.New("authz: forbidden")

// Actor kinds (closed set; the audit vocabulary uses the same names).
const (
	ActorUser    = "user"    // a signed-in platform user (via the gateway)
	ActorService = "service" // a mesh peer (SPIFFE)
	ActorSystem  = "system"  // the engine / trusted internal maintenance
)

// API permissions (resource:action).
const (
	SchedulerRead = "scheduler:read"
	TasksManage   = "tasks:manage"
	TasksDelete   = "tasks:delete"
	TasksControl  = "tasks:control"
	BackupManage  = "backup:manage"
)

// Permissions lists every module permission.
var Permissions = []string{SchedulerRead, TasksManage, TasksDelete, TasksControl, BackupManage}

// RolePlatformAdmin confers the cross-tenant scope and platform-scoped types.
const RolePlatformAdmin = "platform-admin"

// Subjects is the authenticated caller.
type Subjects struct {
	TenantID  string
	UserID    string
	Roles     []string
	ActorKind string // user | service | system
}

// Checker answers "does this user hold this API permission in this tenant". A
// failed lookup is a "no".
type Checker interface {
	Has(ctx context.Context, tenantID, userID, permission string) bool
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context, tenantID, userID, permission string) bool

// Has implements Checker.
func (f CheckerFunc) Has(ctx context.Context, tenantID, userID, permission string) bool {
	return f(ctx, tenantID, userID, permission)
}

// Static is a Checker over a fixed user → permissions map (tests, dev).
type Static map[string][]string

// Has implements Checker.
func (s Static) Has(_ context.Context, _, userID, permission string) bool {
	for _, p := range s[userID] {
		if p == permission {
			return true
		}
	}
	return false
}

// User returns the subject of a signed-in user.
func User(tenantID, userID string, roles []string) Subjects {
	return Subjects{TenantID: tenantID, UserID: userID, Roles: roles, ActorKind: ActorUser}
}

// System returns the trusted engine subject.
func System() Subjects { return Subjects{UserID: ActorSystem, ActorKind: ActorSystem} }

// Service returns the subject of a mesh peer.
func Service(spiffeID string) Subjects { return Subjects{UserID: spiffeID, ActorKind: ActorService} }

// HasRole reports whether the caller carries role.
func (s Subjects) HasRole(role string) bool {
	for _, r := range s.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsPlatformAdmin reports whether the caller has the cross-tenant scope: a
// user with the platform-admin role, or the system subject.
func (s Subjects) IsPlatformAdmin() bool {
	return s.ActorKind == ActorSystem || (s.ActorKind == ActorUser && s.HasRole(RolePlatformAdmin))
}

// ActorID is the user id, falling back to the actor kind.
func (s Subjects) ActorID() string {
	if s.UserID != "" {
		return s.UserID
	}
	return s.ActorKind
}

// Known reports whether perm is a module permission.
func Known(perm string) bool {
	for _, p := range Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Require checks that the caller holds perm. Users are checked through c in
// their own tenant (nil c refuses); the system subject is allowed everything;
// services hold no browser permission.
func Require(ctx context.Context, c Checker, s Subjects, perm string) error {
	if !Known(perm) {
		return fmt.Errorf("%w: unknown permission %q", ErrForbidden, perm)
	}
	switch s.ActorKind {
	case ActorSystem:
		return nil
	case ActorUser:
		if c != nil && s.TenantID != "" && s.UserID != "" && c.Has(ctx, s.TenantID, s.UserID, perm) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s required", ErrForbidden, perm)
}

// Allowed is Require as a boolean.
func Allowed(ctx context.Context, c Checker, s Subjects, perm string) bool {
	return Require(ctx, c, s, perm) == nil
}

// RequirePlatformAdmin permits only platform administrators (or the system).
func RequirePlatformAdmin(s Subjects) error {
	if s.IsPlatformAdmin() {
		return nil
	}
	return fmt.Errorf("%w: platform-admin required", ErrForbidden)
}

// CanUseType reports whether the caller may see and use a type of that scope:
// platform-scoped types are for platform administrators only.
func CanUseType(s Subjects, platform bool) bool { return !platform || s.IsPlatformAdmin() }

// CanSeeTenant reports whether rows of tenantID are visible to the caller.
func CanSeeTenant(s Subjects, tenantID string) bool {
	return s.IsPlatformAdmin() || (s.TenantID != "" && s.TenantID == tenantID)
}

// Split returns the resource and action of a "resource:action" permission.
func Split(perm string) (resource, action string, ok bool) {
	resource, action, ok = strings.Cut(perm, ":")
	return resource, action, ok && resource != "" && action != ""
}
