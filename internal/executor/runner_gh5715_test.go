package executor

import (
	"context"
	"errors"
	"log/slog"
	"os"
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
			"fatal: line from a bad remote URL",
			"failed to push: exit status 128: fatal: '/nope' does not appear to be a git repository\nfatal: Could not read from remote repository.\n\nPlease make sure you have the correct access rights\nand the repository exists.",
			"fatal: '/nope' does not appear to be a git repository",
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

// TestPushAndCreatePRAfterTimeout_PushRejectedIsOneLine (GH-5720) drives the
// timeout-salvage push against a remote whose pre-receive hook rejects the
// push, so git's stderr is multi-line with the "To <url>" header first. The row
// error is what Linear's first-line reason shows: it must be one line and must
// not start with that header.
func TestPushAndCreatePRAfterTimeout_PushRejectedIsOneLine(t *testing.T) {
	const branch = "pilot/LIN-5720-salvage"
	dir, bare := setupFreshnessRepo(t)
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'protected branch hook declined' >&2\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test hook must be executable
		t.Fatalf("write hook: %v", err)
	}
	runGit(t, dir, "checkout", "-b", branch)
	writeUncommittedFile(t, dir, "held.go")
	runGit(t, dir, "add", "held.go")
	runGit(t, dir, "commit", "-m", "held work")

	task := &Task{ID: "LIN-5720", ProjectPath: dir, Branch: branch, SourceAdapter: "linear"}
	git := NewGitOperations(dir)
	if pushErr := git.Push(context.Background(), branch, "GH-5715"); pushErr == nil || !strings.Contains(pushErr.Error(), "\nTo ") && !strings.Contains(pushErr.Error(), ": To ") {
		t.Fatalf("precondition: raw push error = %v, want git's multi-line stderr with a To header", pushErr)
	}

	result := &ExecutionResult{TaskID: task.ID}
	NewRunner().pushAndCreatePRAfterTimeout(context.Background(), task, git, result, nil, slog.Default(), "", nil)

	if result.Outcome != "needs_human" {
		t.Fatalf("Outcome = %q, want needs_human", result.Outcome)
	}
	if strings.Contains(result.Error, "\n") {
		t.Errorf("row error = %q, want one line", result.Error)
	}
	first, _, _ := strings.Cut(result.Error, "\n")
	if strings.Contains(first, "To ") || strings.Contains(first, "failed to push:") {
		t.Errorf("first line of row error = %q, want no git To header / raw wrapper", first)
	}
	if !strings.Contains(result.Error, "declined") {
		t.Errorf("row error = %q, want the hook's rejection reason", result.Error)
	}
}

// TestFormatGitStepFailureWithRecovery_PushIsOneLine (GH-5720): a push step's
// multi-line stderr is reduced to the cause so the recovery tail survives the
// first-line cut; other steps keep their error verbatim.
func TestFormatGitStepFailureWithRecovery_PushIsOneLine(t *testing.T) {
	dir, _ := setupFreshnessRepo(t)
	git := NewGitOperations(dir)
	raw := errors.New("failed to push: exit status 1: To https://github.com/o/r.git\n ! [remote rejected] b -> b (hook declined)\nerror: failed to push some refs")

	got := formatGitStepFailureWithRecovery(context.Background(), git, "push", 3, raw, "GH-5720", "pilot/GH-5720", "abcdef1234")
	if strings.Contains(got, "\n") || strings.Contains(got, "To https") || !strings.Contains(got, "hook declined") || !strings.Contains(got, "recovery: branch=pilot/GH-5720") {
		t.Errorf("push step = %q, want one line with the cause and the recovery tail", got)
	}
	got = formatGitStepFailureWithRecovery(context.Background(), git, "pr-create", 3, errors.New("gh: boom"), "GH-5720", "pilot/GH-5720", "abcdef1234")
	if !strings.Contains(got, "pr-create failed after 3 attempt(s): gh: boom — recovery:") {
		t.Errorf("pr-create step = %q, want the error verbatim", got)
	}
}
