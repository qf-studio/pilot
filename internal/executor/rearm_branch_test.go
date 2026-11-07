package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// rearmBranchFixture pushes a 28-file branch (the 2026-11-06 PR#6165 shape) to
// the bare remote and switches the local repo back to its default branch, the
// state the daemon's project checkout is in when a re-armed source retries.
func rearmBranchFixture(t *testing.T, localDir, branch string) (mainBranch, sha string) {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", localDir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	var err error
	mainBranch, err = NewGitOperations(localDir).GetCurrentBranch(context.Background())
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	run("checkout", "-b", branch)
	for i := 0; i < 28; i++ {
		if err := os.WriteFile(filepath.Join(localDir, fmt.Sprintf("file%02d.txt", i)), []byte("x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	run("add", ".")
	run("commit", "-m", "the dead fix issue's work")
	sha = run("rev-parse", "HEAD")
	run("push", "-u", "origin", branch)
	run("checkout", mainBranch)
	run("branch", "-D", branch)
	return mainBranch, sha
}

// TestReuseBranchFromRearmComment pins GH-6183 at the executor seam: a task
// carrying the branch, PR and sha from an owner-death re-arm comment builds its
// worktree on the footer's commit (never `-B` from origin/main), and a branch
// that is gone from the remote reports unavailable and logs the fallback.
func TestReuseBranchFromRearmComment(t *testing.T) {
	t.Run("existing remote branch continues at the footer sha", func(t *testing.T) {
		_, localDir, cleanup := newRemoteAndLocalRepo(t)
		defer cleanup()
		ctx := context.Background()
		branch := "pilot/GH-6168"
		mainBranch, sha := rearmBranchFixture(t, localDir, branch)

		git := NewGitOperations(localDir)
		if !git.RearmBranchAvailable(ctx, branch) {
			t.Fatal("RearmBranchAvailable = false for a branch present on origin")
		}
		baseRef := git.ResolveFixContinuationBaseRef(ctx, branch, sha)
		if baseRef != "origin/"+branch {
			t.Fatalf("base ref = %q, want origin/%s", baseRef, branch)
		}
		worktreePath, wtCleanup, err := CreateWorktreeWithBranch(ctx, localDir, "gh6183-rearm", branch, baseRef)
		if err != nil {
			t.Fatalf("CreateWorktreeWithBranch: %v", err)
		}
		defer wtCleanup()

		head, err := exec.Command("git", "-C", worktreePath, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatalf("rev-parse HEAD: %v", err)
		}
		if got := strings.TrimSpace(string(head)); got != sha {
			t.Errorf("worktree HEAD = %s, want footer sha %s", got, sha)
		}
		diff, err := exec.Command("git", "-C", worktreePath, "diff", "--name-only", mainBranch+"...HEAD").Output()
		if err != nil {
			t.Fatalf("git diff: %v", err)
		}
		if n := len(strings.Fields(string(diff))); n != 28 {
			t.Errorf("changed files vs %s = %d, want 28", mainBranch, n)
		}
	})

	t.Run("missing remote branch falls back and logs", func(t *testing.T) {
		_, localDir, cleanup := newRemoteAndLocalRepo(t)
		defer cleanup()

		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(prev)

		if NewGitOperations(localDir).RearmBranchAvailable(context.Background(), "pilot/GH-9999") {
			t.Fatal("RearmBranchAvailable = true for a branch absent from origin")
		}
		if !strings.Contains(buf.String(), "re-arm branch missing, starting from main") {
			t.Errorf("fallback not logged, got %q", buf.String())
		}
	})
}
