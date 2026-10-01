package httpapi

import (
	"net/http"
	"time"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

func queryTime(r *http.Request, name string) *time.Time {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil // the OpenAPI validation already refused malformed values
	}
	return &t
}

// registerExecutions mounts the history routes.
func (s *Server) registerExecutions(svc *tasks.Service) {
	s.withSubject("GET", Prefix+"/executions", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		req, ok := parseList(w, r, store.ExecutionList)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := store.ExecFilter{TaskID: q.Get("task_id"), Statuses: q["status"], Trigger: q.Get("trigger"),
			From: queryTime(r, "from"), To: queryTime(r, "to"), List: req}
		if b := queryBool(r, "failed_only"); b != nil {
			f.FailedOnly = *b
		}
		page, err := svc.Executions(r.Context(), subj, f)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})
	s.withSubject("GET", Prefix+"/executions/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		v, err := svc.Execution(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
}
