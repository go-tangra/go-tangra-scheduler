package httpapi

import (
	"io"
	"net/http"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
)

// registerBackup mounts export/import (backup:manage).
func (s *Server) registerBackup(svc *backup.Service, maxBytes int64) {
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	s.withSubject("POST", Prefix+"/backup/export", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		var in struct {
			All bool `json:"all"`
		}
		if r.ContentLength != 0 {
			if err := DecodeJSON(r, &in, 0); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		doc, err := svc.Export(r.Context(), subj, in.All)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="scheduler-backup.json"`)
		WriteJSON(w, http.StatusOK, doc)
	})
	s.withSubject("POST", Prefix+"/backup/import", func(w http.ResponseWriter, r *http.Request, subj authz.Subjects) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
		if err != nil {
			s.fail(w, r, ErrBodyTooLarge)
			return
		}
		doc, err := backup.Decode(raw)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		res, err := svc.Import(r.Context(), subj, doc)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})
}
