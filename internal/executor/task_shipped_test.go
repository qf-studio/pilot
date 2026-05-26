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
			// A row with both pr_url and an error IS still considered shipped: the PR was created,
			// the error may be from a post-PR step (e.g. comment failed). PRUrl is the primary signal.
			name: "completed with error and pr_url (PR created despite error)",
			row:  memory.Execution{Status: "completed", Error: "comment failed", PRUrl: "https://github.com/x/y/pull/2"},
			want: true,
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
