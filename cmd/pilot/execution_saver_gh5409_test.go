package main

import (
	"context"
	"testing"
	"time"

	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"
	sdkgithub "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestSaveDeclinedExecutionRecord_ActivePR_DoesNotCancel is the GH-5409
// regression test for gap #3 (a late decline promoted to done): once a task
// already has a genuine PR open (Controller.HasActivePRForTask), a
// subsequent preflight-decline notification must NOT cancel the still
// in-flight execution — the work is real, and killing it mid-flight would
// strand a half-finished PR while also making the run classify as a
// premature "declined"/"failed" instead of the PRUrl-driven "completed"
// outcome it is actually heading for. Only a decline landing BEFORE any PR
// exists should abort the run — see the companion
// TestSaveDeclinedExecutionRecord_CancelsLiveExecution in
// execution_saver_cancel_gh5400_test.go for that case.
func TestSaveDeclinedExecutionRecord_ActivePR_DoesNotCancel(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner := executor.NewRunnerWithBackend(&blockingCancelBackend{})
	runner.SetSkipPreflightChecks(true)

	task := &executor.Task{
		ID:          "GH-9200",
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

	ghClient := sdkgithub.NewClient(testutil.FakeGitHubToken)
	controller := autopilot.NewController(autopilot.DefaultConfig(), ghClient, nil, "owner", "repo")
	controller.OnPRCreated(555, "https://github.com/owner/repo/pull/555", 9200, "deadbeef", "pilot/GH-9200", "")

	saver := storeExecutionSaver{store: store, runner: runner, controller: controller}
	if err := saver.SaveDeclinedExecutionRecord(sdkCore.DeclinedExecutionRecord{
		TaskID:      task.ID,
		ProjectPath: task.ProjectPath,
		Status:      "declined-preflight",
		Reason:      "reject_vague",
	}); err != nil {
		t.Fatalf("SaveDeclinedExecutionRecord failed: %v", err)
	}

	// Give an (incorrect) cancel a moment to propagate before asserting it
	// did NOT happen.
	time.Sleep(100 * time.Millisecond)
	if !runner.IsRunning(task.ID) {
		t.Fatal("SaveDeclinedExecutionRecord canceled a task with an active PR — expected the in-flight execution to keep running")
	}

	if err := runner.Cancel(task.ID); err != nil {
		t.Fatalf("cleanup Cancel failed: %v", err)
	}
	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after cleanup Cancel")
	}
}
