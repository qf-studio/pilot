package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitOperations handles git operations for tasks
type GitOperations struct {
	projectPath string
}

// NewGitOperations creates new git operations for a project
func NewGitOperations(projectPath string) *GitOperations {
	return &GitOperations{projectPath: projectPath}
}

// CreateBranch creates a new branch
func (g *GitOperations) CreateBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create branch: %w: %s", err, output)
	}
	return nil
}

// SwitchBranch switches to an existing branch
func (g *GitOperations) SwitchBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to switch branch: %w: %s", err, output)
	}
	return nil
}

// Commit stages all changes and commits
func (g *GitOperations) Commit(ctx context.Context, message string) (string, error) {
	// Stage all changes
	stageCmd := exec.CommandContext(ctx, "git", "add", "-A")
	stageCmd.Dir = g.projectPath
	if output, err := stageCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to stage changes: %w: %s", err, output)
	}

	// Commit
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-m", message)
	commitCmd.Dir = g.projectPath
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to commit: %w: %s", err, output)
	}

	// Get commit SHA
	shaCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	shaCmd.Dir = g.projectPath
	output, err := shaCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get commit SHA: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// Push pushes the current branch to remote
func (g *GitOperations) Push(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branchName)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push: %w: %s", err, output)
	}
	return nil
}

// CreatePR creates a pull request using gh CLI
func (g *GitOperations) CreatePR(ctx context.Context, title, body, baseBranch string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
	)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	if err != nil {
		// Check if PR already exists - gh returns exit 1 but includes URL in output
		if strings.Contains(outputStr, "already exists") {
			if url := extractPRURL(outputStr); url != "" {
				return url, nil
			}
		}
		return "", fmt.Errorf("failed to create PR: %w: %s", err, output)
	}

	// Extract PR URL from output
	prURL := strings.TrimSpace(outputStr)
	return prURL, nil
}

// extractPRURL extracts a GitHub PR URL from text
func extractPRURL(text string) string {
	// Look for GitHub PR URL pattern: https://github.com/owner/repo/pull/123
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "github.com") && strings.Contains(line, "/pull/") {
			// Extract just the URL if there's other text
			if idx := strings.Index(line, "https://"); idx >= 0 {
				url := line[idx:]
				// Trim any trailing text after the URL
				if spaceIdx := strings.IndexAny(url, " \t\n"); spaceIdx > 0 {
					url = url[:spaceIdx]
				}
				return url
			}
		}
	}
	return ""
}

// GetCurrentBranch returns the current branch name
func (g *GitOperations) GetCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// GetDefaultBranch returns the default branch (main or master)
func (g *GitOperations) GetDefaultBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		// Fallback to checking for main or master
		if g.branchExists(ctx, "main") {
			return "main", nil
		}
		if g.branchExists(ctx, "master") {
			return "master", nil
		}
		return "", fmt.Errorf("could not determine default branch: %w", err)
	}

	ref := strings.TrimSpace(string(output))
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1], nil
}

// branchExists checks if a branch exists
func (g *GitOperations) branchExists(ctx context.Context, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = g.projectPath
	return cmd.Run() == nil
}

// GetChangedFiles returns list of changed files
func (g *GitOperations) GetChangedFiles(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		return []string{}, nil
	}
	return files, nil
}

// HasUncommittedChanges checks if there are uncommitted changes
func (g *GitOperations) HasUncommittedChanges(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check status: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// PushToMain pushes changes directly to the main/default branch
func (g *GitOperations) PushToMain(ctx context.Context) error {
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main"
	}
	cmd := exec.CommandContext(ctx, "git", "push", "origin", defaultBranch)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to push to %s: %w: %s", defaultBranch, err, output)
	}
	return nil
}

// CountNewCommits returns the number of commits on the current branch
// that are not on the base branch. Uses `git rev-list --count base..HEAD`.
// Returns 0 if the base branch doesn't exist or there are no new commits.
func (g *GitOperations) CountNewCommits(ctx context.Context, baseBranch string) (int, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-list", "--count", baseBranch+"..HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to count new commits: %w", err)
	}
	countStr := strings.TrimSpace(string(output))
	var count int
	if _, parseErr := fmt.Sscanf(countStr, "%d", &count); parseErr != nil {
		return 0, fmt.Errorf("failed to parse commit count %q: %w", countStr, parseErr)
	}
	return count, nil
}

// GetCurrentCommitSHA returns the SHA of the current HEAD commit
func (g *GitOperations) GetCurrentCommitSHA(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = g.projectPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current commit SHA: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// Pull fetches and merges changes from remote for the specified branch
func (g *GitOperations) Pull(ctx context.Context, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "pull", "origin", branch)
	cmd.Dir = g.projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to pull %s: %w: %s", branch, err, output)
	}
	return nil
}

// SwitchToDefaultBranchAndPull switches to the default branch and pulls latest changes.
// This ensures new branches are created from the latest default branch, not from
// whatever branch was previously checked out (fixes GH-279).
func (g *GitOperations) SwitchToDefaultBranchAndPull(ctx context.Context) (string, error) {
	// Get default branch name
	defaultBranch, err := g.GetDefaultBranch(ctx)
	if err != nil {
		defaultBranch = "main" // fallback
	}

	// Switch to default branch
	if err := g.SwitchBranch(ctx, defaultBranch); err != nil {
		return defaultBranch, fmt.Errorf("failed to switch to %s: %w", defaultBranch, err)
	}

	// Pull latest changes
	if err := g.Pull(ctx, defaultBranch); err != nil {
		// Pull failure is non-fatal - we can still create branch from local state
		// This handles offline scenarios or repos without upstream configured
		return defaultBranch, nil
	}

	return defaultBranch, nil
}
