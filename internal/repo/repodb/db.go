// Package repodb binds repo.Store to TimescaleDB via *store.Store. Tenant
// scopes run in a tenant transaction (RLS); the all-tenant scope, the engine,
// the type catalog and the audit writer run under the system scope. The
// engine operations lock rows (FOR UPDATE SKIP LOCKED) so several replicas
// never plan or claim the same work, and completion is fenced by the lease
// owner. Unique violations map to repo.ErrConflict and malformed ids to
// repo.ErrNotFound.
package repodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// DB implements repo.Store over *store.Store.
type DB struct{ St *store.Store }

var _ repo.Store = (*DB)(nil)

// New wraps the store.
func New(st *store.Store) *DB { return &DB{St: st} }

// MaxMessage bounds messages stored on tasks and attempts.
const MaxMessage = 4 << 10

// LostMessage is recorded on attempts whose scheduler instance lost them.
const LostMessage = "scheduler instance lost the run"

func (d *DB) scoped(ctx context.Context, s repo.Scope, fn func(tx pgx.Tx) error) error {
	if s.All {
		return d.St.Tx(ctx, store.Scope{System: true}, fn)
	}
	if s.TenantID == "" {
		return repo.ErrNotFound
	}
	return d.St.Tx(ctx, store.Scope{TenantID: s.TenantID}, fn)
}

func (d *DB) system(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return d.St.Tx(ctx, store.Scope{System: true}, fn)
}

type scanner interface{ Scan(dest ...any) error }

// mapErr maps store errors: no row / malformed id → ErrNotFound, unique
// violation → ErrConflict.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return repo.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return repo.ErrConflict
		case "22P02": // invalid_text_representation (a non-uuid id)
			return repo.ErrNotFound
		}
	}
	return err
}

func bytesOrNil(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func cut(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && (s[max]&0xC0) == 0x80 {
		max--
	}
	return s[:max]
}

func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(q)) + "%"
}

// ---------------------------------------------------------------- types

const typeCols = `name, module, display_name, description, payload_schema, default_cron, default_max_retries, scope,
 available, registered_at, unregistered_at, schema_hash`

func scanType(sc scanner) (store.TaskType, error) {
	var t store.TaskType
	var schema []byte
	err := sc.Scan(&t.Name, &t.Module, &t.DisplayName, &t.Description, &schema, &t.DefaultCron, &t.DefaultMaxRetries, &t.Scope,
		&t.Available, &t.RegisteredAt, &t.UnregisteredAt, &t.SchemaHash)
	if len(schema) > 0 {
		t.PayloadSchema = schema
	}
	return t, err
}

// GetTaskType implements repo.Types.
func (d *DB) GetTaskType(ctx context.Context, name string) (t store.TaskType, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		t, err = scanType(tx.QueryRow(ctx, `SELECT `+typeCols+` FROM scheduler_task_types WHERE name = $1`, name))
		return err
	})
	return t, mapErr(err)
}

// ListTaskTypes implements repo.Types.
func (d *DB) ListTaskTypes(ctx context.Context, f store.TypeFilter) ([]store.TaskType, error) {
	out := []store.TaskType{}
	err := d.system(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+typeCols+` FROM scheduler_task_types
 WHERE ($1 = '' OR module = $1) AND ($2::boolean IS NULL OR available = $2) ORDER BY name`, f.Module, f.Available)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanType(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, mapErr(err)
}

// UpsertTaskTypes implements repo.Types.
func (d *DB) UpsertTaskTypes(ctx context.Context, module string, types []store.TaskType) error {
	err := d.system(ctx, func(tx pgx.Tx) error {
		for _, t := range types {
			if t.Module != module {
				return repo.ErrConflict
			}
			tag, err := tx.Exec(ctx, `INSERT INTO scheduler_task_types (`+typeCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true,$9,NULL,$10)
 ON CONFLICT (name) DO UPDATE SET display_name = EXCLUDED.display_name, description = EXCLUDED.description,
   payload_schema = EXCLUDED.payload_schema, default_cron = EXCLUDED.default_cron,
   default_max_retries = EXCLUDED.default_max_retries, scope = EXCLUDED.scope, available = true,
   registered_at = EXCLUDED.registered_at, unregistered_at = NULL, schema_hash = EXCLUDED.schema_hash
 WHERE scheduler_task_types.module = EXCLUDED.module`,
				t.Name, t.Module, t.DisplayName, t.Description, bytesOrNil(t.PayloadSchema), t.DefaultCron, t.DefaultMaxRetries, t.Scope,
				t.RegisteredAt, t.SchemaHash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 { // the name belongs to another module
				return repo.ErrConflict
			}
		}
		return nil
	})
	return mapErr(err)
}

// MarkModuleUnavailable implements repo.Types.
func (d *DB) MarkModuleUnavailable(ctx context.Context, module string, at time.Time) (n int, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE scheduler_task_types SET available = false, unregistered_at = $2
 WHERE module = $1 AND available`, module, at); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_task_types WHERE module = $1`, module).Scan(&n); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET validity = 'type_unavailable', validity_message = 'task type unavailable'
 WHERE type_name IN (SELECT name FROM scheduler_task_types WHERE module = $1)`, module)
		return err
	})
	return n, mapErr(err)
}

// InsertTaskTypeIfAbsent implements repo.Types.
func (d *DB) InsertTaskTypeIfAbsent(ctx context.Context, t store.TaskType) (ok bool, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO scheduler_task_types (`+typeCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (name) DO NOTHING`,
			t.Name, t.Module, t.DisplayName, t.Description, bytesOrNil(t.PayloadSchema), t.DefaultCron, t.DefaultMaxRetries, t.Scope,
			t.Available, t.RegisteredAt, t.UnregisteredAt, t.SchemaHash)
		ok = err == nil && tag.RowsAffected() == 1
		return err
	})
	return ok, mapErr(err)
}

// CountTypesByModule implements repo.Types.
func (d *DB) CountTypesByModule(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	err := d.system(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT module, count(*) FROM scheduler_task_types WHERE available GROUP BY module`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m string
			var n int64
			if err := rows.Scan(&m, &n); err != nil {
				return err
			}
			out[m] = n
		}
		return rows.Err()
	})
	return out, mapErr(err)
}

// UpdateTasksOfType implements repo.Types.
func (d *DB) UpdateTasksOfType(ctx context.Context, typeName string, fn func(t *store.Task) bool) (n int, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		list, err := queryTasks(ctx, tx, `SELECT `+taskCols+` FROM scheduler_tasks WHERE type_name = $1 ORDER BY id FOR UPDATE`, typeName)
		if err != nil {
			return err
		}
		n = len(list)
		for i := range list {
			if !fn(&list[i]) {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET validity = $2, validity_message = $3 WHERE id = $1`,
				list[i].ID, list[i].Validity, cut(list[i].ValidityMessage, MaxMessage)); err != nil {
				return err
			}
		}
		return nil
	})
	return n, mapErr(err)
}

// ---------------------------------------------------------------- tasks

const taskCols = `id, tenant_id, name, type_name, module, kind, payload, cron, timezone, run_at, enabled, status, validity,
 validity_message, max_retries, timeout_seconds, catch_up, remark, next_run_at, last_run_at, last_status, last_message,
 run_count, missed_count, created_by, updated_by, created_at, updated_at`

func scanTask(sc scanner) (store.Task, error) {
	var t store.Task
	var body []byte
	err := sc.Scan(&t.ID, &t.TenantID, &t.Name, &t.TypeName, &t.Module, &t.Kind, &body, &t.Cron, &t.Timezone, &t.RunAt, &t.Enabled,
		&t.Status, &t.Validity, &t.ValidityMessage, &t.MaxRetries, &t.TimeoutSeconds, &t.CatchUp, &t.Remark, &t.NextRunAt,
		&t.LastRunAt, &t.LastStatus, &t.LastMessage, &t.RunCount, &t.MissedCount, &t.CreatedBy, &t.UpdatedBy, &t.CreatedAt, &t.UpdatedAt)
	t.Payload = body
	return t, err
}

func queryTasks(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]store.Task, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func payloadOf(t store.Task) []byte {
	if len(t.Payload) == 0 {
		return []byte("{}")
	}
	return []byte(t.Payload)
}

func insertTask(ctx context.Context, tx pgx.Tx, t store.Task, onConflictID bool) (pgconn.CommandTag, error) {
	suffix := ""
	if onConflictID {
		suffix = " ON CONFLICT (id) DO NOTHING"
	}
	return tx.Exec(ctx, `INSERT INTO scheduler_tasks (`+taskCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`+suffix,
		t.ID, t.TenantID, t.Name, t.TypeName, t.Module, t.Kind, payloadOf(t), t.Cron, t.Timezone, t.RunAt, t.Enabled, t.Status,
		t.Validity, cut(t.ValidityMessage, MaxMessage), t.MaxRetries, t.TimeoutSeconds, t.CatchUp, t.Remark, t.NextRunAt, t.LastRunAt,
		t.LastStatus, cut(t.LastMessage, MaxMessage), t.RunCount, t.MissedCount, t.CreatedBy, t.UpdatedBy, orNow(t.CreatedAt), orNow(t.UpdatedAt))
}

func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// CreateTask implements repo.Tasks.
func (d *DB) CreateTask(ctx context.Context, t store.Task, first *store.Execution, maxPerTenant int) error {
	err := d.St.Tx(ctx, store.Scope{TenantID: t.TenantID}, func(tx pgx.Tx) error {
		if maxPerTenant > 0 {
			// serialise creations per tenant so the limit holds under concurrency
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 26))`, t.TenantID); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_tasks WHERE tenant_id = $1`, t.TenantID).Scan(&n); err != nil {
				return err
			}
			if n >= maxPerTenant {
				return repo.ErrLimit
			}
		}
		if _, err := insertTask(ctx, tx, t, false); err != nil {
			return err
		}
		if first != nil {
			return insertExec(ctx, tx, *first, false)
		}
		return nil
	})
	return mapErr(err)
}

// GetTask implements repo.Tasks.
func (d *DB) GetTask(ctx context.Context, s repo.Scope, id string) (t store.Task, err error) {
	err = d.scoped(ctx, s, func(tx pgx.Tx) error {
		t, err = scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM scheduler_tasks WHERE id = $1`, id))
		return err
	})
	return t, mapErr(err)
}

// taskWhere builds the filter of ListTasks / TaskIDs.
func taskWhere(s repo.Scope, f store.TaskFilter) (string, []any) {
	conds := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	// The tenant predicate is explicit (not only RLS) so a tenant scope never
	// depends on the session GUC alone and the planner sees the index prefix.
	if !s.All {
		add("tenant_id = $%d::uuid", s.TenantID)
	} else if f.TenantID != "" {
		add("tenant_id = $%d::uuid", f.TenantID)
	}
	if f.Periodic {
		conds = append(conds, "kind = 'periodic'")
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.Validity != "" {
		add("validity = $%d", f.Validity)
	}
	if f.Type != "" {
		add("type_name = $%d", f.Type)
	}
	switch f.State {
	case store.StateEnabled:
		conds = append(conds, "status = 'active' AND enabled")
	case store.StateStopped:
		conds = append(conds, "status = 'active' AND NOT enabled")
	case store.StateCompleted:
		conds = append(conds, "status = 'completed'")
	case store.StateCancelled:
		conds = append(conds, "status = 'cancelled'")
	case "":
	default:
		conds = append(conds, "false")
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		add("(lower(name) LIKE $%[1]d ESCAPE '\\' OR lower(type_name) LIKE $%[1]d ESCAPE '\\')", likePattern(q))
	}
	return strings.Join(conds, " AND "), args
}

// ListTasks implements repo.Tasks.
func (d *DB) ListTasks(ctx context.Context, s repo.Scope, f store.TaskFilter) ([]store.Task, int64, error) {
	req := store.ListRequest(f.List, store.TaskList)
	where, args := taskWhere(s, f)
	var out []store.Task
	var total int64
	err := d.scoped(ctx, s, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_tasks WHERE `+where, args...).Scan(&total); err != nil {
			return err
		}
		req = req.Clamp(int(total))
		var err error
		out, err = queryTasks(ctx, tx, fmt.Sprintf(`SELECT %s FROM scheduler_tasks WHERE %s ORDER BY %s LIMIT %d OFFSET %d`,
			taskCols, where, req.OrderBy(store.TaskList), req.Limit(), req.Offset()), args...)
		return err
	})
	if errors.Is(err, repo.ErrNotFound) {
		return []store.Task{}, 0, nil
	}
	return out, total, mapErr(err)
}

// TaskIDs implements repo.Tasks.
func (d *DB) TaskIDs(ctx context.Context, s repo.Scope, f store.TaskFilter) ([]string, error) {
	where, args := taskWhere(s, f)
	out := []string{}
	err := d.scoped(ctx, s, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM scheduler_tasks WHERE `+where+` ORDER BY lower(name), id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if errors.Is(err, repo.ErrNotFound) {
		return []string{}, nil
	}
	return out, mapErr(err)
}

func busy(ctx context.Context, tx pgx.Tx, taskID string) (bool, error) {
	var b bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scheduler_executions WHERE task_id = $1 AND status IN ('queued','running'))`, taskID).Scan(&b)
	return b, err
}

// MutateTask implements repo.Tasks.
func (d *DB) MutateTask(ctx context.Context, s repo.Scope, id string, fn repo.TaskMutation) (store.Task, repo.MutateResult, error) {
	var res repo.MutateResult
	var t store.Task
	err := d.scoped(ctx, s, func(tx pgx.Tx) error {
		cur, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM scheduler_tasks WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		b, err := busy(ctx, tx, id)
		if err != nil {
			return err
		}
		t = cur
		eff, err := fn(&t, b)
		if err != nil {
			return err
		}
		t.ID, t.TenantID = cur.ID, cur.TenantID
		now := orNow(t.UpdatedAt)
		if eff.CancelQueued {
			tag, err := tx.Exec(ctx, `UPDATE scheduler_executions SET status = 'cancelled', message = $2, final = true, finished_at = $3
 WHERE task_id = $1 AND status = 'queued'`, id, cut(eff.CancelMessage, MaxMessage), now)
			if err != nil {
				return err
			}
			res.Cancelled = int(tag.RowsAffected())
		}
		if eff.Delete {
			_, err := tx.Exec(ctx, `DELETE FROM scheduler_tasks WHERE id = $1`, id)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET name = $2, payload = $3, cron = $4, timezone = $5, run_at = $6, enabled = $7,
 status = $8, validity = $9, validity_message = $10, max_retries = $11, timeout_seconds = $12, catch_up = $13, remark = $14,
 next_run_at = $15, updated_by = $16, updated_at = $17 WHERE id = $1`,
			id, t.Name, payloadOf(t), t.Cron, t.Timezone, t.RunAt, t.Enabled, t.Status, t.Validity, cut(t.ValidityMessage, MaxMessage),
			t.MaxRetries, t.TimeoutSeconds, t.CatchUp, t.Remark, t.NextRunAt, t.UpdatedBy, now); err != nil {
			return err
		}
		if eff.Insert != nil {
			return insertExec(ctx, tx, *eff.Insert, false)
		}
		return nil
	})
	if err != nil {
		return store.Task{}, res, mapErr(err)
	}
	return t, res, nil
}

// ---------------------------------------------------------------- executions

const execCols = `id, tenant_id, task_id, task_name, occurrence_id, type_name, module, trigger, triggered_by, occurrence_at,
 due_at, status, attempt, max_attempts, started_at, finished_at, duration_ms, message, result, result_truncated, final,
 lease_owner, lease_until, created_at`

// execListCols omits the result (lists never carry it).
var execListCols = strings.Replace(execCols, "result,", "NULL::bytea,", 1)

func scanExec(sc scanner) (store.Execution, error) {
	var e store.Execution
	err := sc.Scan(&e.ID, &e.TenantID, &e.TaskID, &e.TaskName, &e.OccurrenceID, &e.TypeName, &e.Module, &e.Trigger, &e.TriggeredBy,
		&e.OccurrenceAt, &e.DueAt, &e.Status, &e.Attempt, &e.MaxAttempts, &e.StartedAt, &e.FinishedAt, &e.DurationMS, &e.Message,
		&e.Result, &e.ResultTruncated, &e.Final, &e.LeaseOwner, &e.LeaseUntil, &e.CreatedAt)
	return e, err
}

func queryExecs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]store.Execution, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.Execution{}
	for rows.Next() {
		e, err := scanExec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func insertExec(ctx context.Context, tx pgx.Tx, e store.Execution, ignoreConflict bool) error {
	suffix := ""
	if ignoreConflict {
		suffix = " ON CONFLICT DO NOTHING"
	}
	_, err := tx.Exec(ctx, `INSERT INTO scheduler_executions (`+execCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`+suffix,
		e.ID, e.TenantID, e.TaskID, e.TaskName, e.OccurrenceID, e.TypeName, e.Module, e.Trigger, e.TriggeredBy, e.OccurrenceAt,
		e.DueAt, e.Status, e.Attempt, e.MaxAttempts, e.StartedAt, e.FinishedAt, e.DurationMS, cut(e.Message, MaxMessage),
		bytesOrNil(e.Result), e.ResultTruncated, e.Final, e.LeaseOwner, e.LeaseUntil, orNow(e.CreatedAt))
	return err
}

// GetExecution implements repo.Executions.
func (d *DB) GetExecution(ctx context.Context, s repo.Scope, id string) (e store.Execution, err error) {
	err = d.scoped(ctx, s, func(tx pgx.Tx) error {
		e, err = scanExec(tx.QueryRow(ctx, `SELECT `+execCols+` FROM scheduler_executions WHERE id = $1`, id))
		return err
	})
	return e, mapErr(err)
}

// ListExecutions implements repo.Executions.
func (d *DB) ListExecutions(ctx context.Context, s repo.Scope, f store.ExecFilter) ([]store.Execution, int64, store.ExecCounts, error) {
	req := store.ListRequest(f.List, store.ExecutionList)
	conds := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if !s.All {
		add("tenant_id = $%d::uuid", s.TenantID)
	}
	if f.TaskID != "" {
		add("task_id = $%d::uuid", f.TaskID)
	}
	if f.Trigger != "" {
		add("trigger = $%d", f.Trigger)
	}
	if f.From != nil {
		add("created_at >= $%d", *f.From)
	}
	if f.To != nil {
		add("created_at < $%d", *f.To)
	}
	base := strings.Join(conds, " AND ")
	status := base
	if f.FailedOnly {
		status += " AND status IN ('failed','timed_out')"
	}
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		status += fmt.Sprintf(" AND status = ANY($%d)", len(args))
	}
	var out []store.Execution
	var total int64
	var counts store.ExecCounts
	err := d.scoped(ctx, s, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'succeeded'),
 count(*) FILTER (WHERE status IN ('failed','timed_out')),
 count(*) FILTER (WHERE status NOT IN ('succeeded','failed','timed_out'))
 FROM scheduler_executions WHERE `+base, args[:len(args)-boolInt(len(f.Statuses) > 0)]...).Scan(&counts.Succeeded, &counts.Failed, &counts.Other); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM scheduler_executions WHERE `+status, args...).Scan(&total); err != nil {
			return err
		}
		req = req.Clamp(int(total))
		var err error
		out, err = queryExecs(ctx, tx, fmt.Sprintf(`SELECT %s FROM scheduler_executions WHERE %s ORDER BY %s LIMIT %d OFFSET %d`,
			execListCols, status, req.OrderBy(store.ExecutionList), req.Limit(), req.Offset()), args...)
		return err
	})
	if errors.Is(err, repo.ErrNotFound) {
		return []store.Execution{}, 0, counts, nil
	}
	return out, total, counts, mapErr(err)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Overview implements repo.Executions.
func (d *DB) Overview(ctx context.Context, s repo.Scope, now time.Time) (store.Overview, error) {
	ov := store.Overview{NextDue: []store.NextDue{}, Failing: []store.Failing{}}
	err := d.scoped(ctx, s, func(tx pgx.Tx) error {
		c := &ov.Tasks
		if err := tx.QueryRow(ctx, `SELECT
 count(*) FILTER (WHERE status = 'active' AND enabled), count(*) FILTER (WHERE status = 'active' AND NOT enabled),
 count(*) FILTER (WHERE status = 'completed'), count(*) FILTER (WHERE status = 'cancelled'),
 count(*) FILTER (WHERE validity = 'type_unavailable'), count(*) FILTER (WHERE validity = 'payload_invalid')
 FROM scheduler_tasks`).Scan(&c.Enabled, &c.Stopped, &c.Completed, &c.Cancelled, &c.TypeUnavailable, &c.PayloadInvalid); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('succeeded','failed','timed_out')),
 count(*) FILTER (WHERE status IN ('failed','timed_out'))
 FROM scheduler_executions WHERE finished_at >= $1`, now.Add(-24*time.Hour)).Scan(&ov.Runs24h, &ov.Failures24h); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, name, next_run_at FROM scheduler_tasks
 WHERE enabled AND status = 'active' AND next_run_at IS NOT NULL ORDER BY next_run_at, id LIMIT 10`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var n store.NextDue
			if err := rows.Scan(&n.TaskID, &n.Name, &n.NextRunAt); err != nil {
				rows.Close()
				return err
			}
			ov.NextDue = append(ov.NextDue, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT id, name, last_status, last_message, last_run_at FROM scheduler_tasks
 WHERE last_status IN ('failed','timed_out') ORDER BY last_run_at DESC NULLS LAST, id LIMIT 10`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f store.Failing
			if err := rows.Scan(&f.TaskID, &f.Name, &f.LastStatus, &f.LastMessage, &f.LastRunAt); err != nil {
				return err
			}
			ov.Failing = append(ov.Failing, f)
		}
		return rows.Err()
	})
	return ov, mapErr(err)
}

// ---------------------------------------------------------------- engine

// PlanDue implements repo.Engine.
func (d *DB) PlanDue(ctx context.Context, now time.Time, limit int, fn repo.PlanFunc) (n int, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		due, err := queryTasks(ctx, tx, `SELECT `+taskCols+` FROM scheduler_tasks
 WHERE enabled AND status = 'active' AND next_run_at IS NOT NULL AND next_run_at <= $1
 ORDER BY next_run_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		n = len(due)
		for _, cur := range due {
			b, err := busy(ctx, tx, cur.ID)
			if err != nil {
				return err
			}
			t := cur
			plan, err := fn(&t, b)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET next_run_at = $2, status = $3, missed_count = $4, last_status = $5,
 last_message = $6 WHERE id = $1`, cur.ID, t.NextRunAt, t.Status, t.MissedCount, t.LastStatus, cut(t.LastMessage, MaxMessage)); err != nil {
				return err
			}
			for _, e := range plan.Insert {
				if err := insertExec(ctx, tx, e, true); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, mapErr(err)
}

// ClaimQueued implements repo.Engine.
func (d *DB) ClaimQueued(ctx context.Context, now time.Time, owner string, limit int, leaseGrace time.Duration) ([]repo.Claim, error) {
	out := []repo.Claim{}
	err := d.system(ctx, func(tx pgx.Tx) error {
		queued, err := queryExecs(ctx, tx, `SELECT `+execCols+` FROM scheduler_executions
 WHERE status = 'queued' AND due_at <= $1 ORDER BY due_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		for _, e := range queued {
			t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM scheduler_tasks WHERE id = $1`, e.TaskID))
			timeout := 300
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				t = store.Task{}
			case err != nil:
				return err
			default:
				timeout = t.TimeoutSeconds
			}
			until := now.Add(time.Duration(timeout)*time.Second + leaseGrace)
			e.Status, e.LeaseOwner, e.StartedAt, e.LeaseUntil = store.ExecRunning, owner, &now, &until
			if _, err := tx.Exec(ctx, `UPDATE scheduler_executions SET status = 'running', lease_owner = $2, started_at = $3, lease_until = $4
 WHERE id = $1`, e.ID, owner, now, until); err != nil {
				return err
			}
			out = append(out, repo.Claim{Exec: e, Task: t})
		}
		return nil
	})
	return out, mapErr(err)
}

// finalise applies a final attempt to its task.
func finalise(ctx context.Context, tx pgx.Tx, e store.Execution) error {
	last := e.StartedAt
	if last == nil {
		last = e.FinishedAt
	}
	_, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET last_run_at = $2, last_status = $3, last_message = $4, run_count = run_count + 1,
 next_run_at = CASE WHEN kind <> 'periodic' AND $5 <> 'manual' AND status = 'active' THEN NULL ELSE next_run_at END,
 status = CASE WHEN kind <> 'periodic' AND $5 <> 'manual' AND status = 'active' THEN 'completed' ELSE status END
 WHERE id = $1`, e.TaskID, last, e.Status, cut(e.Message, MaxMessage), e.Trigger)
	return err
}

// CompleteAttempt implements repo.Engine.
func (d *DB) CompleteAttempt(ctx context.Context, c repo.Completion) (ok bool, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		e, err := scanExec(tx.QueryRow(ctx, `UPDATE scheduler_executions SET status = $3, message = $4, result = $5, result_truncated = $6,
 finished_at = $7, duration_ms = $8, lease_until = NULL, final = $9
 WHERE id = $1 AND status = 'running' AND lease_owner = $2 RETURNING `+execCols,
			c.ExecID, c.Owner, c.Status, cut(c.Message, MaxMessage), bytesOrNil(c.Result), c.ResultTruncated, c.FinishedAt, c.DurationMS, c.Retry == nil))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // fenced: another instance owns (or recovered) the attempt
		}
		if err != nil {
			return err
		}
		ok = true
		if c.Retry != nil {
			if err := insertExec(ctx, tx, *c.Retry, true); err != nil {
				return err
			}
		} else if err := finalise(ctx, tx, e); err != nil {
			return err
		}
		if c.Validity != "" {
			_, err := tx.Exec(ctx, `UPDATE scheduler_tasks SET validity = $2, validity_message = $3 WHERE id = $1`,
				e.TaskID, c.Validity, cut(c.ValidityMessage, MaxMessage))
			return err
		}
		return nil
	})
	return ok, mapErr(err)
}

// RecoverExpired implements repo.Engine.
func (d *DB) RecoverExpired(ctx context.Context, now time.Time, limit int, fn repo.Recovery) ([]store.Execution, error) {
	out := []store.Execution{}
	err := d.system(ctx, func(tx pgx.Tx) error {
		exp, err := queryExecs(ctx, tx, `SELECT `+execCols+` FROM scheduler_executions
 WHERE status = 'running' AND lease_until < $1 ORDER BY lease_until, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return err
		}
		for _, e := range exp {
			e.Status, e.Message, e.FinishedAt, e.LeaseUntil = store.ExecTimedOut, LostMessage, &now, nil
			if e.StartedAt != nil {
				e.DurationMS = now.Sub(*e.StartedAt).Milliseconds()
			}
			retry := fn(e)
			e.Final = retry == nil
			if _, err := tx.Exec(ctx, `UPDATE scheduler_executions SET status = 'timed_out', message = $2, finished_at = $3, duration_ms = $4,
 lease_until = NULL, final = $5 WHERE id = $1`, e.ID, LostMessage, now, e.DurationMS, e.Final); err != nil {
				return err
			}
			if retry != nil {
				if err := insertExec(ctx, tx, *retry, true); err != nil {
					return err
				}
			} else if err := finalise(ctx, tx, e); err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	return out, mapErr(err)
}

// PruneBefore implements repo.Engine.
func (d *DB) PruneBefore(ctx context.Context, t time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM scheduler_executions WHERE created_at < $1 AND status NOT IN ('queued','running')`, t)
		n = tag.RowsAffected()
		return err
	})
	return n, mapErr(err)
}

// ---------------------------------------------------------------- backup

// BackupTasks implements repo.Backup.
func (d *DB) BackupTasks(ctx context.Context, s repo.Scope) (out []store.Task, err error) {
	err = d.scoped(ctx, s, func(tx pgx.Tx) error {
		out, err = queryTasks(ctx, tx, `SELECT `+taskCols+` FROM scheduler_tasks ORDER BY id`)
		return err
	})
	return out, mapErr(err)
}

// BackupExecutions implements repo.Backup.
func (d *DB) BackupExecutions(ctx context.Context, s repo.Scope) (out []store.Execution, err error) {
	err = d.scoped(ctx, s, func(tx pgx.Tx) error {
		out, err = queryExecs(ctx, tx, `SELECT `+execCols+` FROM scheduler_executions ORDER BY created_at, id`)
		return err
	})
	return out, mapErr(err)
}

// ImportTask implements repo.Backup.
func (d *DB) ImportTask(ctx context.Context, t store.Task) (ok bool, err error) {
	err = d.St.Tx(ctx, store.Scope{TenantID: t.TenantID}, func(tx pgx.Tx) error {
		tag, err := insertTask(ctx, tx, t, true)
		ok = err == nil && tag.RowsAffected() == 1
		return err
	})
	return ok, mapErr(err)
}

// ImportExecution implements repo.Backup.
func (d *DB) ImportExecution(ctx context.Context, e store.Execution) (ok bool, err error) {
	err = d.St.Tx(ctx, store.Scope{TenantID: e.TenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO scheduler_executions (`+execCols+`)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24) ON CONFLICT DO NOTHING`,
			e.ID, e.TenantID, e.TaskID, e.TaskName, e.OccurrenceID, e.TypeName, e.Module, e.Trigger, e.TriggeredBy, e.OccurrenceAt,
			e.DueAt, e.Status, e.Attempt, e.MaxAttempts, e.StartedAt, e.FinishedAt, e.DurationMS, cut(e.Message, MaxMessage),
			bytesOrNil(e.Result), e.ResultTruncated, e.Final, "", nil, orNow(e.CreatedAt))
		ok = err == nil && tag.RowsAffected() == 1
		return err
	})
	return ok, mapErr(err)
}

// AppendAudit implements repo.Store.
func (d *DB) AppendAudit(ctx context.Context, row store.AuditRow) error {
	var detail map[string]any
	if row.Detail != nil {
		detail = row.Detail
	}
	return mapErr(d.system(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO scheduler_audit_events (id, tenant_id, at, actor_kind, actor_id, action, subject_kind, subject_id, outcome, reason, detail)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			row.ID, row.TenantID, orNow(row.At), row.ActorKind, row.ActorID, row.Action, row.SubjectKind, row.SubjectID, row.Outcome, row.Reason, detail)
		return err
	}))
}
