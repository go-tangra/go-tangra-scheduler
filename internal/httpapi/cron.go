package httpapi

import (
	"net/http"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// registerCron mounts the cron preview (the next run times, server-side).
func (s *Server) registerCron(svc *tasks.Service) {
	s.withSubject("GET", Prefix+"/cron/preview", func(w http.ResponseWriter, r *http.Request, _ authz.Subjects) {
		q := r.URL.Query()
		next, err := svc.Preview(q.Get("expression"), q.Get("timezone"), queryInt(r, "count"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"next": next})
	})
}
