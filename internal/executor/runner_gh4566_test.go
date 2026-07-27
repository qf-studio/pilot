package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFinalizeEpicBranchPR_StaleLocalMainDoesNotFalsePositive is the
// end-to-end GH-4566 acceptance test: an epic whose umbrella branch was cut
// straight from origin/main (zero commits of its own) — because its children
// shipped their own PRs — must take the clean skip/no-op path even when this
// clone's local `main` ref is stale relative to origin/main. Before the fix,
// finalizeEpicBranchPR's guard compared against the stale local `main` ref,
// saw the sibling children's merged commits as if they belonged to the parent
// branch, and fell through to push+CreatePR — which GitHub then rejected with
// "No commits between main and pilot/GH-NNN", misclassified as an infra
// failure on an epic whose work fully shipped (auth-service GH-431/GH-435,
// 2026-07-26).
func TestFinalizeEpicBranchPR_StaleLocalMainDoesNotFalsePositive(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Children's PRs merging to origin/main while this clone's local `main`
	// never fast-forwards (TASK-402: independent-sibling dispatch never runs
	// syncMainBranch mid-epic).
	pushExtraCommitFromSecondClone(t, origin, "main", "child #472 merged")
	pushExtraCommitFromSecondClone(t, origin, "main", "child #474 merged")

	// Cut the epic's umbrella branch straight from origin/main, exactly as
	// worktree.go does — local `main` is deliberately left behind.
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4566-epic", "origin/main")

	// Sanity: prove this reproduces the bug precondition — the stale local
	// `main` comparison the old guard used is nonzero.
	git := NewGitOperations(local)
	staleCount, err := git.CountNewCommits(context.Background(), "main")
	if err != nil {
		t.Fatalf("CountNewCommits: %v", err)
	}
	if staleCount == 0 {
		t.Fatalf("test setup invalid: expected stale local `main` comparison to be nonzero (repro precondition), got 0")
	}

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-4566", Success: true, IsEpic: true}
	task := &Task{ID: "GH-4566", Title: "epic", Description: "d", Branch: "pilot/GH-4566-epic", BaseBranch: "main", CreatePR: true}
	childStates := []string{"completed", "completed"}

	r.finalizeEpicBranchPR(context.Background(), task, git, result, childStates)

	if !result.Success {
		t.Errorf("expected Success=true (clean skip, children shipped), got false, error=%q", result.Error)
	}
	if result.Outcome != "" {
		t.Errorf("Outcome = %q, want empty (completed)", result.Outcome)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR created — the guard must skip push+CreatePR entirely, got %q", result.PRUrl)
	}
	if result.CommitSHA != "" {
		t.Errorf("expected no harvested SHA when the guard skips, got %q", result.CommitSHA)
	}

	// No push should have happened: the epic branch never reached origin.
	checkCmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/pilot/GH-4566-epic")
	checkCmd.Dir = origin
	if err := checkCmd.Run(); err == nil {
		t.Errorf("epic branch must not have been pushed to origin")
	}
}

// TestFinalizeEpicBranchPR_StaleLocalMainAllChildrenNoOp is the no_op-outcome
// counterpart: when every child no-op'd (nothing shipped anywhere), the
// stale-local-main guard must still classify the parent as no_op, not
// silently as completed.
func TestFinalizeEpicBranchPR_StaleLocalMainAllChildrenNoOp(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	pushExtraCommitFromSecondClone(t, origin, "main", "unrelated main-branch commit")

	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4566-noop", "origin/main")

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-4566", Success: true, IsEpic: true}
	task := &Task{ID: "GH-4566", Title: "epic", Description: "d", Branch: "pilot/GH-4566-noop", BaseBranch: "main", CreatePR: true}
	childStates := []string{"no_op", "no_op"}

	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(local), result, childStates)

	if result.Success {
		t.Errorf("expected Success=false for all-no_op children, got true")
	}
	if result.Outcome != "no_op" {
		t.Errorf("Outcome = %q, want no_op", result.Outcome)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR created, got %q", result.PRUrl)
	}
	if !containsAny(result.Error, noOpErrorSignatures) {
		t.Errorf("Error = %q does not match any recognized no-op signature; would be misclassified as a generic failure", result.Error)
	}
}

// writeFakeGhPRCreateNoCommitsBetween writes a fake "gh" binary whose `pr
// create` fails exactly the way GitHub's GraphQL API does when the branch
// truly carries no commits relative to base: "No commits between <base> and
// <branch> (createPullRequest)". Used to drive the GH-4566 backstop
// classification at finalizeEpicBranchPR's CreatePR-failure exit.
func writeFakeGhPRCreateNoCommitsBetween(t *testing.T, fakeBin string) {
	t.Helper()
	script := `#!/bin/sh
case "$*" in
  *"pr list"*) echo "[]" ;;
  *"pr create"*) echo "GraphQL: No commits between main and pilot/GH-431 (createPullRequest)" >&2; exit 1 ;;
  *) echo "[]" ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

// TestFinalizeEpicBranchPR_NoCommitsBetweenBackstop is the GH-4566 backstop
// acceptance test: if CreatePR itself still returns GitHub's "No commits
// between" error (the origin-relative guard should have caught this first,
// but this is defense in depth for any way it's wrong), the parent must be
// classified the same way the guard classifies an empty branch — off the
// children's terminal states — instead of a generic "infra" failure, and
// TerminalStatus must not read as a genuine failure (no failure alert).
func TestFinalizeEpicBranchPR_NoCommitsBetweenBackstop(t *testing.T) {
	tests := []struct {
		name        string
		slug        string
		childStates []string
		wantSuccess bool
		wantOutcome string
	}{
		{
			name:        "children shipped: classified completed, no infra failure",
			slug:        "shipped",
			childStates: []string{"completed", "completed"},
			wantSuccess: true,
			wantOutcome: "",
		},
		{
			name:        "all children no_op: classified no_op, no infra failure",
			slug:        "noop",
			childStates: []string{"no_op", "no_op"},
			wantSuccess: false,
			wantOutcome: "no_op",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeBin := t.TempDir()
			writeFakeGhPRCreateNoCommitsBetween(t, fakeBin)
			t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

			branch := "pilot/GH-431-backstop-" + tt.slug
			dir := initRepoWithRemoteAndFeatureBranch(t, branch)

			r := newSilentRunnerTask359()
			result := &ExecutionResult{TaskID: "GH-431", Success: true, IsEpic: true}
			task := &Task{ID: "GH-431", Title: "feat: epic work", Description: "d", Branch: branch, BaseBranch: "main", CreatePR: true}

			r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, tt.childStates)

			if result.Success != tt.wantSuccess {
				t.Errorf("Success = %v, want %v (error=%q)", result.Success, tt.wantSuccess, result.Error)
			}
			if result.Outcome != tt.wantOutcome {
				t.Errorf("Outcome = %q, want %q", result.Outcome, tt.wantOutcome)
			}
			if result.PRUrl != "" {
				t.Errorf("PRUrl = %q, want empty", result.PRUrl)
			}
			// The whole point of the backstop: this must never read as an
			// infra/generic failure downstream (that's what drove the false
			// failure alert on shipped epics).
			if status := TerminalStatus(result); status == "infra" || status == "failed" {
				t.Errorf("TerminalStatus = %q, want a non-failure classification (completed/no_op)", status)
			}
		})
	}
}

// TestFinalizeEpicBranchPR_GenericPRCreateFailureStillInfra proves the
// backstop is scoped narrowly to "No commits between" — any other PR-create
// failure (rate limit, transient GitHub error, ...) must still classify as a
// genuine failure exactly as before GH-4566.
func TestFinalizeEpicBranchPR_GenericPRCreateFailureStillInfra(t *testing.T) {
	setUpFakeGhPRCreateFailingPATH(t)
	dir := initRepoWithRemoteAndFeatureBranch(t, "pilot/GH-431-generic-fail")

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-431", Success: true, IsEpic: true}
	task := &Task{ID: "GH-431", Title: "feat: epic work", Description: "d", Branch: "pilot/GH-431-generic-fail", BaseBranch: "main", CreatePR: true}

	r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, []string{"completed"})

	if result.Success {
		t.Errorf("expected Success=false for a generic PR-create failure, got true")
	}
	if status := TerminalStatus(result); status != "infra" {
		t.Errorf("TerminalStatus = %q, want infra (unrelated PR-create failure must stay a genuine failure)", status)
	}
}
