package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestNewGitOperations(t *testing.T) {
	git := NewGitOperations("/test/path")

	if git == nil {
		t.Fatal("NewGitOperations returned nil")
	}
	if git.projectPath != "/test/path" {
		t.Errorf("projectPath = %q, want /test/path", git.projectPath)
	}
}

func TestGitOperationsInTempRepo(t *testing.T) {
	// Skip if git is not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()

	// Initialize git repo
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)

	t.Run("GetCurrentBranch", func(t *testing.T) {
		branch, err := git.GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch failed: %v", err)
		}
		// Could be main or master depending on git config
		if branch != "main" && branch != "master" {
			t.Errorf("branch = %q, want main or master", branch)
		}
	})

	t.Run("CreateBranch", func(t *testing.T) {
		err := git.CreateBranch(ctx, "test-branch")
		if err != nil {
			t.Fatalf("CreateBranch failed: %v", err)
		}

		branch, _ := git.GetCurrentBranch(ctx)
		if branch != "test-branch" {
			t.Errorf("branch = %q, want test-branch", branch)
		}
	})

	t.Run("SwitchBranch", func(t *testing.T) {
		// Switch back to main/master
		mainBranch := "main"
		if git.branchExists(ctx, "master") && !git.branchExists(ctx, "main") {
			mainBranch = "master"
		}

		err := git.SwitchBranch(ctx, mainBranch)
		if err != nil {
			t.Fatalf("SwitchBranch failed: %v", err)
		}

		branch, _ := git.GetCurrentBranch(ctx)
		if branch != mainBranch {
			t.Errorf("branch = %q, want %s", branch, mainBranch)
		}
	})

	t.Run("HasUncommittedChanges", func(t *testing.T) {
		// Should have no changes
		hasChanges, err := git.HasUncommittedChanges(ctx)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if hasChanges {
			t.Error("expected no uncommitted changes")
		}

		// Make a change
		_ = os.WriteFile(testFile, []byte("modified"), 0644)

		hasChanges, err = git.HasUncommittedChanges(ctx)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if !hasChanges {
			t.Error("expected uncommitted changes")
		}
	})

	t.Run("Commit", func(t *testing.T) {
		sha, err := git.Commit(ctx, "test commit")
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}

		if !isValidSHA(sha) {
			t.Errorf("invalid SHA returned: %q", sha)
		}
	})

	t.Run("GetChangedFiles", func(t *testing.T) {
		files, err := git.GetChangedFiles(ctx)
		if err != nil {
			t.Fatalf("GetChangedFiles failed: %v", err)
		}
		// After commit, should be empty
		if len(files) != 0 {
			t.Errorf("expected no changed files, got %v", files)
		}
	})
}

func TestBranchExists(t *testing.T) {
	// Skip if git is not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()

	// Initialize git repo
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)

	// Current branch should exist
	currentBranch, _ := git.GetCurrentBranch(ctx)
	if !git.branchExists(ctx, currentBranch) {
		t.Errorf("branchExists(%q) = false, want true", currentBranch)
	}

	// Nonexistent branch
	if git.branchExists(ctx, "nonexistent-branch-12345") {
		t.Error("branchExists(nonexistent) = true, want false")
	}
}
