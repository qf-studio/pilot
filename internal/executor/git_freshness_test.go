package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupFreshnessTestRepo creates a bare origin and a working clone with one commit
// on main (pushed to origin) and returns (workDir, mainSHA).
func setupFreshnessTestRepo(t *testing.T) (workDir string, mainSHA string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	ctx := context.Background()

	// Create bare repo simulating origin.
	originDir := t.TempDir()
	if out, err := exec.CommandContext(ctx, "git", "init", "--bare", originDir).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	// Create working repo.
	workDir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}

	// Use -b main to explicitly name the initial branch (default varies by git config).
	// Requires git ≥ 2.28 (Aug 2020); CI runners satisfy this.
	run("init", "-b", "main")
	run("config", "user.email", "test@pilot.test")
	run("config", "user.name", "Pilot Test")
	run("remote", "add", "origin", originDir)

	// Initial commit on main.
	if err := os.WriteFile(filepath.Join(workDir, "base.txt"), []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "initial commit")
	run("push", "-u", "origin", "main")

	// Fetch to update remote-tracking refs (refs/remotes/origin/main).
	run("fetch", "origin")

	mainSHA = trimNL(run("rev-parse", "HEAD"))
	return workDir, mainSHA
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func TestCommitSHAIsNew_EmptySHA(t *testing.T) {
	ctx := context.Background()
	isNew, err := commitSHAIsNew(ctx, t.TempDir(), "", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Error("empty SHA should not be considered new")
	}
}

func TestCommitSHAIsNew_SHAOnMain(t *testing.T) {
	workDir, mainSHA := setupFreshnessTestRepo(t)
	ctx := context.Background()

	isNew, err := commitSHAIsNew(ctx, workDir, mainSHA, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Errorf("SHA already on origin/main should not be new (got isNew=true), sha=%s", mainSHA[:7])
	}
}

func TestCommitSHAIsNew_SHAOnFeatureBranch(t *testing.T) {
	workDir, _ := setupFreshnessTestRepo(t)
	ctx := context.Background()

	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}

	// Create a feature branch with a new commit.
	run("checkout", "-b", "feature/test")
	if err := os.WriteFile(filepath.Join(workDir, "feature.txt"), []byte("feature"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "feature commit")

	featureSHA := trimNL(run("rev-parse", "HEAD"))

	isNew, err := commitSHAIsNew(ctx, workDir, featureSHA, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Errorf("SHA on feature branch should be new (got isNew=false), sha=%s", featureSHA[:7])
	}
}
