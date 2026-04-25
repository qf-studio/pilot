package executor

import "strings"

// IsPermanentFailure reports whether an execution error represents a
// non-retriable, environmental or policy-level failure. Such failures will
// not improve on retry and should route the issue to LabelBlocked rather
// than LabelFailed (GH-2402).
//
// Categories considered permanent:
//   - cross-project execution violations (GH-386)
//   - team-permission denials (GH-634)
//   - pre-flight environment failures
//   - worktree creation / Navigator setup failures
//
// Rate-limit errors are explicitly NOT permanent — those route through the
// scheduler. A nil error is treated as non-permanent.
func IsPermanentFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// Rate-limit failures are transient — handled by the scheduler.
	if IsRateLimitError(msg) {
		return false
	}

	permanentMarkers := []string{
		"cross-project execution blocked",
		"permission check failed",
		"permission denied",
		"pre-flight check failed",
		"worktree creation failed",
		"navigator worktree setup failed",
	}
	for _, marker := range permanentMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
