package store

import "github.com/go-tangra/go-tangra/v4/listquery"

// List definitions of the scheduler tables (specs/032-server-side-tables in
// go-tangra). Sort fields map to constant SQL expressions only; the memstore
// sorts the same public names in Go.
var (
	// TaskList pages scheduler_tasks: name order by default (TaskIDs keeps it).
	TaskList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"name": {Expr: "name", Text: true},
			"type": {Expr: "type_name", Text: true},
			// state is derived from status and enabled exactly as Task.State().
			"state":       {Expr: "(CASE WHEN status = 'active' AND enabled THEN 'enabled' WHEN status = 'active' THEN 'stopped' ELSE status END)"},
			"next_run_at": {Expr: "next_run_at"},
			"updated_at":  {Expr: "updated_at", DefaultDir: listquery.Desc},
		},
		Default: "name", TieBreak: "id",
	}
	// ExecutionList pages scheduler_executions: newest first by default.
	ExecutionList = listquery.Spec{
		Fields: map[string]listquery.Field{
			"created_at": {Expr: "created_at", DefaultDir: listquery.Desc},
			"status":     {Expr: "status"},
			"duration":   {Expr: "duration_ms", DefaultDir: listquery.Desc},
			"trigger":    {Expr: "trigger"},
		},
		Default: "created_at", TieBreak: "id",
	}
)

// ListRequest completes r with the Spec's defaults (a zero Request from an
// internal caller pages with the defaults); an invalid hand-built Request
// falls back to the defaults entirely.
func ListRequest(r listquery.Request, s listquery.Spec) listquery.Request {
	out, err := listquery.New(r.Page, r.PageSize, r.Sort, r.Order, s)
	if err != nil {
		out, _ = listquery.New(0, 0, "", "", s)
	}
	return out
}
