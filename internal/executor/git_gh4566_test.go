package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCountNewCommitsAgainstOrigin_StaleLocalBaseFalsePositive reproduces the
// GH-4566 false positive: the local `main` ref is behind `origin/main` (e.g.
// other children's PRs merged after this clone last fetched), and the branch
// under test was cut straight from origin/main (worktree.go semantics) with
// zero commits of its own. The old CountNewCommits(ctx, "main") — comparing
// against the stale local ref — counts the commits that landed on origin as
// if they belonged to this branch. CountNewCommitsAgainstOrigin must not.
func TestCountNewCommitsAgainstOrigin_StaleLocalBaseFalsePositive(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Simulate other work (e.g. a sibling epic child's merged PR) landing on
	// origin/main via a second clone. This clone's local `main` never learns
	// about it — nothing fast-forwards it (TASK-402: independent-sibling
	// dispatch never runs syncMainBranch mid-epic).
	pushExtraCommitFromSecondClone(t, origin, "main", "sibling child PR merged")
	pushExtraCommitFromSecondClone(t, origin, "main", "second sibling child PR merged")

	// Cut the epic's umbrella branch straight from origin/main, exactly as
	// worktree.go does (git fetch origin main; git worktree add -B <branch>
	// <path> origin/main) — local `main` is deliberately left behind.
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4566-epic", "origin/main")

	git := NewGitOperations(local)
	ctx := context.Background()

	// Old behavior: comparing against the stale local `main` ref counts the
	// two commits that landed on origin as if this branch produced them.
	staleCount, err := git.CountNewCommits(ctx, "main")
	if err != nil {
		t.Fatalf("CountNewCommits: %v", err)
	}
	if staleCount == 0 {
		t.Fatalf("test setup invalid: expected stale local `main` comparison to be nonzero (repro precondition), got 0")
	}

	// Fixed behavior: comparing against origin/main (freshly fetched) sees
	// the branch carries zero commits of its own.
	freshCount, err := git.CountNewCommitsAgainstOrigin(ctx, "main")
	if err != nil {
		t.Fatalf("CountNewCommitsAgainstOrigin: %v", err)
	}
	if freshCount != 0 {
		t.Errorf("CountNewCommitsAgainstOrigin = %d, want 0 (branch has no commits vs the ref it was cut from)", freshCount)
	}
}

// TestCountNewCommitsAgainstOrigin_RealCommitsSurviveStaleLocalMain is the
// mirror-image check: a branch that DOES carry its own real commit must still
// report a nonzero count via the origin-relative comparison, even while local
// `main` is stale — the fix must not accidentally zero out legitimate content.
func TestCountNewCommitsAgainstOrigin_RealCommitsSurviveStaleLocalMain(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	pushExtraCommitFromSecondClone(t, origin, "main", "sibling child PR merged")

	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4566-work", "origin/main")

	if err := os.WriteFile(filepath.Join(local, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	runGit(t, local, "add", "feature.txt")
	runGit(t, local, "commit", "-m", "feat: real work")

	git := NewGitOperations(local)
	count, err := git.CountNewCommitsAgainstOrigin(context.Background(), "main")
	if err != nil {
		t.Fatalf("CountNewCommitsAgainstOrigin: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (the branch's own real commit, not the sibling's)", count)
	}
}

// TestCountNewCommitsAgainstOrigin_FetchFailureFallsBackToTrackingRef covers
// the offline case: `git fetch origin <base>` fails (network down / origin
// unreachable), but a previous fetch already populated the local
// refs/remotes/origin/<base> tracking ref. CountNewCommitsAgainstOrigin must
// fall back to that existing tracking ref instead of erroring out.
func TestCountNewCommitsAgainstOrigin_FetchFailureFallsBackToTrackingRef(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Cut the branch from origin/main with zero new commits, then make sure
	// origin/main is resolvable locally (setupSyncTestRepos already fetched
	// implicitly via clone, but fetch explicitly to be sure).
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4566-offline", "origin/main")

	// Go offline: the remote is gone, so `git fetch origin main` will fail,
	// but origin/main is still resolvable from the last fetch.
	if err := os.RemoveAll(origin); err != nil {
		t.Fatalf("remove origin: %v", err)
	}

	git := NewGitOperations(local)
	count, err := git.CountNewCommitsAgainstOrigin(context.Background(), "main")
	if err != nil {
		t.Fatalf("CountNewCommitsAgainstOrigin should fall back to the existing tracking ref, got error: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0 (no new commits vs the last-known origin/main)", count)
	}
}

// TestCountNewCommitsAgainstOrigin_NoOriginRemoteFallsBackToLocalBranch
// covers repos with no "origin" remote configured at all (bare local repos —
// several existing unit tests, and conceivably some non-worktree deployments)
// so CountNewCommitsAgainstOrigin doesn't regress CountNewCommits' original
// behavior in that environment.
func TestCountNewCommitsAgainstOrigin_NoOriginRemoteFallsBackToLocalBranch(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "checkout", "-b", "pilot/GH-4566-local")

	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	runGit(t, dir, "add", "feature.txt")
	runGit(t, dir, "commit", "-m", "feat: local-only work")

	git := NewGitOperations(dir)
	count, err := git.CountNewCommitsAgainstOrigin(context.Background(), "main")
	if err != nil {
		t.Fatalf("CountNewCommitsAgainstOrigin: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (fallback to local `main` ref when no origin remote exists)", count)
	}
}
