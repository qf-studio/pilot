package autopilot

import "time"

// AggregateMetrics presents a single fleet-wide, read-only view over the
// Metrics of every autopilot Controller in multi-repo polling mode. GH-929
// creates one Controller — and one Metrics — per configured repo, and live
// recording is correctly scoped to each controller's own Metrics. But the
// /metrics Prometheus endpoint, MetricsAlerter, and MetricsPersister each
// need exactly ONE view across all of them; previously they read only the
// single default-repo controller, so PR activity recorded on any
// projects-map controller never reached them (GH-4068).
//
// Snapshot() and HistogramSnapshot() recompute the merge on every call
// instead of caching, mirroring Metrics' own read-on-demand Snapshot.
type AggregateMetrics struct {
	controllers []*Controller
}

// NewAggregateMetrics builds an AggregateMetrics over the given controllers.
// Order does not matter — every merge below is commutative (sum or concat).
func NewAggregateMetrics(controllers []*Controller) *AggregateMetrics {
	return &AggregateMetrics{controllers: controllers}
}

// Snapshot returns a merged snapshot: counters and per-stage gauge buckets
// sum across every controller; SuccessRate and the histogram averages are
// recomputed from the merged totals/samples rather than averaged
// per-controller, so the weighting stays correct.
func (a *AggregateMetrics) Snapshot() MetricsSnapshot {
	snap := MetricsSnapshot{
		IssuesProcessed:            make(map[string]int64),
		APIErrors:                  make(map[string]int64),
		LabelCleanups:              make(map[string]int64),
		ApprovalPersistMisses:      make(map[string]int64),
		TokensConsumed:             make(map[tokenKey]int64),
		ExecutionCostUSD:           make(map[string]float64),
		ExecutionsByResult:         make(map[execKey]int64),
		PollerSkipped:              make(map[pollerSkipKey]int64),
		PollerDispatched:           make(map[string]int64),
		PollerDeferredScopeOverlap: make(map[string]int64),
		OrphanPRsRegistered:        make(map[string]int64),
		ActivePRsByStage:           make(map[PRStage]int),
		SnapshotAt:                 time.Now(),
	}

	for _, c := range a.controllers {
		if c == nil {
			continue
		}
		s := c.Metrics().Snapshot()

		for k, v := range s.IssuesProcessed {
			snap.IssuesProcessed[k] += v
		}
		snap.PRsMerged += s.PRsMerged
		snap.PRsFailed += s.PRsFailed
		snap.PRsConflicting += s.PRsConflicting
		snap.CircuitBreakerTrips += s.CircuitBreakerTrips
		for k, v := range s.APIErrors {
			snap.APIErrors[k] += v
		}
		for k, v := range s.LabelCleanups {
			snap.LabelCleanups[k] += v
		}
		for k, v := range s.ApprovalPersistMisses {
			snap.ApprovalPersistMisses[k] += v
		}
		for k, v := range s.TokensConsumed {
			snap.TokensConsumed[k] += v
		}
		for k, v := range s.ExecutionCostUSD {
			snap.ExecutionCostUSD[k] += v
		}
		for k, v := range s.ExecutionsByResult {
			snap.ExecutionsByResult[k] += v
		}
		for k, v := range s.PollerSkipped {
			snap.PollerSkipped[k] += v
		}
		for k, v := range s.PollerDispatched {
			snap.PollerDispatched[k] += v
		}
		for k, v := range s.PollerDeferredScopeOverlap {
			snap.PollerDeferredScopeOverlap[k] += v
		}
		for k, v := range s.OrphanPRsRegistered {
			snap.OrphanPRsRegistered[k] += v
		}
		snap.DuplicateRegistrationsSkipped += s.DuplicateRegistrationsSkipped
		for k, v := range s.ActivePRsByStage {
			snap.ActivePRsByStage[k] += v
		}
		snap.QueueDepth += s.QueueDepth
		snap.FailedQueueDepth += s.FailedQueueDepth
		snap.APIErrorRate += s.APIErrorRate
	}

	snap.TotalActivePRs = sumStageMap(snap.ActivePRsByStage)

	var totalIssues int64
	for _, v := range snap.IssuesProcessed {
		totalIssues += v
	}
	if totalIssues > 0 {
		snap.SuccessRate = float64(snap.IssuesProcessed["success"]) / float64(totalIssues)
	}

	hist := a.HistogramSnapshot()
	snap.AvgPRTimeToMerge = avgDuration(hist.PRTimeToMerge)
	snap.AvgCIWaitDuration = avgDuration(hist.CIWaitDurations)
	snap.AvgExecutionDuration = avgDuration(hist.ExecutionDurations)

	return snap
}

// HistogramSnapshot concatenates raw duration samples across every controller.
func (a *AggregateMetrics) HistogramSnapshot() HistogramData {
	var hist HistogramData
	for _, c := range a.controllers {
		if c == nil {
			continue
		}
		h := c.Metrics().HistogramSnapshot()
		hist.PRTimeToMerge = append(hist.PRTimeToMerge, h.PRTimeToMerge...)
		hist.CIWaitDurations = append(hist.CIWaitDurations, h.CIWaitDurations...)
		hist.ExecutionDurations = append(hist.ExecutionDurations, h.ExecutionDurations...)
	}
	return hist
}

// ActivePRs concatenates the live PRState of every aggregated controller.
// Used by MetricsAlerter for fleet-wide stuck-PR detection (GH-4068).
func (a *AggregateMetrics) ActivePRs() []*PRState {
	var all []*PRState
	for _, c := range a.controllers {
		if c == nil {
			continue
		}
		all = append(all, c.GetActivePRs()...)
	}
	return all
}

// LastProgressAt returns the most recent progress timestamp across every
// controller — the fleet is only considered stalled once EVERY repo has
// gone quiet, not just the first one checked.
func (a *AggregateMetrics) LastProgressAt() time.Time {
	var latest time.Time
	for _, c := range a.controllers {
		if c == nil {
			continue
		}
		if t := c.GetLastProgressAt(); t.After(latest) {
			latest = t
		}
	}
	return latest
}

// RepoNames returns "owner/repo" for every aggregated controller.
func (a *AggregateMetrics) RepoNames() []string {
	names := make([]string, 0, len(a.controllers))
	for _, c := range a.controllers {
		if c == nil {
			continue
		}
		names = append(names, c.repoKey())
	}
	return names
}
