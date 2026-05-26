package executor

import "github.com/qf-studio/pilot/internal/memory"

// IsTaskShipped reports whether an execution row represents real shipped work:
// status must be "completed" AND at least one deliverable (commit_sha or pr_url) must be set.
// Epic-parent rows (status=completed, no deliverable) correctly return false — sub-issues own the work.
// This predicate mirrors the SQL filter in HasCompletedExecution; the cross-site invariant test
// in internal/integration ensures both always agree.
func IsTaskShipped(row memory.Execution) bool {
	if row.Status != "completed" {
		return false
	}
	return row.CommitSHA != "" || row.PRUrl != ""
}
