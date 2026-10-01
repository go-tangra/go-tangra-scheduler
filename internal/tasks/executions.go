package tasks

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// ExecView is an attempt as the API returns it (contracts: Execution). Result
// is set only for a single attempt: the module's JSON, or a string when the
// data is not JSON.
type ExecView struct {
	store.Execution
	Result any `json:"result,omitempty"`
}

// ResultValue renders stored result bytes for the API.
func ResultValue(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return string([]rune(string(b))) // invalid UTF-8 replaced, never raw bytes
}

// ExecPage is one page of history: the list contract plus the summary counts.
type ExecPage struct {
	listquery.Page[ExecView]
	Counts store.ExecCounts `json:"counts"`
}

// Executions lists visible attempts (store.ExecutionList order, newest first by
// default; results omitted).
func (s *Service) Executions(ctx context.Context, subj authz.Subjects, f store.ExecFilter) (ExecPage, error) {
	f.List = s.pageRequest(f.List, store.ExecutionList)
	items, total, counts, err := s.st.ListExecutions(ctx, Scope(subj), f)
	if err != nil {
		return ExecPage{}, err
	}
	views := make([]ExecView, 0, len(items))
	for _, e := range items {
		views = append(views, ExecView{Execution: e})
	}
	return ExecPage{Page: listquery.NewPage(views, int(total), f.List.Clamp(int(total))), Counts: counts}, nil
}

// Execution returns one visible attempt with its message and result.
func (s *Service) Execution(ctx context.Context, subj authz.Subjects, id string) (ExecView, error) {
	e, err := s.st.GetExecution(ctx, Scope(subj), id)
	if err != nil {
		return ExecView{}, err
	}
	return ExecView{Execution: e, Result: ResultValue(e.Result)}, nil
}
