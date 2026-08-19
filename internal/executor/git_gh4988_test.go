package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetDiffAgainstOrigin_StaleLocalBaseExcludesMergedSiblingWork reproduces
// the GH-4988 intent-judge false veto: a sibling issue's PR (e.g. GH-4930)
// merges into origin/main, this task's worktree branch is then cut FROM that
// fresh origin/main (so the sibling's commit is a legitimate ancestor of
// HEAD), but the shared clone/worktree's local `main` ref was never
// fast-forwarded past the point before the sibling merged. The old
// GetDiff(ctx, "main") diffs against that stale local ref, so the resulting
// diff "bundles" the sibling's already-merged work alongside this task's own
// commit — exactly the false "scope creep" signal the intent judge reported
// 5 times in 2 days. GetDiffAgainstOrigin must not reproduce this: it fetches
// origin fresh and diffs from the merge-base with origin/<base>, so only this
// branch's own commits appear.
func TestGetDiffAgainstOrigin_StaleLocalBaseExcludesMergedSiblingWork(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Sibling issue's PR merges into origin/main via a second clone. Local
	// `main` in this clone never learns about it (nothing fast-forwards it
	// mid-task — the same precondition as the GH-4566 CountNewCommits bug).
	pushExtraCommitFromSecondClone(t, origin, "main", "sibling PR merged (GH-4930-style)")

	// Cut this task's branch from a FRESH origin/main, exactly as
	// worktree.go does — so the sibling's commit IS a real ancestor of HEAD.
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4988-task", "origin/main")

	// This task's own commit.
	if err := os.WriteFile(filepath.Join(local, "task.txt"), []byte("task work"), 0644); err != nil {
		t.Fatalf("write task.txt: %v", err)
	}
	runGit(t, local, "add", "task.txt")
	runGit(t, local, "commit", "-m", "feat: task's own change")

	git := NewGitOperations(local)
	ctx := context.Background()

	// Old behavior: diffing against the stale local `main` ref bundles the
	// sibling's already-merged commit into the judge's diff.
	staleDiff, err := git.GetDiff(ctx, "main")
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if !strings.Contains(staleDiff, "remote.txt") {
		t.Fatalf("test setup invalid: expected stale-base diff to bundle the sibling's remote.txt (repro precondition), got:\n%s", staleDiff)
	}

	// Fixed behavior: GetDiffAgainstOrigin must contain only this branch's
	// own commit, never the sibling's already-merged work.
	freshDiff, baseSHA, err := git.GetDiffAgainstOrigin(ctx, "main")
	if err != nil {
		t.Fatalf("GetDiffAgainstOrigin: %v", err)
	}
	if strings.Contains(freshDiff, "remote.txt") {
		t.Errorf("REGRESSION: GetDiffAgainstOrigin diff bundles sibling's merged work (remote.txt):\n%s", freshDiff)
	}
	if !strings.Contains(freshDiff, "task.txt") {
		t.Errorf("GetDiffAgainstOrigin diff missing this branch's own commit (task.txt):\n%s", freshDiff)
	}

	// The base SHA must be origin/main's tip at judge time (the merge-base
	// of origin/main and HEAD, since HEAD is a direct descendant).
	originMainSHA := strings.TrimSpace(gitOutput(t, local, "rev-parse", "origin/main"))
	if baseSHA != originMainSHA {
		t.Errorf("baseSHA = %s, want origin/main tip %s", baseSHA, originMainSHA)
	}
}

// TestGetDiffAgainstOrigin_UpstreamAdvanceAfterWorktreeCut is the acceptance
// criteria's exact scenario: the worktree is cut from a stale main, then
// upstream (origin/main) advances further while the task is executing. At
// judge time, GetDiffAgainstOrigin must fetch that further advance and still
// produce a diff containing only the task's own commits.
func TestGetDiffAgainstOrigin_UpstreamAdvanceAfterWorktreeCut(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Worktree cut from origin/main at its current (soon-to-be-stale) tip.
	runGit(t, local, "fetch", "origin", "main")
	runGit(t, local, "checkout", "-B", "pilot/GH-4988-task2", "origin/main")

	// Task's own commit, made before any further upstream advance.
	if err := os.WriteFile(filepath.Join(local, "task.txt"), []byte("task work"), 0644); err != nil {
		t.Fatalf("write task.txt: %v", err)
	}
	runGit(t, local, "add", "task.txt")
	runGit(t, local, "commit", "-m", "feat: task's own change")

	// Upstream advances further while the task executes (another sibling
	// issue merges into origin/main), independent of this branch.
	pushExtraCommitFromSecondClone(t, origin, "main", "another sibling PR merged mid-task")

	git := NewGitOperations(local)
	ctx := context.Background()

	diff, baseSHA, err := git.GetDiffAgainstOrigin(ctx, "main")
	if err != nil {
		t.Fatalf("GetDiffAgainstOrigin: %v", err)
	}
	if strings.Contains(diff, "remote.txt") {
		t.Errorf("REGRESSION: judge diff contains commits reachable from current origin/main:\n%s", diff)
	}
	if !strings.Contains(diff, "task.txt") {
		t.Errorf("judge diff missing the task's own commit:\n%s", diff)
	}

	// The base SHA must never be reachable-from-origin/main-only content —
	// it should equal the merge-base of (freshly fetched) origin/main and
	// HEAD, which here is origin/main's pre-advance tip (this branch's
	// parent commit).
	branchParentSHA := strings.TrimSpace(gitOutput(t, local, "rev-parse", "HEAD~1"))
	if baseSHA != branchParentSHA {
		t.Errorf("baseSHA = %s, want %s (this branch's parent commit / origin/main's tip at branch-cut time)", baseSHA, branchParentSHA)
	}
}

// TestGetDiffAgainstOrigin_NoOriginRemoteFallsBackToLocalBranch covers repos
// with no "origin" remote configured (bare local repos, some unit tests) so
// GetDiffAgainstOrigin doesn't regress GetDiff's original behavior there.
func TestGetDiffAgainstOrigin_NoOriginRemoteFallsBackToLocalBranch(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	runGit(t, dir, "checkout", "-b", "pilot/GH-4988-local")

	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	runGit(t, dir, "add", "feature.txt")
	runGit(t, dir, "commit", "-m", "feat: local-only work")

	git := NewGitOperations(dir)
	diff, baseSHA, err := git.GetDiffAgainstOrigin(context.Background(), "main")
	if err != nil {
		t.Fatalf("GetDiffAgainstOrigin: %v", err)
	}
	if !strings.Contains(diff, "feature.txt") {
		t.Errorf("diff missing local-only commit when falling back to local main ref:\n%s", diff)
	}
	if baseSHA == "" {
		t.Error("baseSHA must not be empty on the local-branch fallback path")
	}
}
