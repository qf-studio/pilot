package main

import (
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
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
// internal/executor's own GH-5353 test
// (TestEscalateStalledTask_AlreadyStalledRepeatPickup_DoesNotRestampCompletedAt)
// proves the row's completed_at now stays pinned at the FIRST stall
// regardless of how many times escalateStalledTask is re-entered on the
// alreadyStalled path. This test proves the sweep-side consequence of that
// fix: seeded with a stall stamp (T0) that — per the fix — never moves no
// matter how many interleaved poller pickups happen, a re-arm labeled event
// at T1 > T0 still satisfies tryRearmStalled even though "now" (when this
// probe actually runs, standing in for a later interleaved pickup at T2 >
// T1) is later still.
func TestTryRearmStalled_Interleaved_PickupBetweenStallAndRearmEvidence_StillRearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)

	// T0: the original, fresh stall — per the GH-5353 fix, this timestamp is
	// what the row keeps forever, regardless of any later interleaved poller
	// pickup re-entering escalateStalledTask on the alreadyStalled path.
	t0 := time.Now().Add(-2 * time.Hour)
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

	taskID, projectPath := "GH-5139", "/project-gh5353-interleave"
	seedStalledRow(t, store, "exec-stalled-gh5353-interleave", taskID, projectPath, t0)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	// T2 (implicit): this probe itself runs well after T1, standing in for a
	// second interleaved poller pickup that — pre-fix — would have re-stamped
	// completed_at to roughly now and made this assertion fail.
	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — the re-arm evidence at T1 postdates the stall stamp T0 and must stay valid regardless of interleaved poller pickups")
	}

	exec, err := store.GetExecution("exec-stalled-gh5353-interleave")
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
