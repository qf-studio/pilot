package main

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestTryRearmStalled_Interleaved_PickupBetweenStallAndRearmEvidence_StillRearms
// is GH-5353's sweep-level regression coverage. Post-merge review of PR #5343
// (GH-5272) found that escalateStalledTask (internal/executor/dispatcher.go)
// re-stamped completed_at on every call, including the repeat/alreadyStalled
// path taken when the SDK poller re-observes an already-stalled claim before
// this sweep's next pass. That meant a genuine re-arm label event, even one
// applied strictly after the original stall, could be permanently outrun by
// the poller's own re-stamping and never satisfy "evidence after the stall".
//
// Unlike a first draft of this test (comment-only "simulated poller pickup"
// step, never actually calling into the dispatcher), this drives the real
// race end to end through the same public entrypoint a poller uses —
// executor.Dispatcher.QueueTask — twice:
//  1. T0: a genuine, fresh escalation past the repick hard cap
//     (QueueTask -> ... -> stallTaskAfterRepickHardCap -> escalateStalledTask)
//     stalls the claim and stamps completed_at for the first time.
//  2. T1 > T0: the operator's re-arm recipe produces a labeled event.
//  3. T2 > T1: a second QueueTask call reproduces the SDK poller
//     re-observing the same already-stalled claim first and re-entering
//     escalateStalledTask on the identical (status, reason) —
//     alreadyStalled — path.
//
// Before the GH-5353 fix, step 3's unconditional write re-stamped
// completed_at to (approximately) T2, which is >= T1, so the sweep's
// "event.CreatedAt > completed_at" check below would permanently fail and
// this test would correctly fail with it. After the fix, step 3 performs no
// write at all on the alreadyStalled path, so completed_at stays pinned at
// T0 and the sweep can still re-arm.
func TestTryRearmStalled_Interleaved_PickupBetweenStallAndRearmEvidence_StillRearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)

	taskID, projectPath := "GH-5139", "/project-gh5353-interleave"
	task := &executor.Task{ID: taskID, ProjectPath: projectPath}
	runner := executor.NewRunner()
	dispatcher := executor.NewDispatcher(store, runner, nil)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	// A prior claim eligible for a repick: a genuinely failed generation-0
	// execution, distinct from the stall escalation itself.
	execID, err := executor.NewExecutionLifecycle(store).Begin(task, executor.ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	// Push consecutiveDrops safely past the repick hard cap so the very next
	// repick attempt takes the stall-and-hold path, regardless of the cap's
	// exact value (internal/executor's dispatcherRepickHardCap is
	// unexported).
	if err := dispatcher.SetRepickBackoffState(key, 1000, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	ctx := context.Background()

	// T0: the genuine, fresh escalation through the real dispatcher
	// entrypoint a poller uses.
	if _, err := dispatcher.QueueTask(ctx, task); err != nil {
		t.Fatalf("QueueTask (T0 escalation): %v", err)
	}

	stalled, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after T0 escalation: %v", err)
	}
	if stalled.Status != "stalled" || stalled.CompletedAt == nil {
		t.Fatalf("expected T0 escalation to mark the row stalled with completed_at set, got status=%q completed_at=%v", stalled.Status, stalled.CompletedAt)
	}

	// Backdate completed_at well into the past (mirrors seedStalledRow, and
	// internal/executor's own GH-5353 race test) so T1's re-arm event and
	// T2's possible bad re-stamp are observably ordered instead of comparing
	// equal within the same wall-clock second a same-second test run would
	// otherwise produce.
	t0 := stalled.CompletedAt.Add(-2 * time.Hour).UTC()
	if _, err := store.DB().Exec(`UPDATE executions SET completed_at = ? WHERE id = ?`, t0, execID); err != nil {
		t.Fatalf("failed to backdate completed_at: %v", err)
	}

	// T1 > T0: the operator's re-arm recipe (documented by surfaceStalledIssue)
	// lands strictly after the stall.
	t1 := t0.Add(30 * time.Minute)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryReady}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: t0.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: t1, Label: &github.Label{Name: github.LabelRetryReady}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	// T2 > T1: the SDK poller (unsynchronized, same ~30s cadence as the
	// sweep) re-observes the same already-stalled claim first and re-enters
	// the dispatcher's stall escalation via the same QueueTask call site a
	// real repick would use — identical dropCount/cap, so an identical
	// reason string and the alreadyStalled repeat path.
	if _, err := dispatcher.QueueTask(ctx, task); err != nil {
		t.Fatalf("QueueTask (T2 repeat escalation): %v", err)
	}

	// T3 (implicit, "now"): the sweep runs. Before the GH-5353 fix, T2's
	// unconditional write would have re-stamped completed_at to
	// (approximately) now, which is >= T1, permanently outrunning the re-arm
	// evidence and making this assertion fail.
	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — the re-arm evidence at T1 postdates the stall stamp T0 and must stay valid regardless of interleaved poller pickups")
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the stalled row to be reclassified to status=failed, got %q", exec.Status)
	}
}

// TestTryRearmStalled_RetryReadyLabelBeforeStall_NotRearmed is GH-5353's
// negative case: a pilot-retry-ready labeled event that predates the stall
// (e.g. a leftover label from a much earlier retry cycle) must not be
// mistaken for a deliberate post-stall re-arm gesture.
func TestTryRearmStalled_RetryReadyLabelBeforeStall_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryReady}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: stallTime.Add(-time.Minute), Label: &github.Label{Name: github.LabelRetryReady}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5353-before-stall"
	seedStalledRow(t, store, "exec-stalled-gh5353-before-stall", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — the pilot-retry-ready label event predates the stall")
	}

	exec, err := store.GetExecution("exec-stalled-gh5353-before-stall")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled, got %q", exec.Status)
	}
}

// TestTryRearmStalled_RetryExhaustedLabelAfterStall_NotRearmed is GH-5353's
// second negative case: pilot-retry-exhausted means the SDK poller itself
// decided the retry budget is spent — retryReadyRearmLabels deliberately
// excludes it (rearm_stalled.go) so this probe must not fight that call, even
// when the label event postdates the stall.
func TestTryRearmStalled_RetryExhaustedLabelAfterStall_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	exhaustedTime := stallTime.Add(30 * time.Minute)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryExhausted}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: exhaustedTime, Label: &github.Label{Name: github.LabelRetryExhausted}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-gh5353-exhausted"
	seedStalledRow(t, store, "exec-stalled-gh5353-exhausted", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — pilot-retry-exhausted is deliberately excluded from re-arm evidence")
	}

	exec, err := store.GetExecution("exec-stalled-gh5353-exhausted")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled, got %q", exec.Status)
	}
}
