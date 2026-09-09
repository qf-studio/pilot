package main

import (
	"context"
	"testing"
	"time"

	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// blockingCancelBackend is a minimal executor.Backend whose Execute blocks
// until ctx is canceled, then returns ctx.Err(). It lets this test park a
// "task" in a live, in-flight state and assert that canceling it actually
// unblocks Execute — mirroring internal/executor's own unexported
// blockingBackend test helper (dispatcher_test.go), reimplemented here since
// package main cannot reach that unexported type directly.
type blockingCancelBackend struct{}

func (b *blockingCancelBackend) Name() string      { return "test-blocking-cancel" }
func (b *blockingCancelBackend) IsAvailable() bool { return true }
func (b *blockingCancelBackend) Execute(ctx context.Context, _ executor.ExecuteOptions) (*executor.BackendResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// waitUntil polls cond every 10ms until it returns true or timeout elapses,
// failing the test if it never does.
func waitUntil(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", msg)
}

// TestSaveDeclinedExecutionRecord_CancelsLiveExecution is the GH-5400
// regression test for defect #1 (the incident behind issue #5386): a
// preflight decline firing while taskID's execution is still genuinely
// running (e.g. a stale-label re-pick race lets the poller re-evaluate a
// task whose original dispatch never stopped, exactly #5386's 15:22Z
// label-strip -> 15:43:53Z re-pick -> 15:49:20Z decline timeline) must abort
// that execution instead of letting it run straight through to a commit and
// PR while pilot-needs-clarification sits on the issue.
//
// Before the GH-5400 fix, storeExecutionSaver.SaveDeclinedExecutionRecord's
// runner.IsRunning/runner.Cancel calls were wired up but silently never
// fired for a real execution: Runner.running (a map[string]*exec.Cmd) is
// only ever populated by direct test manipulation, never by the actual
// ClaudeCodeBackend/OpenCode/etc. execution path, which owns its subprocess
// entirely internally (see execCancel field doc, runner.go). This test uses
// a fake Backend instead of a real subprocess specifically so it can prove
// the cancellation signal reaches a genuinely in-flight Execute call end to
// end, through the real (non-test-only) production code path.
func TestSaveDeclinedExecutionRecord_CancelsLiveExecution(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner := executor.NewRunnerWithBackend(&blockingCancelBackend{})
	runner.SetSkipPreflightChecks(true)

	task := &executor.Task{
		ID:          "GH-9100",
		Title:       "fix(ci): resolve post-merge CI failure",
		ProjectPath: t.TempDir(),
	}

	resultCh := make(chan *executor.ExecutionResult, 1)
	go func() {
		result, _ := runner.Execute(context.Background(), task)
		resultCh <- result
	}()

	waitUntil(t, 2*time.Second, "runner reports task as running", func() bool {
		return runner.IsRunning(task.ID)
	})

	saver := storeExecutionSaver{store: store, runner: runner}
	if err := saver.SaveDeclinedExecutionRecord(sdkCore.DeclinedExecutionRecord{
		TaskID:      task.ID,
		ProjectPath: task.ProjectPath,
		Status:      "declined-preflight",
		Reason:      "reject_vague",
	}); err != nil {
		t.Fatalf("SaveDeclinedExecutionRecord failed: %v", err)
	}

	var result *executor.ExecutionResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after preflight decline canceled it — the live execution was not actually aborted")
	}

	if result.Success {
		t.Error("result.Success = true, want false — a canceled execution must not report success")
	}
	if result.PRUrl != "" {
		t.Errorf("result.PRUrl = %q, want empty — a declined/canceled execution must not produce a PR", result.PRUrl)
	}
	if runner.IsRunning(task.ID) {
		t.Error("runner.IsRunning(task.ID) = true after Execute returned, want false")
	}
}

// TestSaveDeclinedExecutionRecord_NoRunner_DoesNotPanic verifies the nil
// runner case (a repo with no in-process runner wired) is a no-op, not a
// crash — SaveDeclinedExecutionRecord must keep working for the many
// existing callers that never had a runner reference to begin with.
func TestSaveDeclinedExecutionRecord_NoRunner_DoesNotPanic(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	saver := storeExecutionSaver{store: store}
	if err := saver.SaveDeclinedExecutionRecord(sdkCore.DeclinedExecutionRecord{
		TaskID:      "GH-9101",
		ProjectPath: "/tmp/gh-5400-no-runner",
		Status:      "declined-preflight",
		Reason:      "reject_vague",
	}); err != nil {
		t.Fatalf("SaveDeclinedExecutionRecord failed: %v", err)
	}
}
