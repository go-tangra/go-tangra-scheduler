package authz

import (
	"context"
	"errors"
	"testing"
)

const (
	tn    = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var ctx = context.Background()

func TestRequireUser(t *testing.T) {
	c := Static{"viewer": {SchedulerRead}, "operator": {SchedulerRead, TasksManage, TasksControl}}
	viewer := User(tn, "viewer", nil)
	op := User(tn, "operator", nil)
	if err := Require(ctx, c, viewer, SchedulerRead); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{TasksManage, TasksDelete, TasksControl, BackupManage} {
		if err := Require(ctx, c, viewer, p); !errors.Is(err, ErrForbidden) {
			t.Fatalf("viewer %s: %v", p, err)
		}
	}
	if !Allowed(ctx, c, op, TasksControl) || Allowed(ctx, c, op, TasksDelete) {
		t.Fatal("operator grants")
	}
	if Allowed(ctx, nil, op, SchedulerRead) {
		t.Fatal("nil checker must refuse")
	}
	if Allowed(ctx, c, User(tn, "", nil), SchedulerRead) || Allowed(ctx, c, User("", "operator", nil), SchedulerRead) {
		t.Fatal("user without id or tenant")
	}
	if Allowed(ctx, c, op, "tasks:fly") {
		t.Fatal("unknown permission")
	}
	if Allowed(ctx, c, Subjects{TenantID: tn, UserID: "operator", ActorKind: "robot"}, SchedulerRead) {
		t.Fatal("unknown actor kind")
	}
	called := false
	fn := CheckerFunc(func(_ context.Context, tenant, user, perm string) bool {
		called = true
		return tenant == tn && user == "operator" && perm == BackupManage
	})
	if !Allowed(ctx, fn, op, BackupManage) || !called {
		t.Fatal("checker func")
	}
}

func TestNonHuman(t *testing.T) {
	sys := System()
	for _, p := range Permissions {
		if !Allowed(ctx, nil, sys, p) {
			t.Fatalf("system %s", p)
		}
	}
	svc := Service("spiffe://example.org/svc/ipam")
	if svc.ActorKind != ActorService || svc.ActorID() != "spiffe://example.org/svc/ipam" {
		t.Fatalf("service = %+v", svc)
	}
	for _, p := range Permissions {
		if Allowed(ctx, Static{"spiffe://example.org/svc/ipam": Permissions}, svc, p) {
			t.Fatalf("service must not hold %s", p)
		}
	}
	if svc.IsPlatformAdmin() || !sys.IsPlatformAdmin() {
		t.Fatal("platform admin of non-humans")
	}
}

func TestPlatformScope(t *testing.T) {
	admin := User(tn, "u", []string{"member", RolePlatformAdmin})
	member := User(tn, "m", []string{"member"})
	if !admin.HasRole("member") || admin.HasRole("owner") || !admin.IsPlatformAdmin() || member.IsPlatformAdmin() {
		t.Fatal("roles")
	}
	// a service carrying the role string is still not a platform admin
	if (Subjects{ActorKind: ActorService, Roles: []string{RolePlatformAdmin}}).IsPlatformAdmin() {
		t.Fatal("service platform admin")
	}
	if err := RequirePlatformAdmin(admin); err != nil {
		t.Fatal(err)
	}
	if err := RequirePlatformAdmin(member); !errors.Is(err, ErrForbidden) {
		t.Fatal("member passed platform-admin")
	}
	if !CanUseType(member, false) || CanUseType(member, true) || !CanUseType(admin, true) {
		t.Fatal("platform type use")
	}
	if !CanSeeTenant(member, tn) || CanSeeTenant(member, other) || !CanSeeTenant(admin, other) || CanSeeTenant(User("", "x", nil), "") {
		t.Fatal("tenant visibility")
	}
	if (Subjects{ActorKind: ActorSystem}).ActorID() != ActorSystem || member.ActorID() != "m" {
		t.Fatal("actor id")
	}
}

func TestVocabulary(t *testing.T) {
	if len(Permissions) != 5 || !Known(BackupManage) || Known("x:y") {
		t.Fatal("permissions")
	}
	if r, act, ok := Split(TasksControl); !ok || r != "tasks" || act != "control" {
		t.Fatal("split")
	}
	for _, bad := range []string{"tasks", ":read", "tasks:"} {
		if _, _, ok := Split(bad); ok {
			t.Fatalf("split %q", bad)
		}
	}
}

// Admins and owners of the platform tenant are platform administrators;
// admins of other tenants and plain platform members are not.
func TestPlatformTenantAdmins(t *testing.T) {
	old := PlatformTenant
	defer func() { PlatformTenant = old }()
	PlatformTenant = DefaultPlatformTenant
	other := "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	for _, c := range []struct {
		s    Subjects
		want bool
	}{
		{User(DefaultPlatformTenant, "u", []string{"admin"}), true},
		{User(DefaultPlatformTenant, "u", []string{"owner"}), true},
		{User(DefaultPlatformTenant, "u", []string{"member", "operator"}), false},
		{User(other, "u", []string{"admin", "owner"}), false},
		{User("", "u", []string{"admin"}), false},
		{Subjects{TenantID: DefaultPlatformTenant, UserID: "svc", Roles: []string{"admin"}, ActorKind: ActorService}, false},
	} {
		if got := c.s.IsPlatformAdmin(); got != c.want {
			t.Errorf("%+v: IsPlatformAdmin = %v", c.s, got)
		}
	}
	PlatformTenant = other
	if !User(other, "u", []string{"owner"}).IsPlatformAdmin() || User(DefaultPlatformTenant, "u", []string{"owner"}).IsPlatformAdmin() {
		t.Fatal("configured platform tenant ignored")
	}
}
