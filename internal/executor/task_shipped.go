package executor

import "github.com/qf-studio/pilot/internal/memory"

// IsTaskShipped reports whether an execution row represents real shipped work:
// status must be "completed" AND at least one deliverable (commit_sha or pr_url) must be set.
// Epic-parent rows (status=completed, no deliverable) correctly return false — sub-issues own the work.
// This predicate mirrors the SQL filter in HasCompletedExecution; the cross-site invariant test
// in internal/integration ensures both always agree.
//
// Variant 4 (ghost-SHA) risk: if CommitSHA is set but PRUrl is empty, the SHA may be a parent
// SHA that was never a new commit. The upstream ghost-SHA guard in runner.go (GH-3112) clears
// CommitSHA before recording, so a ghost SHA should never reach this predicate. PRUrl is the
// stronger signal when both are present.
func IsTaskShipped(row memory.Execution) bool {
	if row.Status != "completed" {
		return false
	}
	return row.CommitSHA != "" || row.PRUrl != ""
}
