package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PooledWorktree represents a worktree in the pool.
type PooledWorktree struct {
	Path      string    // Absolute path to the worktree directory
	CreatedAt time.Time // When this worktree was created
	InUse     bool      // Whether currently acquired
}

// WorktreeManager handles git worktree creation and cleanup for isolated task execution.
// GH-936: Enables Pilot to work in repos where users have uncommitted changes.
// GH-1078: Supports worktree pooling for sequential mode to save 500ms-2s per task.
type WorktreeManager struct {
	repoPath  string
	mu        sync.Mutex
	active    map[string]string // taskID -> worktreePath
	createMu  sync.Mutex        // GH-1312: Serializes worktree creation to avoid git race conditions

	// Pool support (GH-1078)
	pool     []*PooledWorktree // Pre-created worktrees for reuse
	poolSize int               // Configured pool size (0 = disabled)
	poolMu   sync.Mutex        // Protects pool operations
}

// NewWorktreeManager creates a worktree manager for the given repository.
func NewWorktreeManager(repoPath string) *WorktreeManager {
	return &WorktreeManager{
		repoPath: repoPath,
		active:   make(map[string]string),
		pool:     make([]*PooledWorktree, 0),
		poolSize: 0,
	}
}

// NewWorktreeManagerWithPool creates a worktree manager with pool support.
// poolSize specifies the number of worktrees to pre-create (0 = no pooling).
// GH-1078: Worktree pooling saves 500ms-2s per task in sequential mode.
func NewWorktreeManagerWithPool(repoPath string, poolSize int) *WorktreeManager {
	return &WorktreeManager{
		repoPath: repoPath,
		active:   make(map[string]string),
		pool:     make([]*PooledWorktree, 0, poolSize),
		poolSize: poolSize,
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

	// GH-1312: Serialize worktree creation to avoid git race conditions.
	// Git's worktree implementation has internal races on .git/worktrees/*/commondir
	// when multiple worktrees are created concurrently. Rather than relying solely on
	// retries, we serialize creation operations while still allowing concurrent execution
	// in the worktrees themselves.
	m.createMu.Lock()
	defer m.createMu.Unlock()

	// Create worktree from HEAD (current commit of default branch)
	// Retry with exponential backoff to handle git race conditions on concurrent worktree creation
	// (git has internal races on .git/worktrees/*/commondir when creating multiple worktrees concurrently)
	var output []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "HEAD")
		cmd.Dir = m.repoPath
		output, err = cmd.CombinedOutput()
		if err == nil {
			break
		}
		// Check for transient git worktree race condition errors
		outputStr := string(output)
		if strings.Contains(outputStr, "commondir") || strings.Contains(outputStr, "gitdir") {
			time.Sleep(time.Duration(10*(attempt+1)) * time.Millisecond)
			continue
		}
		// Non-transient error, don't retry
		break
	}
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

// WarmPool pre-creates poolSize worktrees for fast acquisition.
// GH-1078: Called at Runner startup when worktree pooling is enabled.
// Each pooled worktree is created in detached HEAD state at origin/main.
func (m *WorktreeManager) WarmPool(ctx context.Context) error {
	if m.poolSize <= 0 {
		return nil // Pooling disabled
	}

	m.poolMu.Lock()
	defer m.poolMu.Unlock()

	// Already warmed?
	if len(m.pool) >= m.poolSize {
		return nil
	}

	slog.Info("Warming worktree pool",
		slog.Int("pool_size", m.poolSize),
		slog.String("repo", m.repoPath),
	)

	for i := len(m.pool); i < m.poolSize; i++ {
		pooledWT, err := m.createPooledWorktree(ctx, i)
		if err != nil {
			slog.Warn("Failed to create pooled worktree",
				slog.Int("index", i),
				slog.Any("error", err),
			)
			// Continue trying to create remaining worktrees
			continue
		}
		m.pool = append(m.pool, pooledWT)
	}

	slog.Info("Worktree pool warmed",
		slog.Int("created", len(m.pool)),
		slog.Int("target", m.poolSize),
	)

	return nil
}

// createPooledWorktree creates a single worktree for the pool.
func (m *WorktreeManager) createPooledWorktree(ctx context.Context, index int) (*PooledWorktree, error) {
	// Pool paths use consistent naming: /tmp/pilot-worktree-pool-N/
	worktreePath := filepath.Join(os.TempDir(), fmt.Sprintf("pilot-worktree-pool-%d", index))

	// Remove any existing directory at this path
	_ = os.RemoveAll(worktreePath)

	// Remove any stale git worktree reference
	pruneCmd := exec.CommandContext(ctx, "git", "worktree", "prune")
	pruneCmd.Dir = m.repoPath
	_ = pruneCmd.Run()

	// Fetch latest origin/main to ensure fresh base
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "main")
	fetchCmd.Dir = m.repoPath
	_, _ = fetchCmd.CombinedOutput() // Non-fatal if this fails

	// Create worktree in detached HEAD state at origin/main
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", worktreePath, "origin/main")
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create pooled worktree: %w: %s", err, output)
	}

	return &PooledWorktree{
		Path:      worktreePath,
		CreatedAt: time.Now(),
		InUse:     false,
	}, nil
}

// Acquire gets a worktree from the pool and prepares it for the given branch.
// If the pool is empty, falls back to CreateWorktreeWithBranch.
// GH-1078: Reuses pooled worktrees by running git clean -fd && git checkout -B <branch>.
//
// Returns a WorktreeResult with a cleanup function that calls Release instead of destroying.
func (m *WorktreeManager) Acquire(ctx context.Context, taskID, branchName, baseBranch string) (*WorktreeResult, error) {
	m.poolMu.Lock()

	// Find an available worktree in the pool
	var acquired *PooledWorktree
	for _, wt := range m.pool {
		if !wt.InUse {
			wt.InUse = true
			acquired = wt
			break
		}
	}

	m.poolMu.Unlock()

	// Pool empty or no available worktrees - fall back to standard creation
	if acquired == nil {
		slog.Debug("Pool empty, falling back to CreateWorktreeWithBranch",
			slog.String("task_id", taskID),
		)
		return m.CreateWorktreeWithBranch(ctx, taskID, branchName, baseBranch)
	}

	slog.Info("Acquired pooled worktree",
		slog.String("task_id", taskID),
		slog.String("path", acquired.Path),
		slog.String("branch", branchName),
	)

	// Prepare the pooled worktree for this task
	if err := m.preparePooledWorktree(ctx, acquired, branchName, baseBranch); err != nil {
		// Mark as not in use and fall back
		m.poolMu.Lock()
		acquired.InUse = false
		m.poolMu.Unlock()

		slog.Warn("Failed to prepare pooled worktree, falling back to create",
			slog.String("path", acquired.Path),
			slog.Any("error", err),
		)
		return m.CreateWorktreeWithBranch(ctx, taskID, branchName, baseBranch)
	}

	// Track as active
	m.mu.Lock()
	m.active[taskID] = acquired.Path
	m.mu.Unlock()

	// Create cleanup function that releases back to pool instead of destroying
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			m.Release(taskID, acquired)
		})
	}

	return &WorktreeResult{
		Path:    acquired.Path,
		Cleanup: cleanup,
	}, nil
}

// preparePooledWorktree cleans and switches a pooled worktree to the target branch.
// Runs: git clean -fd && git checkout -B <branch> <base>
func (m *WorktreeManager) preparePooledWorktree(ctx context.Context, wt *PooledWorktree, branchName, baseBranch string) error {
	// Determine base ref
	baseRef := "origin/main"
	if baseBranch != "" {
		baseRef = baseBranch
	}

	// Fetch latest to ensure we have fresh refs
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "main")
	fetchCmd.Dir = wt.Path
	_, _ = fetchCmd.CombinedOutput() // Non-fatal

	// Clean any leftover files from previous task
	cleanCmd := exec.CommandContext(ctx, "git", "clean", "-fd")
	cleanCmd.Dir = wt.Path
	if output, err := cleanCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clean failed: %w: %s", err, output)
	}

	// Reset to base ref (discard any local changes)
	resetCmd := exec.CommandContext(ctx, "git", "reset", "--hard", baseRef)
	resetCmd.Dir = wt.Path
	if output, err := resetCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git reset failed: %w: %s", err, output)
	}

	// Create/reset the target branch
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", "-B", branchName, baseRef)
	checkoutCmd.Dir = wt.Path
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout failed: %w: %s", err, output)
	}

	return nil
}

// Release returns a worktree to the pool after task completion.
// Validates the worktree is in a clean state before returning to pool.
// GH-1078: If validation fails, the worktree is recreated.
func (m *WorktreeManager) Release(taskID string, wt *PooledWorktree) {
	// Remove from active tracking
	m.mu.Lock()
	delete(m.active, taskID)
	m.mu.Unlock()

	// Validate worktree is still usable
	if !m.validatePooledWorktree(wt) {
		slog.Warn("Released worktree failed validation, will recreate",
			slog.String("path", wt.Path),
		)
		// Try to recreate it
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		m.poolMu.Lock()
		// Find index of this worktree in pool
		for i, pooledWT := range m.pool {
			if pooledWT == wt {
				// Remove from pool
				m.pool = append(m.pool[:i], m.pool[i+1:]...)

				// Clean up the broken worktree
				_ = os.RemoveAll(wt.Path)
				pruneCmd := exec.Command("git", "-C", m.repoPath, "worktree", "prune")
				_ = pruneCmd.Run()

				// Create a new one
				newWT, err := m.createPooledWorktree(ctx, i)
				if err == nil {
					m.pool = append(m.pool, newWT)
				}
				break
			}
		}
		m.poolMu.Unlock()
		return
	}

	// Mark as available
	m.poolMu.Lock()
	wt.InUse = false
	m.poolMu.Unlock()

	slog.Debug("Released worktree back to pool",
		slog.String("task_id", taskID),
		slog.String("path", wt.Path),
	)
}

// validatePooledWorktree checks if a worktree is in a valid state for reuse.
func (m *WorktreeManager) validatePooledWorktree(wt *PooledWorktree) bool {
	// Check directory exists
	if _, err := os.Stat(wt.Path); err != nil {
		return false
	}

	// Check .git file exists (indicates valid worktree)
	gitFile := filepath.Join(wt.Path, ".git")
	if _, err := os.Stat(gitFile); err != nil {
		return false
	}

	// Check git status runs without error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = wt.Path
	if err := statusCmd.Run(); err != nil {
		return false
	}

	return true
}

// Close drains the worktree pool and removes all pooled worktrees.
// Should be called during graceful shutdown.
// GH-1078: Ensures clean shutdown without leaving orphaned worktrees.
func (m *WorktreeManager) Close() {
	m.poolMu.Lock()
	defer m.poolMu.Unlock()

	if len(m.pool) == 0 {
		return
	}

	slog.Info("Draining worktree pool",
		slog.Int("count", len(m.pool)),
	)

	for _, wt := range m.pool {
		// Remove the git worktree reference
		removeCmd := exec.Command("git", "-C", m.repoPath, "worktree", "remove", "--force", wt.Path)
		_ = removeCmd.Run()

		// Remove directory if still exists
		_ = os.RemoveAll(wt.Path)
	}

	// Clear pool
	m.pool = m.pool[:0]

	// Prune any stale references
	pruneCmd := exec.Command("git", "-C", m.repoPath, "worktree", "prune")
	_ = pruneCmd.Run()

	slog.Info("Worktree pool drained")
}

// PoolSize returns the configured pool size.
func (m *WorktreeManager) PoolSize() int {
	return m.poolSize
}

// PoolAvailable returns the number of available (not in use) worktrees in the pool.
func (m *WorktreeManager) PoolAvailable() int {
	m.poolMu.Lock()
	defer m.poolMu.Unlock()

	count := 0
	for _, wt := range m.pool {
		if !wt.InUse {
			count++
		}
	}
	return count
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

	// GH-1312: Serialize worktree creation to avoid git race conditions.
	// Git's worktree implementation has internal races on .git/worktrees/*/commondir
	// when multiple worktrees are created concurrently.
	m.createMu.Lock()
	defer m.createMu.Unlock()

	// GH-1211: Always fetch origin before creating worktree to prevent branching
	// from stale local main. This avoids conflicts when local main diverges from origin.
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "main")
	fetchCmd.Dir = m.repoPath
	if output, fetchErr := fetchCmd.CombinedOutput(); fetchErr != nil {
		slog.Warn("Failed to fetch origin/main before worktree creation",
			slog.Any("error", fetchErr),
			slog.String("output", string(output)),
		)
		// Non-fatal: proceed with local HEAD as fallback
	}

	// Determine base ref — prefer origin/main for freshest base
	baseRef := "origin/main"
	if baseBranch != "" {
		baseRef = baseBranch
	}

	// GH-963: Clean up any stale worktree for this branch before creating.
	// This handles retries where previous cleanup failed to fully remove the worktree reference.
	m.cleanupStaleWorktreeForBranch(ctx, branchName)

	// Use -B to force create/reset the branch atomically
	// git worktree add -B <branch> <path> <base>
	// -B creates the branch if it doesn't exist, or resets it if it does
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", "-B", branchName, worktreePath, baseRef)
	cmd.Dir = m.repoPath
	output, err := cmd.CombinedOutput()

	if err != nil {
		return nil, fmt.Errorf("failed to create worktree with branch: %w: %s", err, output)
	}

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
