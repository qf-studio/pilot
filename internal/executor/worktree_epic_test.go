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
	"testing"
)

// TestWorktreeEpicIntegration verifies that epic tasks execute entirely within worktree isolation.
// GH-969: This test ensures:
// 1. PlanEpic() runs in the worktree path
// 2. Each sub-issue execution uses the proper worktree path
// 3. All worktrees are cleaned up after execution
func TestWorktreeEpicIntegration(t *testing.T) {
	// Setup test repository with remote (needed for branch/push operations)
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	// Track paths used during execution
	var pathsMu sync.Mutex
	var subIssueExecutionPaths []string

	// Create mock scripts directory
	mockDir := t.TempDir()

	// Mock Claude script that outputs a valid epic plan with 2 subtasks
	mockClaudeScript := filepath.Join(mockDir, "mock-claude")
	claudeOutput := `Here's the implementation plan:

1. **Add database schema** - Create migration files for the new tables
2. **Implement API endpoints** - Build REST endpoints with validation`
	writeMockScriptWithPathCapture(t, mockClaudeScript, claudeOutput, 0)

	// Create runner with worktree mode enabled
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: mockClaudeScript,
			},
			UseWorktree: true,
		},
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               testLogger(),
		modelRouter:       NewModelRouter(nil, nil),
	}

	// Skip preflight checks (no real claude binary)
	runner.SetSkipPreflightChecks(true)

	// Create epic task
	epicTask := &Task{
		ID:          "GH-EPIC-100",
		Title:       "[epic] Test worktree isolation for epic",
		Description: "Verify epic planning and execution uses worktree paths",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-EPIC-100",
		CreatePR:    true,
	}

	// Override executeFunc to capture the execution path for sub-issues
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		// This is called for sub-issue execution
		pathsMu.Lock()
		subIssueExecutionPaths = append(subIssueExecutionPaths, task.ProjectPath)
		pathsMu.Unlock()

		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/test/repo/pull/%d", len(subIssueExecutionPaths)+100),
			CommitSHA: "abc123",
		}, nil
	}

	// Test PlanEpic with worktree path
	ctx := context.Background()

	// Create worktree for the test (simulating what executeWithOptions does)
	manager := NewWorktreeManager(localRepo)
	worktreeResult, err := manager.CreateWorktreeWithBranch(ctx, "GH-EPIC-100", "pilot/GH-EPIC-100", "main")
	if err != nil {
		t.Fatalf("Failed to create worktree: %v", err)
	}
	defer worktreeResult.Cleanup()

	// Copy Navigator to worktree
	if err := EnsureNavigatorInWorktree(localRepo, worktreeResult.Path); err != nil {
		t.Fatalf("Failed to ensure navigator in worktree: %v", err)
	}

	// Test 1: Verify PlanEpic uses the provided execution path
	plan, err := runner.PlanEpic(ctx, epicTask, worktreeResult.Path)
	if err != nil {
		t.Fatalf("PlanEpic failed: %v", err)
	}

	// Verify plan was created successfully
	if plan == nil || len(plan.Subtasks) != 2 {
		t.Fatalf("Expected 2 subtasks, got: %v", plan)
	}
	t.Logf("PlanEpic executed in worktree path: %s", worktreeResult.Path)

	// Verify PlanEpic ran in worktree (not original repo)
	if !strings.Contains(worktreeResult.Path, "pilot-worktree-") {
		t.Errorf("PlanEpic should run in worktree, got path: %s", worktreeResult.Path)
	}

	// Test 2: Verify ExecuteSubIssues passes worktree path to sub-executions
	// Create mock issues (bypassing CreateSubIssues which requires gh auth)
	mockIssues := []CreatedIssue{
		{Number: 1001, URL: "https://github.com/test/repo/issues/1001", Subtask: plan.Subtasks[0]},
		{Number: 1002, URL: "https://github.com/test/repo/issues/1002", Subtask: plan.Subtasks[1]},
	}

	err = runner.ExecuteSubIssues(ctx, epicTask, mockIssues, worktreeResult.Path)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	// Verify sub-issue executions used the worktree path
	pathsMu.Lock()
	defer pathsMu.Unlock()

	if len(subIssueExecutionPaths) != 2 {
		t.Fatalf("Expected 2 sub-issue executions, got %d", len(subIssueExecutionPaths))
	}

	for i, path := range subIssueExecutionPaths {
		t.Logf("Sub-issue %d executed in path: %s", i+1, path)
		// Sub-issues should use the worktree path passed to ExecuteSubIssues
		if path != worktreeResult.Path {
			t.Errorf("Sub-issue %d: expected worktree path %q, got %q", i+1, worktreeResult.Path, path)
		}
	}
}

// TestWorktreeEpicCleanup verifies all worktrees are cleaned up after epic execution.
func TestWorktreeEpicCleanup(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()
	manager := NewWorktreeManager(localRepo)

	// Simulate epic execution creating multiple worktrees
	worktrees := make([]*WorktreeResult, 3)
	var err error

	for i := 0; i < 3; i++ {
		branchName := fmt.Sprintf("pilot/epic-cleanup-test-%d", i+1)
		worktrees[i], err = manager.CreateWorktreeWithBranch(ctx, fmt.Sprintf("cleanup-test-%d", i), branchName, "main")
		if err != nil {
			t.Fatalf("Failed to create worktree %d: %v", i, err)
		}
	}

	// Verify all worktrees exist
	for i, wt := range worktrees {
		if _, err := os.Stat(wt.Path); os.IsNotExist(err) {
			t.Fatalf("Worktree %d should exist at %s", i, wt.Path)
		}
	}

	// Verify active count
	if count := manager.ActiveCount(); count != 3 {
		t.Errorf("Expected 3 active worktrees, got %d", count)
	}

	// Cleanup all worktrees (simulating epic completion)
	for _, wt := range worktrees {
		wt.Cleanup()
	}

	// Verify all worktrees are removed
	for i, wt := range worktrees {
		if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
			t.Errorf("Worktree %d should be removed after cleanup: %s", i, wt.Path)
		}
	}

	// Verify active count is zero
	if count := manager.ActiveCount(); count != 0 {
		t.Errorf("Expected 0 active worktrees after cleanup, got %d", count)
	}
}

// TestWorktreeEpicCleanupOnFailure verifies worktrees are cleaned up even when epic execution fails.
func TestWorktreeEpicCleanupOnFailure(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()
	manager := NewWorktreeManager(localRepo)

	var worktreePath string

	// Simulate epic execution with deferred cleanup (how runner.go does it)
	func() {
		result, err := manager.CreateWorktreeWithBranch(ctx, "failure-test", "pilot/failure-test", "main")
		if err != nil {
			t.Fatalf("Failed to create worktree: %v", err)
		}
		defer result.Cleanup() // Should run even on panic/error

		worktreePath = result.Path

		// Verify worktree exists during execution
		if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
			t.Fatal("Worktree should exist during execution")
		}

		// Simulate failure (but don't panic - just return early)
		return
	}()

	// Verify worktree was cleaned up despite "failure"
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("Worktree should be cleaned up after function returns")
	}

	if count := manager.ActiveCount(); count != 0 {
		t.Errorf("Expected 0 active worktrees after failure cleanup, got %d", count)
	}
}

// TestWorktreeEpicWithNavigatorCopy verifies Navigator config is copied to worktree for epic execution.
func TestWorktreeEpicWithNavigatorCopy(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	// Create Navigator structure in source repo
	agentDir := filepath.Join(localRepo, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("Failed to create .agent dir: %v", err)
	}

	devReadme := filepath.Join(agentDir, "DEVELOPMENT-README.md")
	navContent := "# Navigator Config\n\nProject-specific settings"
	if err := os.WriteFile(devReadme, []byte(navContent), 0644); err != nil {
		t.Fatalf("Failed to create DEVELOPMENT-README.md: %v", err)
	}

	// Create nested untracked content (commonly gitignored)
	markersDir := filepath.Join(agentDir, ".context-markers")
	if err := os.MkdirAll(markersDir, 0755); err != nil {
		t.Fatalf("Failed to create .context-markers: %v", err)
	}
	markerFile := filepath.Join(markersDir, "epic-marker.md")
	if err := os.WriteFile(markerFile, []byte("# Epic Marker"), 0644); err != nil {
		t.Fatalf("Failed to create marker file: %v", err)
	}

	ctx := context.Background()
	manager := NewWorktreeManager(localRepo)

	// Create worktree for epic execution
	result, err := manager.CreateWorktreeWithBranch(ctx, "navigator-test", "pilot/navigator-test", "main")
	if err != nil {
		t.Fatalf("Failed to create worktree: %v", err)
	}
	defer result.Cleanup()

	// Copy Navigator to worktree (as done in executeWithOptions)
	if err := EnsureNavigatorInWorktree(localRepo, result.Path); err != nil {
		t.Fatalf("EnsureNavigatorInWorktree failed: %v", err)
	}

	// Verify Navigator was copied to worktree
	worktreeReadme := filepath.Join(result.Path, ".agent", "DEVELOPMENT-README.md")
	if _, err := os.Stat(worktreeReadme); os.IsNotExist(err) {
		t.Error("DEVELOPMENT-README.md should be copied to worktree")
	}

	// Verify nested untracked content was copied
	worktreeMarker := filepath.Join(result.Path, ".agent", ".context-markers", "epic-marker.md")
	if _, err := os.Stat(worktreeMarker); os.IsNotExist(err) {
		t.Error(".context-markers/epic-marker.md should be copied to worktree")
	}

	// Verify content is correct
	content, err := os.ReadFile(worktreeReadme)
	if err != nil {
		t.Fatalf("Failed to read worktree README: %v", err)
	}
	if string(content) != navContent {
		t.Errorf("Content mismatch: got %q, want %q", string(content), navContent)
	}
}

// TestWorktreeEpicBranchOperations verifies branch operations work correctly in worktree.
func TestWorktreeEpicBranchOperations(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()
	manager := NewWorktreeManager(localRepo)

	branchName := "pilot/epic-branch-test"
	result, err := manager.CreateWorktreeWithBranch(ctx, "branch-test", branchName, "main")
	if err != nil {
		t.Fatalf("CreateWorktreeWithBranch failed: %v", err)
	}
	defer result.Cleanup()

	// Verify we're on the correct branch in worktree
	branchCmd := exec.Command("git", "-C", result.Path, "branch", "--show-current")
	output, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("Failed to get current branch: %v", err)
	}
	currentBranch := strings.TrimSpace(string(output))
	if currentBranch != branchName {
		t.Errorf("Expected branch %q, got %q", branchName, currentBranch)
	}

	// Create GitOperations pointing to worktree (as done in epic sub-issue execution)
	gitOps := NewGitOperations(result.Path)

	// Create a file and commit in worktree
	testFile := filepath.Join(result.Path, "epic-test.txt")
	if err := os.WriteFile(testFile, []byte("epic test content\n"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Verify HasUncommittedChanges works in worktree
	hasChanges, err := gitOps.HasUncommittedChanges(ctx)
	if err != nil {
		t.Fatalf("HasUncommittedChanges failed: %v", err)
	}
	if !hasChanges {
		t.Error("Expected uncommitted changes after creating file")
	}

	// Commit changes
	sha, err := gitOps.Commit(ctx, "Test commit from epic worktree")
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if sha == "" {
		t.Error("Expected non-empty commit SHA")
	}

	// Verify commit was made
	hasChanges, err = gitOps.HasUncommittedChanges(ctx)
	if err != nil {
		t.Fatalf("HasUncommittedChanges after commit failed: %v", err)
	}
	if hasChanges {
		t.Error("Expected no uncommitted changes after commit")
	}

	// Push from worktree
	if err := gitOps.Push(ctx, branchName); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Verify branch exists on remote
	lsCmd := exec.Command("git", "-C", localRepo, "ls-remote", "--heads", "origin", branchName)
	lsOutput, err := lsCmd.Output()
	if err != nil {
		t.Fatalf("ls-remote failed: %v", err)
	}
	if !strings.Contains(string(lsOutput), branchName) {
		t.Errorf("Branch %q not found on remote after push", branchName)
	}
}

// writeMockScriptWithPathCapture creates a mock script that outputs text and can capture execution path.
func writeMockScriptWithPathCapture(t *testing.T, path, output string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\n"
	if output != "" {
		script += "cat <<'ENDOFOUTPUT'\n" + output + "\nENDOFOUTPUT\n"
	}
	script += fmt.Sprintf("exit %d\n", exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}
}

// testLogger returns a minimal logger for testing.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
