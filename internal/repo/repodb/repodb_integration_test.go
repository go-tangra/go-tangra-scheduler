//go:build integration

// Package repodb integration test: runs the scheduler store against a real
// TimescaleDB (testcontainers) and checks what only a database can prove:
// migrations are idempotent, row-level security isolates two tenants even for
// raw SQL under the app role, names are unique per tenant, two engines
// planning concurrently never create a duplicate occurrence (SKIP LOCKED +
// the unique occurrence index), completion is fenced by the lease owner,
// expired leases are recovered and history is pruned. Run with:
//
//	go test -tags integration ./internal/repo/repodb/
//
// It skips cleanly when Docker/testcontainers is unavailable.
package repodb_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/engine"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

const (
	tenantA  = "11111111-1111-7111-8111-111111111111"
	tenantB  = "22222222-2222-7222-8222-222222222222"
	scanType = "ipam:scan-network"
	schema   = `{"type":"object","properties":{"all":{"type":"boolean"}},"additionalProperties":false}`
)

var ctx = context.Background()

type dbEnv struct{ adminDSN, appDSN string }

func startDB(t *testing.T) dbEnv {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "scheduler"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	env := dbEnv{
		adminDSN: "postgres://postgres:test@" + host + ":" + port.Port() + "/scheduler?sslmode=disable",
		appDSN:   "postgres://scheduler_app:app@" + host + ":" + port.Port() + "/scheduler?sslmode=disable",
	}
	conn, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	// the stack's init-db creates the role; migrations only grant to it
	if _, err := conn.Exec(ctx, "CREATE ROLE scheduler_app LOGIN PASSWORD 'app' NOBYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Migrate(ctx, env.adminDSN); err != nil {
		t.Fatalf("migrate idempotent: %v", err)
	}
	return env
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func TestRepoDB(t *testing.T) {
	env := startDB(t)
	st, err := store.Open(ctx, env.appDSN, 8)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	db := repodb.New(st)
	admin, err := pgx.Connect(ctx, env.adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	truncate := func(t *testing.T) {
		t.Helper()
		if _, err := admin.Exec(ctx, `TRUNCATE scheduler_executions, scheduler_tasks, scheduler_task_types`); err != nil {
			t.Fatal(err)
		}
	}
	clk := &clock{t: time.Now().UTC().Truncate(time.Second)}
	setup := func(t *testing.T) (*registry.Registry, *tasks.Service) {
		t.Helper()
		truncate(t)
		reg := registry.New(registry.Deps{Store: db, Now: clk.Now, MaxPayloadBytes: 1 << 16})
		if _, err := reg.Register(ctx, "ipam", "spiffe://example.org/svc/ipam", []registry.Descriptor{
			{Type: scanType, DisplayName: "Scan network", PayloadSchema: schema, DefaultCron: "0 3 * * *", DefaultMaxRetry: 2},
			{Type: "ipam:sweep", DisplayName: "Sweep", Scope: registry.ScopePlatform},
		}); err != nil {
			t.Fatal(err)
		}
		return reg, tasks.New(tasks.Deps{Store: db, Registry: reg, Now: clk.Now, Limits: tasks.Limits{MaxTasksPerTenant: 100}})
	}
	userA := authz.User(tenantA, "alice", nil)
	userB := authz.User(tenantB, "bob", nil)
	root := authz.User(tenantA, "root", []string{authz.RolePlatformAdmin})

	t.Run("types catalog", func(t *testing.T) {
		reg, _ := setup(t)
		tt, err := db.GetTaskType(ctx, scanType)
		if err != nil || tt.Module != "ipam" || len(tt.PayloadSchema) == 0 || !tt.Available || tt.SchemaHash == "" {
			t.Fatalf("type = %+v %v", tt, err)
		}
		if _, err := db.GetTaskType(ctx, "ipam:none"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("missing type: %v", err)
		}
		avail := true
		if list, err := db.ListTaskTypes(ctx, store.TypeFilter{Module: "ipam", Available: &avail}); err != nil || len(list) != 2 {
			t.Fatalf("list = %v %v", list, err)
		}
		// another module can never take over the name (the schema binds the
		// name's prefix to the owner)
		if err := db.UpsertTaskTypes(ctx, "lcm", []store.TaskType{{Name: scanType, Module: "lcm", DisplayName: "x", Scope: store.ScopeTenant}}); err == nil {
			t.Fatal("foreign upsert admitted")
		}
		if err := db.UpsertTaskTypes(ctx, "lcm", []store.TaskType{{Name: "ipam:x", Module: "ipam", DisplayName: "x", Scope: store.ScopeTenant}}); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("mismatched module: %v", err)
		}
		counts, err := db.CountTypesByModule(ctx)
		if err != nil || counts["ipam"] != 2 {
			t.Fatalf("counts = %v %v", counts, err)
		}
		n, err := reg.Unregister(ctx, "ipam", "spiffe://example.org/svc/ipam")
		if err != nil || n != 2 {
			t.Fatalf("unregister = %d %v", n, err)
		}
		if ok, err := db.InsertTaskTypeIfAbsent(ctx, store.TaskType{Name: scanType, Module: "ipam", DisplayName: "x", Scope: store.ScopeTenant}); ok || err != nil {
			t.Fatalf("insert over existing = %v %v", ok, err)
		}
		// the app role may never delete a type
		conn, err := pgx.Connect(ctx, env.appDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, `DELETE FROM scheduler_task_types`); err == nil {
			t.Fatal("app role deleted task types")
		}
	})

	t.Run("rls isolates tenants", func(t *testing.T) {
		_, svc := setup(t)
		a, err := svc.Create(ctx, userA, tasks.CreateInput{Name: "A", TypeName: scanType, Kind: store.KindPeriodic, Payload: json.RawMessage(`{"all":true}`)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Create(ctx, root, tasks.CreateInput{Name: "sweep", TypeName: "ipam:sweep", Kind: store.KindDelayed}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Get(ctx, userB, a.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("tenant B reads A: %v", err)
		}
		var n int
		if err := st.Tx(ctx, store.Scope{TenantID: tenantB}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_tasks`).Scan(&n)
		}); err != nil || n != 0 {
			t.Fatalf("tenant B sees %d rows (%v)", n, err)
		}
		// the unscoped app role sees nothing at all
		conn, err := pgx.Connect(ctx, env.appDSN)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM scheduler_tasks`).Scan(&n); err == nil && n != 0 {
			t.Fatalf("unscoped app role read %d tasks", n)
		}
		// writing another tenant's row is refused by WITH CHECK
		err = st.Tx(ctx, store.Scope{TenantID: tenantB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET tenant_id = $1`, tenantA)
			return err
		})
		if err != nil {
			t.Fatalf("no-op update failed: %v", err)
		}
		err = st.Tx(ctx, store.Scope{TenantID: tenantB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO scheduler_executions (id, tenant_id, task_id, occurrence_id, type_name, module, trigger, occurrence_at, due_at, status, attempt, max_attempts)
 VALUES ($1, $2, $1, $1, 'x:y', 'x', 'manual', now(), now(), 'queued', 1, 1)`, store.NewID(), tenantA)
			return err
		})
		if err == nil {
			t.Fatal("cross-tenant insert admitted")
		}
		// platform admins see every tenant, platform rows included
		if pg, err := svc.List(ctx, root, store.TaskFilter{}); err != nil || pg.Total != 2 || len(pg.Items) != 2 {
			t.Fatalf("admin list = %d %v", pg.Total, err)
		}
		if pg, err := svc.List(ctx, root, store.TaskFilter{TenantID: tenantA, Kind: store.KindPeriodic, State: store.StateEnabled, Validity: store.ValidityOK, Type: scanType, Query: "a"}); err != nil || len(pg.Items) != 1 {
			t.Fatalf("filtered admin list = %v %v", pg.Items, err)
		}
		if _, err := db.GetTask(ctx, repo.Tenant(tenantA), "not-a-uuid"); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("malformed id: %v", err)
		}
		if _, err := db.GetTask(ctx, repo.Scope{}, a.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("empty scope: %v", err)
		}
	})

	t.Run("names and limits", func(t *testing.T) {
		reg, _ := setup(t)
		svc := tasks.New(tasks.Deps{Store: db, Registry: reg, Now: clk.Now, Limits: tasks.Limits{MaxTasksPerTenant: 3}})
		mk := func(subj authz.Subjects, name string) error {
			_, err := svc.Create(ctx, subj, tasks.CreateInput{Name: name, TypeName: scanType, Kind: store.KindPeriodic})
			return err
		}
		if err := mk(userA, "Scan"); err != nil {
			t.Fatal(err)
		}
		var te *tasks.Error
		if err := mk(userA, "SCAN"); !errors.As(err, &te) || te.Reason != tasks.ReasonNameTaken {
			t.Fatalf("case-insensitive duplicate: %v", err)
		}
		if err := mk(userB, "Scan"); err != nil {
			t.Fatalf("same name other tenant: %v", err)
		}
		_ = mk(userA, "two")
		_ = mk(userA, "three")
		if err := mk(userA, "four"); !errors.As(err, &te) || te.Reason != tasks.ReasonTaskLimit {
			t.Fatalf("limit: %v", err)
		}
	})

	t.Run("two engines never duplicate an occurrence", func(t *testing.T) {
		_, svc := setup(t)
		for i := 0; i < 20; i++ {
			v, err := svc.Create(ctx, userA, tasks.CreateInput{Name: "t" + string(rune('a'+i)), TypeName: scanType, Kind: store.KindPeriodic})
			if err != nil {
				t.Fatal(err)
			}
			// due now
			if _, err := admin.Exec(ctx, `UPDATE scheduler_tasks SET next_run_at = $2 WHERE id = $1`, v.ID, clk.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		var mu sync.Mutex
		dispatched := map[string]int{}
		disp := engine.DispatcherFunc(func(_ context.Context, a engine.Attempt) engine.Outcome {
			mu.Lock()
			dispatched[a.Exec.TaskID+a.Exec.OccurrenceAt.String()]++
			mu.Unlock()
			return engine.Outcome{Status: store.ExecSucceeded, Message: "ok", Result: []byte(`{"ok":true}`)}
		})
		e1 := engine.New(engine.Config{InstanceID: "e1", Batch: 5}, engine.Deps{Store: db, Dispatcher: disp, Now: clk.Now})
		e2 := engine.New(engine.Config{InstanceID: "e2", Batch: 5}, engine.Deps{Store: db, Dispatcher: disp, Now: clk.Now})
		var wg sync.WaitGroup
		for _, e := range []*engine.Engine{e1, e2} {
			wg.Add(1)
			go func(e *engine.Engine) {
				defer wg.Done()
				for i := 0; i < 8; i++ {
					e.Cycle(ctx)
				}
				e.Wait()
			}(e)
		}
		wg.Wait()
		if len(dispatched) != 20 {
			t.Fatalf("occurrences dispatched = %d", len(dispatched))
		}
		for k, n := range dispatched {
			if n != 1 {
				t.Fatalf("occurrence %s dispatched %d times", k, n)
			}
		}
		var execs int
		_ = admin.QueryRow(ctx, `SELECT count(*) FROM scheduler_executions`).Scan(&execs)
		if execs != 20 {
			t.Fatalf("attempt rows = %d", execs)
		}
		page, err := svc.Executions(ctx, userA, store.ExecFilter{Statuses: []string{store.ExecSucceeded}, Trigger: store.TriggerSchedule, List: listquery.Request{PageSize: 5}})
		if err != nil || page.Total != 20 || len(page.Items) != 5 || page.Counts.Succeeded != 20 || page.Items[0].Result != nil {
			t.Fatalf("history = %+v %v", page, err)
		}
		one, err := svc.Execution(ctx, userA, page.Items[0].ID)
		if err != nil || one.Result == nil {
			t.Fatalf("single attempt = %+v %v", one, err)
		}
		ov, err := svc.Overview(ctx, userA)
		if err != nil || ov.Tasks.Enabled != 20 || ov.Runs24h != 20 || len(ov.NextDue) != 10 {
			t.Fatalf("overview = %+v %v", ov, err)
		}
	})

	t.Run("retry, fencing, recovery and retention", func(t *testing.T) {
		_, svc := setup(t)
		v, err := svc.Create(ctx, userA, tasks.CreateInput{Name: "r", TypeName: scanType, Kind: store.KindPeriodic, MaxRetries: ptr(1)})
		if err != nil {
			t.Fatal(err)
		}
		id, err := svc.Run(ctx, userA, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := db.ClaimQueued(ctx, clk.Now(), "e1", 10, time.Second)
		if err != nil || len(claims) != 1 || claims[0].Exec.ID != id || claims[0].Task.ID != v.ID {
			t.Fatalf("claim = %+v %v", claims, err)
		}
		// a completion by another owner is fenced out
		if ok, err := db.CompleteAttempt(ctx, repo.Completion{ExecID: id, Owner: "e2", Status: store.ExecSucceeded, FinishedAt: clk.Now()}); ok || err != nil {
			t.Fatalf("fence = %v %v", ok, err)
		}
		// the lease expires: recovery times the attempt out and retries it
		clk.Add(10 * time.Minute)
		lost, err := db.RecoverExpired(ctx, clk.Now(), 10, func(x store.Execution) *store.Execution {
			r := x
			r.ID, r.Attempt, r.Status, r.DueAt, r.StartedAt, r.FinishedAt, r.LeaseOwner, r.LeaseUntil = store.NewID(), 2, store.ExecQueued, clk.Now(), nil, nil, "", nil
			return &r
		})
		if err != nil || len(lost) != 1 || lost[0].Status != store.ExecTimedOut {
			t.Fatalf("recover = %+v %v", lost, err)
		}
		if ok, _ := db.CompleteAttempt(ctx, repo.Completion{ExecID: id, Owner: "e1", Status: store.ExecSucceeded, FinishedAt: clk.Now()}); ok {
			t.Fatal("completion after recovery admitted")
		}
		claims, _ = db.ClaimQueued(ctx, clk.Now(), "e1", 10, time.Second)
		if len(claims) != 1 || claims[0].Exec.Attempt != 2 {
			t.Fatalf("retry claim = %+v", claims)
		}
		ok, err := db.CompleteAttempt(ctx, repo.Completion{ExecID: claims[0].Exec.ID, Owner: "e1", Status: store.ExecFailed, Message: "boom",
			FinishedAt: clk.Now(), Validity: store.ValidityPayloadInvalid, ValidityMessage: "/x: bad"})
		if !ok || err != nil {
			t.Fatalf("complete = %v %v", ok, err)
		}
		got, _ := svc.Get(ctx, userA, v.ID)
		if got.LastStatus != store.ExecFailed || got.RunCount != 1 || got.Validity != store.ValidityPayloadInvalid {
			t.Fatalf("task after final attempt = %+v", got)
		}
		// cancel queued attempts through delete; history survives the task
		if err := svc.Delete(ctx, userA, v.ID); err != nil {
			t.Fatal(err)
		}
		if page, _ := svc.Executions(ctx, userA, store.ExecFilter{TaskID: v.ID, FailedOnly: true}); page.Total != 2 {
			t.Fatalf("history after delete = %d", page.Total)
		}
		n, err := db.PruneBefore(ctx, time.Now().Add(time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("pruned = %d %v", n, err)
		}
	})

	t.Run("one-shot completes and control", func(t *testing.T) {
		_, svc := setup(t)
		w, err := svc.Create(ctx, userA, tasks.CreateInput{Name: "w", TypeName: scanType, Kind: store.KindWaitResult})
		if err != nil || w.ExecutionID == "" {
			t.Fatalf("wait_result = %+v %v", w, err)
		}
		e := engine.New(engine.Config{InstanceID: "e1"}, engine.Deps{Store: db, Now: clk.Now,
			Dispatcher: engine.DispatcherFunc(func(context.Context, engine.Attempt) engine.Outcome {
				return engine.Outcome{Status: store.ExecSucceeded}
			})})
		e.Cycle(ctx)
		e.Wait()
		got, _ := svc.Get(ctx, userA, w.ID)
		if got.Status != store.TaskCompleted || got.NextRunAt != nil || got.RunCount != 1 {
			t.Fatalf("completed = %+v", got)
		}
		p, _ := svc.Create(ctx, userA, tasks.CreateInput{Name: "p", TypeName: scanType, Kind: store.KindPeriodic})
		if n, err := svc.Bulk(ctx, userA, tasks.ActionStop); err != nil || n != 1 {
			t.Fatalf("bulk stop = %d %v", n, err)
		}
		if got, _ = svc.Start(ctx, userA, p.ID); !got.Enabled {
			t.Fatal("start")
		}
		if _, err := svc.Update(ctx, userA, p.ID, tasks.UpdateInput{Name: "renamed", Payload: json.RawMessage(`{"all":false}`), Cron: ptr("5 4 * * *")}); err != nil {
			t.Fatal(err)
		}
		d, _ := svc.Create(ctx, userA, tasks.CreateInput{Name: "d", TypeName: scanType, Kind: store.KindDelayed, DelaySeconds: ptr(600)})
		if c, err := svc.Cancel(ctx, userA, d.ID); err != nil || c.Status != store.TaskCancelled {
			t.Fatalf("cancel = %v", err)
		}
	})

	t.Run("backup rows and audit", func(t *testing.T) {
		_, svc := setup(t)
		v, _ := svc.Create(ctx, userA, tasks.CreateInput{Name: "b", TypeName: scanType, Kind: store.KindPeriodic})
		ts, err := db.BackupTasks(ctx, repo.Tenant(tenantA))
		if err != nil || len(ts) != 1 {
			t.Fatalf("backup tasks = %d %v", len(ts), err)
		}
		if ok, err := db.ImportTask(ctx, ts[0]); ok || err != nil {
			t.Fatalf("existing id import = %v %v", ok, err)
		}
		dup := ts[0]
		dup.ID = store.NewID()
		if _, err := db.ImportTask(ctx, dup); !errors.Is(err, repo.ErrConflict) {
			t.Fatalf("name clash import: %v", err)
		}
		x := store.Execution{ID: store.NewID(), TenantID: tenantA, TaskID: v.ID, OccurrenceID: store.NewID(), TypeName: scanType, Module: "ipam",
			Trigger: store.TriggerManual, OccurrenceAt: clk.Now(), DueAt: clk.Now(), Status: store.ExecSucceeded, Attempt: 1, MaxAttempts: 1}
		if ok, err := db.ImportExecution(ctx, x); !ok || err != nil {
			t.Fatalf("import execution = %v %v", ok, err)
		}
		if ok, _ := db.ImportExecution(ctx, x); ok {
			t.Fatal("duplicate execution imported")
		}
		if xs, err := db.BackupExecutions(ctx, repo.AllTenants()); err != nil || len(xs) != 1 {
			t.Fatalf("backup executions = %d %v", len(xs), err)
		}
		if err := db.AppendAudit(ctx, store.AuditRow{ID: store.NewID(), TenantID: tenantA, ActorKind: "user", Action: "task.create",
			SubjectKind: "task", Outcome: "ok", Detail: map[string]any{"type": scanType}}); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := st.Tx(ctx, store.Scope{TenantID: tenantB}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_audit_events`).Scan(&n)
		}); err != nil || n != 0 {
			t.Fatalf("tenant B reads A's audit: %d %v", n, err)
		}
	})
}

func ptr[T any](v T) *T { return &v }
