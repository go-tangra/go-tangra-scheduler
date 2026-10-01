//go:build integration

package repodb_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// TestListPaging (specs 032): every sort field × direction pages a static list
// exactly once, SQL and the memstore agree on the order, totals are per
// tenant, a page beyond the end answers the last page, the history counts are
// unchanged and TaskIDs keeps name order.
func TestListPaging(t *testing.T) {
	env := startDB(t)
	st, err := store.Open(ctx, env.appDSN, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	db := repodb.New(st)
	mem := memstore.New()
	base := time.Now().UTC().Truncate(time.Second)

	// Tenant A: 23 tasks with repeated names (case only), types, states,
	// next runs (some null) and update times, so every sort hits ties.
	var aTasks []store.Task
	for i := 0; i < 23; i++ {
		tk := store.Task{ID: store.NewID(), TenantID: tenantA, Name: fmt.Sprintf("%s %02d", []string{"Alpha", "beta", "gamma"}[i%3], i/2),
			TypeName: []string{scanType, "ipam:discover"}[i%2], Module: "ipam", Kind: store.KindDelayed, Payload: []byte(`{}`),
			Enabled: i%4 != 0, Status: []string{store.TaskActive, store.TaskActive, store.TaskCompleted, store.TaskCancelled}[i%4],
			Validity: store.ValidityOK, TimeoutSeconds: 300, Timezone: "UTC",
			CreatedAt: base, UpdatedAt: base.Add(time.Duration(i%5) * time.Minute)}
		if i%3 != 0 {
			next := base.Add(time.Duration(i%4) * time.Hour)
			tk.NextRunAt = &next
		}
		aTasks = append(aTasks, tk)
	}
	bTasks := []store.Task{}
	for i := 0; i < 4; i++ {
		bTasks = append(bTasks, store.Task{ID: store.NewID(), TenantID: tenantB, Name: fmt.Sprintf("b%d", i), TypeName: scanType, Module: "ipam",
			Kind: store.KindDelayed, Payload: []byte(`{}`), Enabled: true, Status: store.TaskActive, Validity: store.ValidityOK, TimeoutSeconds: 300,
			Timezone: "UTC", CreatedAt: base, UpdatedAt: base})
	}
	for _, tk := range append(slices.Clone(aTasks), bTasks...) {
		for _, s := range []repo.Store{db, mem} {
			if ok, err := s.ImportTask(ctx, tk); !ok || err != nil {
				t.Fatalf("import task %s: %v %v", tk.Name, ok, err)
			}
		}
	}
	// History: 41 attempts in tenant A (ties on status, duration, trigger and
	// created_at), 3 in tenant B.
	statuses := []string{store.ExecSucceeded, store.ExecFailed, store.ExecTimedOut, store.ExecSkipped, store.ExecQueued}
	triggers := []string{store.TriggerSchedule, store.TriggerManual, store.TriggerCatchUp}
	mkExec := func(tenant string, i int) store.Execution {
		return store.Execution{ID: store.NewID(), TenantID: tenant, TaskID: aTasks[i%len(aTasks)].ID, OccurrenceID: store.NewID(), TypeName: scanType,
			Module: "ipam", Trigger: triggers[i%3], OccurrenceAt: base.Add(time.Duration(i) * time.Second), DueAt: base,
			Status: statuses[i%len(statuses)], Attempt: 1, MaxAttempts: 1, DurationMS: int64((i % 4) * 250),
			Result: []byte(`{"ok":true}`), CreatedAt: base.Add(time.Duration(i/3) * time.Second)}
	}
	for i := 0; i < 44; i++ {
		tenant := tenantA
		if i >= 41 {
			tenant = tenantB
		}
		e := mkExec(tenant, i)
		for _, s := range []repo.Store{db, mem} {
			if ok, err := s.ImportExecution(ctx, e); !ok || err != nil {
				t.Fatalf("import execution: %v %v", ok, err)
			}
		}
	}

	scopeA := repo.Tenant(tenantA)
	pageTasks := func(t *testing.T, s repo.Store, scope repo.Scope, f store.TaskFilter, req listquery.Request) []string {
		t.Helper()
		var ids []string
		for page := 1; ; page++ {
			req.Page = page
			f.List = req
			items, total, err := s.ListTasks(ctx, scope, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				ids = append(ids, it.ID)
			}
			if page*req.PageSize >= int(total) {
				return ids
			}
		}
	}
	pageExecs := func(t *testing.T, s repo.Store, scope repo.Scope, f store.ExecFilter, req listquery.Request) []string {
		t.Helper()
		var ids []string
		for page := 1; ; page++ {
			req.Page = page
			f.List = req
			items, total, _, err := s.ListExecutions(ctx, scope, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				if it.Result != nil {
					t.Fatal("list returned a result")
				}
				ids = append(ids, it.ID)
			}
			if page*req.PageSize >= int(total) {
				return ids
			}
		}
	}
	once := func(t *testing.T, label string, ids []string, want int) {
		t.Helper()
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("%s: %s returned twice", label, id)
			}
			seen[id] = true
		}
		if len(seen) != want {
			t.Fatalf("%s: %d distinct rows, want %d", label, len(seen), want)
		}
	}

	t.Run("tasks every sort and direction", func(t *testing.T) {
		for _, sort := range sortedKeys(store.TaskList) {
			for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
				label := sort + " " + string(dir)
				req := listquery.Request{PageSize: 5, Sort: sort, Order: dir}
				sqlIDs := pageTasks(t, db, scopeA, store.TaskFilter{}, req)
				once(t, label, sqlIDs, 23)
				if memIDs := pageTasks(t, mem, scopeA, store.TaskFilter{}, req); !slices.Equal(sqlIDs, memIDs) {
					t.Fatalf("%s: SQL and memstore orders differ\n%v\n%v", label, sqlIDs, memIDs)
				}
			}
		}
		// NULL next runs come last in both directions.
		for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
			items, _, err := db.ListTasks(ctx, scopeA, store.TaskFilter{List: listquery.Request{Page: 1, PageSize: 200, Sort: "next_run_at", Order: dir}})
			if err != nil || items[0].NextRunAt == nil || items[len(items)-1].NextRunAt != nil {
				t.Fatalf("next_run_at %s nulls: %v", dir, err)
			}
		}
		// Filtered paging and an unset request (defaults: name asc, 25).
		once(t, "filtered", pageTasks(t, db, scopeA, store.TaskFilter{State: store.StateCompleted}, listquery.Request{PageSize: 2, Sort: "updated_at", Order: listquery.Desc}), 6)
		items, total, err := db.ListTasks(ctx, scopeA, store.TaskFilter{})
		if err != nil || total != 23 || len(items) != 23 || !strings.EqualFold(items[0].Name, "Alpha 00") {
			t.Fatalf("defaults = %d %d %v", total, len(items), err)
		}
	})

	t.Run("executions every sort and direction", func(t *testing.T) {
		for _, sort := range sortedKeys(store.ExecutionList) {
			for _, dir := range []listquery.Dir{listquery.Asc, listquery.Desc} {
				label := sort + " " + string(dir)
				req := listquery.Request{PageSize: 6, Sort: sort, Order: dir}
				sqlIDs := pageExecs(t, db, scopeA, store.ExecFilter{}, req)
				once(t, label, sqlIDs, 41)
				if memIDs := pageExecs(t, mem, scopeA, store.ExecFilter{}, req); !slices.Equal(sqlIDs, memIDs) {
					t.Fatalf("%s: SQL and memstore orders differ\n%v\n%v", label, sqlIDs, memIDs)
				}
			}
		}
		once(t, "status filter", pageExecs(t, db, scopeA, store.ExecFilter{FailedOnly: true}, listquery.Request{PageSize: 4, Sort: "status", Order: listquery.Asc}), 16)
	})

	t.Run("totals per tenant, clamp, counts, TaskIDs", func(t *testing.T) {
		svc := tasks.New(tasks.Deps{Store: db, Limits: tasks.Limits{MaxTasksPerTenant: 100}})
		userA := authz.User(tenantA, "alice", nil)
		userB := authz.User(tenantB, "bob", nil)
		root := authz.User(tenantA, "root", []string{authz.RolePlatformAdmin})
		for _, c := range []struct {
			subj authz.Subjects
			want int
		}{{userA, 23}, {userB, 4}, {root, 27}} {
			if pg, err := svc.List(ctx, c.subj, store.TaskFilter{}); err != nil || pg.Total != c.want {
				t.Fatalf("task total = %d (want %d) %v", pg.Total, c.want, err)
			}
		}
		pg, err := svc.List(ctx, userA, store.TaskFilter{List: listquery.Request{Page: 99, PageSize: 10}})
		if err != nil || pg.Page != 3 || len(pg.Items) != 3 || pg.Total != 23 {
			t.Fatalf("task clamp = %+v %v", pg, err)
		}
		ep, err := svc.Executions(ctx, userA, store.ExecFilter{Statuses: []string{store.ExecSucceeded}, List: listquery.Request{Page: 50, PageSize: 3, Sort: "duration"}})
		// 41 attempts: 9 succeeded, 16 failed/timed out, 16 other; 9 rows → page 3
		if err != nil || ep.Total != 9 || ep.Page.Page != 3 || len(ep.Items) != 3 || ep.Order != listquery.Desc ||
			ep.Counts != (store.ExecCounts{Succeeded: 9, Failed: 16, Other: 16}) {
			t.Fatalf("history clamp/counts = %+v %v", ep, err)
		}
		if ep, _ := svc.Executions(ctx, userB, store.ExecFilter{}); ep.Total != 3 {
			t.Fatalf("tenant B history total = %d", ep.Total)
		}
		ids, err := db.TaskIDs(ctx, scopeA, store.TaskFilter{List: listquery.Request{Sort: "updated_at", Order: listquery.Desc}})
		if err != nil {
			t.Fatal(err)
		}
		byName := slices.Clone(aTasks)
		slices.SortFunc(byName, func(a, b store.Task) int {
			if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		want := make([]string, 0, len(byName))
		for _, tk := range byName {
			want = append(want, tk.ID)
		}
		if !slices.Equal(ids, want) {
			t.Fatal("TaskIDs no longer in name order")
		}
	})
}

func sortedKeys(s listquery.Spec) []string {
	out := make([]string, 0, len(s.Fields))
	for k := range s.Fields {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
