// Package schedulermanifest declares what the scheduler module registers with
// the application gateway: routes derived from the embedded OpenAPI document,
// the API permissions, the CASL abilities and the navigation entries; plus the
// module roles and built-in role grants it registers with the auth service.
// The scheduler's gRPC surface (scheduler.v1.Registration) is service to
// service and is not proxied by the gateway.
package schedulermanifest

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"
	"github.com/go-tangra/go-tangra-scheduler/v4/api/openapi"
)

// Module identity.
const (
	Module       = "scheduler"
	DisplayName  = "Scheduler"
	Version      = "1.0.0"
	RemotePrefix = "/ui"
	APIPrefix    = "/api/scheduler"
)

// OpenAPI operation extensions.
const (
	PermissionExtension = "x-freya-permission"
	PublicExtension     = "x-freya-public"
	BodyLimitExtension  = "x-freya-max-body-bytes"
	TimeoutExtension    = "x-freya-timeout-seconds"
)

// Permissions the module registers (research D5).
var Permissions = []gatewayclient.Permission{
	{Resource: "scheduler", Action: "read", Description: "List task types, tasks, execution history and the overview, preview cron schedules and follow the live stream"},
	{Resource: "tasks", Action: "manage", Description: "Create and edit scheduled tasks, enable or disable them, and retire a removed module's task types (platform administrators)"},
	{Resource: "tasks", Action: "delete", Description: "Delete scheduled tasks"},
	{Resource: "tasks", Action: "control", Description: "Start, stop, restart, run now, run again and cancel tasks, including bulk actions"},
	{Resource: "backup", Action: "manage", Description: "Export and import scheduler tasks and history"},
}

// Permission sets of the module roles.
var (
	adminPermissions    = PermissionRefs()
	operatorPermissions = []string{"scheduler:read", "tasks:manage", "tasks:control"}
	viewerPermissions   = []string{"scheduler:read"}
)

// Roles is the module's role set (feature 019): ready-made roles auth offers in
// every tenant, locked there (administrators assign or clone them).
var Roles = []authclient.ModuleRole{
	{
		Slug: "administrator", DisplayName: DisplayName + " administrator",
		Description: "Full scheduler management: tasks, deletion, control and backups",
		Permissions: adminPermissions,
	},
	{
		Slug: "operator", DisplayName: DisplayName + " operator",
		Description: "Create, edit and control scheduled tasks (no deletion, no backups)",
		Permissions: operatorPermissions,
	},
	{
		Slug: "viewer", DisplayName: DisplayName + " viewer",
		Description: "Read tasks, execution history and the overview",
		Permissions: viewerPermissions,
	},
}

// Grants maps the platform's built-in role slugs to the module role
// permission sets: owners and admins hold the administrator set, operators
// the operator set, members and auditors the viewer set.
var Grants = map[string][]string{
	"owner":    adminPermissions,
	"admin":    adminPermissions,
	"operator": operatorPermissions,
	"member":   viewerPermissions,
	"auditor":  viewerPermissions,
}

// Methods proxied by the gateway: none (scheduler gRPC is service to service).
var Methods []gatewayclient.Method

// Abilities are the CASL rules bound to the permissions.
var Abilities = []gatewayclient.Ability{
	{Action: []string{"read"}, Subject: []string{"SchedulerTask", "SchedulerExecution", "SchedulerOverview", "SchedulerTaskType"}, Requires: "scheduler:read"},
	{Action: []string{"create", "update"}, Subject: []string{"SchedulerTask"}, Requires: "tasks:manage"},
	{Action: []string{"delete"}, Subject: []string{"SchedulerTask"}, Requires: "tasks:delete"},
	{Action: []string{"control"}, Subject: []string{"SchedulerTask"}, Requires: "tasks:control"},
	{Action: []string{"manage"}, Subject: []string{"SchedulerBackup"}, Requires: "backup:manage"},
}

// Nav lists the navigation contributions.
var Nav = []gatewayclient.NavEntry{
	{Title: "Scheduler", Path: "/scheduler", Icon: "mdi-calendar-clock", Order: 980, Requires: "scheduler:read"},
	{Title: "Overview", Path: "/scheduler/overview", Icon: "mdi-chart-timeline-variant", Order: 985, Requires: "scheduler:read"},
}

// PermissionRefs lists "resource:action" for every declared permission.
func PermissionRefs() []string {
	out := make([]string, 0, len(Permissions))
	for _, p := range Permissions {
		out = append(out, p.Resource+":"+p.Action)
	}
	return out
}

// Routes derives the gateway routes from the OpenAPI document.
func Routes(doc *openapi3.T) ([]gatewayclient.Route, error) {
	known := map[string]bool{}
	for _, p := range PermissionRefs() {
		known[p] = true
	}
	var routes []gatewayclient.Route
	for p, item := range doc.Paths.Map() {
		for m, op := range item.Operations() {
			r := gatewayclient.Route{Method: strings.ToUpper(m), Path: p}
			perm, _ := op.Extensions[PermissionExtension].(string)
			public, _ := op.Extensions[PublicExtension].(bool)
			switch {
			case public:
				r.Public = true
			case perm == "":
				return nil, fmt.Errorf("schedulermanifest: %s %s declares no permission", r.Method, p)
			case !known[perm]:
				return nil, fmt.Errorf("schedulermanifest: %s %s uses undeclared permission %q", r.Method, p, perm)
			default:
				r.Permission = perm
			}
			if v, ok := op.Extensions[BodyLimitExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 {
					return nil, fmt.Errorf("schedulermanifest: %s %s has a bad body limit", r.Method, p)
				}
				r.MaxBodyBytes = uint64(n)
			}
			if v, ok := op.Extensions[TimeoutExtension]; ok {
				n, ok := v.(float64)
				if !ok || n <= 0 || n > 600 {
					return nil, fmt.Errorf("schedulermanifest: %s %s has a bad timeout", r.Method, p)
				}
				r.Timeout = time.Duration(n) * time.Second
			}
			routes = append(routes, r)
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Method+" "+routes[i].Path < routes[j].Method+" "+routes[j].Path })
	return routes, nil
}

// Load parses the embedded document.
func Load() (*openapi3.T, error) {
	return openapi3.NewLoader().LoadFromData(openapi.Scheduler)
}

// Manifest builds the gateway manifest from the embedded OpenAPI document.
func Manifest() (gatewayclient.Manifest, error) {
	doc, err := Load()
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	routes, err := Routes(doc)
	if err != nil {
		return gatewayclient.Manifest{}, err
	}
	return gatewayclient.Manifest{
		Module: Module, DisplayName: DisplayName, Version: Version,
		Prefixes:    []string{APIPrefix},
		Routes:      routes,
		Methods:     Methods,
		Permissions: Permissions,
		Abilities:   Abilities,
		Exposes:     []string{"./routes", "./nav"},
		Nav:         Nav,
	}, nil
}

// BuiltinRoles lists the built-in role slugs granted, in a stable order.
var BuiltinRoles = []string{"owner", "admin", "member", "auditor", "operator"}

// Registration is what the module registers with auth: every module
// permission, the module roles and the built-in role grants.
func Registration() authclient.Registration {
	reg := authclient.Registration{Module: Module, DisplayName: DisplayName, Roles: Roles, BuiltinGrants: Grants}
	for _, p := range Permissions {
		reg.Permissions = append(reg.Permissions, authclient.Permission{Resource: p.Resource, Action: p.Action, Description: p.Description})
	}
	return reg
}
