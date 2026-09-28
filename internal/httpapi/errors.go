package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/backup"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/registry"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/repo"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/tasks"
)

// Error is a refusal with a stable reason from the closed vocabulary of
// contracts/scheduler-api.md (plus the platform envelope reasons).
type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string { return e.Reason }

// Refusals.
var (
	ErrUnauthenticated = &Error{http.StatusUnauthorized, "unauthenticated"}
	ErrForbidden       = &Error{http.StatusForbidden, "forbidden"}
	ErrNotFound        = &Error{http.StatusNotFound, "not_found"}
	ErrMalformed       = &Error{http.StatusBadRequest, "malformed_body"}
	ErrValidation      = &Error{http.StatusUnprocessableEntity, "validation_failed"}
	ErrConflict        = &Error{http.StatusConflict, "conflict"}
	ErrBodyTooLarge    = &Error{http.StatusRequestEntityTooLarge, "body_too_large"}
	ErrRateLimited     = &Error{http.StatusTooManyRequests, "rate_limited"}
	ErrUnavailable     = &Error{http.StatusServiceUnavailable, "temporarily_unavailable"}
	ErrNotImplemented  = &Error{http.StatusNotImplemented, "not_implemented"}
	ErrInvalidBackup   = &Error{http.StatusUnprocessableEntity, "invalid_backup"}
)

// reasonStatus maps the task service's reasons to HTTP statuses.
var reasonStatus = map[string]int{
	tasks.ReasonInvalidPayload:  http.StatusUnprocessableEntity,
	tasks.ReasonPayloadTooLarge: http.StatusUnprocessableEntity,
	tasks.ReasonPayloadTooDeep:  http.StatusUnprocessableEntity,
	tasks.ReasonInvalidCron:     http.StatusUnprocessableEntity,
	tasks.ReasonCronTooFrequent: http.StatusUnprocessableEntity,
	tasks.ReasonInvalidTimezone: http.StatusUnprocessableEntity,
	tasks.ReasonInvalidOptions:  http.StatusUnprocessableEntity,
	tasks.ReasonInvalidRunAt:    http.StatusUnprocessableEntity,
	tasks.ReasonTypeUnavailable: http.StatusUnprocessableEntity,
	tasks.ReasonValidation:      http.StatusUnprocessableEntity,
	tasks.ReasonNameTaken:       http.StatusConflict,
	tasks.ReasonNotPeriodic:     http.StatusConflict,
	tasks.ReasonRunInProgress:   http.StatusConflict,
	tasks.ReasonNotCancellable:  http.StatusConflict,
	tasks.ReasonTaskLimit:       http.StatusConflict,
}

// MaxBodyBytes bounds JSON bodies of ordinary operations.
const MaxBodyBytes = 256 << 10

// WriteJSON encodes v with status; API responses are never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError emits {"reason": ...} and nothing else.
func WriteError(w http.ResponseWriter, status int, reason string) {
	WriteJSON(w, status, map[string]string{"reason": reason})
}

// WriteDetail emits {"reason": ..., "detail": {...}}; detail values are never
// payload values or secrets.
func WriteDetail(w http.ResponseWriter, status int, reason string, detail map[string]any) {
	if detail == nil {
		WriteError(w, status, reason)
		return
	}
	WriteJSON(w, status, map[string]any{"reason": reason, "detail": detail})
}

// Fail maps err to a response: *Error verbatim, domain refusals with their
// detail, store and authz sentinels to their reasons, anything else to 503
// (details logged only, never returned).
func Fail(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var te *tasks.Error
	if errors.As(err, &te) {
		status, ok := reasonStatus[te.Reason]
		if !ok {
			status = http.StatusUnprocessableEntity
		}
		WriteDetail(w, status, te.Reason, te.Detail)
		return
	}
	status, reason := Status(err)
	if status >= 500 && log != nil {
		log.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "request_id", RequestID(r), "err", err)
	}
	WriteError(w, status, reason)
}

// Status maps an error to its status and reason.
func Status(err error) (int, string) {
	var e *Error
	var mbe *http.MaxBytesError
	var re *registry.Error
	switch {
	case errors.As(err, &e):
		return e.Status, e.Reason
	case errors.As(err, &mbe):
		return ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason
	case errors.Is(err, authz.ErrForbidden):
		return ErrForbidden.Status, ErrForbidden.Reason
	case errors.Is(err, repo.ErrNotFound):
		return ErrNotFound.Status, ErrNotFound.Reason
	case errors.Is(err, repo.ErrConflict), errors.Is(err, repo.ErrLimit):
		return ErrConflict.Status, ErrConflict.Reason
	case errors.Is(err, backup.ErrInvalid):
		return ErrInvalidBackup.Status, ErrInvalidBackup.Reason
	case errors.As(err, &re):
		return ErrValidation.Status, ErrValidation.Reason
	}
	return ErrUnavailable.Status, ErrUnavailable.Reason
}

// DecodeJSON reads a bounded JSON body into v, refusing unknown fields and
// trailing data. limit <= 0 means MaxBodyBytes.
func DecodeJSON(r *http.Request, v any, limit int64) error {
	if limit <= 0 {
		limit = MaxBodyBytes
	}
	body := http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return ErrBodyTooLarge
		}
		return ErrMalformed
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrMalformed
	}
	return nil
}
