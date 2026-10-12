//go:build !linux && !darwin

package workspace

import (
	"context"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

func (w *Workspace) snapshotConditional(context.Context, string, int64) (Snapshot, error) {
	return Snapshot{}, conditionalError(model.CodeUnavailable, "unsupported_platform", nil)
}
func (w *Workspace) applyConditional(_ context.Context, r Receipt, _ *string) (Receipt, error) {
	return r, conditionalError(model.CodeUnavailable, "unsupported_platform", nil)
}
func (w *Workspace) recoverConditional(_ context.Context, r Receipt, _ *string) (Receipt, error) {
	return r, conditionalError(model.CodeUnavailable, "unsupported_platform", nil)
}
func (w *Workspace) finalizeConditional(context.Context, string, string) error {
	return conditionalError(model.CodeUnavailable, "unsupported_platform", nil)
}
