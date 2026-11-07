package executor

import (
	"context"
	"log/slog"
)

// RearmBranchAvailable reports whether the branch a re-armed source issue's
// owner-death re-arm comment points at (GH-6183) still exists on origin, so the
// retry can continue it. When it is gone — swept, deleted with the PR, or never
// pushed — the retry falls back to the from-scratch path (a fresh worktree off
// the default branch) and says so loudly: the closed PR's commits are not
// recoverable by name then, and a silent rebuild from main is exactly the
// failure this guard exists to make visible.
//
// Decision: this deliberately ignores the footer's sha. A branch-less retry must
// not reach runner.go's escalateUnfetchableFixSHA (a recorded-but-unfetchable
// SHA parks the issue under pilot-needs-human); the spec for a missing re-arm
// branch is the plain from-main path, so the caller drops branch, PR and SHA
// together when this returns false.
func (g *GitOperations) RearmBranchAvailable(ctx context.Context, branch string) bool {
	if branch == "" {
		return false
	}
	if g.RemoteBranchExists(ctx, branch) {
		return true
	}
	slog.Warn("re-arm branch missing, starting from main", slog.String("branch", branch))
	return false
}
