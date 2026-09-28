package httpapi

import (
	"net/http"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// registerOverview mounts the US6 overview of the caller's scope.
func (s *Server) registerOverview(svc *tasks.Service) {
	s.withSubject("GET", Prefix+"/overview", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		ov, err := svc.Overview(r.Context(), subj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, ov)
	})
}
