package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// setupFreshnessRepo creates a git repo with an initial commit on `main` and a bare
// remote, then pushes main so origin/main exists as a remote-tracking ref.
// Returns (repoDir, bareDir).
func setupFreshnessRepo(t *testing.T) (string, string) {
	t.Helper()

	bareDir := t.TempDir()
	runGit(t, bareDir, "init", "--bare", "-b", "main")

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "commit", "--allow-empty", "-m", "initial")
	runGit(t, repoDir, "remote", "add", "origin", bareDir)
	runGit(t, repoDir, "push", "-u", "origin", "main")

	return repoDir, bareDir
}

func TestCommitSHAIsNew(t *testing.T) {
	ctx := context.Background()

	t.Run("empty SHA returns false", func(t *testing.T) {
		repoDir, _ := setupFreshnessRepo(t)
		isNew, err := commitSHAIsNew(ctx, repoDir, "", "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isNew {
			t.Error("expected false for empty SHA, got true")
		}
	})

	t.Run("SHA already on main returns false (ghost-SHA pattern)", func(t *testing.T) {
		repoDir, _ := setupFreshnessRepo(t)
		sha := gitOutput(t, repoDir, "rev-parse", "HEAD")
		sha = sha[:len(sha)-1] // trim trailing newline from gitOutput
		if len(sha) > 0 && sha[len(sha)-1] == '\n' {
			sha = sha[:len(sha)-1]
		}
		isNew, err := commitSHAIsNew(ctx, repoDir, sha, "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if isNew {
			t.Errorf("expected false for SHA %s already on main, got true", sha[:7])
		}
	})

	t.Run("SHA on feature branch ahead of main returns true", func(t *testing.T) {
		repoDir, _ := setupFreshnessRepo(t)

		runGit(t, repoDir, "checkout", "-b", "feat/new-work")
		featureFile := filepath.Join(repoDir, "work.txt")
		if err := os.WriteFile(featureFile, []byte("new work"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, repoDir, "add", "work.txt")
		runGit(t, repoDir, "commit", "-m", "feat: new work")

		sha := gitOutput(t, repoDir, "rev-parse", "HEAD")
		sha = sha[:len(sha)-1]
		isNew, err := commitSHAIsNew(ctx, repoDir, sha, "main")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !isNew {
			t.Errorf("expected true for SHA %s on feature branch ahead of main, got false", sha[:7])
		}
	})

	t.Run("non-existent SHA returns error", func(t *testing.T) {
		repoDir, _ := setupFreshnessRepo(t)
		_, err := commitSHAIsNew(ctx, repoDir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "main")
		if err == nil {
			t.Error("expected error for non-existent SHA, got nil")
		}
	})
}
