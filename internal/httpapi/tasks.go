package httpapi

import (
	"context"
	"net/http"
	"path"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// registerTasks mounts the task CRUD routes. Route permissions are enforced
// by the authorize middleware from the OpenAPI document; the service applies
// the tenant / platform scope rules.
func (s *Server) registerTasks(svc *tasks.Service) {
	s.withSubject("GET", Prefix+"/tasks", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		q := r.URL.Query()
		f := store.TaskFilter{Kind: q.Get("kind"), State: q.Get("state"), Validity: q.Get("validity"), Type: q.Get("type"),
			Query: q.Get("q"), TenantID: q.Get("tenant_id"), Page: queryInt(r, "page"), PageSize: queryInt(r, "page_size")}
		items, total, err := svc.List(r.Context(), subj, f)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
	})
	s.withSubject("POST", Prefix+"/tasks", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tasks.CreateInput
		if err := DecodeJSON(r, &in, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		v, err := svc.Create(r.Context(), subj, in)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusCreated, v)
	})
	s.withSubject("GET", Prefix+"/tasks/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		v, err := svc.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.withSubject("PUT", Prefix+"/tasks/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in tasks.UpdateInput
		if err := DecodeJSON(r, &in, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		v, err := svc.Update(r.Context(), subj, r.PathValue("id"), in)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.withSubject("DELETE", Prefix+"/tasks/{id}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		if err := svc.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			s.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// registerControl mounts start/stop/restart/run/cancel and the bulk actions.
func (s *Server) registerControl(svc *tasks.Service) {
	for action, call := range map[string]func(context.Context, authz.Subjects, string) (tasks.View, error){
		"start": svc.Start, "stop": svc.Stop, "restart": svc.Restart, "cancel": svc.Cancel,
	} {
		call := call
		s.withSubject("POST", Prefix+"/tasks/{id}/"+action, func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
			v, err := call(r.Context(), subj, r.PathValue("id"))
			if err != nil {
				s.fail(w, r, err)
				return
			}
			WriteJSON(w, http.StatusOK, v)
		})
	}
	s.withSubject("POST", Prefix+"/tasks/{id}/run", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		id, err := svc.Run(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]string{"execution_id": id})
	})
	s.withSubject("POST", Prefix+"/tasks/bulk/{action}", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		n, err := svc.Bulk(r.Context(), subj, path.Base(r.URL.Path)) // literal patterns (enum expansion)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]int{"affected": n})
	})
}
