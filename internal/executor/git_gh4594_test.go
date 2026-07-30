package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCreateOrResetBranchFromOrigin_StaleLocalHEAD_UsesOriginTip is the
// GH-4594 repro/regression: direct-mode (non-worktree) execution runs
// against the daemon's shared clone, whose local base-branch ref can lag
// origin (or, per GH-4594's "corrupts the daemon clone" framing, even carry
// stray local-only commits from a prior run). The old flow
// (SwitchToDefaultBranchAndPull + CreateBranch) checked out that local ref
// and relied on a best-effort `git pull` to catch it up before cutting the
// task branch from whatever HEAD was left — silently cutting from stale
// local state whenever the pull/fetch hiccuped.
//
// This test leaves local `main` deliberately stale (one commit behind
// origin, never fetched) and verifies CreateOrResetBranchFromOrigin still
// cuts the new task branch from the freshly fetched origin/main tip, not the
// stale local HEAD.
func TestCreateOrResetBranchFromOrigin_StaleLocalHEAD_UsesOriginTip(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	// A second clone pushes a commit to origin/main. The primary `local`
	// clone never fetches it — its local `main` ref is now stale.
	pushExtraCommitFromSecondClone(t, origin, "main", "remote new")

	staleLocalHEAD := headSHA(t, local)

	git := NewGitOperations(local)
	baseRef, err := git.CreateOrResetBranchFromOrigin(ctx, "pilot/GH-4594", "main")
	if err != nil {
		t.Fatalf("CreateOrResetBranchFromOrigin: %v", err)
	}
	if baseRef != "origin/main" {
		t.Errorf("baseRef = %q, want origin/main", baseRef)
	}

	currentBranch, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if currentBranch != "pilot/GH-4594" {
		t.Fatalf("current branch = %q, want pilot/GH-4594", currentBranch)
	}

	// The new branch's tip must match origin/main's tip (fetched fresh by
	// CreateOrResetBranchFromOrigin), not the stale local HEAD this clone
	// had before the call.
	newBranchHEAD := headSHA(t, local)
	if newBranchHEAD == staleLocalHEAD {
		t.Fatalf("REGRESSION: task branch cut from stale local HEAD %s instead of the fetched origin/main tip", staleLocalHEAD[:8])
	}

	originTip := gitOutput(t, local, "rev-parse", "origin/main")
	if newBranchHEAD != originTip {
		t.Errorf("task branch HEAD = %s, want it to match freshly-fetched origin/main tip %s", newBranchHEAD, originTip)
	}
}

// TestCreateOrResetBranchFromOrigin_ResetsExistingStaleBranch covers the
// GH-912 stale-branch-recreation case that CreateOrResetBranchFromOrigin's
// -B flag now folds into a single atomic step (replacing the old
// CommitsBehindMain/DeleteBranch dance): a task branch left over from a
// previous, now-superseded run must be force-reset to the fresh origin tip
// rather than reused as-is.
func TestCreateOrResetBranchFromOrigin_ResetsExistingStaleBranch(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	git := NewGitOperations(local)

	// Simulate a prior, now-stale run: the task branch already exists,
	// forked from the old origin/main tip.
	if err := git.CreateBranch(ctx, "pilot/GH-4594"); err != nil {
		t.Fatalf("seed stale branch: %v", err)
	}
	staleBranchHEAD := headSHA(t, local)
	runGit(t, local, "checkout", "main")

	// Origin advances after the stale branch was cut.
	pushExtraCommitFromSecondClone(t, origin, "main", "remote advanced")

	baseRef, err := git.CreateOrResetBranchFromOrigin(ctx, "pilot/GH-4594", "main")
	if err != nil {
		t.Fatalf("CreateOrResetBranchFromOrigin: %v", err)
	}
	if baseRef != "origin/main" {
		t.Errorf("baseRef = %q, want origin/main", baseRef)
	}

	resetHEAD := headSHA(t, local)
	if resetHEAD == staleBranchHEAD {
		t.Fatalf("REGRESSION: pre-existing branch was reused instead of force-reset to the fresh origin/main tip")
	}
	originTip := gitOutput(t, local, "rev-parse", "origin/main")
	if resetHEAD != originTip {
		t.Errorf("reset branch HEAD = %s, want origin/main tip %s", resetHEAD, originTip)
	}
}

// TestCreateOrResetBranchFromOrigin_NoOriginRemote_FallsBackToLocal covers
// repos with no "origin" remote at all (offline / bare local repos used by
// several existing unit tests), preserving the prior tolerant behavior in
// that environment rather than hard-failing the branch step.
func TestCreateOrResetBranchFromOrigin_NoOriginRemote_FallsBackToLocal(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	// initTestRepo doesn't pin a branch name — respect whatever `git init`
	// picked locally (main or master) rather than assuming "main".
	localBranch, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}

	baseRef, err := git.CreateOrResetBranchFromOrigin(ctx, "pilot/GH-4594-offline", localBranch)
	if err != nil {
		t.Fatalf("CreateOrResetBranchFromOrigin: %v", err)
	}
	if baseRef != localBranch {
		t.Errorf("baseRef = %q, want local %q fallback (no origin remote)", baseRef, localBranch)
	}

	currentBranch, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if currentBranch != "pilot/GH-4594-offline" {
		t.Errorf("current branch = %q, want pilot/GH-4594-offline", currentBranch)
	}
}

// TestEnsureBranchFromOrigin_NewBranch_CutFromFreshOriginTip is
// EnsureBranchFromOrigin's version of the stale-local-HEAD repro: it's the
// method the direct-mode single-task execution path (runner.go) actually
// calls, and must cut a brand-new task branch from the freshly fetched
// origin tip even though local `main` never fetched it.
func TestEnsureBranchFromOrigin_NewBranch_CutFromFreshOriginTip(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	pushExtraCommitFromSecondClone(t, origin, "main", "remote new")
	staleLocalHEAD := headSHA(t, local)

	git := NewGitOperations(local)
	baseRef, created, err := git.EnsureBranchFromOrigin(ctx, "pilot/GH-4594", "main")
	if err != nil {
		t.Fatalf("EnsureBranchFromOrigin: %v", err)
	}
	if baseRef != "origin/main" {
		t.Errorf("baseRef = %q, want origin/main", baseRef)
	}
	if !created {
		t.Error("created = false, want true (branch did not exist yet)")
	}

	newBranchHEAD := headSHA(t, local)
	if newBranchHEAD == staleLocalHEAD {
		t.Fatalf("REGRESSION: task branch cut from stale local HEAD %s instead of the fetched origin/main tip", staleLocalHEAD[:8])
	}
	originTip := gitOutput(t, local, "rev-parse", "origin/main")
	if newBranchHEAD != originTip {
		t.Errorf("task branch HEAD = %s, want origin/main tip %s", newBranchHEAD, originTip)
	}
}

// TestEnsureBranchFromOrigin_StaleExistingBranch_Recreated covers the GH-912
// case: a task branch left over from a prior, now-superseded run (behind the
// freshly fetched base) must be recreated from the fresh tip, not reused.
func TestEnsureBranchFromOrigin_StaleExistingBranch_Recreated(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	git := NewGitOperations(local)
	if err := git.CreateBranch(ctx, "pilot/GH-4594"); err != nil {
		t.Fatalf("seed stale branch: %v", err)
	}
	staleBranchHEAD := headSHA(t, local)
	runGit(t, local, "checkout", "main")

	pushExtraCommitFromSecondClone(t, origin, "main", "remote advanced")

	baseRef, created, err := git.EnsureBranchFromOrigin(ctx, "pilot/GH-4594", "main")
	if err != nil {
		t.Fatalf("EnsureBranchFromOrigin: %v", err)
	}
	if baseRef != "origin/main" {
		t.Errorf("baseRef = %q, want origin/main", baseRef)
	}
	if !created {
		t.Error("created = false, want true (stale branch should be recreated)")
	}

	resetHEAD := headSHA(t, local)
	if resetHEAD == staleBranchHEAD {
		t.Fatalf("REGRESSION: stale branch was reused instead of recreated from the fresh origin/main tip")
	}
	originTip := gitOutput(t, local, "rev-parse", "origin/main")
	if resetHEAD != originTip {
		t.Errorf("recreated branch HEAD = %s, want origin/main tip %s", resetHEAD, originTip)
	}
}

// TestEnsureBranchFromOrigin_NonStaleExistingBranch_PreservesCommits is the
// counterpart guarding against over-eager reset: a task branch that already
// carries legitimate work-in-progress commits from an earlier attempt on
// this same clone (and is NOT behind the fresh base) must be switched to
// as-is, not wiped. This is the exact shape TestRunner_PRCreate_HasCommits_
// ProceedsToCreate depends on at the runner.go level.
func TestEnsureBranchFromOrigin_NonStaleExistingBranch_PreservesCommits(t *testing.T) {
	local, _ := setupSyncTestRepos(t, "main")
	ctx := context.Background()

	git := NewGitOperations(local)
	if err := git.CreateBranch(ctx, "pilot/GH-4594"); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "work.txt"), []byte("in progress"), 0644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	if _, err := git.Commit(ctx, "wip: legitimate in-progress work"); err != nil {
		t.Fatalf("commit wip: %v", err)
	}
	wantHEAD := headSHA(t, local)

	baseRef, created, err := git.EnsureBranchFromOrigin(ctx, "pilot/GH-4594", "main")
	if err != nil {
		t.Fatalf("EnsureBranchFromOrigin: %v", err)
	}
	if baseRef != "origin/main" {
		t.Errorf("baseRef = %q, want origin/main", baseRef)
	}
	if created {
		t.Error("created = true, want false (existing non-stale branch should be preserved, not recreated)")
	}

	gotHEAD := headSHA(t, local)
	if gotHEAD != wantHEAD {
		t.Fatalf("REGRESSION: in-progress commit was discarded — HEAD = %s, want %s", gotHEAD, wantHEAD)
	}
}
