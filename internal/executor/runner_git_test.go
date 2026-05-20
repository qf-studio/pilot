package executor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupSyncTestRepos builds a bare "origin" repo and a "local" clone with an
// initial commit on the given branch. Returns localDir, originDir, and a cleanup.
//
// The two-repo pattern is required because syncMainBranch calls `git fetch
// origin <branch>` — a single git-init tmpdir has no fetchable remote.
func setupSyncTestRepos(t *testing.T, branch string) (localDir, originDir string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()

	originDir = t.TempDir()
	if err := exec.CommandContext(ctx, "git", "init", "--bare", "-b", branch, originDir).Run(); err != nil {
		t.Fatalf("init bare origin: %v", err)
	}

	localDir = t.TempDir()
	if err := exec.CommandContext(ctx, "git", "clone", "-b", branch, originDir, localDir).Run(); err != nil {
		// Older gits may not accept -b on clone of empty bare; fall back.
		if err := exec.CommandContext(ctx, "git", "clone", originDir, localDir).Run(); err != nil {
			t.Fatalf("clone local: %v", err)
		}
	}

	runGit(t, localDir, "config", "user.email", "test@test.com")
	runGit(t, localDir, "config", "user.name", "Test User")
	runGit(t, localDir, "checkout", "-B", branch)

	// Initial commit + push so origin has a tip.
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("init\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, localDir, "add", "README.md")
	runGit(t, localDir, "commit", "-m", "initial")
	runGit(t, localDir, "push", "-u", "origin", branch)

	return localDir, originDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return gitOutput(t, dir, "rev-parse", "HEAD")
}

func newSyncTestRunner() *Runner {
	return &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// Helper: add a commit in another clone of origin and push it, so the
// original local clone is "behind" origin.
func pushExtraCommitFromSecondClone(t *testing.T, originDir, branch, msg string) {
	t.Helper()
	tmp := t.TempDir()
	if err := exec.Command("git", "clone", "-b", branch, originDir, tmp).Run(); err != nil {
		if err := exec.Command("git", "clone", originDir, tmp).Run(); err != nil {
			t.Fatalf("clone second: %v", err)
		}
	}
	runGit(t, tmp, "config", "user.email", "other@test.com")
	runGit(t, tmp, "config", "user.name", "Other")
	runGit(t, tmp, "checkout", "-B", branch)
	if err := os.WriteFile(filepath.Join(tmp, "remote.txt"), []byte(msg), 0644); err != nil {
		t.Fatalf("write remote file: %v", err)
	}
	runGit(t, tmp, "add", "remote.txt")
	runGit(t, tmp, "commit", "-m", msg)
	runGit(t, tmp, "push", "origin", branch)
}

// Case 1: local is behind origin → fast-forward succeeds.
func TestSyncMainBranch_Behind_FastForwards(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	pushExtraCommitFromSecondClone(t, origin, "main", "remote new")

	before := headSHA(t, local)

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error: %v", err)
	}

	after := headSHA(t, local)
	if before == after {
		t.Fatalf("expected local HEAD to advance from %s, still at same SHA", before[:8])
	}
}

// Case 2: local is AHEAD of origin (the bug case) → must NOT lose the commit.
func TestSyncMainBranch_Ahead_PreservesLocalCommit(t *testing.T) {
	local, _ := setupSyncTestRepos(t, "main")

	// Make a local-only commit (no push).
	if err := os.WriteFile(filepath.Join(local, "local.txt"), []byte("local only"), 0644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	runGit(t, local, "add", "local.txt")
	runGit(t, local, "commit", "-m", "local-only commit")

	beforeSHA := headSHA(t, local)

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error: %v", err)
	}

	afterSHA := headSHA(t, local)
	if beforeSHA != afterSHA {
		t.Fatalf("REGRESSION: local commit was lost! before=%s after=%s", beforeSHA[:8], afterSHA[:8])
	}
}

// Case 3: divergence — both sides have unique commits.
func TestSyncMainBranch_Diverged_PreservesLocalCommit(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")

	// Local-only commit.
	if err := os.WriteFile(filepath.Join(local, "local.txt"), []byte("local"), 0644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	runGit(t, local, "add", "local.txt")
	runGit(t, local, "commit", "-m", "local-only")

	// Origin-only commit via second clone.
	pushExtraCommitFromSecondClone(t, origin, "main", "remote-only")

	beforeSHA := headSHA(t, local)

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error: %v", err)
	}

	afterSHA := headSHA(t, local)
	if beforeSHA != afterSHA {
		t.Fatalf("REGRESSION: local diverged-commit was lost! before=%s after=%s", beforeSHA[:8], afterSHA[:8])
	}
}

// Case 4: dirty working tree + behind → uncommitted changes preserved, no merge.
func TestSyncMainBranch_DirtyTree_PreservesUncommittedChanges(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "main")
	pushExtraCommitFromSecondClone(t, origin, "main", "remote new")

	// Dirty change to the same file the upstream commit will not touch.
	dirtyPath := filepath.Join(local, "README.md")
	if err := os.WriteFile(dirtyPath, []byte("local dirty changes"), 0644); err != nil {
		t.Fatalf("dirty README: %v", err)
	}

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error: %v", err)
	}

	// The dirty file must still contain our content.
	got, err := os.ReadFile(dirtyPath)
	if err != nil {
		t.Fatalf("read dirty file: %v", err)
	}
	if string(got) != "local dirty changes" {
		t.Fatalf("uncommitted changes overwritten: got %q", got)
	}
}

// Case 5: on a feature branch → skip without touching anything.
func TestSyncMainBranch_FeatureBranch_Skips(t *testing.T) {
	local, _ := setupSyncTestRepos(t, "main")
	runGit(t, local, "checkout", "-b", "feat/x")

	beforeSHA := headSHA(t, local)

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error: %v", err)
	}

	if headSHA(t, local) != beforeSHA {
		t.Fatalf("feature branch should be untouched")
	}
}

// Case 6: master-branch repo → uses origin/master, not hardcoded origin/main.
// Regression test for the latent bug in the pre-fix code.
func TestSyncMainBranch_MasterBranch_Works(t *testing.T) {
	local, origin := setupSyncTestRepos(t, "master")
	pushExtraCommitFromSecondClone(t, origin, "master", "remote new on master")

	before := headSHA(t, local)

	r := newSyncTestRunner()
	if err := r.syncMainBranch(context.Background(), local); err != nil {
		t.Fatalf("syncMainBranch returned error on master repo: %v", err)
	}

	after := headSHA(t, local)
	if before == after {
		t.Fatalf("expected master HEAD to advance, still at %s", before[:8])
	}
}
