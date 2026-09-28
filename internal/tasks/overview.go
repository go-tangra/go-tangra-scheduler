package tasks

import (
	"context"

	"github.com/go-tangra/go-tangra-scheduler/v4/internal/authz"
	"github.com/go-tangra/go-tangra-scheduler/v4/internal/store"
)

// Overview returns the US6 figures of the caller's scope.
func (s *Service) Overview(ctx context.Context, subj authz.Subjects) (store.Overview, error) {
	return s.st.Overview(ctx, Scope(subj), s.Now())
}
