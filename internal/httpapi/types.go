package httpapi

import (
	"net/http"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// registerTypes mounts the type catalog and module retirement.
func (s *Server) registerTypes(reg *registry.Registry) {
	s.withSubject("GET", Prefix+"/task-types", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		items, err := reg.List(r.Context(), subj, store.TypeFilter{Module: r.URL.Query().Get("module"), Available: queryBool(r, "available")})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	})
	s.withSubject("POST", Prefix+"/modules/{module}/unregister", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		n, err := reg.Retire(r.Context(), subj, r.PathValue("module"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"affected": n})
	})
}
