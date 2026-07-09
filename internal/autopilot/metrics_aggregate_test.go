package autopilot

import (
	"testing"
	"time"
)

// TestAggregateMetrics_SumsCountersAcrossControllers reproduces the GH-4068
// bug: a PR merged on a non-default (projects-map) controller must still
// show up in the exported /metrics surface, which reads the aggregate.
func TestAggregateMetrics_SumsCountersAcrossControllers(t *testing.T) {
	defaultMetrics := NewMetrics()
	otherMetrics := NewMetrics()

	// PR merged only on the non-default controller's Metrics.
	otherMetrics.RecordPRMerged()
	otherMetrics.RecordPRMerged()
	defaultMetrics.RecordPRFailed()

	agg := NewAggregateMetrics(defaultMetrics, otherMetrics)
	snap := agg.Snapshot()

	if snap.PRsMerged < 1 {
		t.Fatalf("expected aggregate pilot_prs_merged_total >= 1, got %d", snap.PRsMerged)
	}
	if snap.PRsMerged != 2 {
		t.Errorf("expected aggregate PRsMerged=2 (summed across controllers), got %d", snap.PRsMerged)
	}
	if snap.PRsFailed != 1 {
		t.Errorf("expected aggregate PRsFailed=1, got %d", snap.PRsFailed)
	}
}

// TestAggregateMetrics_SumsCIRunsAcrossControllers pins GH-4134: a CI verdict
// recorded on a non-default (projects-map) controller must still show up in
// the aggregate pilot_ci_runs_total surface, mirroring the PRsMerged/
// PRsFailed sum-across-controllers guarantee above.
func TestAggregateMetrics_SumsCIRunsAcrossControllers(t *testing.T) {
	defaultMetrics := NewMetrics()
	otherMetrics := NewMetrics()

	otherMetrics.RecordCIRun("pass")
	otherMetrics.RecordCIRun("pass")
	defaultMetrics.RecordCIRun("fail")

	agg := NewAggregateMetrics(defaultMetrics, otherMetrics)
	snap := agg.Snapshot()

	if got := snap.CIRuns["pass"]; got != 2 {
		t.Errorf("expected aggregate CIRuns[pass]=2 (summed across controllers), got %d", got)
	}
	if got := snap.CIRuns["fail"]; got != 1 {
		t.Errorf("expected aggregate CIRuns[fail]=1, got %d", got)
	}
}

// TestAggregateMetrics_SumsActivePRsByStagePerTick verifies pilot_active_prs{stage}
// sums across controllers on every scrape, not just at hydration/startup.
func TestAggregateMetrics_SumsActivePRsByStagePerTick(t *testing.T) {
	m1 := NewMetrics()
	m2 := NewMetrics()

	agg := NewAggregateMetrics(m1, m2)

	m1.UpdateActivePRs([]*PRState{{Stage: StageWaitingCI}, {Stage: StageWaitingCI}})
	m2.UpdateActivePRs([]*PRState{{Stage: StageWaitingCI}})

	snap := agg.Snapshot()
	if got := snap.ActivePRsByStage[StageWaitingCI]; got != 3 {
		t.Fatalf("expected pilot_active_prs{stage=waiting_ci}=3 summed across controllers, got %d", got)
	}
	if snap.TotalActivePRs != 3 {
		t.Errorf("expected TotalActivePRs=3, got %d", snap.TotalActivePRs)
	}

	// A second tick with changed gauges must reflect the new totals, not the
	// first tick's — UpdateActivePRs replaces (not accumulates) per source.
	m1.UpdateActivePRs([]*PRState{{Stage: StageWaitingCI}})
	m2.UpdateActivePRs(nil)

	snap = agg.Snapshot()
	if got := snap.ActivePRsByStage[StageWaitingCI]; got != 1 {
		t.Fatalf("expected pilot_active_prs{stage=waiting_ci}=1 after second tick, got %d", got)
	}
}

// TestAggregateMetrics_HistogramMergesSamples verifies histogram samples
// from every controller are concatenated so bucket/sum/count reflect the
// whole fleet.
func TestAggregateMetrics_HistogramMergesSamples(t *testing.T) {
	m1 := NewMetrics()
	m2 := NewMetrics()

	m1.RecordPRTimeToMerge(10 * time.Minute)
	m2.RecordPRTimeToMerge(20 * time.Minute)
	m2.RecordPRTimeToMerge(30 * time.Minute)

	agg := NewAggregateMetrics(m1, m2)
	hist := agg.HistogramSnapshot()

	if len(hist.PRTimeToMerge) != 3 {
		t.Fatalf("expected 3 merged PRTimeToMerge samples, got %d", len(hist.PRTimeToMerge))
	}

	snap := agg.Snapshot()
	wantAvg := (10*time.Minute + 20*time.Minute + 30*time.Minute) / 3
	if snap.AvgPRTimeToMerge != wantAvg {
		t.Errorf("expected AvgPRTimeToMerge=%v (recomputed from merged samples), got %v", wantAvg, snap.AvgPRTimeToMerge)
	}
}

// TestAggregateMetrics_NewHistogramsMergeAcrossControllers verifies the
// GH-4128 histograms (TimeToPR/QueueWait/ApprovalWait) sum across
// controllers just like the pre-existing histograms — guarding against the
// TASK-390 idle-default-controller regression for these new metrics before
// any Record* call site exists.
func TestAggregateMetrics_NewHistogramsMergeAcrossControllers(t *testing.T) {
	m1 := NewMetrics()
	m2 := NewMetrics()

	m1.RecordTimeToPR(5 * time.Minute)
	m2.RecordTimeToPR(15 * time.Minute)

	m1.RecordQueueWaitDuration(1 * time.Minute)
	m2.RecordQueueWaitDuration(2 * time.Minute)
	m2.RecordQueueWaitDuration(3 * time.Minute)

	m2.RecordApprovalWaitDuration(1 * time.Hour)

	agg := NewAggregateMetrics(m1, m2)
	hist := agg.HistogramSnapshot()

	if len(hist.TimeToPRDurations) != 2 {
		t.Errorf("expected 2 merged TimeToPRDurations samples, got %d", len(hist.TimeToPRDurations))
	}
	if len(hist.QueueWaitDurations) != 3 {
		t.Errorf("expected 3 merged QueueWaitDurations samples, got %d", len(hist.QueueWaitDurations))
	}
	if len(hist.ApprovalWaitDurations) != 1 {
		t.Errorf("expected 1 merged ApprovalWaitDurations sample, got %d", len(hist.ApprovalWaitDurations))
	}
}

// TestAggregateMetrics_SuccessRateRecomputedFromMergedCounters guards against
// naively averaging each controller's SuccessRate, which would be wrong when
// controllers have different attempt volumes.
func TestAggregateMetrics_SuccessRateRecomputedFromMergedCounters(t *testing.T) {
	m1 := NewMetrics()
	m2 := NewMetrics()

	// m1: 1 success, 1 failed (50%). m2: 9 successes, 1 failed (90%).
	m1.RecordIssueProcessed("success")
	m1.RecordIssueProcessed("failed")
	for i := 0; i < 9; i++ {
		m2.RecordIssueProcessed("success")
	}
	m2.RecordIssueProcessed("failed")

	agg := NewAggregateMetrics(m1, m2)
	snap := agg.Snapshot()

	// Fleet-wide: 10 successes, 2 failed => 10/12, not (0.5+0.9)/2=0.7.
	const want = float64(10) / float64(12)
	if diff := snap.SuccessRate - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("expected recomputed SuccessRate=%.4f, got %.4f", want, snap.SuccessRate)
	}
}

// TestAggregateMetrics_NilSourcesIgnored ensures a nil *Metrics entry (e.g.
// from an unpopulated map lookup) doesn't panic.
func TestAggregateMetrics_NilSourcesIgnored(t *testing.T) {
	m1 := NewMetrics()
	m1.RecordPRMerged()

	agg := NewAggregateMetrics(m1, nil)
	snap := agg.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("expected PRsMerged=1 with nil source ignored, got %d", snap.PRsMerged)
	}
}

// TestAggregateMetrics_EmptySourcesZeroSnapshot documents behavior when
// autopilot is disabled or has no controllers.
func TestAggregateMetrics_EmptySourcesZeroSnapshot(t *testing.T) {
	agg := NewAggregateMetrics()
	snap := agg.Snapshot()
	if snap.PRsMerged != 0 || snap.TotalActivePRs != 0 || snap.SuccessRate != 0 {
		t.Errorf("expected all-zero snapshot for empty aggregate, got %+v", snap)
	}
}
