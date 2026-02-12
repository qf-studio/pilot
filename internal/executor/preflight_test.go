package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckClaudeAvailable(t *testing.T) {
	ctx := context.Background()

	// This test assumes claude CLI is installed
	// If not installed, the test verifies the error handling
	err := checkClaudeAvailable(ctx, "")
	if err != nil {
		// Check it's a meaningful error
		if !strings.Contains(err.Error(), "claude") {
			t.Errorf("expected error to mention 'claude', got: %v", err)
		}
	}
	// If no error, claude is installed and working
}

func TestCheckGitRepo(t *testing.T) {
	ctx := context.Background()

	// Test with actual git repo (the pilot project itself)
	t.Run("valid_git_repo", func(t *testing.T) {
		// Use current working directory which should be the pilot repo
		cwd, _ := os.Getwd()
		// Walk up to find .git
		for {
			if _, err := os.Stat(filepath.Join(cwd, ".git")); err == nil {
				break
			}
			parent := filepath.Dir(cwd)
			if parent == cwd {
				t.Skip("not running in a git repository")
			}
			cwd = parent
		}

		err := checkGitRepo(ctx, cwd)
		if err != nil {
			t.Errorf("expected no error for valid git repo, got: %v", err)
		}
	})

	// Test with non-git directory
	t.Run("non_git_dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		err := checkGitRepo(ctx, tmpDir)
		if err == nil {
			t.Error("expected error for non-git directory")
		}
		if !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("expected error to contain 'not a git repository', got: %v", err)
		}
	})
}

func TestCheckGitClean(t *testing.T) {
	ctx := context.Background()

	// Create a temp git repo for testing
	tmpDir := t.TempDir()
	if err := exec.Command("git", "init", tmpDir).Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Configure git user for commits
	_ = exec.Command("git", "-C", tmpDir, "config", "user.email", "test@test.com").Run()
	_ = exec.Command("git", "-C", tmpDir, "config", "user.name", "Test").Run()

	// Test clean repo (empty is considered clean)
	t.Run("clean_repo", func(t *testing.T) {
		err := checkGitClean(ctx, tmpDir)
		if err != nil {
			t.Errorf("expected no error for clean repo, got: %v", err)
		}
	})

	// Create uncommitted file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	t.Run("dirty_repo", func(t *testing.T) {
		err := checkGitClean(ctx, tmpDir)
		if err == nil {
			t.Error("expected error for dirty repo")
		}
		if !strings.Contains(err.Error(), "uncommitted change") {
			t.Errorf("expected error to mention uncommitted changes, got: %v", err)
		}
	})
}

func TestRunPreflightChecks(t *testing.T) {
	ctx := context.Background()

	// Test with a failing check
	t.Run("failing_check", func(t *testing.T) {
		checks := []PreflightCheck{
			{
				Name:        "always_fail",
				Description: "Always fails",
				Check: func(ctx context.Context, projectPath string) error {
					return errors.New("intentional failure")
				},
			},
		}

		err := RunPreflightChecksCustom(ctx, "", checks)
		if err == nil {
			t.Error("expected error from failing check")
		}

		var preflightErr *PreflightError
		if !errors.As(err, &preflightErr) {
			t.Errorf("expected PreflightError, got: %T", err)
		} else if preflightErr.CheckName != "always_fail" {
			t.Errorf("expected check name 'always_fail', got: %s", preflightErr.CheckName)
		}
	})

	// Test with all passing checks
	t.Run("all_pass", func(t *testing.T) {
		checks := []PreflightCheck{
			{
				Name: "pass1",
				Check: func(ctx context.Context, projectPath string) error {
					return nil
				},
			},
			{
				Name: "pass2",
				Check: func(ctx context.Context, projectPath string) error {
					return nil
				},
			},
		}

		err := RunPreflightChecksCustom(ctx, "", checks)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	// Test stops at first failure
	t.Run("stops_at_first_failure", func(t *testing.T) {
		checkOrder := []string{}
		checks := []PreflightCheck{
			{
				Name: "first",
				Check: func(ctx context.Context, projectPath string) error {
					checkOrder = append(checkOrder, "first")
					return errors.New("first failed")
				},
			},
			{
				Name: "second",
				Check: func(ctx context.Context, projectPath string) error {
					checkOrder = append(checkOrder, "second")
					return nil
				},
			},
		}

		_ = RunPreflightChecksCustom(ctx, "", checks)
		if len(checkOrder) != 1 || checkOrder[0] != "first" {
			t.Errorf("expected only 'first' to run, got: %v", checkOrder)
		}
	})
}

func TestPreflightError(t *testing.T) {
	innerErr := errors.New("inner error")
	err := &PreflightError{
		CheckName: "test_check",
		Err:       innerErr,
	}

	// Test Error()
	errStr := err.Error()
	if !strings.Contains(errStr, "test_check") {
		t.Errorf("error string should contain check name, got: %s", errStr)
	}
	if !strings.Contains(errStr, "inner error") {
		t.Errorf("error string should contain inner error, got: %s", errStr)
	}

	// Test Unwrap()
	if err.Unwrap() != innerErr {
		t.Errorf("Unwrap() should return inner error")
	}
}
