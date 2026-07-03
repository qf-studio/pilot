package executor

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// mockLearningRecorder implements LearningRecorder for testing.
type mockLearningRecorder struct {
	mu        sync.Mutex
	calls     []*learningCall
	returnErr error
}

type learningCall struct {
	exec            *memory.Execution
	appliedPatterns []string
}

func (m *mockLearningRecorder) RecordExecution(_ context.Context, exec *memory.Execution, appliedPatterns []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, &learningCall{exec: exec, appliedPatterns: appliedPatterns})
	return m.returnErr
}

func TestRecordLearning_Success(t *testing.T) {
	runner := NewRunner()
	mock := &mockLearningRecorder{}
	runner.SetLearningLoop(mock)

	task := &Task{
		ID:          "task-success",
		Title:       "Test task",
		ProjectPath: "/tmp/test-project",
	}

	result := &ExecutionResult{
		Success:      true,
		Output:       "all tests passed",
		Duration:     5 * time.Second,
		PRUrl:        "https://github.com/org/repo/pull/42",
		CommitSHA:    "abc123",
		TokensInput:  1000,
		TokensOutput: 500,
		FilesChanged: 3,
		ModelName:    "claude-sonnet-4-5-20250514",
	}

	runner.recordLearning(context.Background(), task, result)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 RecordExecution call, got %d", len(mock.calls))
	}

	call := mock.calls[0]
	if call.exec.Status != "completed" {
		t.Errorf("status = %q, want %q", call.exec.Status, "completed")
	}
	if call.exec.TaskID != "task-success" {
		t.Errorf("taskID = %q, want %q", call.exec.TaskID, "task-success")
	}
	if call.exec.ProjectPath != "/tmp/test-project" {
		t.Errorf("projectPath = %q, want %q", call.exec.ProjectPath, "/tmp/test-project")
	}
	if call.exec.Output != "all tests passed" {
		t.Errorf("output = %q, want %q", call.exec.Output, "all tests passed")
	}
	if call.exec.PRUrl != "https://github.com/org/repo/pull/42" {
		t.Errorf("prUrl = %q, want %q", call.exec.PRUrl, "https://github.com/org/repo/pull/42")
	}
	if call.exec.CommitSHA != "abc123" {
		t.Errorf("commitSHA = %q, want %q", call.exec.CommitSHA, "abc123")
	}
	if call.exec.DurationMs != 5000 {
		t.Errorf("durationMs = %d, want %d", call.exec.DurationMs, 5000)
	}
	if call.exec.TokensInput != 1000 {
		t.Errorf("tokensInput = %d, want %d", call.exec.TokensInput, 1000)
	}
	if call.exec.TokensOutput != 500 {
		t.Errorf("tokensOutput = %d, want %d", call.exec.TokensOutput, 500)
	}
	if call.exec.FilesChanged != 3 {
		t.Errorf("filesChanged = %d, want %d", call.exec.FilesChanged, 3)
	}
	if call.exec.ModelName != "claude-sonnet-4-5-20250514" {
		t.Errorf("modelName = %q, want %q", call.exec.ModelName, "claude-sonnet-4-5-20250514")
	}
	if call.appliedPatterns != nil {
		t.Errorf("appliedPatterns = %v, want nil", call.appliedPatterns)
	}
}

// TestRecordLearning_UsesExecutionID verifies that when the dispatcher has set
// Task.ExecutionID (the executions.id UUID), recordLearning uses it for
// memory.Execution.ID instead of the human-readable task ID, so
// pattern_feedback.execution_id (feedback.go:80) can join against executions.id.
// GH-3764.
func TestRecordLearning_UsesExecutionID(t *testing.T) {
	runner := NewRunner()
	mock := &mockLearningRecorder{}
	runner.SetLearningLoop(mock)

	task := &Task{
		ID:          "GH-3714",
		ExecutionID: "11111111-2222-3333-4444-555555555555",
		Title:       "Test task",
		ProjectPath: "/tmp/test-project",
	}

	result := &ExecutionResult{Success: true, Output: "done", Duration: time.Second}

	runner.recordLearning(context.Background(), task, result)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 RecordExecution call, got %d", len(mock.calls))
	}
	call := mock.calls[0]
	if call.exec.ID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("exec.ID = %q, want the execution UUID", call.exec.ID)
	}
	if call.exec.TaskID != "GH-3714" {
		t.Errorf("exec.TaskID = %q, want the human-readable task ID", call.exec.TaskID)
	}
}

// TestRecordLearning_FallsBackToTaskID verifies that tasks without a dedicated
// executions row (decomposed subtasks, epic sub-issues, local/bench runs) still
// get a non-empty memory.Execution.ID by falling back to task.ID. GH-3764.
func TestRecordLearning_FallsBackToTaskID(t *testing.T) {
	runner := NewRunner()
	mock := &mockLearningRecorder{}
	runner.SetLearningLoop(mock)

	task := &Task{ID: "task-no-exec-id", Title: "Test task", ProjectPath: "/tmp/test-project"}
	result := &ExecutionResult{Success: true, Output: "done", Duration: time.Second}

	runner.recordLearning(context.Background(), task, result)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 RecordExecution call, got %d", len(mock.calls))
	}
	if call := mock.calls[0]; call.exec.ID != "task-no-exec-id" {
		t.Errorf("exec.ID = %q, want fallback to task.ID", call.exec.ID)
	}
}

func TestRecordLearning_Failure(t *testing.T) {
	runner := NewRunner()
	mock := &mockLearningRecorder{}
	runner.SetLearningLoop(mock)

	task := &Task{
		ID:          "task-failure",
		Title:       "Failing task",
		ProjectPath: "/tmp/test-project",
	}

	result := &ExecutionResult{
		Success:      false,
		Error:        "compilation failed",
		Output:       "error: undefined variable",
		Duration:     2 * time.Second,
		TokensInput:  800,
		TokensOutput: 200,
		FilesChanged: 1,
		ModelName:    "claude-sonnet-4-5-20250514",
	}

	runner.recordLearning(context.Background(), task, result)

	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 RecordExecution call, got %d", len(mock.calls))
	}

	call := mock.calls[0]
	if call.exec.Status != "failed" {
		t.Errorf("status = %q, want %q", call.exec.Status, "failed")
	}
	if call.exec.Error != "compilation failed" {
		t.Errorf("error = %q, want %q", call.exec.Error, "compilation failed")
	}
	if call.exec.TaskID != "task-failure" {
		t.Errorf("taskID = %q, want %q", call.exec.TaskID, "task-failure")
	}
}

func TestRecordLearning_NilLearningLoop(t *testing.T) {
	runner := NewRunner()
	// learningLoop is nil by default

	task := &Task{
		ID:          "task-nil",
		Title:       "Test nil loop",
		ProjectPath: "/tmp/test",
	}

	result := &ExecutionResult{
		Success:  true,
		Output:   "done",
		Duration: time.Second,
	}

	// Should not panic
	runner.recordLearning(context.Background(), task, result)
}

func TestRecordLearning_ErrorDoesNotPanic(t *testing.T) {
	runner := NewRunner()
	mock := &mockLearningRecorder{
		returnErr: fmt.Errorf("database connection lost"),
	}
	runner.SetLearningLoop(mock)

	task := &Task{
		ID:          "task-learn-err",
		Title:       "Test learning error",
		ProjectPath: "/tmp/test",
	}

	result := &ExecutionResult{
		Success:  true,
		Output:   "all good",
		Duration: time.Second,
	}

	// Should not panic - error is logged but not propagated
	runner.recordLearning(context.Background(), task, result)

	if len(mock.calls) != 1 {
		t.Fatalf("expected RecordExecution to still be called, got %d calls", len(mock.calls))
	}
}

func TestRecordPatternOutcomes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "outcome-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Create patterns linked to a project
	_ = store.SaveCrossPattern(&memory.CrossPattern{
		ID: "pat-1", Type: "code", Title: "P1", Confidence: 0.8, Scope: "org",
	})
	_ = store.SaveCrossPattern(&memory.CrossPattern{
		ID: "pat-2", Type: "code", Title: "P2", Confidence: 0.7, Scope: "org",
	})
	_ = store.LinkPatternToProject("pat-1", "/test/project")
	_ = store.LinkPatternToProject("pat-2", "/test/project")

	runner := NewRunner()
	runner.SetLogStore(store)

	task := &Task{
		ID:          "task-outcomes",
		Title:       "feat: add login",
		ProjectPath: "/test/project",
	}

	result := &ExecutionResult{
		Success:   true,
		ModelName: "claude-opus-4-6",
	}

	runner.recordPatternOutcomes(task, result)

	// Verify outcomes were recorded via contextual confidence
	c1 := store.GetContextualConfidence("pat-1", "/test/project", "feat")
	if c1 <= 0 {
		t.Errorf("expected positive contextual confidence for pat-1, got %f", c1)
	}
	c2 := store.GetContextualConfidence("pat-2", "/test/project", "feat")
	if c2 <= 0 {
		t.Errorf("expected positive contextual confidence for pat-2, got %f", c2)
	}
}

// TestRecordPatternOutcomes_EmptyModelName covers the GH-3764 fix: when the
// backend stream never surfaced a model name, recordPatternOutcomes must fall
// back through Runner.fallbackModelName() (config-derived) instead of the
// stale hardcoded "claude-opus-4-6" literal that GH-2428 already eliminated
// at the other execution_metrics call sites.
func TestRecordPatternOutcomes_EmptyModelName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "outcome-empty-model-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveCrossPattern(&memory.CrossPattern{
		ID: "pat-empty-model", Type: "code", Title: "P1", Confidence: 0.8, Scope: "org",
	})
	_ = store.LinkPatternToProject("pat-empty-model", "/test/project")

	runner := NewRunner()
	runner.SetLogStore(store)

	task := &Task{
		ID:          "task-empty-model",
		Title:       "feat: add signup",
		ProjectPath: "/test/project",
	}
	result := &ExecutionResult{Success: true, ModelName: ""}

	runner.recordPatternOutcomes(task, result)

	c := store.GetContextualConfidence("pat-empty-model", "/test/project", "feat")
	if c <= 0 {
		t.Errorf("expected positive contextual confidence for pat-empty-model, got %f", c)
	}
}

func TestRecordPatternOutcomes_NilStore(t *testing.T) {
	runner := NewRunner()
	// logStore is nil by default

	task := &Task{ID: "task-nil", Title: "fix: nil", ProjectPath: "/test"}
	result := &ExecutionResult{Success: true}

	// Should not panic
	runner.recordPatternOutcomes(task, result)
}

func TestLearningRecorder_Interface(t *testing.T) {
	// Verify that *memory.LearningLoop satisfies the LearningRecorder interface
	// at compile time. This is a compile-time check only.
	var _ LearningRecorder = (*memory.LearningLoop)(nil)
}
