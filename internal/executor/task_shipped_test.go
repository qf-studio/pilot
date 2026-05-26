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
			name: "completed with commit_sha",
			row:  memory.Execution{Status: "completed", CommitSHA: "abc123"},
			want: true,
		},
		{
			name: "completed with pr_url",
			row:  memory.Execution{Status: "completed", PRUrl: "https://github.com/x/y/pull/1"},
			want: true,
		},
		{
			name: "completed with both commit_sha and pr_url",
			row:  memory.Execution{Status: "completed", CommitSHA: "abc123", PRUrl: "https://github.com/x/y/pull/1"},
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
			name: "completed with error and commit_sha (orphan recovery)",
			row:  memory.Execution{Status: "completed", Error: "stale running task recovered", CommitSHA: "abc123"},
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
