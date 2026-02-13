package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WorktreeManager handles git worktree creation and cleanup for isolated task execution.
// GH-936: Enables Pilot to work in repos where users have uncommitted changes.
type WorktreeManager struct {
	repoPath string
	mu       sync.Mutex
	active   map[string]string // taskID -> worktreePath
}

// NewWorktreeManager creates a worktree manager for the given repository.
func NewWorktreeManager(repoPath string) *WorktreeManager {
	return &WorktreeManager{
		repoPath: repoPath,
		active:   make(map[string]string),
	}
}

// WorktreeResult contains the worktree path and cleanup function.
// The Cleanup function MUST be called when execution completes (success or failure).
type WorktreeResult struct {
	Path    string
	Cleanup func()
}

// CreateWorktree creates an isolated worktree for task execution.
// Returns a WorktreeResult containing the path and a cleanup function.
//
// CRITICAL: The cleanup function is safe to call multiple times and handles:
// - Normal completion
// - Context cancellation
// - Panic recovery (via defer in caller)
// - Process termination (best-effort via runtime finalizer)
//
// Usage:
//
//	result, err := manager.CreateWorktree(ctx, taskID)
//	if err != nil {
//	    return err
//	}
//	defer result.Cleanup() // Always cleanup, even on panic
//
//	// ... use result.Path for execution ...
func (m *WorktreeManager) CreateWorktree(ctx context.Context, taskID string) (*WorktreeResult, error) {
	// Generate unique worktree path using taskID and timestamp to handle concurrent tasks
	worktreeName := fmt.Sprintf("pilot-worktree-%s-%d", sanitizeBranchName(taskID), time.Now().UnixNano())
	worktreePath := filepath.Join(os.TempDir(), worktreeName)

	// Create worktree from HEAD (current commit of default branch)
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "HEAD")
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w: %s", err, output)
	}

	// Track active worktree
	m.mu.Lock()
	m.active[taskID] = worktreePath
	m.mu.Unlock()

	// Create cleanup function with panic-safe, idempotent behavior
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			m.cleanupWorktree(taskID, worktreePath)
		})
	}

	return &WorktreeResult{
		Path:    worktreePath,
		Cleanup: cleanup,
	}, nil
}

// cleanupWorktree removes a worktree and cleans up tracking state.
// This is called by the cleanup function returned from CreateWorktree.
func (m *WorktreeManager) cleanupWorktree(taskID, worktreePath string) {
	// Remove from tracking first
	m.mu.Lock()
	delete(m.active, taskID)
	m.mu.Unlock()

	// Remove the git worktree reference
	// Use --force to handle any uncommitted changes in the worktree
	removeCmd := exec.Command("git", "-C", m.repoPath, "worktree", "remove", "--force", worktreePath)
	_ = removeCmd.Run() // Ignore error - worktree may already be removed

	// Belt and suspenders: also remove the directory if it still exists
	// This handles edge cases where git worktree remove didn't fully clean up
	_ = os.RemoveAll(worktreePath)

	// Prune stale worktree references from git
	pruneCmd := exec.Command("git", "-C", m.repoPath, "worktree", "prune")
	_ = pruneCmd.Run()
}

// CleanupAll removes all active worktrees managed by this instance.
// Useful for graceful shutdown or error recovery.
func (m *WorktreeManager) CleanupAll() {
	m.mu.Lock()
	active := make(map[string]string)
	for k, v := range m.active {
		active[k] = v
	}
	m.mu.Unlock()

	for taskID, path := range active {
		m.cleanupWorktree(taskID, path)
	}
}

// ActiveCount returns the number of active worktrees.
func (m *WorktreeManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.active)
}

// sanitizeBranchName converts a task ID into a safe worktree directory name.
func sanitizeBranchName(taskID string) string {
	result := make([]byte, 0, len(taskID))
	for i := 0; i < len(taskID); i++ {
		c := taskID[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result = append(result, c)
		} else {
			result = append(result, '-')
		}
	}
	return string(result)
}

// CreateWorktreeWithBranch creates an isolated worktree with a proper branch (not detached HEAD).
// This is the preferred method when the worktree needs to push to remote, as detached HEAD
// makes push operations more complex.
//
// The branch is created from the specified baseBranch (e.g., "main").
// If baseBranch is empty, HEAD is used.
//
// GH-1016: Uses atomic creation with retry to handle race conditions when two Pilots
// try to create the same branch simultaneously. Uses git worktree add -B to force
// create/reset the branch.
//
// Usage:
//
//	result, err := manager.CreateWorktreeWithBranch(ctx, taskID, "pilot/GH-123", "main")
//	if err != nil {
//	    return err
//	}
//	defer result.Cleanup()
//
//	// Worktree is on branch "pilot/GH-123", ready for commits and push
func (m *WorktreeManager) CreateWorktreeWithBranch(ctx context.Context, taskID, branchName, baseBranch string) (*WorktreeResult, error) {
	// Generate unique worktree path
	worktreeName := fmt.Sprintf("pilot-worktree-%s-%d", sanitizeBranchName(taskID), time.Now().UnixNano())
	worktreePath := filepath.Join(os.TempDir(), worktreeName)

	// Determine base ref
	baseRef := "HEAD"
	if baseBranch != "" {
		baseRef = baseBranch
	}

	// GH-963: Clean up any stale worktree for this branch before creating.
	// This handles retries where previous cleanup failed to fully remove the worktree reference.
	m.cleanupStaleWorktreeForBranch(ctx, branchName)

	// GH-1016: Atomic branch creation with retry loop
	// Uses -B flag to force create/reset branch, avoiding TOCTOU race conditions
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms
			sleepDuration := time.Duration(100<<attempt) * time.Millisecond
			time.Sleep(sleepDuration)
			// Clean up any stale state before retry
			m.cleanupStaleWorktreeForBranch(ctx, branchName)
		}

		// Use -B to force create/reset the branch atomically
		// git worktree add -B <branch> <path> <base>
		// -B creates the branch if it doesn't exist, or resets it if it does
		cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-B", branchName, worktreePath, baseRef)
		cmd.Dir = m.repoPath
		output, err := cmd.CombinedOutput()

		if err == nil {
			// Success - track and return
			m.mu.Lock()
			m.active[taskID] = worktreePath
			m.mu.Unlock()

			var cleanupOnce sync.Once
			cleanup := func() {
				cleanupOnce.Do(func() {
					m.cleanupWorktreeAndBranch(taskID, worktreePath, branchName)
				})
			}

			return &WorktreeResult{
				Path:    worktreePath,
				Cleanup: cleanup,
			}, nil
		}

		// Handle specific error cases
		outputStr := string(output)
		if strings.Contains(outputStr, "already checked out") ||
			strings.Contains(outputStr, "is already used by worktree") ||
			strings.Contains(outputStr, "failed to read") && strings.Contains(outputStr, "commondir") {
			// Branch is in use by another worktree, or Git race condition with commondir
			// The commondir error occurs when multiple worktrees are created concurrently
			// due to Git's internal lock file handling not being fully thread-safe
			lastErr = fmt.Errorf("worktree conflict (attempt %d/%d): %s", attempt+1, maxRetries, outputStr)
			continue
		}

		// Non-retryable error
		return nil, fmt.Errorf("failed to create worktree with branch: %w: %s", err, output)
	}

	return nil, fmt.Errorf("failed to create worktree after %d attempts: %w", maxRetries, lastErr)
}

// cleanupWorktreeAndBranch removes a worktree, its branch, and cleans up tracking state.
// The branch is also deleted since it was created specifically for this worktree.
func (m *WorktreeManager) cleanupWorktreeAndBranch(taskID, worktreePath, branchName string) {
	// First remove the worktree
	m.cleanupWorktree(taskID, worktreePath)

	// Then delete the local branch (it was created for this worktree)
	// Use -D to force delete even if not merged
	deleteCmd := exec.Command("git", "-C", m.repoPath, "branch", "-D", branchName)
	_ = deleteCmd.Run() // Ignore error - branch may have been pushed and deleted elsewhere
}

// cleanupStaleWorktreeForBranch removes any stale worktree reference for the given branch.
// GH-963: When a task fails and Pilot retries, the worktree cleanup may have failed to fully
// remove the reference in .git/worktrees/. This leaves git thinking the branch is still in use
// by another worktree, causing "is already used by worktree" errors on retry.
//
// GH-1017: Enhanced cleanup with additional steps:
// 1. Run git worktree prune -v first to clean up orphaned refs
// 2. Scan /tmp/pilot-worktree-* for orphaned directories
// 3. Delete stale branch refs only if no commits ahead of main
//
// This function is best-effort: errors are ignored since we're just trying to clean up
// stale state before creating a new worktree.
func (m *WorktreeManager) cleanupStaleWorktreeForBranch(ctx context.Context, branchName string) {
	// GH-1017: Run prune first to clean up any orphaned worktree references
	pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune", "-v")
	pruneCmd.Dir = m.repoPath
	_ = pruneCmd.Run()

	// GH-1017: Scan temp directory for orphaned pilot worktree directories
	// These may exist if Pilot crashed before cleanup
	tmpDir := os.TempDir()
	entries, err := os.ReadDir(tmpDir)
	if err == nil {
		branchSafe := sanitizeBranchName(branchName)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Match pilot-worktree-*-<branchSafe>-* pattern
			if strings.HasPrefix(name, "pilot-worktree-") && strings.Contains(name, branchSafe) {
				orphanPath := filepath.Join(tmpDir, name)
				// Try to remove the worktree via git first
				removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", orphanPath)
				removeCmd.Dir = m.repoPath
				_ = removeCmd.Run()
				// Then remove directory
				_ = os.RemoveAll(orphanPath)
			}
		}
	}

	// Get list of all worktrees in porcelain format
	listCmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	listCmd.Dir = m.repoPath
	output, err := listCmd.Output()
	if err != nil {
		return // Ignore - best effort cleanup
	}

	// Parse output to find worktree using this branch
	// Porcelain format:
	//   worktree /path/to/worktree
	//   HEAD abc123def456...
	//   branch refs/heads/pilot/GH-963
	//   <blank line>
	//   worktree /path/to/another
	//   ...
	lines := strings.Split(string(output), "\n")
	var staleWorktreePath string
	targetBranch := "branch refs/heads/" + branchName

	for i, line := range lines {
		if strings.TrimSpace(line) == targetBranch {
			// Found the branch - now find the worktree path (should be a few lines before)
			for j := i - 1; j >= 0; j-- {
				if strings.HasPrefix(lines[j], "worktree ") {
					staleWorktreePath = strings.TrimPrefix(lines[j], "worktree ")
					break
				}
			}
			break
		}
	}

	if staleWorktreePath == "" || staleWorktreePath == m.repoPath {
		// No stale worktree found, or it's the main repo (don't remove that!)
		return
	}

	// Found a stale worktree - remove it
	removeCmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", staleWorktreePath)
	removeCmd.Dir = m.repoPath
	_ = removeCmd.Run() // Ignore error - may already be partially removed

	// Belt and suspenders: also remove the directory if it still exists
	_ = os.RemoveAll(staleWorktreePath)

	// GH-1017: Clean up stale branch ref if it has no commits ahead of main
	// Check if branch has commits not in main
	revListCmd := exec.CommandContext(ctx, "git", "rev-list", "--count", "main.."+branchName)
	revListCmd.Dir = m.repoPath
	countOutput, err := revListCmd.Output()
	if err == nil {
		count := strings.TrimSpace(string(countOutput))
		if count == "0" {
			// Branch has no unique commits - safe to delete
			deleteCmd := exec.CommandContext(ctx, "git", "branch", "-D", branchName)
			deleteCmd.Dir = m.repoPath
			_ = deleteCmd.Run()
		}
	}

	// Final prune to clean up any remaining references
	finalPruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
	finalPruneCmd.Dir = m.repoPath
	_ = finalPruneCmd.Run()
}

// VerifyRemoteAccess checks that the worktree can access the remote.
// This is useful for pre-flight validation before long-running tasks.
func (m *WorktreeManager) VerifyRemoteAccess(ctx context.Context, worktreePath string) error {
	// Check that 'origin' remote exists and is accessible
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = worktreePath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remote 'origin' not accessible from worktree: %w: %s", err, output)
	}

	// Verify we can ls-remote (lightweight check without fetching)
	lsCmd := exec.CommandContext(ctx, "git", "ls-remote", "--exit-code", "origin", "HEAD")
	lsCmd.Dir = worktreePath
	if lsOutput, lsErr := lsCmd.CombinedOutput(); lsErr != nil {
		return fmt.Errorf("cannot reach remote 'origin': %w: %s", lsErr, lsOutput)
	}

	return nil
}

// CreateWorktree is a standalone helper function for simple use cases.
// Returns the worktree path and a cleanup function.
//
// CRITICAL: The cleanup function MUST be called via defer to ensure cleanup
// even on panic or early return.
//
// Usage:
//
//	worktreePath, cleanup, err := CreateWorktree(ctx, repoPath, taskID)
//	if err != nil {
//	    return err
//	}
//	defer cleanup() // ALWAYS defer cleanup immediately after creation
//
//	// ... use worktreePath for execution ...
func CreateWorktree(ctx context.Context, repoPath, taskID string) (string, func(), error) {
	manager := NewWorktreeManager(repoPath)
	result, err := manager.CreateWorktree(ctx, taskID)
	if err != nil {
		return "", nil, err
	}
	return result.Path, result.Cleanup, nil
}

// CreateWorktreeWithBranch is a standalone helper that creates a worktree with a branch.
// Returns the worktree path and a cleanup function.
//
// Use this when you need to push changes to remote, as it creates a proper branch
// instead of a detached HEAD state.
func CreateWorktreeWithBranch(ctx context.Context, repoPath, taskID, branchName, baseBranch string) (string, func(), error) {
	manager := NewWorktreeManager(repoPath)
	result, err := manager.CreateWorktreeWithBranch(ctx, taskID, branchName, baseBranch)
	if err != nil {
		return "", nil, err
	}
	return result.Path, result.Cleanup, nil
}

// CopyNavigatorToWorktree copies the .agent/ directory from the original repo to the worktree.
// This handles cases where .agent/ contains untracked content (common when .agent/ is gitignored).
//
// GH-936-4: Worktrees only contain tracked files from HEAD. If .agent/ has untracked content
// (like .context-markers/, research notes, or custom SOPs), they won't exist in the worktree.
// This function copies the entire .agent/ directory to ensure Navigator functionality.
//
// Behavior:
// - If .agent/ doesn't exist in source, returns nil (no-op)
// - If .agent/ already exists in worktree (from git), merges untracked content
// - Preserves file permissions during copy
func CopyNavigatorToWorktree(sourceRepo, worktreePath string) error {
	sourceAgent := filepath.Join(sourceRepo, ".agent")
	destAgent := filepath.Join(worktreePath, ".agent")

	// Check if source .agent/ exists
	sourceInfo, err := os.Stat(sourceAgent)
	if err != nil {
		if os.IsNotExist(err) {
			// No .agent/ in source - nothing to copy
			return nil
		}
		return fmt.Errorf("failed to stat source .agent: %w", err)
	}
	if !sourceInfo.IsDir() {
		return nil // .agent is a file, not a directory - skip
	}

	// Copy directory recursively
	return copyDir(sourceAgent, destAgent)
}

// copyDir recursively copies a directory from src to dst.
// If dst exists, files are merged (existing files in dst are overwritten).
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Create destination directory with same permissions
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("failed to read directory %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copies a single file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// Read source file
	content, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}

	// Write to destination with same permissions
	if err := os.WriteFile(dst, content, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to write %s: %w", dst, err)
	}

	return nil
}

// EnsureNavigatorInWorktree ensures the worktree has Navigator structure.
// This is the primary function to call after creating a worktree.
//
// Strategy:
// 1. Copy .agent/ from source repo (handles untracked content)
// 2. If .agent/ still doesn't exist, initialize Navigator from templates
//
// The sourceRepo is the original repository path where the user may have
// an existing .agent/ directory with project-specific configuration.
func EnsureNavigatorInWorktree(sourceRepo, worktreePath string) error {
	// First, copy from source to preserve any existing Navigator config
	if err := CopyNavigatorToWorktree(sourceRepo, worktreePath); err != nil {
		return fmt.Errorf("failed to copy navigator to worktree: %w", err)
	}

	// Check if .agent/ now exists in worktree
	agentDir := filepath.Join(worktreePath, ".agent")
	if _, err := os.Stat(agentDir); err == nil {
		// Navigator exists (either from git or from copy)
		return nil
	}

	// No .agent/ exists - will be initialized by runner.maybeInitNavigator()
	// Return nil here to let the normal init flow handle it
	return nil
}

// CleanupOrphanedWorktrees scans for orphaned pilot worktree directories and removes them.
// This handles cases where Pilot crashed before proper cleanup, leaving stale worktrees.
//
// GH-962: During normal operation, worktrees are cleaned up by deferred functions.
// However, if Pilot crashes mid-execution, the worktrees remain as orphans in /tmp/.
// This function provides startup cleanup to remove these stale directories.
//
// Strategy:
// 1. Scan /tmp/ for directories matching "pilot-worktree-*" pattern
// 2. Check if each directory is a valid git worktree
// 3. Use `git worktree prune` to remove stale references
// 4. Remove orphaned directories from filesystem
//
// This function is safe to call at startup and will not affect active worktrees
// managed by running Pilot instances (they maintain their tracking maps).
func CleanupOrphanedWorktrees(ctx context.Context, repoPath string) error {
	tmpDir := os.TempDir()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read temp directory %s: %w", tmpDir, err)
	}

	orphanCount := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Check if this looks like a pilot worktree
		name := entry.Name()
		if !strings.HasPrefix(name, "pilot-worktree-") {
			continue
		}

		worktreePath := filepath.Join(tmpDir, name)

		// Check if this is still a valid worktree by checking if .git file exists
		gitFile := filepath.Join(worktreePath, ".git")
		if _, err := os.Stat(gitFile); err != nil {
			// .git file doesn't exist - this is an orphaned directory
			// Remove it directly
			if removeErr := os.RemoveAll(worktreePath); removeErr == nil {
				orphanCount++
			}
			continue
		}

		// Directory has .git file - check if it's actually connected to our repo
		// Read the .git file to see if it points to our repository
		gitContent, err := os.ReadFile(gitFile)
		if err != nil {
			continue
		}

		// .git file contains: "gitdir: /path/to/repo/.git/worktrees/name"
		gitdirLine := strings.TrimSpace(string(gitContent))
		if !strings.HasPrefix(gitdirLine, "gitdir: ") {
			continue
		}

		gitdir := strings.TrimPrefix(gitdirLine, "gitdir: ")

		// Check if the gitdir points to our repository's worktree area
		expectedPrefix := filepath.Join(repoPath, ".git", "worktrees")
		if !strings.HasPrefix(gitdir, expectedPrefix) {
			continue
		}

		// This is a worktree for our repository but may be stale
		// Check if the worktree directory referenced in .git still exists
		if _, err := os.Stat(gitdir); err != nil {
			// Gitdir doesn't exist - this worktree is orphaned
			if removeErr := os.RemoveAll(worktreePath); removeErr == nil {
				orphanCount++
			}
		}
	}

	// Run git worktree prune to clean up any stale references in .git/worktrees/
	// This removes references to worktrees that no longer exist on disk
	if repoPath != "" {
		pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune", "-v")
		pruneCmd.Dir = repoPath
		// Ignore errors - prune is best-effort cleanup
		_ = pruneCmd.Run()
	}

	if orphanCount > 0 {
		return fmt.Errorf("cleaned up %d orphaned pilot worktree directories", orphanCount)
	}

	return nil
}
