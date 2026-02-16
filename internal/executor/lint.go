package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LintResult contains the result of a lint check
type LintResult struct {
	Clean    bool
	Linter   string
	Issues   []string
	FixedAll bool
}

// runLintCheck detects and runs the appropriate linter for the project
func (g *GitOperations) runLintCheck(ctx context.Context) *LintResult {
	result := &LintResult{
		Clean:  true,
		Issues: []string{},
	}

	// Detect linter config (Go projects)
	if hasGoLintConfig(g.projectPath) {
		result.Linter = "golangci-lint"
		// Check for lint issues on changed files only
		output, err := g.runGoLint(ctx)
		if err != nil {
			result.Clean = false
			result.Issues = []string{output}
		}
	}
	// Could extend with eslint, ruff, etc. in the future

	return result
}

// autoFixLint attempts to auto-fix linting issues
func (g *GitOperations) autoFixLint(ctx context.Context) *LintResult {
	result := g.runLintCheck(ctx)
	if result.Clean {
		return result
	}

	if result.Linter == "golangci-lint" {
		// Attempt auto-fix
		fixCmd := exec.CommandContext(ctx, "golangci-lint", "run", "--fix", "./...")
		fixCmd.Dir = g.projectPath
		output, err := fixCmd.CombinedOutput()
		if err == nil {
			// Verify fixes worked
			verifyResult := g.runLintCheck(ctx)
			if verifyResult.Clean {
				// Stage and amend the last commit
				if amendErr := g.stageAndAmendCommit(ctx, "[lint fix] Auto-fixed linting issues"); amendErr != nil {
					result.Issues = []string{fmt.Sprintf("failed to amend commit with lint fixes: %v", amendErr)}
				} else {
					result.FixedAll = true
					result.Clean = true
					result.Issues = []string{}
				}
			} else {
				result.Issues = verifyResult.Issues
			}
		} else {
			result.Issues = []string{fmt.Sprintf("golangci-lint --fix failed: %s", output)}
		}
	}

	return result
}

// runGoLint runs golangci-lint on changed files
func (g *GitOperations) runGoLint(ctx context.Context) (string, error) {
	// Run golangci-lint on changed files from origin/main
	cmd := exec.CommandContext(ctx, "golangci-lint", "run",
		"--new-from-rev=origin/main",
		"./...",
	)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		// Exit code 1 means issues found
		return outputStr, err
	}
	return outputStr, nil
}

// stageAndAmendCommit stages all changes and amends the last commit
func (g *GitOperations) stageAndAmendCommit(ctx context.Context, message string) error {
	// Stage all changes
	stageCmd := exec.CommandContext(ctx, "git", "add", "-A")
	stageCmd.Dir = g.projectPath
	if _, err := stageCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to stage changes: %w", err)
	}

	// Amend commit
	amendCmd := exec.CommandContext(ctx, "git", "commit", "--amend", "--no-edit")
	amendCmd.Dir = g.projectPath
	if _, err := amendCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to amend commit: %w", err)
	}

	return nil
}

// hasGoLintConfig checks if the project has golangci-lint config
func hasGoLintConfig(projectPath string) bool {
	// Check for common golangci-lint config file names
	configFiles := []string{
		".golangci.yml",
		".golangci.yaml",
		".golangci.toml",
		"golangci.yml",
		"golangci.yaml",
		"golangci.toml",
	}

	for _, cf := range configFiles {
		if lintFileExists(filepath.Join(projectPath, cf)) {
			return true
		}
	}

	// Also check for go.mod which indicates a Go project
	return lintFileExists(filepath.Join(projectPath, "go.mod"))
}

// lintFileExists checks if a file exists
func lintFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
