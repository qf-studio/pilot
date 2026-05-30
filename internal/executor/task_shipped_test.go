package executor

import (
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestIsTaskShipped(t *testing.T) {
	tests := []struct {
		name string
		row  memory.Execution
		want bool
	}{
		{
			name: "completed with pr_url (primary signal)",
			row:  memory.Execution{Status: "completed", PRUrl: "https://github.com/x/y/pull/1"},
			want: true,
		},
		{
			name: "completed with both commit_sha and pr_url",
			row:  memory.Execution{Status: "completed", CommitSHA: "abc123", PRUrl: "https://github.com/x/y/pull/1"},
			want: true,
		},
		{
			name: "completed with commit_sha only and no error (backwards-compat direct-commit path)",
			row:  memory.Execution{Status: "completed", CommitSHA: "abc123"},
			want: true,
		},
		{
			name: "completed but no deliverables (epic-parent false-positive pattern, TASK-296)",
			row:  memory.Execution{Status: "completed"},
			want: false,
		},
		{
			name: "running with commit_sha",
			row:  memory.Execution{Status: "running", CommitSHA: "abc123"},
			want: false,
		},
		{
			name: "failed with pr_url",
			row:  memory.Execution{Status: "failed", PRUrl: "https://github.com/x/y/pull/1"},
			want: false,
		},
		{
			name: "queued, no deliverables",
			row:  memory.Execution{Status: "queued"},
			want: false,
		},
		{
			name: "empty row",
			row:  memory.Execution{},
			want: false,
		},
		{
			// GH-3126: orphan-recovery rows have a non-empty Error field.
			// IsTaskShipped now requires Error=="" for CommitSHA-only trust — consistent with
			// HasCompletedExecution SQL which also excludes error!='' rows. Previously these
			// two sites diverged; now both return false, eliminating the known divergence.
			name: "completed with error and commit_sha but no pr_url (orphan recovery — not shipped)",
			row:  memory.Execution{Status: "completed", Error: "stale running task recovered", CommitSHA: "abc123"},
			want: false,
		},
		{
			// TASK-334: rows with pr_url AND a non-empty error are NOT shipped.
			// HasCompletedExecution SQL excludes error!='' unconditionally; IsTaskShipped must
			// agree to prevent invariant divergence. The error may indicate a partial failure
			// (e.g. comment failed after PR creation), but the cross-site invariant takes
			// precedence — callers that need finer-grained distinction must query pr_url directly.
			name: "completed with error and pr_url (error present — not shipped, TASK-334)",
			row:  memory.Execution{Status: "completed", Error: "comment failed", PRUrl: "https://github.com/x/y/pull/2"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTaskShipped(tc.row)
			if got != tc.want {
				t.Errorf("IsTaskShipped(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}
