package orchestrator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
)

// mockQualityChecker implements executor.QualityChecker for testing
type mockQualityChecker struct {
	outcomes []*executor.QualityOutcome // Sequential outcomes for multiple calls
	callIdx  int32                      // Atomic counter for thread-safe access
	err      error
}

func (m *mockQualityChecker) Check(ctx context.Context) (*executor.QualityOutcome, error) {
	if m.err != nil {
		return nil, m.err
	}
	idx := atomic.AddInt32(&m.callIdx, 1) - 1
	if int(idx) >= len(m.outcomes) {
		// Return last outcome if we've exhausted the list
		return m.outcomes[len(m.outcomes)-1], nil
	}
	return m.outcomes[idx], nil
}

func (m *mockQualityChecker) callCount() int {
	return int(atomic.LoadInt32(&m.callIdx))
}

// mockBackend implements executor.Backend for testing
type mockBackend struct {
	name           string
	execResults    []*executor.BackendResult // Results to return for each Execute call
	execIdx        int32
	execErr        error
	capturedPrompts []string // Capture prompts for verification
	mu             sync.Mutex
}

func (m *mockBackend) Name() string {
	return m.name
}

func (m *mockBackend) Execute(ctx context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	// Capture prompt
	m.mu.Lock()
	m.capturedPrompts = append(m.capturedPrompts, opts.Prompt)
	m.mu.Unlock()

	if m.execErr != nil {
		return nil, m.execErr
	}
	idx := atomic.AddInt32(&m.execIdx, 1) - 1
	if int(idx) >= len(m.execResults) {
		return m.execResults[len(m.execResults)-1], nil
	}
	return m.execResults[idx], nil
}

func (m *mockBackend) IsAvailable() bool {
	return true
}

func (m *mockBackend) execCount() int {
	return int(atomic.LoadInt32(&m.execIdx))
}

func (m *mockBackend) getPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.capturedPrompts))
	copy(result, m.capturedPrompts)
	return result
}

// TestQualityGatesHappyPath tests that quality gates pass and task completes successfully
func TestQualityGatesHappyPath(t *testing.T) {
	// Setup mock backend that returns success
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{
				Success: true,
				Output:  "Task completed",
			},
		},
	}

	// Setup mock quality checker that passes
	qualityChecker := &mockQualityChecker{
		outcomes: []*executor.QualityOutcome{
			{
				Passed:      true,
				ShouldRetry: false,
				Attempt:     0,
			},
		},
	}

	// Create runner with mock backend
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false) // Disable recording for tests

	// Set quality checker factory
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		return qualityChecker
	})

	// Create task
	task := &executor.Task{
		ID:          "TEST-001",
		Title:       "Test happy path",
		Description: "Test that quality gates pass",
		ProjectPath: t.TempDir(),
	}

	// Execute task
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify task succeeded
	if !result.Success {
		t.Errorf("Expected task to succeed, got failure: %s", result.Error)
	}

	// Verify quality checker was called exactly once
	if callCount := qualityChecker.callCount(); callCount != 1 {
		t.Errorf("Expected quality checker to be called 1 time, got %d", callCount)
	}

	// Verify backend was called exactly once (no retries)
	if execCount := backend.execCount(); execCount != 1 {
		t.Errorf("Expected backend to be called 1 time, got %d", execCount)
	}
}

// TestQualityGatesRetrySuccess tests that failing quality gates trigger retry and succeed
func TestQualityGatesRetrySuccess(t *testing.T) {
	// Setup mock backend - first call succeeds, retry call also succeeds
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{
				Success: true,
				Output:  "Initial implementation",
			},
			{
				Success: true,
				Output:  "Fixed implementation",
			},
		},
	}

	// Setup mock quality checker:
	// - First call fails with retry
	// - Second call passes
	qualityChecker := &mockQualityChecker{
		outcomes: []*executor.QualityOutcome{
			{
				Passed:        false,
				ShouldRetry:   true,
				RetryFeedback: "Test failed: expected 2 got 1",
				Attempt:       0,
			},
			{
				Passed:      true,
				ShouldRetry: false,
				Attempt:     1,
			},
		},
	}

	// Create runner with mock backend
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		return qualityChecker
	})

	task := &executor.Task{
		ID:          "TEST-002",
		Title:       "Test retry success",
		Description: "Test that retry fixes quality gate failure",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify task succeeded after retry
	if !result.Success {
		t.Errorf("Expected task to succeed after retry, got failure: %s", result.Error)
	}

	// Verify quality checker was called twice (initial + after retry)
	if callCount := qualityChecker.callCount(); callCount != 2 {
		t.Errorf("Expected quality checker to be called 2 times, got %d", callCount)
	}

	// Verify backend was called twice (initial + retry)
	if execCount := backend.execCount(); execCount != 2 {
		t.Errorf("Expected backend to be called 2 times, got %d", execCount)
	}
}

// TestQualityGatesMaxRetriesExhausted tests that task fails after max retries
func TestQualityGatesMaxRetriesExhausted(t *testing.T) {
	// Setup mock backend - all calls return success
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{Success: true, Output: "Attempt 1"},
			{Success: true, Output: "Attempt 2"},
			{Success: true, Output: "Attempt 3"},
		},
	}

	// Setup mock quality checker - always fails with retry enabled
	// After maxAutoRetries (2), the runner should stop
	qualityChecker := &mockQualityChecker{
		outcomes: []*executor.QualityOutcome{
			{Passed: false, ShouldRetry: true, RetryFeedback: "Error 1", Attempt: 0},
			{Passed: false, ShouldRetry: true, RetryFeedback: "Error 2", Attempt: 1},
			{Passed: false, ShouldRetry: true, RetryFeedback: "Error 3", Attempt: 2},
		},
	}

	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		return qualityChecker
	})

	task := &executor.Task{
		ID:          "TEST-003",
		Title:       "Test max retries",
		Description: "Test that task fails after max retries",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify task failed
	if result.Success {
		t.Error("Expected task to fail after max retries, but it succeeded")
	}

	// Verify error mentions retries
	if result.Error == "" {
		t.Error("Expected error message to be set")
	}

	// Quality checker should be called 3 times:
	// 1. After initial execution (fails)
	// 2. After retry 1 (fails)
	// 3. After retry 2 (fails, max reached)
	if callCount := qualityChecker.callCount(); callCount != 3 {
		t.Errorf("Expected quality checker to be called 3 times, got %d", callCount)
	}

	// Backend should be called 3 times:
	// 1. Initial execution
	// 2. Retry 1
	// 3. Retry 2
	if execCount := backend.execCount(); execCount != 3 {
		t.Errorf("Expected backend to be called 3 times, got %d", execCount)
	}
}

// TestQualityGatesDisabled tests that gates are skipped when factory is not set
func TestQualityGatesDisabled(t *testing.T) {
	// Setup mock backend
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{
				Success: true,
				Output:  "Task completed without quality gates",
			},
		},
	}

	// Create runner WITHOUT setting quality checker factory
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	// No quality checker factory set - gates should be skipped

	task := &executor.Task{
		ID:          "TEST-004",
		Title:       "Test disabled gates",
		Description: "Test that quality gates are skipped when disabled",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify task succeeded (no quality gates to fail)
	if !result.Success {
		t.Errorf("Expected task to succeed with disabled gates, got failure: %s", result.Error)
	}

	// Verify backend was called exactly once
	if execCount := backend.execCount(); execCount != 1 {
		t.Errorf("Expected backend to be called 1 time, got %d", execCount)
	}
}

// TestQualityGatesNoRetryOnNoShouldRetry tests that when ShouldRetry is false, no retry happens
func TestQualityGatesNoRetryOnNoShouldRetry(t *testing.T) {
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{Success: true, Output: "Completed"},
		},
	}

	// Quality checker fails but indicates no retry should happen
	qualityChecker := &mockQualityChecker{
		outcomes: []*executor.QualityOutcome{
			{
				Passed:      false,
				ShouldRetry: false, // No retry
				Attempt:     0,
			},
		},
	}

	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		return qualityChecker
	})

	task := &executor.Task{
		ID:          "TEST-005",
		Title:       "Test no retry when ShouldRetry=false",
		Description: "Test that task fails immediately when ShouldRetry is false",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	// Verify task failed
	if result.Success {
		t.Error("Expected task to fail when quality gates fail with ShouldRetry=false")
	}

	// Quality checker called only once - no retry
	if callCount := qualityChecker.callCount(); callCount != 1 {
		t.Errorf("Expected quality checker to be called 1 time, got %d", callCount)
	}

	// Backend called only once - no retry
	if execCount := backend.execCount(); execCount != 1 {
		t.Errorf("Expected backend to be called 1 time, got %d", execCount)
	}
}

// TestQualityGatesOrchestratorWiring tests that orchestrator properly wires quality checker to runner
func TestQualityGatesOrchestratorWiring(t *testing.T) {
	// Create orchestrator
	cfg := &Config{
		MaxConcurrent: 1,
	}
	orch, err := NewOrchestrator(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create orchestrator: %v", err)
	}
	defer orch.Stop()

	// Track whether factory was called
	var factoryCalled bool
	var passedTaskID, passedProjectPath string

	// Set quality checker factory on orchestrator
	orch.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		factoryCalled = true
		passedTaskID = taskID
		passedProjectPath = projectPath
		return &mockQualityChecker{
			outcomes: []*executor.QualityOutcome{
				{Passed: true},
			},
		}
	})

	// Verify internal runner has the factory set
	// We can test this indirectly by verifying the orchestrator's qualityCheckerFactory field is set
	if orch.qualityCheckerFactory == nil {
		t.Error("Expected orchestrator's qualityCheckerFactory to be set")
	}

	// Test that factory produces valid checker
	checker := orch.qualityCheckerFactory("TEST-TASK", "/test/path")
	if checker == nil {
		t.Error("Expected factory to produce a checker")
	}

	if !factoryCalled {
		t.Error("Expected factory to be called")
	}
	if passedTaskID != "TEST-TASK" {
		t.Errorf("Expected taskID 'TEST-TASK', got '%s'", passedTaskID)
	}
	if passedProjectPath != "/test/path" {
		t.Errorf("Expected projectPath '/test/path', got '%s'", passedProjectPath)
	}

	// Verify checker works
	outcome, err := checker.Check(context.Background())
	if err != nil {
		t.Errorf("Checker returned error: %v", err)
	}
	if !outcome.Passed {
		t.Error("Expected checker to pass")
	}
}

// TestQualityGatesRetryFeedbackPropagation tests that retry feedback is properly passed to backend
func TestQualityGatesRetryFeedbackPropagation(t *testing.T) {
	// Backend that captures prompts via the mockBackend struct
	backend := &mockBackend{
		name: "test-backend",
		execResults: []*executor.BackendResult{
			{Success: true, Output: "Initial"},
			{Success: true, Output: "After retry"},
		},
	}

	qualityChecker := &mockQualityChecker{
		outcomes: []*executor.QualityOutcome{
			{
				Passed:        false,
				ShouldRetry:   true,
				RetryFeedback: "### Test Failure\n\nExpected: 42\nActual: 0",
				Attempt:       0,
			},
			{Passed: true},
		},
	}

	runner := executor.NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
		return qualityChecker
	})

	task := &executor.Task{
		ID:          "TEST-006",
		Title:       "Test retry feedback",
		Description: "Original task description",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected task to succeed, got failure: %s", result.Error)
	}

	// Get captured prompts
	receivedPrompts := backend.getPrompts()

	// Should have 2 prompts
	if len(receivedPrompts) != 2 {
		t.Fatalf("Expected 2 prompts, got %d", len(receivedPrompts))
	}

	// First prompt should be original task
	if receivedPrompts[0] == "" {
		t.Error("First prompt should not be empty")
	}

	// Second prompt (retry) should contain the feedback
	retryPrompt := receivedPrompts[1]
	if retryPrompt == "" {
		t.Error("Retry prompt should not be empty")
	}

	// Verify retry prompt contains Quality Gate Retry header and feedback
	if !strings.Contains(retryPrompt, "Quality Gate Retry") {
		t.Error("Retry prompt should contain 'Quality Gate Retry'")
	}
	if !strings.Contains(retryPrompt, "Expected: 42") {
		t.Error("Retry prompt should contain the error feedback")
	}
}
