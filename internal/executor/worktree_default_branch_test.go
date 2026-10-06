package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestRepoWithBranchRemote builds a local clone-like repo plus a bare
// "remote" whose only branch is defaultBranch (no "main" anywhere). When
// setHead is true, refs/remotes/origin/HEAD is pointed at it, as `git clone`
// would leave it.
//
// The existing setupTestRepoWithRemote helper hardcodes "main", which is
// exactly the assumption the worktree base used to make, so it cannot
// exercise a repository whose default branch is anything else.
func setupTestRepoWithBranchRemote(t *testing.T, defaultBranch string, setHead bool) (localDir, remoteDir string) {
	t.Helper()

	remoteDir, err := os.MkdirTemp("", "worktree-branch-remote-*")
	if err != nil {
		t.Fatalf("failed to create remote dir: %v", err)
	}
	localDir, err = os.MkdirTemp("", "worktree-branch-local-*")
	if err != nil {
		_ = os.RemoveAll(remoteDir)
		t.Fatalf("failed to create local dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(remoteDir)
		_ = os.RemoveAll(localDir)
	})

	run := func(dir string, args ...string) {
		t.Helper()
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, output)
		}
	}

	run(remoteDir, "init", "--bare", "-b", defaultBranch)
	run(localDir, "init", "-b", defaultBranch)
	run(localDir, "config", "user.email", "test@example.com")
	run(localDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	run(localDir, "add", ".")
	run(localDir, "commit", "-m", "Initial commit")
	run(localDir, "remote", "add", "origin", remoteDir)
	run(localDir, "push", "-u", "origin", "HEAD:"+defaultBranch)

	if setHead {
		run(localDir, "remote", "set-head", "origin", defaultBranch)
	}

	return localDir, remoteDir
}

// pushNewCommitToRemoteBranch advances branch on the bare remote by one commit
// from a scratch clone and returns the new tip SHA, so a test can tell a fresh
// base from a stale one.
func pushNewCommitToRemoteBranch(t *testing.T, remoteDir, branch string) string {
	t.Helper()

	scratchDir, err := os.MkdirTemp("", "worktree-branch-push-*")
	if err != nil {
		t.Fatalf("failed to create scratch dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(scratchDir) }()

	run := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", append([]string{"-C", scratchDir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, output)
		}
	}

	if output, err := exec.Command("git", "clone", "-b", branch, remoteDir, scratchDir).CombinedOutput(); err != nil {
		t.Fatalf("failed to clone remote: %v: %s", err, output)
	}
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(scratchDir, "new-remote-commit.txt"), []byte("advance remote\n"), 0644); err != nil {
		t.Fatalf("failed to write new file: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "advance remote")
	run("push", "origin", "HEAD:"+branch)

	shaOutput, err := exec.Command("git", "-C", scratchDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("failed to resolve new commit SHA: %v", err)
	}
	return strings.TrimSpace(string(shaOutput))
}

func worktreeHeadSHA(t *testing.T, worktreePath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", worktreePath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("failed to resolve worktree HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestWorktreeManager_ResolveBaseBranch(t *testing.T) {
	tests := []struct {
		name       string
		remoteName string // default branch of the remote
		setHead    bool
		configured string
		want       string
	}{
		{name: "origin/HEAD names a non-main default", remoteName: "mainline", setHead: true, want: "mainline"},
		{name: "origin/HEAD with a slash in the branch name", remoteName: "release/1.x", setHead: true, want: "release/1.x"},
		{name: "configured branch wins over origin/HEAD", remoteName: "mainline", setHead: true, configured: "develop", want: "develop"},
		{name: "no origin/HEAD falls back to main", remoteName: "mainline", setHead: false, want: "main"},
		{name: "no origin/HEAD, configured branch is used", remoteName: "mainline", setHead: false, configured: "mainline", want: "mainline"},
		{name: "whitespace-only configured branch is ignored", remoteName: "mainline", setHead: true, configured: "  ", want: "mainline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo, _ := setupTestRepoWithBranchRemote(t, tt.remoteName, tt.setHead)

			manager := NewWorktreeManager(localRepo)
			manager.SetBaseBranch(tt.configured)

			if got := manager.resolveBaseBranch(context.Background()); got != tt.want {
				t.Errorf("resolveBaseBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCreateWorktreeWithBranch_NonMainDefaultBranch is the regression test for
// the worktree base being hardcoded to origin/main: on a repository whose
// default branch is not "main" (and which has no "main" on the remote) every
// task used to die with "couldn't find remote ref main" before any work began.
func TestCreateWorktreeWithBranch_NonMainDefaultBranch(t *testing.T) {
	tests := []struct {
		name       string
		setHead    bool   // origin/HEAD present, as after `git clone`
		configured string // base branch from the project config / task
	}{
		{name: "detected from origin/HEAD", setHead: true},
		{name: "configured explicitly", setHead: false, configured: "mainline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo, remoteRepo := setupTestRepoWithBranchRemote(t, "mainline", tt.setHead)
			newSHA := pushNewCommitToRemoteBranch(t, remoteRepo, "mainline")

			manager := NewWorktreeManager(localRepo)
			manager.SetBaseBranch(tt.configured)

			result, err := manager.CreateWorktreeWithBranch(context.Background(), "GH-1", "pilot/gh-1", "")
			if err != nil {
				t.Fatalf("CreateWorktreeWithBranch failed on a repo whose default branch is not main: %v", err)
			}
			defer result.Cleanup()

			// The base must be the freshly fetched remote tip, not the stale
			// local copy of the branch.
			if got := worktreeHeadSHA(t, result.Path); got != newSHA {
				t.Errorf("worktree HEAD = %s, want fresh origin/mainline tip %s", got, newSHA)
			}
		})
	}
}

// TestWorktreePool_NonMainDefaultBranch covers the pooled path: both the
// warm-up (detached worktree at the default branch) and Acquire (reset onto a
// task branch) used to assume origin/main.
func TestWorktreePool_NonMainDefaultBranch(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithBranchRemote(t, "mainline", true)

	ctx := context.Background()
	manager := NewWorktreeManagerWithPool(localRepo, 1)
	manager.SetBaseBranch("mainline")
	if err := manager.WarmPool(ctx); err != nil {
		t.Fatalf("WarmPool failed: %v", err)
	}
	defer manager.Close()
	if got := manager.PoolAvailable(); got != 1 {
		t.Fatalf("PoolAvailable() = %d after WarmPool, want 1 (warm-up must not need origin/main)", got)
	}

	newSHA := pushNewCommitToRemoteBranch(t, remoteRepo, "mainline")

	result, err := manager.Acquire(ctx, "GH-2", "pilot/gh-2", "")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	defer result.Cleanup()

	if got := worktreeHeadSHA(t, result.Path); got != newSHA {
		t.Errorf("acquired worktree HEAD = %s, want fresh origin/mainline tip %s", got, newSHA)
	}
}

// TestFetchOriginBranchForWorktree_MissingBranchFailsClosed keeps the GH-5449
// guarantee when the named branch does not exist on the remote: an error, not
// a silent fallback to some other base.
func TestFetchOriginBranchForWorktree_MissingBranchFailsClosed(t *testing.T) {
	localRepo, _ := setupTestRepoWithBranchRemote(t, "mainline", true)

	baseRef, err := fetchOriginBranchForWorktree(context.Background(), localRepo, "main")
	if err == nil {
		t.Fatalf("expected error fetching a branch absent from the remote, got baseRef=%q", baseRef)
	}
	if !strings.Contains(err.Error(), "worktree base fetch failed") {
		t.Errorf("expected 'worktree base fetch failed' error, got: %v", err)
	}
}
