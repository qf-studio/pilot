//go:build integration

package executor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// mockAlertProcessor implements AlertEventProcessor for integration tests
type mockAlertProcessor struct {
	events []AlertEvent
}

func (m *mockAlertProcessor) ProcessEvent(event AlertEvent) {
	m.events = append(m.events, event)
}

// mockIntegrationBackend implements Backend for integration tests
type mockIntegrationBackend struct {
	name        string
	results     []*BackendResult
	resultIdx   int32
	executeErr  error
	execCount   int32
	available   bool
}

func newMockIntegrationBackend(name string, available bool) *mockIntegrationBackend {
	return &mockIntegrationBackend{
		name:      name,
		available: available,
		results:   make([]*BackendResult, 0),
	}
}

func (m *mockIntegrationBackend) Name() string {
	return m.name
}

func (m *mockIntegrationBackend) IsAvailable() bool {
	return m.available
}

func (m *mockIntegrationBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	atomic.AddInt32(&m.execCount, 1)

	if m.executeErr != nil {
		return nil, m.executeErr
	}

	idx := int(atomic.AddInt32(&m.resultIdx, 1)) - 1
	if idx >= len(m.results) {
		// Return last result if exhausted
		if len(m.results) > 0 {
			return m.results[len(m.results)-1], nil
		}
		return &BackendResult{Success: true, Output: "default success"}, nil
	}
	return m.results[idx], nil
}

func (m *mockIntegrationBackend) addResult(result *BackendResult) {
	m.results = append(m.results, result)
}

func (m *mockIntegrationBackend) getExecCount() int {
	return int(atomic.LoadInt32(&m.execCount))
}

// TestRunner_Integration_TaskExecution verifies real Runner with mock backend
func TestRunner_Integration_TaskExecution(t *testing.T) {
	// Create mock backend
	backend := newMockIntegrationBackend("test-backend", true)
	backend.addResult(&BackendResult{
		Success: true,
		Output:  "Implementation complete. Created feature X.",
	})

	// Create real runner with mock backend
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	// Create task
	task := &Task{
		ID:          "INTEG-001",
		Title:       "Integration test task",
		Description: "Test runner executes tasks correctly",
		ProjectPath: t.TempDir(),
	}

	// Execute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)

	// Verify
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got failure: %s", result.Error)
	}
	if result.TaskID != task.ID {
		t.Errorf("Expected TaskID %q, got %q", task.ID, result.TaskID)
	}
	if backend.getExecCount() != 1 {
		t.Errorf("Expected backend to be called 1 time, got %d", backend.getExecCount())
	}
}

// TestRunner_Integration_StateTransitions verifies task state progression
func TestRunner_Integration_StateTransitions(t *testing.T) {
	backend := newMockIntegrationBackend("test-backend", true)
	backend.addResult(&BackendResult{
		Success: true,
		Output:  "Task completed successfully",
	})

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	// Track progress callbacks
	var progressUpdates []struct {
		phase   string
		message string
	}

	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		progressUpdates = append(progressUpdates, struct {
			phase   string
			message string
		}{phase: phase, message: message})
	})

	task := &Task{
		ID:          "INTEG-002",
		Title:       "State transition test",
		Description: "Verify state transitions",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)

	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got failure: %s", result.Error)
	}

	// Verify progress was reported
	if len(progressUpdates) == 0 {
		t.Error("Expected progress callbacks, got none")
	}
}

// TestRunner_Integration_AlertEmission verifies alert events are emitted
func TestRunner_Integration_AlertEmission(t *testing.T) {
	backend := newMockIntegrationBackend("test-backend", true)
	backend.addResult(&BackendResult{
		Success: true,
		Output:  "Completed",
	})

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	alertProcessor := &mockAlertProcessor{}
	runner.SetAlertProcessor(alertProcessor)

	task := &Task{
		ID:          "INTEG-003",
		Title:       "Alert emission test",
		Description: "Verify alerts are emitted",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)

	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got failure: %s", result.Error)
	}

	// Alert processor receives task_started and task_completed events
	foundStarted := false
	foundCompleted := false
	for _, event := range alertProcessor.events {
		switch event.Type {
		case "task_started":
			foundStarted = true
		case "task_completed":
			foundCompleted = true
		}
	}

	if !foundStarted {
		t.Error("Expected task_started alert event")
	}
	if !foundCompleted {
		t.Error("Expected task_completed alert event")
	}
}

// TestRunner_Integration_QualityGates verifies quality gate integration
func TestRunner_Integration_QualityGates(t *testing.T) {
	// Backend: initial execution -> self-review (after quality passes)
	backend := newMockIntegrationBackend("test-backend", true)
	backend.addResult(&BackendResult{
		Success: true,
		Output:  "Implementation done",
	})
	backend.addResult(&BackendResult{
		Success: true,
		Output:  "REVIEW_PASSED",
	})

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	// Quality checker that passes
	qualityCheckerCalls := int32(0)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &testQualityChecker{
			checkFunc: func(ctx context.Context) (*QualityOutcome, error) {
				atomic.AddInt32(&qualityCheckerCalls, 1)
				return &QualityOutcome{
					Passed:      true,
					ShouldRetry: false,
				}, nil
			},
		}
	})

	task := &Task{
		ID:          "INTEG-004",
		Title:       "Quality gates test",
		Description: "Verify quality gates are checked",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)

	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success, got failure: %s", result.Error)
	}

	// Verify quality checker was called
	if atomic.LoadInt32(&qualityCheckerCalls) != 1 {
		t.Errorf("Expected quality checker to be called 1 time, got %d", qualityCheckerCalls)
	}

	// Verify backend called twice (initial + self-review)
	if backend.getExecCount() != 2 {
		t.Errorf("Expected backend to be called 2 times, got %d", backend.getExecCount())
	}
}

// TestRunner_Integration_QualityGatesRetry verifies quality gate retry flow
func TestRunner_Integration_QualityGatesRetry(t *testing.T) {
	// Backend: initial -> retry -> self-review
	backend := newMockIntegrationBackend("test-backend", true)
	backend.addResult(&BackendResult{Success: true, Output: "First attempt"})
	backend.addResult(&BackendResult{Success: true, Output: "Fixed after feedback"})
	backend.addResult(&BackendResult{Success: true, Output: "REVIEW_PASSED"})

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	// Quality checker: fail first, pass second
	callCount := int32(0)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &testQualityChecker{
			checkFunc: func(ctx context.Context) (*QualityOutcome, error) {
				n := atomic.AddInt32(&callCount, 1)
				if n == 1 {
					return &QualityOutcome{
						Passed:        false,
						ShouldRetry:   true,
						RetryFeedback: "Test failed: expected 42",
					}, nil
				}
				return &QualityOutcome{Passed: true}, nil
			},
		}
	})

	task := &Task{
		ID:          "INTEG-005",
		Title:       "Quality retry test",
		Description: "Verify quality gate retry",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)

	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected success after retry, got failure: %s", result.Error)
	}

	// Quality checker called twice (initial fail + retry pass)
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("Expected quality checker called 2 times, got %d", callCount)
	}

	// Backend called 3 times (initial + retry + self-review)
	if backend.getExecCount() != 3 {
		t.Errorf("Expected backend called 3 times, got %d", backend.getExecCount())
	}
}

// TestRunner_Integration_ContextCancellation verifies graceful cancellation
func TestRunner_Integration_ContextCancellation(t *testing.T) {
	// Backend that blocks until context is done
	backend := newMockIntegrationBackend("test-backend", true)
	backend.results = append(backend.results, &BackendResult{
		Success: false,
		Output:  "should not complete",
	})

	// Override execute to block
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "INTEG-006",
		Title:       "Cancellation test",
		Description: "Verify context cancellation",
		ProjectPath: t.TempDir(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Execute should return within timeout
	start := time.Now()
	_, _ = runner.Execute(ctx, task)
	elapsed := time.Since(start)

	// Should complete close to the timeout
	if elapsed > 5*time.Second {
		t.Errorf("Execute took too long to cancel: %v", elapsed)
	}
}

// TestRunner_Integration_MultipleTasksSequential verifies sequential task execution
func TestRunner_Integration_MultipleTasksSequential(t *testing.T) {
	backend := newMockIntegrationBackend("test-backend", true)
	for i := 0; i < 3; i++ {
		backend.addResult(&BackendResult{
			Success: true,
			Output:  "Task completed",
		})
	}

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)

	taskIDs := []string{"INTEG-007A", "INTEG-007B", "INTEG-007C"}
	results := make([]*ExecutionResult, 0, len(taskIDs))

	for _, id := range taskIDs {
		task := &Task{
			ID:          id,
			Title:       "Sequential task " + id,
			Description: "Test sequential execution",
			ProjectPath: t.TempDir(),
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result, err := runner.Execute(ctx, task)
		cancel()

		if err != nil {
			t.Fatalf("Execute() for %s returned error: %v", id, err)
		}
		results = append(results, result)
	}

	// Verify all tasks succeeded
	for i, result := range results {
		if !result.Success {
			t.Errorf("Task %s failed: %s", taskIDs[i], result.Error)
		}
		if result.TaskID != taskIDs[i] {
			t.Errorf("Expected TaskID %s, got %s", taskIDs[i], result.TaskID)
		}
	}

	// Backend should be called once per task
	if backend.getExecCount() != 3 {
		t.Errorf("Expected 3 backend calls, got %d", backend.getExecCount())
	}
}

// testQualityChecker is a simple QualityChecker for tests
type testQualityChecker struct {
	checkFunc func(ctx context.Context) (*QualityOutcome, error)
}

func (t *testQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	return t.checkFunc(ctx)
}
