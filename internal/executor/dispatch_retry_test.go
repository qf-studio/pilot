package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestDispatcher_QueueSingleTask_RetriesGenerationAfterTerminalFailure is the
// GH-4372 regression test: a task whose generation-0 execution terminated
// "failed" must not be permanently stuck. Before this fix, queueSingleTask
// always called Begin at generation 0 — since that generation's claim was
// already permanently occupied by the dead (failed) execution, every re-pick
// lost with ErrClaimLost and was dropped silently forever, identical to the
// live-owner duplicate-pickup case it was meant to guard against.
func TestDispatcher_QueueSingleTask_RetriesGenerationAfterTerminalFailure(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-4370", "/project-gh-4370"

	// Generation 0 ran and failed — mirrors GH-4370's repro (a machine-wide
	// DNS outage killed the session mid-run).
	failedTask := &Task{ID: taskID, ProjectPath: projectPath}
	failedExecID, err := NewExecutionLifecycle(store).Begin(failedTask, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: generation 0 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(failedExecID, "failed", "dial tcp: lookup api.github.com: no such host"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// The next poll tick re-picks the same task_id.
	retryTask := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err := dispatcher.queueSingleTask(context.Background(), retryTask)
	if err != nil {
		t.Fatalf("expected the re-pick to claim generation+1 and queue successfully, got error: %v", err)
	}
	if execID == "" {
		t.Fatal("expected a fresh execution ID for the generation+1 retry, got empty string (still stuck dropping the pickup)")
	}
	if execID == failedExecID {
		t.Errorf("expected a distinct execution ID from the failed generation 0 attempt, got the same ID %q", execID)
	}

	gen, claimedExecID, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration failed: %v", err)
	}
	if !found {
		t.Fatal("expected a claim row after the retry")
	}
	if gen != 1 {
		t.Errorf("expected the retry to claim generation 1, got generation %d", gen)
	}
	if claimedExecID != execID {
		t.Errorf("expected the generation 1 claim to reference %q, got %q", execID, claimedExecID)
	}

	if strings.Contains(buf.String(), "dispatch claim lost — task already owned") {
		t.Errorf("did not expect the duplicate-pickup drop message for a successful retry, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "claiming next generation for retry") {
		t.Errorf("expected a log noting the generation+1 retry, got: %s", buf.String())
	}
}

// TestDispatcher_QueueSingleTask_LiveOwnerStillDropsSilently proves GH-4372's
// fix preserves the original ErrClaimLost duplicate-prevention: a claim held
// by a still-LIVE execution (queued/running) must keep dropping the
// duplicate pickup silently rather than being (mis)treated as a dead claim
// eligible for a generation+1 retry.
func TestDispatcher_QueueSingleTask_LiveOwnerStillDropsSilently(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-4372-LIVE", "/project-gh-4372-live"

	liveTask := &Task{ID: taskID, ProjectPath: projectPath}
	liveExecID, err := NewExecutionLifecycle(store).Begin(liveTask, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: winning Begin failed: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	dupTask := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err := dispatcher.queueSingleTask(context.Background(), dupTask)
	if err != nil {
		t.Fatalf("expected the duplicate pickup against a live owner to drop silently, got error: %v", err)
	}
	if execID != "" {
		t.Errorf("expected empty execID for a live-owner duplicate pickup, got %q", execID)
	}

	gen, claimedExecID, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration failed: %v", err)
	}
	if !found || gen != 0 || claimedExecID != liveExecID {
		t.Errorf("expected the claim to remain at generation 0 owned by %q, got generation=%d execID=%q found=%v", liveExecID, gen, claimedExecID, found)
	}

	if !strings.Contains(buf.String(), "dispatch claim lost") {
		t.Errorf("expected an info log noting the dropped claim, got: %s", buf.String())
	}
}

// TestDispatcher_QueueSingleTask_NoOpDoesNotReArm preserves the GH-4350
// invariant through the new retry path: a task whose generation-0 execution
// terminated "no_op" with no error (a legitimate "nothing to change"
// completion) must NOT be re-armed by the generation+1 retry decider, even
// though no_op is a terminal-for-claim status like failed.
func TestDispatcher_QueueSingleTask_NoOpDoesNotReArm(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-4350-NOOP", "/project-gh-4350-noop"

	noOpTask := &Task{ID: taskID, ProjectPath: projectPath}
	noOpExecID, err := NewExecutionLifecycle(store).Begin(noOpTask, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: generation 0 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(noOpExecID, "no_op"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as no_op: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	rePick := &Task{ID: taskID, ProjectPath: projectPath}
	execID, err := dispatcher.queueSingleTask(context.Background(), rePick)
	if err != nil {
		t.Fatalf("expected a no_op task to drop the re-pick silently (nil error), got: %v", err)
	}
	if execID != "" {
		t.Errorf("expected empty execID — a no_op'd task must not be re-armed, got %q", execID)
	}

	gen, _, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration failed: %v", err)
	}
	if !found || gen != 0 {
		t.Errorf("expected the claim to remain at generation 0 (no retry claimed), got generation=%d found=%v", gen, found)
	}
}
