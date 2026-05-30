package executor

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/memory"
)

// IsTaskShipped reports whether an execution row represents real shipped work.
// PRUrl is the primary signal — it proves a PR was opened against the remote, but only
// when the row has no error (mirrors the HasCompletedExecution SQL which excludes error!=''
// rows unconditionally, preventing the two sites from diverging on the {pr_url, error} case).
// CommitSHA alone is accepted for backwards-compat (direct-commit workflows) but
// logs a warning because it can be a parent SHA if the ghost-SHA guard was bypassed.
// Epic-parent rows (status=completed, no deliverable) correctly return false.
func IsTaskShipped(row memory.Execution) bool {
	if row.Status != "completed" {
		return false
	}
	if row.PRUrl != "" && row.Error == "" {
		return true
	}
	if row.CommitSHA != "" && row.Error == "" {
		slog.Warn("IsTaskShipped: trusting CommitSHA without PRUrl — verify freshness",
			"sha", row.CommitSHA)
		return true
	}
	return false
}
