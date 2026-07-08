package autopilot

import (
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func newTestControllerForAggregate(t *testing.T, owner, repo string) *Controller {
	t.Helper()
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	return NewController(DefaultConfig(), ghClient, nil, owner, repo)
}

// TestAggregateMetrics_PRMergedOnNonDefaultController is a regression test
// for GH-4068: recording a merge on a projects-map controller (not the
// backwards-compat "default" one) must still surface in the fleet-wide
// aggregate that /metrics, MetricsAlerter, and MetricsPersister read.
func TestAggregateMetrics_PRMergedOnNonDefaultController(t *testing.T) {
	defaultController := newTestControllerForAggregate(t, "qf-studio", "pilot")
	projectController := newTestControllerForAggregate(t, "qf-studio", "studio-sdk")

	// PR activity recorded on the non-default (project) controller only.
	projectController.Metrics().RecordPRMerged()

	agg := NewAggregateMetrics([]*Controller{defaultController, projectController})
	snap := agg.Snapshot()

	if snap.PRsMerged < 1 {
		t.Fatalf("expected aggregate pilot_prs_merged_total >= 1, got %d", snap.PRsMerged)
	}
	if snap.PRsMerged != 1 {
		t.Errorf("expected exactly 1 merged PR summed across controllers, got %d", snap.PRsMerged)
	}
}

// TestAggregateMetrics_ActivePRsSumAcrossControllers pins that
// pilot_active_prs{stage} sums per-stage counts across every controller,
// not just the default one (GH-4068).
func TestAggregateMetrics_ActivePRsSumAcrossControllers(t *testing.T) {
	c1 := newTestControllerForAggregate(t, "qf-studio", "pilot")
	c2 := newTestControllerForAggregate(t, "qf-studio", "studio-sdk")

	c1.Metrics().UpdateActivePRs([]*PRState{
		{PRNumber: 1, Stage: StageWaitingCI},
		{PRNumber: 2, Stage: StageWaitingCI},
	})
	c2.Metrics().UpdateActivePRs([]*PRState{
		{PRNumber: 3, Stage: StageWaitingCI},
		{PRNumber: 4, Stage: StageMerging},
	})

	agg := NewAggregateMetrics([]*Controller{c1, c2})
	snap := agg.Snapshot()

	if got := snap.ActivePRsByStage[StageWaitingCI]; got != 3 {
		t.Errorf("expected 3 active PRs in stage %q summed across controllers, got %d", StageWaitingCI, got)
	}
	if got := snap.ActivePRsByStage[StageMerging]; got != 1 {
		t.Errorf("expected 1 active PR in stage %q, got %d", StageMerging, got)
	}
	if snap.TotalActivePRs != 4 {
		t.Errorf("expected TotalActivePRs=4, got %d", snap.TotalActivePRs)
	}
}

// TestAggregateMetrics_HydrationCountedOnce pins the GH-4068 hydration
// invariant: the store-lifetime baseline is applied to exactly one
// controller's Metrics (the designated "default" hydration owner), and the
// aggregate must reflect it exactly once — not doubled just because there
// are multiple controllers being summed.
func TestAggregateMetrics_HydrationCountedOnce(t *testing.T) {
	hydratedController := newTestControllerForAggregate(t, "qf-studio", "pilot")
	otherController := newTestControllerForAggregate(t, "qf-studio", "studio-sdk")

	// Simulate HydrateFromStore landing on the designated owner only.
	hydratedController.Metrics().HydrateIssuesProcessed("success", 1723)

	agg := NewAggregateMetrics([]*Controller{hydratedController, otherController})
	snap := agg.Snapshot()

	if got := snap.IssuesProcessed["success"]; got != 1723 {
		t.Errorf("expected hydrated baseline counted exactly once (1723), got %d", got)
	}
}
