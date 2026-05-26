package executor

import (
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestTaskCompletionInvariant verifies that IsTaskShipped (Go predicate) and
// HasCompletedExecution (SQL) always agree for a given Execution row.
// This is the cross-site invariant from TASK-296: a single source of truth for
// "is this task genuinely shipped?" prevents the GH-3080 / TASK-288 false-positive pattern
// where one site accepts a row the other would reject.
func TestTaskCompletionInvariant(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-invariant-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	rows := []struct {
		name string
		exec memory.Execution
		// wantShipped is the expected result of both IsTaskShipped and HasCompletedExecution.
		wantShipped bool
	}{
		{
			name:        "completed with commit_sha",
			exec:        memory.Execution{ID: "inv-1", TaskID: "GH-100", ProjectPath: "/proj", Status: "completed", CommitSHA: "abc"},
			wantShipped: true,
		},
		{
			name:        "completed with pr_url",
			exec:        memory.Execution{ID: "inv-2", TaskID: "GH-101", ProjectPath: "/proj", Status: "completed", PRUrl: "https://github.com/x/y/pull/1"},
			wantShipped: true,
		},
		{
			name:        "completed with both deliverables",
			exec:        memory.Execution{ID: "inv-3", TaskID: "GH-102", ProjectPath: "/proj", Status: "completed", CommitSHA: "def", PRUrl: "https://github.com/x/y/pull/2"},
			wantShipped: true,
		},
		{
			// Reproduces the GitNation GH-21/22/26 false-positive: epic-parent row with
			// status=completed, error='', commit_sha='', pr_url='', no deliverables.
			// Both IsTaskShipped and HasCompletedExecution must return false.
			name:        "completed no deliverables (epic-parent false-positive, GH-3080)",
			exec:        memory.Execution{ID: "inv-4", TaskID: "GH-21", ProjectPath: "/proj", Status: "completed"},
			wantShipped: false,
		},
		{
			name:        "failed with commit_sha",
			exec:        memory.Execution{ID: "inv-5", TaskID: "GH-103", ProjectPath: "/proj", Status: "failed", CommitSHA: "ghi"},
			wantShipped: false,
		},
		{
			name:        "running",
			exec:        memory.Execution{ID: "inv-6", TaskID: "GH-104", ProjectPath: "/proj", Status: "running"},
			wantShipped: false,
		},
		{
			// GH-3126: orphan-recovery rows are no longer a divergence between IsTaskShipped and
			// HasCompletedExecution. Both now return false for error!='' rows without a pr_url:
			// - HasCompletedExecution SQL excludes error!='' (GH-2315 guard)
			// - IsTaskShipped requires error=='' for CommitSHA-only trust (GH-3126 hardening)
			// This closes the ghost-SHA ghost-close variant where a parent SHA was accepted as proof
			// of shipped work.
			name:        "orphan-recovery: completed with error and commit_sha only (not shipped)",
			exec:        memory.Execution{ID: "inv-7", TaskID: "GH-999", ProjectPath: "/proj", Status: "completed", Error: "stale running task recovered", CommitSHA: "xyz"},
			wantShipped: false,
		},
	}

	for i, tc := range rows {
		if err := store.SaveExecution(&tc.exec); err != nil {
			t.Fatalf("row %d (%s): SaveExecution: %v", i, tc.name, err)
		}
	}

	// Invariant check: IsTaskShipped and HasCompletedExecution must agree on every row.
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			goResult := IsTaskShipped(tc.exec)
			sqlResult, err := store.HasCompletedExecution(tc.exec.TaskID, tc.exec.ProjectPath)
			if err != nil {
				t.Fatalf("HasCompletedExecution: %v", err)
			}

			if goResult != tc.wantShipped {
				t.Errorf("IsTaskShipped = %v, want %v", goResult, tc.wantShipped)
			}
			if sqlResult != tc.wantShipped {
				t.Errorf("HasCompletedExecution = %v, want %v", sqlResult, tc.wantShipped)
			}
			if goResult != sqlResult {
				t.Errorf("INVARIANT VIOLATED: IsTaskShipped=%v != HasCompletedExecution=%v for row %+v",
					goResult, sqlResult, tc.exec)
			}
		})
	}
}
