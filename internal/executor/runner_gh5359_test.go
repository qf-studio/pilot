package executor

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// setupPRGuardRepoNoLocalBase mirrors setupPRGuardRepo (runner_test.go) but
// additionally deletes the local "main" ref after checking out branch and
// configures no origin remote, so that both origin/<base> and the local
// <base> ref are unresolvable from the branch's worktree — reproducing the
// GH-5359 incident shape verbatim: git merge-base for "main" against
// origin/main (and local main) fails even though the branch itself has real
// commits and/or an already-opened PR.
func setupPRGuardRepoNoLocalBase(t *testing.T, branch string, addCommit bool) string {
	t.Helper()
	dir := setupPRGuardRepo(t, branch, addCommit)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Branch is already checked out by setupPRGuardRepo; deleting "main"
	// (a different branch than the one currently checked out) is safe.
	run("branch", "-D", "main")
	return dir
}

// TestRunner_PRGuard_MergeBaseFailure_OpenPRExists_EndsCompleted is GH-5359's
// primary regression test: a worktree where merge-base against origin/<base>
// (and local <base>) cannot be resolved, but the branch has a real commit and
// an open PR already exists on GitHub. Before this fix, CountNewCommitsAgainstOrigin
// would return an error (or, in other environments, a false zero), the direct
// PR guard would immediately record `no_changes` (PR guard) — recording a
// run that already shipped a PR as a no_op that the dispatcher then re-picks.
// See GH-5351 run 1 / PR #5356 for the exact production incident this
// reproduces.
func TestRunner_PRGuard_MergeBaseFailure_OpenPRExists_EndsCompleted(t *testing.T) {
	const branch = "pilot/GH-5359-a"
	dir := setupPRGuardRepoNoLocalBase(t, branch, true) // one commit on branch, no local "main" ref

	setUpFakeGhPATH(t,
		[]byte(`[]`), // --state merged: nothing merged
		[]byte(`[{"url":"https://github.com/o/r/pull/5356"}]`), // --state open: PR already exists
	)

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "implementation complete"},
	}
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	task := &Task{
		ID:          "GH-5359-a",
		Title:       "fix(executor): merge-base failure with existing PR",
		Description: "Verify a merge-base failure doesn't clobber a real PR into no_op",
		ProjectPath: dir,
		Branch:      branch,
		CreatePR:    true,
	}

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Expected success (existing PR adopted), got failure: %q", result.Error)
	}
	if got := TerminalStatus(result); got != "completed" {
		t.Errorf("TerminalStatus = %q, want %q", got, "completed")
	}
	if strings.HasPrefix(result.Error, "no_changes:") {
		t.Errorf("Guard incorrectly reported no_changes despite existing commit+PR: %q", result.Error)
	}
	if result.PRUrl != "https://github.com/o/r/pull/5356" {
		t.Errorf("result.PRUrl = %q, want the existing open PR", result.PRUrl)
	}
}

// TestRunner_PRGuard_GenuinelyEmptyBranch_StillNoOp is GH-5359's regression
// test for the baseline no_op case: origin/<base> is absent (no remote
// configured at all, same as setupPRGuardRepo's default), but the local
// "main" ref is present and resolvable, and the branch truly has zero
// commits. Confirms the new GH-5359 fallback logic (which now intercepts
// every guardCount==0 result, not just guard errors) doesn't regress the
// ordinary no_op path — merge-base resolves cleanly via the local "main"
// fallback, so resolveEmptyBranchWithFallback's very first check
// short-circuits back to "confirmed empty" without ever shelling out to gh.
func TestRunner_PRGuard_GenuinelyEmptyBranch_StillNoOp(t *testing.T) {
	const branch = "pilot/GH-5359-b"
	dir := setupPRGuardRepo(t, branch, false) // no additional commits; local "main" left intact

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "analysis complete"},
	}
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	task := &Task{
		ID:          "GH-5359-b",
		Title:       "fix(executor): genuinely empty branch still no_op",
		Description: "Verify the GH-5359 fallback doesn't regress the ordinary confirmed-empty case",
		ProjectPath: dir,
		Branch:      branch,
		CreatePR:    true,
	}

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Success {
		t.Error("Expected failure (genuinely empty branch), got success")
	}
	if !strings.HasPrefix(result.Error, "no_changes:") {
		t.Errorf("Expected no_changes error, got: %q", result.Error)
	}
	if got := TerminalStatus(result); got != "no_op" {
		t.Errorf("TerminalStatus = %q, want %q", got, "no_op")
	}
}

// TestRunner_PRGuard_MergeBaseFailure_NoPRFound_FailsClosedNotNoOp is GH-5359's
// fail-closed regression test: when merge-base against origin/<base> AND the
// local <base> ref are BOTH unresolvable (so there is no git ref at all to
// count commits against) and no PR exists for the branch either, the guard
// cannot actually confirm the branch is empty — there is no base to diff
// against. Mirroring the GH-5342 precedent ("a count failure is not evidence
// of zero commits"), this must surface as a hard failure so the task is
// retried/escalated, NOT silently recorded as no_op (which would risk the
// exact false negative this fix exists to prevent) and NOT recorded as
// completed either (there's no confirmed deliverable).
func TestRunner_PRGuard_MergeBaseFailure_NoPRFound_FailsClosedNotNoOp(t *testing.T) {
	const branch = "pilot/GH-5359-c"
	dir := setupPRGuardRepoNoLocalBase(t, branch, false) // no commits, no local "main" ref either

	setUpFakeGhPATH(t,
		[]byte(`[]`), // --state merged: none
		[]byte(`[]`), // --state open: none
	)

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "analysis complete"},
	}
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	task := &Task{
		ID:          "GH-5359-c",
		Title:       "fix(executor): merge-base failure with no PR anywhere",
		Description: "Verify a totally unresolvable base with no PR fails closed instead of guessing",
		ProjectPath: dir,
		Branch:      branch,
		CreatePR:    true,
	}

	result, err := runner.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Success {
		t.Error("Expected failure (branch state could not be confirmed), got success")
	}
	if strings.HasPrefix(result.Error, "no_changes:") {
		t.Errorf("Guard must not guess no_changes when it cannot confirm branch state, got: %q", result.Error)
	}
	if got := TerminalStatus(result); got == "completed" || got == "no_op" {
		t.Errorf("TerminalStatus = %q, must be neither completed nor no_op when inconclusive", got)
	}
}
