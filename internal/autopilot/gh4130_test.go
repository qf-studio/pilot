package autopilot

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// gh4130ExecPersister is a minimal approvalPersister that returns a
// caller-configured *memory.Execution from GetLatestExecutionByTaskID, so
// tests can control StartedAt/CreatedAt independent of the shared
// mockApprovalPersister (which always returns a bare {ID, TaskID} row).
type gh4130ExecPersister struct {
	execByTask map[string]*memory.Execution
}

func (m *gh4130ExecPersister) SetApprovalRequestID(_ context.Context, _, _ string) error { return nil }
func (m *gh4130ExecPersister) SetApprovalDecision(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *gh4130ExecPersister) InsertExecutionEvent(_ string, _ memory.Stage, _ string) error {
	return nil
}
func (m *gh4130ExecPersister) GetLatestExecutionByTaskID(taskID string) (*memory.Execution, error) {
	exec, ok := m.execByTask[taskID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return exec, nil
}

// TestExecutionEventStageFor_WaitingCI verifies GH-4130's core change: the
// waiting_ci in-progress skip is removed, so entering StageWaitingCI now
// produces a durable execution_events row instead of being dropped.
func TestExecutionEventStageFor_WaitingCI(t *testing.T) {
	stage, ok := executionEventStageFor(StageWaitingCI)
	if !ok {
		t.Fatal("executionEventStageFor(StageWaitingCI) ok = false, want true")
	}
	if stage != memory.StageWaitingCI {
		t.Errorf("executionEventStageFor(StageWaitingCI) = %q, want %q", stage, memory.StageWaitingCI)
	}
}

// TestOnPRCreated_ObservesTimeToPRAndQueueWait verifies OnPRCreated reads the
// execution row's started_at/created_at and feeds pilot_time_to_pr_seconds /
// pilot_queue_wait_seconds — the two histograms observable at PR-creation time.
func TestOnPRCreated_ObservesTimeToPRAndQueueWait(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	createdAt := time.Now().Add(-10 * time.Minute)
	startedAt := createdAt.Add(6 * time.Minute) // queue wait = 6m
	c.memoryStore = &gh4130ExecPersister{
		execByTask: map[string]*memory.Execution{
			"GH-77": {ID: "exec-77", TaskID: "GH-77", CreatedAt: createdAt, StartedAt: &startedAt},
		},
	}

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 77, "abc1234", "pilot/GH-77", "")

	hist := c.metrics.HistogramSnapshot()
	if len(hist.TimeToPRDurations) != 1 {
		t.Fatalf("TimeToPRDurations = %v, want 1 sample", hist.TimeToPRDurations)
	}
	if len(hist.QueueWaitDurations) != 1 {
		t.Fatalf("QueueWaitDurations = %v, want 1 sample", hist.QueueWaitDurations)
	}

	// time_to_pr is measured from startedAt (~4m ago) to now: expect roughly 4m, well above 0.
	if hist.TimeToPRDurations[0] < 3*time.Minute {
		t.Errorf("TimeToPRDurations[0] = %v, want >= 3m (started_at was ~4m ago)", hist.TimeToPRDurations[0])
	}
	// queue_wait is exactly startedAt - createdAt = 6m.
	if got := hist.QueueWaitDurations[0]; got < 5*time.Minute || got > 7*time.Minute {
		t.Errorf("QueueWaitDurations[0] = %v, want ~6m", got)
	}
}

// TestOnPRCreated_NoExecutionRow_SkipsObservation verifies a missing/incomplete
// execution row (no started_at, e.g. a test double or a pre-GH-4033 row) does
// not observe the histograms — the guard mirrors recordExecutionEvent's
// best-effort, non-fatal lookup-miss handling.
func TestOnPRCreated_NoExecutionRow_SkipsObservation(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.memoryStore = &gh4130ExecPersister{execByTask: map[string]*memory.Execution{}}

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 77, "abc1234", "pilot/GH-77", "")

	hist := c.metrics.HistogramSnapshot()
	if len(hist.TimeToPRDurations) != 0 || len(hist.QueueWaitDurations) != 0 {
		t.Errorf("expected no histogram samples on lookup miss, got TimeToPR=%v QueueWait=%v",
			hist.TimeToPRDurations, hist.QueueWaitDurations)
	}
}

// TestOnPRCreated_NoExecutionRow_LogsWarnOnSkip verifies the GH-4212 fail-loud
// fix: a lookup miss on GetLatestExecutionByTaskID no longer skips the
// time-to-PR/queue-wait observation silently — it must warn-log with the
// task_id and underlying error so a wiring/scoping regression is visible.
func TestOnPRCreated_NoExecutionRow_LogsWarnOnSkip(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.memoryStore = &gh4130ExecPersister{execByTask: map[string]*memory.Execution{}}

	var buf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&buf, nil))

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 77, "abc1234", "pilot/GH-77", "")

	out := buf.String()
	if !strings.Contains(out, "skipping time-to-PR/queue-wait sample") {
		t.Fatalf("expected fail-loud warn log on observation skip, got log output: %q", out)
	}
	if !strings.Contains(out, "task_id=GH-77") {
		t.Errorf("expected warn log to carry task_id=GH-77, got: %q", out)
	}
}

// TestOnPRCreated_NoStartedAt_LogsWarnOnSkip verifies the same fail-loud
// treatment applies when the execution row exists but started_at is nil
// (the second silent-skip branch the old `err == nil && exec.StartedAt != nil`
// guard collapsed into one).
func TestOnPRCreated_NoStartedAt_LogsWarnOnSkip(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.memoryStore = &gh4130ExecPersister{
		execByTask: map[string]*memory.Execution{
			"GH-77": {ID: "exec-77", TaskID: "GH-77", CreatedAt: time.Now(), StartedAt: nil},
		},
	}

	var buf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&buf, nil))

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 77, "abc1234", "pilot/GH-77", "")

	out := buf.String()
	if !strings.Contains(out, "execution row has no started_at") {
		t.Fatalf("expected fail-loud warn log for nil started_at, got log output: %q", out)
	}

	hist := c.metrics.HistogramSnapshot()
	if len(hist.TimeToPRDurations) != 0 || len(hist.QueueWaitDurations) != 0 {
		t.Errorf("expected no histogram samples when started_at is nil, got TimeToPR=%v QueueWait=%v",
			hist.TimeToPRDurations, hist.QueueWaitDurations)
	}
}

// TestApplyApprovalDecision_ObservesApprovalWait verifies applyApprovalDecision
// feeds pilot_approval_wait_seconds from ApprovalRequestedAt (the
// awaiting_approval entry point) to the moment the decision is applied,
// covering both the webhook-decision path and the wall-clock-expiry
// default-action path (both call applyApprovalDecision).
func TestApplyApprovalDecision_ObservesApprovalWait(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	mgr := approval.NewManager(nil)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	requestedAt := time.Now().Add(-90 * time.Second)
	prState := &PRState{
		PRNumber:            99,
		IssueNumber:         10,
		ApprovalRequestID:   "req-1",
		ApprovalRequestedAt: requestedAt,
		ApprovalDecision:    string(approval.DecisionApproved),
	}

	if err := c.applyApprovalDecision(prState); err != nil {
		t.Fatalf("applyApprovalDecision: %v", err)
	}

	hist := c.metrics.HistogramSnapshot()
	if len(hist.ApprovalWaitDurations) != 1 {
		t.Fatalf("ApprovalWaitDurations = %v, want 1 sample", hist.ApprovalWaitDurations)
	}
	if got := hist.ApprovalWaitDurations[0]; got < 60*time.Second || got > 120*time.Second {
		t.Errorf("ApprovalWaitDurations[0] = %v, want ~90s", got)
	}
	if prState.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (approved decision should still advance the state machine)", prState.Stage, StageMerging)
	}
}

// TestApplyApprovalDecision_ZeroRequestedAt_SkipsObservation guards against a
// zero-value ApprovalRequestedAt (defensive: shouldn't happen on any real path
// since both submitAsyncApprovalRequest and the timeout branch set it first)
// producing a bogus multi-decade duration sample.
func TestApplyApprovalDecision_ZeroRequestedAt_SkipsObservation(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         99,
		ApprovalDecision: string(approval.DecisionApproved),
	}

	if err := c.applyApprovalDecision(prState); err != nil {
		t.Fatalf("applyApprovalDecision: %v", err)
	}

	hist := c.metrics.HistogramSnapshot()
	if len(hist.ApprovalWaitDurations) != 0 {
		t.Errorf("ApprovalWaitDurations = %v, want no sample for zero ApprovalRequestedAt", hist.ApprovalWaitDurations)
	}
}

// TestPollerOnPRCreatedHook_ObservesTimeToPR pins the GH-4212 live entry
// point: cmd/pilot/poller_github.go wires pollerDeps.OnPRCreated as a closure
// that parses sdkcore.PRCreatedEvent.IssueID via strconv.Atoi and forwards to
// Controller.OnPRCreated. This test replicates that exact closure (rather
// than calling Controller.OnPRCreated directly, as the other tests in this
// file do) so a future edit to the wiring — e.g. the issueNumber conversion,
// or dropping the closure — regresses here instead of only being caught by
// the direct-call tests above, which do not exercise the poller-deps hook.
func TestPollerOnPRCreatedHook_ObservesTimeToPR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	createdAt := time.Now().Add(-10 * time.Minute)
	startedAt := createdAt.Add(6 * time.Minute)
	c.memoryStore = &gh4130ExecPersister{
		execByTask: map[string]*memory.Execution{
			"GH-77": {ID: "exec-77", TaskID: "GH-77", CreatedAt: createdAt, StartedAt: &startedAt},
		},
	}

	// Mirrors cmd/pilot/poller_github.go's pollerDeps.OnPRCreated assignment.
	onPRCreated := func(prEv sdkcore.PRCreatedEvent) {
		issueNumber, _ := strconv.Atoi(prEv.IssueID)
		c.OnPRCreated(prEv.PRNumber, prEv.PRURL, issueNumber, prEv.HeadSHA, prEv.BranchName, prEv.IssueNodeID)
	}

	before := len(c.metrics.HistogramSnapshot().TimeToPRDurations)

	onPRCreated(sdkcore.PRCreatedEvent{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		IssueID:     "77",
		HeadSHA:     "abc1234",
		BranchName:  "pilot/GH-77",
		IssueNodeID: "",
	})

	hist := c.metrics.HistogramSnapshot()
	if got := len(hist.TimeToPRDurations); got != before+1 {
		t.Fatalf("TimeToPRDurations grew by %d, want 1 (before=%d, after=%d)", got-before, before, got)
	}
	if len(hist.QueueWaitDurations) != 1 {
		t.Fatalf("QueueWaitDurations = %v, want 1 sample", hist.QueueWaitDurations)
	}
}
