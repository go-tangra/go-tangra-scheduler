package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/stream"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// Prefix of the browser API.
const Prefix = "/api/scheduler/v1"

// Deps wire the HTTP handlers. A route whose service is not wired answers 501
// not_implemented.
type Deps struct {
	Hub            *stream.Hub
	Health         func() map[string]string // component status for /health
	Registry       *registry.Registry       // task types, module retirement
	Tasks          *tasks.Service           // tasks, control, history, overview, cron preview
	Backup         *backup.Service          // export / import
	MaxBackupBytes int64                    // import body bound (limits_scheduler.max_backup_bytes)
}

// Register mounts the handlers of every wired dependency.
func (s *Server) Register(d Deps) {
	s.MustHandle("GET", Prefix+"/health", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"status": "ok"}
		if d.Health != nil {
			comps := d.Health()
			for _, v := range comps {
				if v != "ok" {
					out["status"] = "degraded"
				}
			}
			out["components"] = comps
		}
		WriteJSON(w, http.StatusOK, out)
	})
	if d.Hub != nil {
		s.RegisterStream(d.Hub)
	}
	if d.Registry != nil {
		s.registerTypes(d.Registry)
	}
	if d.Tasks != nil {
		s.registerCron(d.Tasks)
		s.registerTasks(d.Tasks)
		s.registerControl(d.Tasks)
		s.registerExecutions(d.Tasks)
		s.registerOverview(d.Tasks)
	}
	if d.Backup != nil {
		s.registerBackup(d.Backup, d.MaxBackupBytes)
	}
}

// withSubject wraps a handler needing the verified caller.
func (s *Server) withSubject(method, path string, fn func(w http.ResponseWriter, r *http.Request, subj authz.Subjects)) {
	s.MustHandle(method, path, func(w http.ResponseWriter, r *http.Request) {
		subj, err := Subjects(r)
		if err != nil {
			Fail(w, r, nil, err)
			return
		}
		fn(w, r, subj)
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) { Fail(w, r, s.log, err) }

func queryInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return n
}

func queryBool(r *http.Request, name string) *bool {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil
	}
	b := v == "true" || v == "1"
	return &b
}

// parseList reads the list contract parameters (page, page_size, sort, order;
// go-tangra specs/032-server-side-tables) against spec. An invalid value is
// answered with validation_failed naming the parameter (never its value).
func parseList(w http.ResponseWriter, r *http.Request, spec listquery.Spec) (listquery.Request, bool) {
	req, err := listquery.Parse(r.URL.Query(), spec)
	var le *listquery.Error
	if errors.As(err, &le) {
		WriteDetail(w, ErrValidation.Status, ErrValidation.Reason, map[string]any{"param": le.Param})
		return req, false
	}
	return req, true
}
