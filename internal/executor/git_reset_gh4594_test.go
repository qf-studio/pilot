package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestResetHardToCommit_DiscardsCommittedAndUncommittedChanges is the
// GH-4594 unit-level regression guard for ResetHardToCommit: everything made
// since the given SHA — a new commit, a dirty edit to a tracked file, and a
// brand-new untracked file — must be gone afterward, and HEAD must land
// exactly back on that SHA.
func TestResetHardToCommit_DiscardsCommittedAndUncommittedChanges(t *testing.T) {
	dir, cleanup := initTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	git := NewGitOperations(dir)

	preAttemptSHA, err := git.GetCurrentCommitSHA(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommitSHA: %v", err)
	}

	// Simulate a rejected attempt: a committed change to a tracked file...
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("attempt edit"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "attempt commit")

	// ...plus leftover uncommitted dirt on top: a dirty tracked file...
	if err := os.WriteFile(readmePath, []byte("uncommitted dirt"), 0644); err != nil {
		t.Fatalf("dirty README: %v", err)
	}
	// ...and a stray untracked file.
	strayPath := filepath.Join(dir, "stray.txt")
	if err := os.WriteFile(strayPath, []byte("stray"), 0644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	if err := git.ResetHardToCommit(ctx, preAttemptSHA); err != nil {
		t.Fatalf("ResetHardToCommit: %v", err)
	}

	afterSHA, err := git.GetCurrentCommitSHA(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommitSHA after reset: %v", err)
	}
	if afterSHA != preAttemptSHA {
		t.Fatalf("HEAD = %s, want pre-attempt SHA %s", afterSHA, preAttemptSHA)
	}

	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README after reset: %v", err)
	}
	if string(got) != "root" {
		t.Errorf("README.md content = %q, want original %q (attempt commit + dirty edit both discarded)", got, "root")
	}

	if _, err := os.Stat(strayPath); !os.IsNotExist(err) {
		t.Errorf("stray.txt should have been removed by the post-reset clean, got err=%v", err)
	}

	dirty, err := git.HasUncommittedChanges(ctx)
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if dirty {
		out := gitOutput(t, dir, "status", "--porcelain")
		t.Errorf("working tree should be clean after ResetHardToCommit, got dirty status:\n%s", out)
	}
}

// TestResetHardToCommit_PreservesExcludedScaffold verifies the post-reset
// `git clean` leaves Navigator/build scaffold paths alone (the same
// defaultExcludeDirs/defaultExcludeGlobs allowlist Commit()/checkGitClean()
// use), so a quality-gate retry reset can't nuke a hosted tenant's untracked
// .agent/ bootstrap (GH-4526) as a side effect.
func TestResetHardToCommit_PreservesExcludedScaffold(t *testing.T) {
	dir, cleanup := initTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	git := NewGitOperations(dir)

	preAttemptSHA, err := git.GetCurrentCommitSHA(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommitSHA: %v", err)
	}

	scaffoldDir := filepath.Join(dir, ".agent", "knowledge")
	if err := os.MkdirAll(scaffoldDir, 0755); err != nil {
		t.Fatalf("mkdir scaffold: %v", err)
	}
	scaffoldFile := filepath.Join(scaffoldDir, "graph.json")
	if err := os.WriteFile(scaffoldFile, []byte("{}"), 0644); err != nil {
		t.Fatalf("write scaffold file: %v", err)
	}

	if err := git.ResetHardToCommit(ctx, preAttemptSHA); err != nil {
		t.Fatalf("ResetHardToCommit: %v", err)
	}

	if _, err := os.Stat(scaffoldFile); err != nil {
		t.Errorf("untracked .agent/ scaffold should survive the reset's clean step, got err=%v", err)
	}
}
