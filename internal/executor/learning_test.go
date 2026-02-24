package executor

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
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

func TestLearningRecorder_Interface(t *testing.T) {
	// Verify that *memory.LearningLoop satisfies the LearningRecorder interface
	// at compile time. This is a compile-time check only.
	var _ LearningRecorder = (*memory.LearningLoop)(nil)
}
