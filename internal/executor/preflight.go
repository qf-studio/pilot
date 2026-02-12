package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// PreflightCheck represents a single pre-execution health check.
// GH-915: Pre-flight checks catch environmental issues early before wasting time on execution.
type PreflightCheck struct {
	Name        string
	Description string
	Check       func(ctx context.Context, projectPath string) error
}

// DefaultPreflightChecks returns the standard set of pre-flight checks.
var DefaultPreflightChecks = []PreflightCheck{
	{
		Name:        "claude_available",
		Description: "Verify Claude Code CLI is available",
		Check:       checkClaudeAvailable,
	},
	{
		Name:        "git_clean",
		Description: "Verify git working directory is clean",
		Check:       checkGitClean,
	},
	{
		Name:        "git_repo",
		Description: "Verify directory is a git repository",
		Check:       checkGitRepo,
	},
}

// RunPreflightChecks executes all default pre-flight checks.
// Returns the first error encountered, or nil if all checks pass.
func RunPreflightChecks(ctx context.Context, projectPath string) error {
	return RunPreflightChecksCustom(ctx, projectPath, DefaultPreflightChecks)
}

// RunPreflightChecksCustom executes a custom set of pre-flight checks.
func RunPreflightChecksCustom(ctx context.Context, projectPath string, checks []PreflightCheck) error {
	for _, check := range checks {
		if err := check.Check(ctx, projectPath); err != nil {
			return &PreflightError{
				CheckName: check.Name,
				Err:       err,
			}
		}
	}
	return nil
}

// PreflightError represents a failed pre-flight check.
type PreflightError struct {
	CheckName string
	Err       error
}

func (e *PreflightError) Error() string {
	return fmt.Sprintf("preflight check %q failed: %v", e.CheckName, e.Err)
}

func (e *PreflightError) Unwrap() error {
	return e.Err
}

// checkClaudeAvailable verifies the claude CLI is installed and accessible.
func checkClaudeAvailable(ctx context.Context, _ string) error {
	cmd := exec.CommandContext(ctx, "claude", "--version")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude command not available: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// checkGitClean verifies the git working directory has no uncommitted changes.
// This prevents execution from accidentally including unrelated changes.
func checkGitClean(ctx context.Context, projectPath string) error {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	changes := strings.TrimSpace(string(output))
	if len(changes) > 0 {
		// Count number of changed files
		lines := strings.Split(changes, "\n")
		return fmt.Errorf("working directory has %d uncommitted change(s): run 'git stash' or 'git commit' first", len(lines))
	}
	return nil
}

// checkGitRepo verifies the directory is a valid git repository.
func checkGitRepo(ctx context.Context, projectPath string) error {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("not a git repository: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
