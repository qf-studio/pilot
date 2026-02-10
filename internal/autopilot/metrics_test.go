package autopilot

import (
	"testing"
	"time"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	snap := m.Snapshot()
	if snap.TotalIssuesProcessed() != 0 {
		t.Errorf("expected 0 issues processed, got %d", snap.TotalIssuesProcessed())
	}
	if snap.PRsMerged != 0 || snap.PRsFailed != 0 {
		t.Error("expected zero PR counters")
	}
}

func TestCounters(t *testing.T) {
	m := NewMetrics()

	m.RecordIssueProcessed("success")
	m.RecordIssueProcessed("success")
	m.RecordIssueProcessed("failed")
	m.RecordIssueProcessed("rate_limited")

	m.RecordPRMerged()
	m.RecordPRMerged()
	m.RecordPRFailed()
	m.RecordPRConflicting()

	m.RecordCircuitBreakerTrip()
	m.RecordCircuitBreakerTrip()

	m.RecordAPIError("GetPullRequest")
	m.RecordAPIError("GetPullRequest")
	m.RecordAPIError("MergePR")

	m.RecordLabelCleanup("pilot-in-progress")
	m.RecordLabelCleanup("pilot-failed")
	m.RecordLabelCleanup("pilot-failed")

	snap := m.Snapshot()

	if snap.IssuesProcessed["success"] != 2 {
		t.Errorf("expected 2 success, got %d", snap.IssuesProcessed["success"])
	}
	if snap.IssuesProcessed["failed"] != 1 {
		t.Errorf("expected 1 failed, got %d", snap.IssuesProcessed["failed"])
	}
	if snap.TotalIssuesProcessed() != 4 {
		t.Errorf("expected 4 total, got %d", snap.TotalIssuesProcessed())
	}
	if snap.PRsMerged != 2 {
		t.Errorf("expected 2 merged, got %d", snap.PRsMerged)
	}
	if snap.PRsFailed != 1 {
		t.Errorf("expected 1 failed PR, got %d", snap.PRsFailed)
	}
	if snap.PRsConflicting != 1 {
		t.Errorf("expected 1 conflicting, got %d", snap.PRsConflicting)
	}
	if snap.CircuitBreakerTrips != 2 {
		t.Errorf("expected 2 CB trips, got %d", snap.CircuitBreakerTrips)
	}
	if snap.APIErrors["GetPullRequest"] != 2 {
		t.Errorf("expected 2 GetPullRequest errors, got %d", snap.APIErrors["GetPullRequest"])
	}
	if snap.LabelCleanups["pilot-failed"] != 2 {
		t.Errorf("expected 2 pilot-failed cleanups, got %d", snap.LabelCleanups["pilot-failed"])
	}
}

func TestGauges(t *testing.T) {
	m := NewMetrics()

	prs := []*PRState{
		{PRNumber: 1, Stage: StageWaitingCI},
		{PRNumber: 2, Stage: StageWaitingCI},
		{PRNumber: 3, Stage: StageMerging},
	}
	m.UpdateActivePRs(prs)
	m.SetQueueDepth(5)
	m.SetFailedQueueDepth(2)

	snap := m.Snapshot()

	if snap.ActivePRsByStage[StageWaitingCI] != 2 {
		t.Errorf("expected 2 waiting_ci, got %d", snap.ActivePRsByStage[StageWaitingCI])
	}
	if snap.ActivePRsByStage[StageMerging] != 1 {
		t.Errorf("expected 1 merging, got %d", snap.ActivePRsByStage[StageMerging])
	}
	if snap.TotalActivePRs != 3 {
		t.Errorf("expected 3 total active, got %d", snap.TotalActivePRs)
	}
	if snap.QueueDepth != 5 {
		t.Errorf("expected queue depth 5, got %d", snap.QueueDepth)
	}
	if snap.FailedQueueDepth != 2 {
		t.Errorf("expected failed depth 2, got %d", snap.FailedQueueDepth)
	}
}

func TestHistograms(t *testing.T) {
	m := NewMetrics()

	m.RecordPRTimeToMerge(10 * time.Minute)
	m.RecordPRTimeToMerge(20 * time.Minute)
	m.RecordCIWaitDuration(3 * time.Minute)
	m.RecordCIWaitDuration(7 * time.Minute)
	m.RecordExecutionDuration(30 * time.Second)
	m.RecordExecutionDuration(90 * time.Second)

	snap := m.Snapshot()

	if snap.AvgPRTimeToMerge != 15*time.Minute {
		t.Errorf("expected avg merge time 15m, got %v", snap.AvgPRTimeToMerge)
	}
	if snap.AvgCIWaitDuration != 5*time.Minute {
		t.Errorf("expected avg CI wait 5m, got %v", snap.AvgCIWaitDuration)
	}
	if snap.AvgExecutionDuration != 60*time.Second {
		t.Errorf("expected avg exec 60s, got %v", snap.AvgExecutionDuration)
	}
}

func TestSuccessRate(t *testing.T) {
	m := NewMetrics()

	m.RecordIssueProcessed("success")
	m.RecordIssueProcessed("success")
	m.RecordIssueProcessed("success")
	m.RecordIssueProcessed("failed")

	snap := m.Snapshot()

	if snap.SuccessRate != 0.75 {
		t.Errorf("expected success rate 0.75, got %f", snap.SuccessRate)
	}
}

func TestAPIErrorRate(t *testing.T) {
	m := NewMetrics()

	// Add 10 errors "now" (within 5 min window)
	for i := 0; i < 10; i++ {
		m.RecordAPIError("test")
	}

	snap := m.Snapshot()

	// 10 errors / 5 minutes = 2.0 per minute
	if snap.APIErrorRate != 2.0 {
		t.Errorf("expected API error rate 2.0/min, got %f", snap.APIErrorRate)
	}
}

func TestSnapshotIsolation(t *testing.T) {
	m := NewMetrics()
	m.RecordPRMerged()

	snap := m.Snapshot()

	// Mutate after snapshot
	m.RecordPRMerged()

	if snap.PRsMerged != 1 {
		t.Errorf("snapshot should be isolated, expected 1, got %d", snap.PRsMerged)
	}

	snap2 := m.Snapshot()
	if snap2.PRsMerged != 2 {
		t.Errorf("new snapshot should reflect mutation, expected 2, got %d", snap2.PRsMerged)
	}
}

func TestUpdateActivePRsReset(t *testing.T) {
	m := NewMetrics()

	m.UpdateActivePRs([]*PRState{
		{PRNumber: 1, Stage: StageWaitingCI},
	})
	if snap := m.Snapshot(); snap.TotalActivePRs != 1 {
		t.Fatalf("expected 1, got %d", snap.TotalActivePRs)
	}

	// Replace with different set
	m.UpdateActivePRs([]*PRState{
		{PRNumber: 2, Stage: StageMerging},
		{PRNumber: 3, Stage: StageMerging},
	})
	snap := m.Snapshot()
	if snap.TotalActivePRs != 2 {
		t.Errorf("expected 2 after reset, got %d", snap.TotalActivePRs)
	}
	if snap.ActivePRsByStage[StageWaitingCI] != 0 {
		t.Errorf("waiting_ci should be 0 after reset, got %d", snap.ActivePRsByStage[StageWaitingCI])
	}
}
