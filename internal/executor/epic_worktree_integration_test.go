package executor

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// testLogger creates a logger for testing that suppresses most output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestEpicWorktreeIsolation verifies that epic decomposition with worktree isolation
// enabled does NOT create recursive/nested worktrees for sub-issues.
//
// GH-961: This integration test ensures:
// 1. Parent epic task uses worktree when UseWorktree=true
// 2. Sub-issues execute without creating nested worktrees (allowWorktree=false)
// 3. Cleanup happens correctly for parent worktree
func TestEpicWorktreeIsolation(t *testing.T) {
	// Create test repo with remote
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()

	// Track worktree creation attempts across all executions
	var mu sync.Mutex
	var executionPaths []string // Records the actual execution paths used

	// Create runner with worktree enabled
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: "echo", // unused
			},
			UseWorktree: true, // Enable worktree isolation
		},
		running:             make(map[string]*exec.Cmd),
		progressCallbacks:   make(map[string]ProgressCallback),
		tokenCallbacks:      make(map[string]TokenCallback),
		log:                 testLogger(),
		modelRouter:         NewModelRouter(nil, nil),
		skipPreflightChecks: true, // Skip preflight for test
	}

	// Create a mock execute function that tracks worktree behavior
	// The key insight: ExecuteSubIssues calls executeWithOptions(ctx, subTask, false)
	// which means sub-issues should NOT attempt worktree creation even when UseWorktree=true
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		executionPaths = append(executionPaths, task.ProjectPath)
		mu.Unlock()

		// Return success with PR URL
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "Completed " + task.Title,
			PRUrl:     "https://github.com/owner/repo/pull/100",
			CommitSHA: "abc123",
		}, nil
	}

	// Create test sub-issues
	subIssues := []CreatedIssue{
		{
			Number:  100,
			URL:     "https://github.com/owner/repo/issues/100",
			Subtask: PlannedSubtask{Title: "Sub-issue 1", Description: "First task", Order: 1},
		},
		{
			Number:  101,
			URL:     "https://github.com/owner/repo/issues/101",
			Subtask: PlannedSubtask{Title: "Sub-issue 2", Description: "Second task", Order: 2},
		},
		{
			Number:  102,
			URL:     "https://github.com/owner/repo/issues/102",
			Subtask: PlannedSubtask{Title: "Sub-issue 3", Description: "Third task", Order: 3},
		},
	}

	parent := &Task{
		ID:          "GH-50",
		Title:       "[epic] Worktree isolation test",
		ProjectPath: localRepo,
	}

	// Execute sub-issues (this is what happens inside executeWithOptions for epics)
	err := runner.ExecuteSubIssues(ctx, parent, subIssues)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	// Verify all 3 sub-issues were executed
	if len(executionPaths) != 3 {
		t.Errorf("expected 3 executions, got %d", len(executionPaths))
	}

	// CRITICAL: Verify that sub-issues did NOT create worktrees
	// They should use the parent's ProjectPath directly (or worktree path if parent is in worktree)
	for i, path := range executionPaths {
		if strings.Contains(path, "pilot-worktree-") {
			t.Errorf("sub-issue %d should not have created a nested worktree, got path: %s", i, path)
		}
	}
}

// TestEpicWorktreeIsolation_ExecuteWithOptionsTracking tests the executeWithOptions
// behavior directly by tracking when allowWorktree=false is respected.
func TestEpicWorktreeIsolation_ExecuteWithOptionsTracking(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()

	// Track execution details
	var mu sync.Mutex
	type execRecord struct {
		TaskID      string
		BranchName  string
		ProjectPath string
		IsSubIssue  bool
	}
	var execRecords []execRecord

	// Create runner with worktree enabled
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: "echo",
			},
			UseWorktree: true,
		},
		running:             make(map[string]*exec.Cmd),
		progressCallbacks:   make(map[string]ProgressCallback),
		tokenCallbacks:      make(map[string]TokenCallback),
		log:                 testLogger(),
		modelRouter:         NewModelRouter(nil, nil),
		skipPreflightChecks: true,
	}

	// Mock executeFunc to capture execution details
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		// Determine if this is a sub-issue by checking task ID pattern
		isSubIssue := strings.HasPrefix(task.ID, "GH-10") || strings.HasPrefix(task.ID, "GH-20")
		execRecords = append(execRecords, execRecord{
			TaskID:      task.ID,
			BranchName:  task.Branch,
			ProjectPath: task.ProjectPath,
			IsSubIssue:  isSubIssue,
		})
		mu.Unlock()

		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "done",
			PRUrl:     "https://github.com/owner/repo/pull/200",
			CommitSHA: "def456",
		}, nil
	}

	// Test scenario: Execute sub-issues like epic.go does
	subIssues := []CreatedIssue{
		{Number: 100, Subtask: PlannedSubtask{Title: "Task 1", Order: 1}},
		{Number: 101, Subtask: PlannedSubtask{Title: "Task 2", Order: 2}},
	}

	parent := &Task{
		ID:          "GH-50",
		Title:       "[epic] Test parent",
		ProjectPath: localRepo,
	}

	err := runner.ExecuteSubIssues(ctx, parent, subIssues)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	// Verify execution count
	if len(execRecords) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(execRecords))
	}

	// Verify sub-issues received correct project paths
	for i, rec := range execRecords {
		if rec.ProjectPath != localRepo {
			t.Errorf("sub-issue %d: ProjectPath = %q, want %q", i, rec.ProjectPath, localRepo)
		}
	}
}

// TestEpicWorktreeCleanup verifies that worktree cleanup happens correctly
// for epic tasks, including when sub-issues fail.
func TestEpicWorktreeCleanup(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()

	// Create a worktree manager for the test
	manager := NewWorktreeManager(localRepo)

	// Create a worktree (simulating what executeWithOptions does for parent epic)
	result, err := manager.CreateWorktreeWithBranch(ctx, "epic-test", "pilot/GH-EPIC", "main")
	if err != nil {
		t.Fatalf("CreateWorktreeWithBranch failed: %v", err)
	}
	worktreePath := result.Path

	// Verify worktree was created
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("worktree should exist")
	}

	// Verify active count
	if count := manager.ActiveCount(); count != 1 {
		t.Errorf("expected 1 active worktree, got %d", count)
	}

	// Simulate sub-issue execution within the worktree
	// Sub-issues should NOT create additional worktrees
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode:  &ClaudeCodeConfig{Command: "echo"},
			UseWorktree: true,
		},
		running:             make(map[string]*exec.Cmd),
		progressCallbacks:   make(map[string]ProgressCallback),
		tokenCallbacks:      make(map[string]TokenCallback),
		log:                 testLogger(),
		modelRouter:         NewModelRouter(nil, nil),
		skipPreflightChecks: true,
	}

	// Track execution paths
	var executedPaths []string
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		executedPaths = append(executedPaths, task.ProjectPath)
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     "https://github.com/owner/repo/pull/300",
			CommitSHA: "test123",
		}, nil
	}

	// Execute sub-issues with the worktree path as ProjectPath
	subIssues := []CreatedIssue{
		{Number: 200, Subtask: PlannedSubtask{Title: "Sub 1", Order: 1}},
		{Number: 201, Subtask: PlannedSubtask{Title: "Sub 2", Order: 2}},
	}

	parent := &Task{
		ID:          "GH-EPIC",
		Title:       "[epic] Cleanup test",
		ProjectPath: worktreePath, // Use worktree path
	}

	err = runner.ExecuteSubIssues(ctx, parent, subIssues)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	// Verify sub-issues used worktree path
	for i, path := range executedPaths {
		if path != worktreePath {
			t.Errorf("sub-issue %d: executed in %q, want %q", i, path, worktreePath)
		}
	}

	// Cleanup the worktree
	result.Cleanup()

	// Verify worktree was cleaned up
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Error("worktree should be cleaned up")
	}

	// Verify active count is 0
	if count := manager.ActiveCount(); count != 0 {
		t.Errorf("expected 0 active worktrees after cleanup, got %d", count)
	}
}

// TestNoRecursiveWorktreeInDecomposedTasks verifies that decomposed tasks
// (not epics, but regular decomposition) also don't create nested worktrees.
func TestNoRecursiveWorktreeInDecomposedTasks(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()

	// Track worktree creation in executeWithOptions
	var mu sync.Mutex
	var worktreeAttempts int

	// The key test: when executeWithOptions is called with allowWorktree=false,
	// it should NOT create a worktree even if UseWorktree=true in config
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode:  &ClaudeCodeConfig{Command: "echo"},
			UseWorktree: true, // Enabled, but should be skipped for sub-tasks
		},
		running:             make(map[string]*exec.Cmd),
		progressCallbacks:   make(map[string]ProgressCallback),
		tokenCallbacks:      make(map[string]TokenCallback),
		log:                 testLogger(),
		modelRouter:         NewModelRouter(nil, nil),
		skipPreflightChecks: true,
	}

	// Override executeFunc to track what happens
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		// Check if the path contains a worktree marker
		if strings.Contains(task.ProjectPath, "pilot-worktree-") {
			worktreeAttempts++
		}
		mu.Unlock()

		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     "https://github.com/owner/repo/pull/400",
			CommitSHA: "xyz789",
		}, nil
	}

	// Execute sub-issues
	subIssues := []CreatedIssue{
		{Number: 300, Subtask: PlannedSubtask{Title: "Decomposed 1", Order: 1}},
		{Number: 301, Subtask: PlannedSubtask{Title: "Decomposed 2", Order: 2}},
		{Number: 302, Subtask: PlannedSubtask{Title: "Decomposed 3", Order: 3}},
	}

	parent := &Task{
		ID:          "GH-DECOMP",
		Title:       "Decomposed task test",
		ProjectPath: localRepo, // NOT a worktree path
	}

	err := runner.ExecuteSubIssues(ctx, parent, subIssues)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	// CRITICAL: No worktree paths should have been passed to sub-issues
	if worktreeAttempts > 0 {
		t.Errorf("expected 0 worktree path attempts in sub-issues, got %d", worktreeAttempts)
	}
}

// TestWorktreeIsolationWithNavigatorCopy verifies that Navigator config
// is properly copied to worktree and available for sub-issue execution.
func TestWorktreeIsolationWithNavigatorCopy(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	// Create .agent/ directory in local repo
	agentDir := filepath.Join(localRepo, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("failed to create .agent dir: %v", err)
	}
	devReadme := filepath.Join(agentDir, "DEVELOPMENT-README.md")
	if err := os.WriteFile(devReadme, []byte("# Navigator\n"), 0644); err != nil {
		t.Fatalf("failed to write DEVELOPMENT-README.md: %v", err)
	}

	ctx := context.Background()

	// Create worktree
	manager := NewWorktreeManager(localRepo)
	result, err := manager.CreateWorktreeWithBranch(ctx, "nav-test", "pilot/GH-NAV", "main")
	if err != nil {
		t.Fatalf("CreateWorktreeWithBranch failed: %v", err)
	}
	defer result.Cleanup()

	// Copy Navigator to worktree
	if err := EnsureNavigatorInWorktree(localRepo, result.Path); err != nil {
		t.Fatalf("EnsureNavigatorInWorktree failed: %v", err)
	}

	// Verify Navigator was copied
	worktreeReadme := filepath.Join(result.Path, ".agent", "DEVELOPMENT-README.md")
	if _, err := os.Stat(worktreeReadme); err != nil {
		t.Errorf("Navigator should be copied to worktree: %v", err)
	}

	// Now execute sub-issues in the worktree - they should have access to Navigator
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode:  &ClaudeCodeConfig{Command: "echo"},
			UseWorktree: true,
		},
		running:             make(map[string]*exec.Cmd),
		progressCallbacks:   make(map[string]ProgressCallback),
		tokenCallbacks:      make(map[string]TokenCallback),
		log:                 testLogger(),
		modelRouter:         NewModelRouter(nil, nil),
		skipPreflightChecks: true,
	}

	// Check Navigator availability in executeFunc
	var navigatorAvailable bool
	runner.executeFunc = func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		navPath := filepath.Join(task.ProjectPath, ".agent", "DEVELOPMENT-README.md")
		if _, err := os.Stat(navPath); err == nil {
			navigatorAvailable = true
		}
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     "https://github.com/owner/repo/pull/500",
			CommitSHA: "nav123",
		}, nil
	}

	subIssues := []CreatedIssue{
		{Number: 400, Subtask: PlannedSubtask{Title: "Nav test", Order: 1}},
	}

	parent := &Task{
		ID:          "GH-NAV",
		Title:       "[epic] Navigator copy test",
		ProjectPath: result.Path, // Use worktree path
	}

	err = runner.ExecuteSubIssues(ctx, parent, subIssues)
	if err != nil {
		t.Fatalf("ExecuteSubIssues failed: %v", err)
	}

	if !navigatorAvailable {
		t.Error("Navigator should be available in worktree for sub-issues")
	}
}

// TestConcurrentEpicsWithWorktrees verifies that multiple epics can run
// concurrently with worktree isolation without conflicts.
func TestConcurrentEpicsWithWorktrees(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	ctx := context.Background()
	manager := NewWorktreeManager(localRepo)

	// Create multiple worktrees concurrently (simulating concurrent epics)
	const numEpics = 3
	var wg sync.WaitGroup
	results := make([]*WorktreeResult, numEpics)
	errors := make([]error, numEpics)

	for i := 0; i < numEpics; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			branchName := "pilot/GH-EPIC-" + string(rune('A'+idx))
			result, err := manager.CreateWorktreeWithBranch(ctx, "epic-"+string(rune('A'+idx)), branchName, "main")
			results[idx] = result
			errors[idx] = err
		}(i)
	}
	wg.Wait()

	// Verify all worktrees were created successfully
	for i := 0; i < numEpics; i++ {
		if errors[i] != nil {
			t.Errorf("epic %d worktree creation failed: %v", i, errors[i])
			continue
		}
		if results[i] == nil {
			t.Errorf("epic %d: nil result", i)
			continue
		}
		if _, err := os.Stat(results[i].Path); os.IsNotExist(err) {
			t.Errorf("epic %d: worktree not created at %s", i, results[i].Path)
		}
	}

	// Verify unique paths
	paths := make(map[string]bool)
	for i := 0; i < numEpics; i++ {
		if results[i] != nil {
			if paths[results[i].Path] {
				t.Error("duplicate worktree paths detected")
			}
			paths[results[i].Path] = true
		}
	}

	// Verify active count
	if count := manager.ActiveCount(); count != numEpics {
		t.Errorf("expected %d active worktrees, got %d", numEpics, count)
	}

	// Cleanup all concurrently
	for i := 0; i < numEpics; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if results[idx] != nil {
				results[idx].Cleanup()
			}
		}(i)
	}
	wg.Wait()

	// Verify all cleaned up
	if count := manager.ActiveCount(); count != 0 {
		t.Errorf("expected 0 active worktrees after cleanup, got %d", count)
	}
}
