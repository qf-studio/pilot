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
			input: "a pull request for branch `feature` into `main` already exists:\nhttps://github.com/alekspetrov/pilot/pull/285",
			want:  "https://github.com/alekspetrov/pilot/pull/285",
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
