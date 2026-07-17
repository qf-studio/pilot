package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	t.Run("CreateOrResetBranch_NewBranch", func(t *testing.T) {
		// GH-1235: CreateOrResetBranch should create new branch
		err := git.CreateOrResetBranch(ctx, "new-reset-branch")
		if err != nil {
			t.Fatalf("CreateOrResetBranch (new) failed: %v", err)
		}

		branch, _ := git.GetCurrentBranch(ctx)
		if branch != "new-reset-branch" {
			t.Errorf("branch = %q, want new-reset-branch", branch)
		}
	})

	t.Run("CreateOrResetBranch_ExistingBranch", func(t *testing.T) {
		// GH-1235: CreateOrResetBranch should succeed even if branch exists
		// First, go back to main
		mainBranch := "main"
		if git.branchExists(ctx, "master") && !git.branchExists(ctx, "main") {
			mainBranch = "master"
		}
		_ = git.SwitchBranch(ctx, mainBranch)

		// Now try to create/reset the branch that already exists
		err := git.CreateOrResetBranch(ctx, "new-reset-branch")
		if err != nil {
			t.Fatalf("CreateOrResetBranch (existing) failed: %v", err)
		}

		branch, _ := git.GetCurrentBranch(ctx)
		if branch != "new-reset-branch" {
			t.Errorf("branch = %q, want new-reset-branch", branch)
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

func TestSwitchToDefaultBranchAndPull(t *testing.T) {
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

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)

	// Get default branch name (main or master)
	defaultBranch, _ := git.GetCurrentBranch(ctx)

	// Create and switch to a feature branch
	_ = git.CreateBranch(ctx, "feature-branch")
	currentBranch, _ := git.GetCurrentBranch(ctx)
	if currentBranch != "feature-branch" {
		t.Fatalf("expected to be on feature-branch, got %s", currentBranch)
	}

	// Make a commit on feature branch
	_ = os.WriteFile(testFile, []byte("feature change"), 0644)
	_, _ = git.Commit(ctx, "feature commit")

	// Now SwitchToDefaultBranchAndPull should switch us back to main/master
	// Note: Pull will fail since there's no remote, but the function handles this gracefully
	branch, err := git.SwitchToDefaultBranchAndPull(ctx)
	if err != nil {
		// The switch should succeed even if pull fails (no remote)
		t.Logf("SwitchToDefaultBranchAndPull returned error (expected, no remote): %v", err)
	}

	if branch != defaultBranch {
		t.Errorf("returned branch = %q, want %q", branch, defaultBranch)
	}

	// Verify we're now on the default branch
	currentBranch, _ = git.GetCurrentBranch(ctx)
	if currentBranch != defaultBranch {
		t.Errorf("current branch = %q, want %q", currentBranch, defaultBranch)
	}
}

// TestSwitchToBranchAndPull verifies that SwitchToBranchAndPull honors an
// explicit branch override (GH-2290: project.default_branch / branch_from).
func TestSwitchToBranchAndPull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "t@t").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "T").Run()

	testFile := filepath.Join(tmpDir, "a.txt")
	_ = os.WriteFile(testFile, []byte("x"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "init").Run()

	git := NewGitOperations(tmpDir)

	// Create a `dev` branch and a feature branch off of it.
	_ = git.CreateBranch(ctx, "dev")
	_ = os.WriteFile(testFile, []byte("dev"), 0644)
	_, _ = git.Commit(ctx, "dev commit")
	_ = git.CreateBranch(ctx, "feature")

	// Explicit override must switch to dev, not the git default.
	branch, err := git.SwitchToBranchAndPull(ctx, "dev")
	if err != nil {
		t.Logf("pull failed (expected, no remote): %v", err)
	}
	if branch != "dev" {
		t.Errorf("branch = %q, want dev", branch)
	}
	current, _ := git.GetCurrentBranch(ctx)
	if current != "dev" {
		t.Errorf("current = %q, want dev", current)
	}

	// Empty override should fall back to SwitchToDefaultBranchAndPull.
	if _, err := git.SwitchToBranchAndPull(ctx, ""); err != nil {
		t.Logf("fallback returned err (ok): %v", err)
	}
}

func TestSwitchToDefaultBranchAndPull_NewBranchFromMain(t *testing.T) {
	// This test verifies the fix for GH-279: new branches should fork from main, not previous branch
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

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("main content"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial main commit").Run()

	git := NewGitOperations(tmpDir)
	defaultBranch, _ := git.GetCurrentBranch(ctx)

	// Get the main branch commit SHA
	mainSHA, _ := git.GetCurrentCommitSHA(ctx)

	// Create first feature branch and add commits (simulating pilot/GH-18)
	_ = git.CreateBranch(ctx, "pilot/GH-18")
	_ = os.WriteFile(filepath.Join(tmpDir, "feature1.txt"), []byte("feature 1"), 0644)
	_, _ = git.Commit(ctx, "feat: GH-18 changes")
	gh18SHA, _ := git.GetCurrentCommitSHA(ctx)

	// WITHOUT the fix: creating a new branch from here would fork from GH-18
	// WITH the fix: we switch to main first, so new branch forks from main

	// Switch to main first (this is what the fix does)
	_, _ = git.SwitchToDefaultBranchAndPull(ctx)

	// Create second feature branch (simulating pilot/GH-20)
	_ = git.CreateBranch(ctx, "pilot/GH-20")
	gh20ParentSHA, _ := git.GetCurrentCommitSHA(ctx)

	// The parent of GH-20 should be main, NOT GH-18
	if gh20ParentSHA != mainSHA {
		t.Errorf("GH-20 forked from wrong commit: got %s (GH-18=%s), want %s (main)", gh20ParentSHA, gh18SHA, mainSHA)
	}

	// Verify we're on the new branch
	currentBranch, _ := git.GetCurrentBranch(ctx)
	if currentBranch != "pilot/GH-20" {
		t.Errorf("expected to be on pilot/GH-20, got %s", currentBranch)
	}

	// Double-check: the main branch SHA should be our starting point
	_ = git.SwitchBranch(ctx, defaultBranch)
	currentMainSHA, _ := git.GetCurrentCommitSHA(ctx)
	if currentMainSHA != mainSHA {
		t.Errorf("main branch SHA changed unexpectedly: was %s, now %s", mainSHA, currentMainSHA)
	}
}

func TestCountNewCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()

	// Initialize git repo with initial commit
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()
	_ = os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)
	defaultBranch, _ := git.GetCurrentBranch(ctx)

	// Create feature branch
	_ = git.CreateBranch(ctx, "pilot/GH-99")

	t.Run("zero commits on new branch", func(t *testing.T) {
		count, err := git.CountNewCommits(ctx, defaultBranch)
		if err != nil {
			t.Fatalf("CountNewCommits failed: %v", err)
		}
		if count != 0 {
			t.Errorf("count = %d, want 0", count)
		}
	})

	t.Run("one commit on branch", func(t *testing.T) {
		_ = os.WriteFile(filepath.Join(tmpDir, "feature.txt"), []byte("feature"), 0644)
		_, _ = git.Commit(ctx, "feat: add feature")

		count, err := git.CountNewCommits(ctx, defaultBranch)
		if err != nil {
			t.Fatalf("CountNewCommits failed: %v", err)
		}
		if count != 1 {
			t.Errorf("count = %d, want 1", count)
		}
	})

	t.Run("multiple commits on branch", func(t *testing.T) {
		_ = os.WriteFile(filepath.Join(tmpDir, "feature2.txt"), []byte("feature2"), 0644)
		_, _ = git.Commit(ctx, "feat: add feature2")
		_ = os.WriteFile(filepath.Join(tmpDir, "feature3.txt"), []byte("feature3"), 0644)
		_, _ = git.Commit(ctx, "feat: add feature3")

		count, err := git.CountNewCommits(ctx, defaultBranch)
		if err != nil {
			t.Fatalf("CountNewCommits failed: %v", err)
		}
		if count != 3 {
			t.Errorf("count = %d, want 3", count)
		}
	})
}

func TestExtractPRURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple URL",
			input: "https://github.com/owner/repo/pull/123",
			want:  "https://github.com/owner/repo/pull/123",
		},
		{
			name:  "URL with already exists message",
			input: "a]ready exists:\nhttps://github.com/owner/repo/pull/456\n",
			want:  "https://github.com/owner/repo/pull/456",
		},
		{
			name:  "gh CLI already exists output",
			input: "a pull request for branch `feature` into `main` already exists:\nhttps://github.com/qf-studio/pilot/pull/285",
			want:  "https://github.com/qf-studio/pilot/pull/285",
		},
		{
			name:  "URL with trailing text",
			input: "https://github.com/owner/repo/pull/789 (created)",
			want:  "https://github.com/owner/repo/pull/789",
		},
		{
			name:  "no URL",
			input: "failed to create pull request",
			want:  "",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "github URL but not PR",
			input: "https://github.com/owner/repo/issues/123",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPRURL(tt.input)
			if got != tt.want {
				t.Errorf("extractPRURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestSwitchToDefaultBranchAndPull_FailsOnNonGitDir validates that
// SwitchToDefaultBranchAndPull returns an error for non-git directories.
// GH-836: This error MUST cause execution to abort (hard fail) rather than
// continuing on the wrong branch which corrupts PRs.
func TestSwitchToDefaultBranchAndPull_FailsOnNonGitDir(t *testing.T) {
	// Create temp directory without git init
	tmpDir, err := os.MkdirTemp("", "pilot-git-test-nogit-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()
	git := NewGitOperations(tmpDir)

	// SwitchToDefaultBranchAndPull should fail on non-git directory
	_, err = git.SwitchToDefaultBranchAndPull(ctx)
	if err == nil {
		t.Error("SwitchToDefaultBranchAndPull should fail on non-git directory")
	}
}

// TestSwitchBranch_FailsOnNonExistentBranch validates that
// SwitchBranch returns an error for non-existent branches.
// GH-836: This error MUST cause execution to abort when branch doesn't exist.
func TestSwitchBranch_FailsOnNonExistentBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()

	// Initialize git repo with initial commit
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()
	_ = os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)

	// SwitchBranch should fail on non-existent branch
	err = git.SwitchBranch(ctx, "nonexistent-branch-xyz123")
	if err == nil {
		t.Error("SwitchBranch should fail on non-existent branch")
	}
}

// TestRemoteBranchExists_NoRemote validates that RemoteBranchExists returns false
// when there is no remote configured.
// GH-1389: This method is used to detect if push actually succeeded despite worktree errors.
func TestRemoteBranchExists_NoRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()

	// Initialize git repo with initial commit (no remote)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()
	_ = os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(tmpDir)

	// RemoteBranchExists should return false when no remote is configured
	exists := git.RemoteBranchExists(ctx, "main")
	if exists {
		t.Error("RemoteBranchExists should return false when no remote configured")
	}

	// Should also return false for any branch name
	exists = git.RemoteBranchExists(ctx, "nonexistent-branch")
	if exists {
		t.Error("RemoteBranchExists should return false for nonexistent branch")
	}
}

// TestRemoteBranchExists_WithRemote validates that RemoteBranchExists correctly
// detects branches on a remote.
// GH-1389: This verifies the core fix for detecting successful pushes.
func TestRemoteBranchExists_WithRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a "remote" repo (bare)
	remoteDir, err := os.MkdirTemp("", "pilot-git-remote-*")
	if err != nil {
		t.Fatalf("failed to create remote dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(remoteDir) }()

	// Create local repo
	localDir, err := os.MkdirTemp("", "pilot-git-local-*")
	if err != nil {
		t.Fatalf("failed to create local dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(localDir) }()

	ctx := context.Background()

	// Initialize bare remote repo
	_ = exec.CommandContext(ctx, "git", "-C", remoteDir, "init", "--bare").Run()

	// Initialize local repo
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "config", "user.name", "Test User").Run()

	// Add remote
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "remote", "add", "origin", remoteDir).Run()

	// Create initial commit
	_ = os.WriteFile(filepath.Join(localDir, "test.txt"), []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "commit", "-m", "initial").Run()

	git := NewGitOperations(localDir)

	// Get current branch name
	currentBranch, _ := git.GetCurrentBranch(ctx)

	// Branch doesn't exist on remote yet (not pushed)
	exists := git.RemoteBranchExists(ctx, currentBranch)
	if exists {
		t.Error("RemoteBranchExists should return false before push")
	}

	// Push the branch
	_ = exec.CommandContext(ctx, "git", "-C", localDir, "push", "-u", "origin", currentBranch).Run()

	// Now branch should exist on remote
	exists = git.RemoteBranchExists(ctx, currentBranch)
	if !exists {
		t.Error("RemoteBranchExists should return true after push")
	}

	// Nonexistent branch should still return false
	exists = git.RemoteBranchExists(ctx, "nonexistent-branch-12345")
	if exists {
		t.Error("RemoteBranchExists should return false for nonexistent branch")
	}
}

// initTestRepo creates a temp git repo with a user config and initial commit,
// returning the repo path and a cleanup func.
func initTestRepo(t *testing.T) (string, func()) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test User")

	// Initial commit so HEAD exists.
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("root"), 0644)
	run("add", "README.md")
	run("commit", "-m", "init")
	return dir, func() {} // t.TempDir cleans up automatically
}

// TestIsExcluded covers the isExcluded helper with a table of known cases.
func TestIsExcluded(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".agent/tasks/TASK-99.md", true},
		{".claude/settings.json", true},
		{"node_modules/pkg/index.js", true},
		{"node_modules/foo", true},
		{"dist/bundle.js", true},
		{"build/out.o", true},
		{"coverage/lcov.info", true},
		{".cache/foo", true},
		{"package-lock.json", true},
		{"yarn.lock", true},
		{".DS_Store", true},
		{"Thumbs.db", true},
		// prefix must not substring-match
		{".agentless/foo.md", false},
		// regular source files must not be excluded
		{"internal/executor/git.go", false},
		{"cmd/pilot/main.go", false},
		{"README.md", false},
	}
	for _, tc := range cases {
		got := isExcluded(tc.path)
		if got != tc.want {
			t.Errorf("isExcluded(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestCommitScopedStaging runs integration tests against a real temp git repo.
func TestCommitScopedStaging(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	mkdir := func(rel string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, rel), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		mkdir(filepath.Dir(rel))
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	t.Run("pure code change commits cleanly", func(t *testing.T) {
		write("internal/foo.go", "package foo")
		sha, err := git.Commit(ctx, "feat: add foo")
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if !isValidSHA(sha) {
			t.Errorf("bad SHA: %q", sha)
		}
		// file should be committed (no longer untracked/modified)
		hasChanges, _ := git.HasUncommittedChanges(ctx)
		if hasChanges {
			t.Error("expected clean state after commit")
		}
	})

	t.Run("pure exclude change returns ErrNoStageableChanges", func(t *testing.T) {
		write(".agent/tasks/TASK-99.md", "# draft")
		_, err := git.Commit(ctx, "should not commit")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrNoStageableChanges) {
			t.Errorf("expected ErrNoStageableChanges, got: %v", err)
		}
		// excluded file must remain untracked (repo unchanged)
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = dir
		out, _ := cmd.Output()
		if len(out) == 0 {
			t.Error("expected dirty state; excluded file should still be untracked")
		}
	})

	t.Run("mixed scope commits only code file", func(t *testing.T) {
		// .agent file already exists from previous sub-test; add a new code file.
		write("internal/bar.go", "package bar")
		sha, err := git.Commit(ctx, "feat: add bar")
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if !isValidSHA(sha) {
			t.Errorf("bad SHA: %q", sha)
		}
		// .agent/tasks/TASK-99.md should still be untracked
		cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", ".agent/tasks/TASK-99.md")
		cmd.Dir = dir
		out, _ := cmd.Output()
		if len(out) == 0 {
			t.Error(".agent/tasks/TASK-99.md should remain untracked after mixed-scope commit")
		}
	})

	t.Run("lock file excluded", func(t *testing.T) {
		write("internal/baz.go", "package baz")
		write("package-lock.json", "{}")
		sha, err := git.Commit(ctx, "feat: add baz")
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if !isValidSHA(sha) {
			t.Errorf("bad SHA: %q", sha)
		}
		// lock file should remain untracked
		cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "package-lock.json")
		cmd.Dir = dir
		out, _ := cmd.Output()
		if len(out) == 0 {
			t.Error("package-lock.json should remain untracked after commit")
		}
	})

	t.Run("nested node_modules excluded", func(t *testing.T) {
		write("internal/qux.go", "package qux")
		write("node_modules/some-pkg/bin.js", "// bin")
		sha, err := git.Commit(ctx, "feat: add qux")
		if err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if !isValidSHA(sha) {
			t.Errorf("bad SHA: %q", sha)
		}
		// node_modules entry should remain untracked
		cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard", "node_modules/some-pkg/bin.js")
		cmd.Dir = dir
		out, _ := cmd.Output()
		if len(out) == 0 {
			t.Error("node_modules/some-pkg/bin.js should remain untracked after commit")
		}
	})
}

// TestStripUnindexedMemoryDocs covers GH-4286: a task whose session commits a
// memory doc under .agent/knowledge/memories without indexing it in
// graph.json must have that doc stripped from the branch before push, so the
// PR never trips the Knowledge Graph Drift Gate (scripts/check-graph.py).
func TestStripUnindexedMemoryDocs(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	base, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// A graph.json must exist on the base branch for any of these scenarios to
	// be meaningful: without one there is no drift gate to protect, and the
	// guard intentionally no-ops (see the "no graph.json" case covered by
	// indexedMemoryPaths' fail-open behavior).
	write(".agent/knowledge/graph.json", `{"nodes":{"memories":{}}}`)
	run("add", ".agent/knowledge/graph.json")
	run("commit", "-m", "chore: seed empty graph.json")

	t.Run("strips an unindexed memory doc, keeps code commits", func(t *testing.T) {
		run("checkout", "-b", "pilot/GH-unindexed")

		write("internal/foo.go", "package foo")
		run("add", "internal/foo.go")
		run("commit", "-m", "feat: add foo")

		docRel := ".agent/knowledge/memories/learnings/mem_task_unindexed.md"
		write(docRel, "# a learning the session captured but never indexed")
		run("add", docRel)
		run("commit", "-m", "docs: capture learning")

		stripped, err := git.StripUnindexedMemoryDocs(ctx, base)
		if err != nil {
			t.Fatalf("StripUnindexedMemoryDocs failed: %v", err)
		}
		if len(stripped) != 1 || stripped[0] != docRel {
			t.Fatalf("stripped = %v, want [%s]", stripped, docRel)
		}

		if _, statErr := os.Stat(filepath.Join(dir, docRel)); !os.IsNotExist(statErr) {
			t.Errorf("expected %s removed from working tree, stat err = %v", docRel, statErr)
		}

		// The code commit must survive untouched.
		diffCmd := exec.Command("git", "diff", "--name-only", base+"...HEAD")
		diffCmd.Dir = dir
		out, err := diffCmd.Output()
		if err != nil {
			t.Fatalf("git diff failed: %v", err)
		}
		if !strings.Contains(string(out), "internal/foo.go") {
			t.Errorf("expected internal/foo.go still in branch diff, got: %s", out)
		}
		if strings.Contains(string(out), docRel) {
			t.Errorf("expected %s removed from branch diff, got: %s", docRel, out)
		}
	})

	t.Run("leaves an indexed memory doc alone", func(t *testing.T) {
		run("checkout", base)
		run("checkout", "-b", "pilot/GH-indexed")

		docRel := ".agent/knowledge/memories/learnings/mem_task_indexed.md"
		write(docRel, "# a learning the session captured and indexed")

		graph := fmt.Sprintf(`{"nodes":{"memories":{"mem-test":{"file":%q}}}}`, docRel)
		write(".agent/knowledge/graph.json", graph)

		run("add", docRel, ".agent/knowledge/graph.json")
		run("commit", "-m", "docs: capture and index learning")

		stripped, err := git.StripUnindexedMemoryDocs(ctx, base)
		if err != nil {
			t.Fatalf("StripUnindexedMemoryDocs failed: %v", err)
		}
		if len(stripped) != 0 {
			t.Fatalf("stripped = %v, want none (doc is indexed)", stripped)
		}
		if _, statErr := os.Stat(filepath.Join(dir, docRel)); statErr != nil {
			t.Errorf("expected %s to remain, stat err = %v", docRel, statErr)
		}
	})

	t.Run("no-op when no memory docs were added", func(t *testing.T) {
		run("checkout", base)
		run("checkout", "-b", "pilot/GH-plain")

		write("internal/bar.go", "package bar")
		run("add", "internal/bar.go")
		run("commit", "-m", "feat: add bar")

		stripped, err := git.StripUnindexedMemoryDocs(ctx, base)
		if err != nil {
			t.Fatalf("StripUnindexedMemoryDocs failed: %v", err)
		}
		if len(stripped) != 0 {
			t.Fatalf("stripped = %v, want none", stripped)
		}
	})
}

// TestRestoreDeletedIndexedMemoryDocs covers GH-4398: the restore leg of the
// GH-4387 protected-memory guard. A session that deletes a memory doc still
// referenced by .agent/knowledge/graph.json must have that file restored via
// `git checkout <base> -- <path>` and staged into a follow-up commit, so the
// PR never trips the Knowledge Graph Drift Gate; deleting a genuinely
// unindexed doc must remain allowed.
func TestRestoreDeletedIndexedMemoryDocs(t *testing.T) {
	dir, _ := initTestRepo(t)
	ctx := context.Background()
	git := NewGitOperations(dir)

	base, err := git.GetCurrentBranch(ctx)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	t.Run("restores an indexed memory doc deleted via the file key", func(t *testing.T) {
		docRel := ".agent/knowledge/memories/pitfalls/mem_task_indexed.md"
		graph := fmt.Sprintf(`{"nodes":{"memories":{"mem-restore-1":{"file":%q}}}}`, docRel)
		write(docRel, "# an indexed pitfall the branch base already carries")
		write(".agent/knowledge/graph.json", graph)
		run("add", docRel, ".agent/knowledge/graph.json")
		run("commit", "-m", "chore: seed indexed memory doc on base")
		base2, err := git.GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch failed: %v", err)
		}

		run("checkout", "-b", "pilot/GH-restore-indexed")
		run("rm", docRel)
		run("commit", "-m", "chore(memory): strip what looked like an unused doc")

		if _, statErr := os.Stat(filepath.Join(dir, docRel)); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s deleted before restore, stat err = %v", docRel, statErr)
		}

		restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, base2)
		if err != nil {
			t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
		}
		if len(restored) != 1 || restored[0].Path != docRel || restored[0].NodeID != "mem-restore-1" {
			t.Fatalf("restored = %+v, want [{%s mem-restore-1}]", restored, docRel)
		}

		content, statErr := os.ReadFile(filepath.Join(dir, docRel))
		if statErr != nil {
			t.Fatalf("expected %s restored to disk, err = %v", docRel, statErr)
		}
		if string(content) != "# an indexed pitfall the branch base already carries" {
			t.Errorf("restored content = %q, want original base content", content)
		}

		// The restoration must be committed so it rides the same PR/branch.
		hasChanges, err := git.HasUncommittedChanges(ctx)
		if err != nil {
			t.Fatalf("HasUncommittedChanges failed: %v", err)
		}
		if hasChanges {
			t.Error("expected restoration committed, working tree still dirty")
		}
		diffCmd := exec.Command("git", "diff", "--name-only", base2+"...HEAD")
		diffCmd.Dir = dir
		out, err := diffCmd.Output()
		if err != nil {
			t.Fatalf("git diff failed: %v", err)
		}
		if strings.Contains(string(out), docRel) {
			t.Errorf("expected %s to have zero net diff vs base after restore, got: %s", docRel, out)
		}
	})

	t.Run("leaves a genuinely unindexed memory doc deletion alone", func(t *testing.T) {
		run("checkout", base)
		docRel := ".agent/knowledge/memories/learnings/mem_task_unindexed.md"
		write(docRel, "# never indexed")
		write(".agent/knowledge/graph.json", `{"nodes":{"memories":{}}}`)
		run("add", docRel, ".agent/knowledge/graph.json")
		run("commit", "-m", "chore: seed unindexed memory doc on base")
		base3, err := git.GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch failed: %v", err)
		}

		run("checkout", "-b", "pilot/GH-restore-unindexed")
		run("rm", docRel)
		run("commit", "-m", "chore(memory): strip unindexed doc")

		restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, base3)
		if err != nil {
			t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
		}
		if len(restored) != 0 {
			t.Fatalf("restored = %v, want none (doc was never indexed)", restored)
		}
		if _, statErr := os.Stat(filepath.Join(dir, docRel)); !os.IsNotExist(statErr) {
			t.Errorf("expected %s to remain deleted, stat err = %v", docRel, statErr)
		}
	})

	t.Run("protects a legacy path-key node", func(t *testing.T) {
		run("checkout", base)
		docRel := ".agent/knowledge/memories/pitfalls/mem_task_legacy.md"
		write(docRel, "# indexed via the legacy path key")
		write(".agent/knowledge/graph.json", `{"nodes":{"memories":{"mem-legacy-1":{"path":"memories/pitfalls/mem_task_legacy.md"}}}}`)
		run("add", docRel, ".agent/knowledge/graph.json")
		run("commit", "-m", "chore: seed legacy-path-indexed memory doc on base")
		base4, err := git.GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch failed: %v", err)
		}

		run("checkout", "-b", "pilot/GH-restore-legacy")
		run("rm", docRel)
		run("commit", "-m", "chore(memory): strip legacy-indexed doc")

		restored, err := git.RestoreDeletedIndexedMemoryDocs(ctx, base4)
		if err != nil {
			t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
		}
		if len(restored) != 1 || restored[0].Path != docRel || restored[0].NodeID != "mem-legacy-1" {
			t.Fatalf("restored = %+v, want [{%s mem-legacy-1}]", restored, docRel)
		}
		if _, statErr := os.Stat(filepath.Join(dir, docRel)); statErr != nil {
			t.Errorf("expected %s restored to disk, err = %v", docRel, statErr)
		}
	})

	t.Run("no-op when graph.json is absent at base", func(t *testing.T) {
		noGraphDir := t.TempDir()
		runIn := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...)
			cmd.Dir = noGraphDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		writeIn := func(rel, content string) {
			t.Helper()
			full := filepath.Join(noGraphDir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatalf("mkdir for %s: %v", rel, err)
			}
			if err := os.WriteFile(full, []byte(content), 0644); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}

		runIn("init")
		runIn("config", "user.email", "test@test.com")
		runIn("config", "user.name", "Test User")
		docRel := ".agent/knowledge/memories/pitfalls/mem_task_no_graph.md"
		writeIn(docRel, "# no graph.json in this project")
		runIn("add", docRel)
		runIn("commit", "-m", "chore: seed doc, no graph.json")
		noGraphGit := NewGitOperations(noGraphDir)
		noGraphBase, err := noGraphGit.GetCurrentBranch(ctx)
		if err != nil {
			t.Fatalf("GetCurrentBranch failed: %v", err)
		}

		runIn("checkout", "-b", "pilot/GH-restore-no-graph")
		runIn("rm", docRel)
		runIn("commit", "-m", "chore(memory): strip doc, no graph.json to protect")

		restored, err := noGraphGit.RestoreDeletedIndexedMemoryDocs(ctx, noGraphBase)
		if err != nil {
			t.Fatalf("RestoreDeletedIndexedMemoryDocs failed: %v", err)
		}
		if len(restored) != 0 {
			t.Fatalf("restored = %v, want none (no graph.json to protect)", restored)
		}
	})
}

// TestCreateRecoveryRef verifies GH-3785's recovery-ref mechanism: a commit
// pinned under refs/pilot-recovery/<taskID> resolves to the expected sha,
// survives being looked up independent of any branch, and sanitizes the
// task ID into a safe ref path.
func TestCreateRecoveryRef(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir, err := os.MkdirTemp("", "pilot-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	ctx := context.Background()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "init").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "config", "user.name", "Test User").Run()
	_ = os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0644)
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "add", ".").Run()
	_ = exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "-m", "initial").Run()

	headSHA, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("failed to resolve HEAD: %v", err)
	}
	wantSHA := trimNewline(string(headSHA))

	git := NewGitOperations(tmpDir)

	t.Run("pins HEAD under a stable ref name", func(t *testing.T) {
		refName, err := git.CreateRecoveryRef(ctx, "GH-3764", "HEAD")
		if err != nil {
			t.Fatalf("CreateRecoveryRef failed: %v", err)
		}
		if refName != "refs/pilot-recovery/GH-3764" {
			t.Errorf("refName = %q, want refs/pilot-recovery/GH-3764", refName)
		}

		resolved, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", refName).Output()
		if err != nil {
			t.Fatalf("recovery ref did not resolve: %v", err)
		}
		if trimNewline(string(resolved)) != wantSHA {
			t.Errorf("recovery ref resolved to %q, want %q", trimNewline(string(resolved)), wantSHA)
		}
	})

	t.Run("sanitizes unsafe task IDs", func(t *testing.T) {
		refName, err := git.CreateRecoveryRef(ctx, "weird/task id!", "HEAD")
		if err != nil {
			t.Fatalf("CreateRecoveryRef failed: %v", err)
		}
		if strings.ContainsAny(refName, " !") {
			t.Errorf("refName not sanitized: %q", refName)
		}
	})

	t.Run("defaults empty fromRef to HEAD", func(t *testing.T) {
		refName, err := git.CreateRecoveryRef(ctx, "GH-9000", "")
		if err != nil {
			t.Fatalf("CreateRecoveryRef failed: %v", err)
		}
		resolved, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", refName).Output()
		if err != nil {
			t.Fatalf("recovery ref did not resolve: %v", err)
		}
		if trimNewline(string(resolved)) != wantSHA {
			t.Errorf("recovery ref resolved to %q, want %q", trimNewline(string(resolved)), wantSHA)
		}
	})

	t.Run("fails cleanly on invalid fromRef", func(t *testing.T) {
		_, err := git.CreateRecoveryRef(ctx, "GH-9001", "refs/heads/does-not-exist")
		if err == nil {
			t.Fatal("expected error for nonexistent fromRef, got nil")
		}
		if !strings.Contains(err.Error(), "failed to create recovery ref") {
			t.Errorf("error missing context: %v", err)
		}
	})
}

func trimNewline(s string) string {
	return strings.TrimRight(s, "\n")
}
