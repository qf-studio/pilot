package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// mockPRVerifier implements PRVerifier for testing.
type mockPRVerifier struct {
	returnErr error
	returnNum int
}

func (m *mockPRVerifier) GetPRByURL(_ context.Context, _ string) (int, error) {
	return m.returnNum, m.returnErr
}

// TestPRVerifier_Interface verifies that mockPRVerifier satisfies PRVerifier.
func TestPRVerifier_Interface(t *testing.T) {
	var _ PRVerifier = &mockPRVerifier{}
}

// TestSetPRVerifier_Wiring verifies that SetPRVerifier stores the verifier on the runner.
func TestSetPRVerifier_Wiring(t *testing.T) {
	r := NewRunner()
	if r.prVerifier != nil {
		t.Fatal("prVerifier should be nil before SetPRVerifier")
	}
	v := &mockPRVerifier{returnNum: 42}
	r.SetPRVerifier(v)
	if r.prVerifier == nil {
		t.Fatal("prVerifier should be set after SetPRVerifier")
	}
	got, err := r.prVerifier.GetPRByURL(context.Background(), "https://github.com/o/r/pull/42")
	if err != nil || got != 42 {
		t.Errorf("GetPRByURL() = (%d, %v), want (42, nil)", got, err)
	}
}

// TestDispatcher_EmptyPRURL_PersistedAsFailed verifies that when the runner signals
// failure because PR creation returned an empty URL (GH-2478), the dispatcher
// persists the execution row as "failed" — not "completed".
//
// The executeFunc simulates what runner.Execute now returns after the empty-URL guard:
// Success=false with the canonical error message.
func TestDispatcher_EmptyPRURL_PersistedAsFailed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Simulate runner behaviour after GH-2478 fix: CreatePR returns ("", nil)
	// so runner sets Success=false with the sentinel error.
	execFn := func(_ context.Context, _ *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			Success: false,
			Error:   "PR creation reported success but returned empty URL",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	dispatcher := NewDispatcher(store, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-2478-empty-url",
		Title:       "feat(test): verify empty PR URL persisted as failed",
		Description: "Regression test for GH-2478",
		ProjectPath: "/tmp/gh2478-test",
		CreatePR:    true,
		Branch:      "pilot/GH-2478-empty-url",
	}

	execID, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("QueueTask: %v", err)
	}

	// Poll until terminal status (failed or completed).
	deadline := time.Now().Add(8 * time.Second)
	var finalExec *memory.Execution
	for time.Now().Before(deadline) {
		exec, getErr := store.GetExecution(execID)
		if getErr != nil {
			t.Fatalf("GetExecution: %v", getErr)
		}
		if exec.Status == "failed" || exec.Status == "completed" {
			finalExec = exec
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalExec == nil {
		t.Fatal("execution never reached terminal state within deadline")
	}
	if finalExec.Status != "failed" {
		t.Errorf("execution status = %q, want %q (GH-2478: empty PR URL must not be completed)",
			finalExec.Status, "failed")
	}
}

// TestDispatcher_PRVerifierFailure_PersistedAsFailed verifies that when the PRVerifier
// cannot confirm the PR is observable, the dispatcher persists the row as "failed".
func TestDispatcher_PRVerifierFailure_PersistedAsFailed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Simulate runner behaviour when prVerifier.GetPRByURL returns an error.
	execFn := func(_ context.Context, _ *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			Success: false,
			Error:   "PR created but not observable via API: 404 not found",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	dispatcher := NewDispatcher(store, runner, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-2478-verify-fail",
		Title:       "feat(test): verify unobservable PR persisted as failed",
		Description: "Regression test for GH-2478",
		ProjectPath: "/tmp/gh2478-verify-test",
		CreatePR:    true,
		Branch:      "pilot/GH-2478-verify-fail",
	}

	execID, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("QueueTask: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var finalExec *memory.Execution
	for time.Now().Before(deadline) {
		exec, getErr := store.GetExecution(execID)
		if getErr != nil {
			t.Fatalf("GetExecution: %v", getErr)
		}
		if exec.Status == "failed" || exec.Status == "completed" {
			finalExec = exec
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalExec == nil {
		t.Fatal("execution never reached terminal state within deadline")
	}
	if finalExec.Status != "failed" {
		t.Errorf("execution status = %q, want %q (GH-2478: unobservable PR must not be completed)",
			finalExec.Status, "failed")
	}
}

// TestPRVerifier_MockSuccess verifies mock returns correct PR number on success path.
func TestPRVerifier_MockSuccess(t *testing.T) {
	v := &mockPRVerifier{returnNum: 99, returnErr: nil}
	num, err := v.GetPRByURL(context.Background(), "https://github.com/o/r/pull/99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if num != 99 {
		t.Errorf("got PR number %d, want 99", num)
	}
}

// TestPRVerifier_MockFailure verifies mock returns error on failure path.
func TestPRVerifier_MockFailure(t *testing.T) {
	sentinel := errors.New("PR not found")
	v := &mockPRVerifier{returnErr: sentinel}
	_, err := v.GetPRByURL(context.Background(), "https://github.com/o/r/pull/99")
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}
