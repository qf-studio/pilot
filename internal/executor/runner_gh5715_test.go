package executor

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestHoldPushedBranch_RecordsPushResult (GH-5715) pins the structured push
// signal: holdPushedBranch records pushed / push failed / nothing pushed on the
// result, the lifecycle persists it as executions.branch_pushed (1 / 0 / NULL),
// and a failed push puts the one-line reason (not git's "To <url>" header) in
// the row error.
func TestHoldPushedBranch_RecordsPushResult(t *testing.T) {
	const branch = "pilot/LIN-5715-hold"

	tests := []struct {
		name        string
		useGit      bool
		breakOrigin bool
		want        *bool
		wantReason  string
	}{
		{name: "push ok", useGit: true, want: boolPtr(true)},
		{name: "push failed", useGit: true, breakOrigin: true, want: boolPtr(false), wantReason: "branch push also failed: "},
		{name: "direct-commit mode: no push attempted", useGit: false, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, _ := setupFreshnessRepo(t)
			runGit(t, dir, "checkout", "-b", branch)
			writeUncommittedFile(t, dir, "held.go")
			runGit(t, dir, "add", "held.go")
			runGit(t, dir, "commit", "-m", "held work")
			if tt.breakOrigin {
				runGit(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable-remote"))
			}

			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			lc := NewExecutionLifecycle(store)

			task := &Task{ID: "LIN-5715", ProjectPath: dir, Branch: branch, SourceAdapter: "linear"}
			execID, err := lc.Begin(task, ExecStatusRunning, 0)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}

			var git *GitOperations
			if tt.useGit {
				git = NewGitOperations(dir)
			}
			result := &ExecutionResult{TaskID: task.ID}
			NewRunner().holdPushedBranch(context.Background(), task, git, result, slog.Default(), "timeout salvage", "", nil)

			if result.Outcome != "needs_human" {
				t.Fatalf("Outcome = %q, want needs_human", result.Outcome)
			}
			if _, err := lc.Finish(execID, result, nil, 0, ExecStatusNeedsHuman); err != nil {
				t.Fatalf("Finish: %v", err)
			}

			row, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			switch {
			case tt.want == nil && row.BranchPushed != nil:
				t.Errorf("branch_pushed = %v, want NULL", *row.BranchPushed)
			case tt.want != nil && (row.BranchPushed == nil || *row.BranchPushed != *tt.want):
				t.Errorf("branch_pushed = %v, want %v", row.BranchPushed, *tt.want)
			}
			if tt.wantReason == "" {
				if strings.Contains(row.Error, "push also failed") {
					t.Errorf("row error = %q, want no push-failure text", row.Error)
				}
				return
			}
			if !strings.Contains(row.Error, tt.wantReason) {
				t.Fatalf("row error = %q, want it to contain %q", row.Error, tt.wantReason)
			}
			if strings.Contains(row.Error, "\n") || strings.Contains(row.Error, "failed to push:") {
				t.Errorf("row error = %q, want the one-line git reason only", row.Error)
			}
		})
	}
}

func TestPushFailureReason(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			"bang line wins over the To header",
			"failed to push: exit status 1: To https://github.com/o/r.git\n ! [remote rejected] b -> b (protected branch hook declined)\nerror: failed to push some refs",
			"! [remote rejected] b -> b (protected branch hook declined)",
		},
		{
			"denied",
			"failed to push: exit status 128: To https://github.com/o/r.git\nremote: Permission to o/r.git denied to bot.",
			"remote: Permission to o/r.git denied to bot.",
		},
		{
			"error: line",
			"failed to push: exit status 1: hint: updates were rejected",
			"failed to push: exit status 1: hint: updates were rejected",
		},
		{
			"no match falls back to the last non-empty line",
			"failed to push: exit status 1: To https://x\nsomething odd happened\n\n",
			"something odd happened",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PushFailureReason(errors.New(tt.in)); got != tt.want {
				t.Errorf("PushFailureReason = %q, want %q", got, tt.want)
			}
		})
	}
}
