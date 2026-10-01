package executor

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// TestQualityCheckerFactory_IsolatedWorktreeGetsProjectPath guards GH-5577: a
// task executed in an isolated worktree must hand the factory the task's
// project root (what per-project `quality:` overrides are keyed on) as its
// projectPath argument, and the worktree directory only as the executionPath
// the gates run in. Passing the worktree as the lookup key made every
// worktree execution silently fall back to the global gates.
func TestQualityCheckerFactory_IsolatedWorktreeGetsProjectPath(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	runner := NewRunnerWithBackend(&stackingAttemptRecorderBackend{})
	runner.config = &BackendConfig{UseWorktree: true}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	var mu sync.Mutex
	type factoryCall struct{ taskID, projectPath, executionPath string }
	var calls []factoryCall
	runner.SetQualityCheckerFactory(func(taskID, projectPath, executionPath string) QualityChecker {
		mu.Lock()
		calls = append(calls, factoryCall{taskID, projectPath, executionPath})
		mu.Unlock()
		return &mockQualityChecker{outcome: &QualityOutcome{Passed: true}}
	})

	task := &Task{
		ID:          "GH-5577-worktree",
		Title:       "quality factory lookup path in worktree mode",
		Description: "the factory must be keyed by the project root, not the worktree",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-5577-worktree",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Outcome (PR creation etc.) is irrelevant here — only the factory args.
	_, _ = runner.Execute(ctx, task)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("quality checker factory was never called")
	}
	for _, c := range calls {
		if c.projectPath != localRepo {
			t.Errorf("factory projectPath = %q, want the task's ProjectPath %q (the worktree path must not be used for the lookup)", c.projectPath, localRepo)
		}
		if c.executionPath == "" || c.executionPath == localRepo {
			t.Errorf("factory executionPath = %q, want an isolated worktree distinct from the project root %q", c.executionPath, localRepo)
		}
	}
}

// TestQualityCheckerFactory_AutoEnabledGateRunsInExecutionPath guards that the
// auto-enabled minimal gate (no factory configured) still runs in the
// execution path after the GH-5577 signature change, ignoring the lookup path.
func TestQualityCheckerFactory_AutoEnabledGateRunsInExecutionPath(t *testing.T) {
	execDir := t.TempDir()
	if err := os.WriteFile(execDir+"/go.mod", []byte("module autoenabletest\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := NewRunner()
	runner.ensureQualityCheckerFactory(execDir, testLog)
	if runner.qualityCheckerFactory == nil {
		t.Fatal("expected ensureQualityCheckerFactory to auto-enable a factory for a Go project")
	}

	checker, ok := runner.qualityCheckerFactory("t", "/some/other/project/root", execDir).(*simpleQualityChecker)
	if !ok {
		t.Fatal("expected a *simpleQualityChecker")
	}
	if checker.projectPath != execDir {
		t.Errorf("auto-enabled gate projectPath = %q, want execution path %q", checker.projectPath, execDir)
	}
}
