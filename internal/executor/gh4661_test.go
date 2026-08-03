package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecuteSubIssuesTracked_GenerationRetry_RedispatchesFailedChild is the
// core GH-4661 unit test for the "re-dispatch failed children at
// generation+1" half of the coordinator-resume contract.
//
// TestExecuteSubIssues_RepickDoesNotBypassBackoff (epic_repick_backoff_test.go)
// already proves that with generationRetry=false (every caller before
// GH-4661), a repicked epic re-discovering an already-failed child collides
// with its own terminal generation-0 claim and never re-invokes the backend.
// This test proves the deliberate exception GH-4661 adds: when
// executeSubIssuesTracked is driven with generationRetry=true (the
// coordinator-resume path, resumeDecomposedCoordinator) and
// r.childGenerationRetryFn grants a fresh generation for the failed child
// (as Dispatcher.beginWithGenerationRetry's childGenerationRetry wrapper
// would for a genuinely dead, terminal-but-not-done claim), the backend IS
// invoked again — the failed child gets a real second attempt instead of
// being silently dropped.
func TestExecuteSubIssuesTracked_GenerationRetry_RedispatchesFailedChild(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	var mu sync.Mutex
	execCalls := 0
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		defer mu.Unlock()
		execCalls++
		if execCalls == 1 {
			return &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom: first attempt fails"}, nil
		}
		return &ExecutionResult{TaskID: task.ID, Success: true, TokensOutput: 100, FilesChanged: 1}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second

	issues := makeSubIssues(1, 9601)
	parent := &Task{ID: "GH-9600", Title: "[epic] GH-4661 generation-retry redispatch test"}

	// First attempt: plain generation-0 claim (every pre-GH-4661 caller).
	// Claims the child, invokes the backend once, and fails.
	_, _, err1 := runner.executeSubIssuesTracked(context.Background(), parent, issues, "", "", false)
	if err1 == nil {
		t.Fatal("expected the first attempt to report the child as failed")
	}

	mu.Lock()
	firstCount := execCalls
	mu.Unlock()
	if firstCount != 1 {
		t.Fatalf("backend invocations after first attempt = %d, want 1", firstCount)
	}

	// Second attempt: the coordinator-resume path (resumeDecomposedCoordinator,
	// GH-4661) re-discovers the same failed child and grants it a fresh
	// generation via childGenerationRetryFn — simulating what
	// Dispatcher.childGenerationRetry would do once nextRetryGeneration
	// confirms the prior claim is genuinely dead (terminal-but-not-done).
	runner.childGenerationRetryFn = func(subTask *Task) (string, bool, error) {
		execID := "exec-retry-gen1-" + subTask.ID
		// Mirrors what Dispatcher.beginWithGenerationRetry's real Begin call
		// does: write a fresh execution row for the new generation so the
		// caller's later finalize step has a real row to update.
		if saveErr := store.SaveExecution(&memory.Execution{ID: execID, TaskID: subTask.ID, Status: "running"}); saveErr != nil {
			return "", false, saveErr
		}
		return execID, true, nil
	}

	_, _, err2 := runner.executeSubIssuesTracked(context.Background(), parent, issues, "", "", true)
	if err2 != nil {
		t.Fatalf("expected the retried attempt to succeed, got error: %v", err2)
	}

	mu.Lock()
	secondCount := execCalls
	mu.Unlock()
	if secondCount != 2 {
		t.Errorf("total backend invocations = %d, want 2 (generation+1 retry must re-invoke the backend for the failed child)", secondCount)
	}
}

// TestExecuteSubIssuesTracked_GenerationRetry_WaitsOnLiveChild is the GH-4661
// unit test for the other half of the coordinator-resume contract: "wait on
// any in-flight children" instead of racing them with a duplicate dispatch.
//
// When r.childGenerationRetryFn reports ok=false (the signal
// Dispatcher.childGenerationRetry/beginWithGenerationRetry/nextRetryGeneration
// produce for a still-LIVE owner — a claim that is not actually dead), this
// must never fall through to invoking the backend a second time. Instead it
// routes into the same ErrClaimLost -> reconcileChildOutcome(externallyOwned)
// poll every other externally-owned child already uses, adopting the live
// owner's real terminal outcome once it lands.
func TestExecuteSubIssuesTracked_GenerationRetry_WaitsOnLiveChild(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-9701"
	if err := store.SaveExecution(&memory.Execution{
		ID:     "exec-live-owner",
		TaskID: taskID,
		Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	var mu sync.Mutex
	execCalls := 0
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		execCalls++
		mu.Unlock()
		return &ExecutionResult{TaskID: task.ID, Success: true}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second
	// Reports "no fresh generation warranted" — the live owner above is still
	// genuinely in flight, so nextRetryGeneration (dispatcher.go) would decline
	// the retry rather than grant a colliding generation+1 claim.
	runner.childGenerationRetryFn = func(subTask *Task) (string, bool, error) {
		return "", false, nil
	}

	issues := makeSubIssues(1, 9701)
	parent := &Task{ID: "GH-9700", Title: "[epic] GH-4661 generation-retry wait test"}

	var prCalls []subIssuePRCall
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName, issueNodeID string) {
		prCalls = append(prCalls, subIssuePRCall{PRNumber: prNumber, PRURL: prURL, IssueNumber: issueNumber, CommitSHA: commitSHA, BranchName: branchName})
	})

	// The live owner finishes shortly after the coordinator-resume call starts
	// waiting on it.
	go func() {
		time.Sleep(60 * time.Millisecond)
		if mcErr := store.MarkExecutionCompleted("exec-live-owner", "https://github.com/owner/repo/pull/9701", "sha-live", 1000); mcErr != nil {
			t.Errorf("MarkExecutionCompleted: %v", mcErr)
		}
	}()

	_, _, err = runner.executeSubIssuesTracked(context.Background(), parent, issues, "", "", true)
	if err != nil {
		t.Fatalf("expected the coordinator-resume wait to adopt the live owner's eventual success, got error: %v", err)
	}

	mu.Lock()
	calls := execCalls
	mu.Unlock()
	if calls != 0 {
		t.Errorf("backend invocations = %d, want 0 — a genuinely live child must be waited on, never re-dispatched", calls)
	}
	if len(prCalls) != 1 {
		t.Fatalf("PR callback count = %d, want 1 (from the live owner's real completion)", len(prCalls))
	}
	if prCalls[0].PRNumber != 9701 {
		t.Errorf("PR callback PRNumber = %d, want 9701", prCalls[0].PRNumber)
	}
	if !strings.Contains(prCalls[0].CommitSHA, "sha-live") {
		t.Errorf("PR callback CommitSHA = %q, want it to reflect the live owner's row", prCalls[0].CommitSHA)
	}
}
