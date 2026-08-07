package autopilot

import "time"

// SnapshotSource is satisfied by *Metrics and *AggregateMetrics — anything
// that can produce a MetricsSnapshot for alerting/persistence/export
// purposes (GH-4068).
type SnapshotSource interface {
	Snapshot() MetricsSnapshot
}

// AggregateMetrics merges Snapshot()/HistogramSnapshot() calls across every
// autopilot Controller's *Metrics into one fleet-wide view.
//
// GH-4068: every Controller (default + one per projects-map entry, see
// cmd/pilot/main.go) owns its own *Metrics, and live recording correctly
// scopes merges/failures/active-PR gauges to the matching controller. But
// the Prometheus exporter, hydrator, alerter, and persister were all wired
// to exactly one *Metrics (the backward-compat default controller), so PR
// activity recorded on any other controller never reached /metrics.
// AggregateMetrics is the single source those four consumers now share.
//
// Counters sum across sources. ActivePRsByStage gauges sum per stage.
// Histogram samples concatenate so bucket/sum/count reflect the whole
// fleet, and Avg* fields are recomputed from the concatenated samples
// (an average-of-averages would be wrong when sources have different
// sample counts). SuccessRate/IssueLevelSuccessRate are likewise
// recomputed from the merged counters/gauges, not summed.
//
// Store-hydrated lifetime baselines (HydrateFromStore) and the
// issue-level ship/attempt gauges (all-model and per-model, GH-4483) are
// owned by exactly one controller — the default, see the HydrateFromStore
// call site in main.go — so summing the other (zero-valued for these
// fields) controllers alongside it does not double-count.
//
// GH-4735/GH-4738: the rolling-window gauges (WindowDays, WindowCostUSD,
// WindowCostPerDeliveredUSD, WindowDeliveryRate, WindowAttemptSuccessRate)
// are likewise owned by exactly one controller, but are taken verbatim from
// that source (first WindowDays > 0 wins) rather than summed — two of them
// are pre-computed rates that summing would corrupt even in the degenerate
// single-owner case.
type AggregateMetrics struct {
	sources []*Metrics
}

// NewAggregateMetrics builds a fleet-wide view over every controller's
// Metrics. Nil entries are ignored so callers can pass results of a lookup
// map without filtering first.
func NewAggregateMetrics(sources ...*Metrics) *AggregateMetrics {
	return &AggregateMetrics{sources: sources}
}

// Snapshot returns a merged, point-in-time copy of all sources' metrics.
func (a *AggregateMetrics) Snapshot() MetricsSnapshot {
	agg := MetricsSnapshot{
		IssuesProcessed:            make(map[string]int64),
		CIRuns:                     make(map[string]int64),
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
		UnsourcedLabeledIssues:     make(map[string]int64),
		ActivePRsByStage:           make(map[PRStage]int),
		IssuesShippedByModel:       make(map[string]int64),
		IssuesAttemptedByModel:     make(map[string]int64),
		SnapshotAt:                 time.Now(),
	}

	for _, m := range a.sources {
		if m == nil {
			continue
		}
		s := m.Snapshot()

		for k, v := range s.IssuesProcessed {
			agg.IssuesProcessed[k] += v
		}
		agg.PRsMerged += s.PRsMerged
		agg.PRsFailed += s.PRsFailed
		// PRsMergedLifetime/PRsFailedLifetime (GH-4511) are hydrated on exactly
		// one controller (the default, same as IssuesShipped/IssuesAttempted
		// above) — summing the other zero-valued sources alongside it is safe
		// and matches the pattern documented in the AggregateMetrics doc comment.
		agg.PRsMergedLifetime += s.PRsMergedLifetime
		agg.PRsFailedLifetime += s.PRsFailedLifetime
		agg.PRsConflicting += s.PRsConflicting

		// GH-4738: WindowDays/Window* (GH-4735) are, like the lifetime
		// baselines above, seeded on exactly one controller — the default,
		// see the HydrateWindowStats/StartWindowStatsRefresher call sites in
		// cmd/pilot/main.go — so every other source's WindowDays stays 0.
		// Unlike the lifetime baselines, two of these fields
		// (WindowDeliveryRate, WindowAttemptSuccessRate) are already-computed
		// rates in [0,1], so summing them across sources would be wrong even
		// though only one source is ever non-zero in practice; take the
		// whole group verbatim from the first source with WindowDays > 0
		// instead. Before this fix, the aggregate never touched these five
		// fields at all — they stayed at MetricsSnapshot's zero value
		// regardless of what HydrateWindowStats seeded on the owning
		// controller — so every daemon path that reads the aggregate (i.e.
		// every real deployment via SetMetricsSource(autopilotMetricsAggregate))
		// served pilot_window_*{window="0d"} 0 no matter what the store held.
		if agg.WindowDays == 0 && s.WindowDays > 0 {
			agg.WindowDays = s.WindowDays
			agg.WindowCostUSD = s.WindowCostUSD
			agg.WindowCostPerDeliveredUSD = s.WindowCostPerDeliveredUSD
			agg.WindowDeliveryRate = s.WindowDeliveryRate
			agg.WindowAttemptSuccessRate = s.WindowAttemptSuccessRate
		}
		for k, v := range s.CIRuns {
			agg.CIRuns[k] += v
		}
		agg.CircuitBreakerTrips += s.CircuitBreakerTrips
		// GH-4798: PlatformBreakerTrips/PlatformBreakerOpen (GH-4791) were
		// added to Metrics/MetricsSnapshot but never copied here — same bug
		// shape as GH-4738 above. Trips sum (each controller counts its own
		// suppressed actions); Open is OR'd (each controller mirrors the
		// shared process-wide breaker state as of its own most recent
		// Observe, so any controller having last seen it open means open).
		agg.PlatformBreakerTrips += s.PlatformBreakerTrips
		agg.PlatformBreakerOpen = agg.PlatformBreakerOpen || s.PlatformBreakerOpen
		for k, v := range s.APIErrors {
			agg.APIErrors[k] += v
		}
		for k, v := range s.LabelCleanups {
			agg.LabelCleanups[k] += v
		}
		for k, v := range s.ApprovalPersistMisses {
			agg.ApprovalPersistMisses[k] += v
		}
		for k, v := range s.TokensConsumed {
			agg.TokensConsumed[k] += v
		}
		for k, v := range s.ExecutionCostUSD {
			agg.ExecutionCostUSD[k] += v
		}
		for k, v := range s.ExecutionsByResult {
			agg.ExecutionsByResult[k] += v
		}
		for k, v := range s.PollerSkipped {
			agg.PollerSkipped[k] += v
		}
		for k, v := range s.PollerDispatched {
			agg.PollerDispatched[k] += v
		}
		for k, v := range s.PollerDeferredScopeOverlap {
			agg.PollerDeferredScopeOverlap[k] += v
		}
		for k, v := range s.OrphanPRsRegistered {
			agg.OrphanPRsRegistered[k] += v
		}
		for k, v := range s.UnsourcedLabeledIssues {
			agg.UnsourcedLabeledIssues[k] += v
		}
		agg.DuplicateRegistrationsSkipped += s.DuplicateRegistrationsSkipped

		for stage, count := range s.ActivePRsByStage {
			agg.ActivePRsByStage[stage] += count
		}
		agg.QueueDepth += s.QueueDepth
		agg.FailedQueueDepth += s.FailedQueueDepth
		agg.IssuesShipped += s.IssuesShipped
		agg.IssuesAttempted += s.IssuesAttempted
		for k, v := range s.IssuesShippedByModel {
			agg.IssuesShippedByModel[k] += v
		}
		for k, v := range s.IssuesAttemptedByModel {
			agg.IssuesAttemptedByModel[k] += v
		}

		// Each source's APIErrorRate is already "errors in the last 5min / 5"
		// for that source's independent error timestamps, so the fleet-wide
		// rate is a plain sum, not an average.
		agg.APIErrorRate += s.APIErrorRate
	}

	agg.TotalActivePRs = sumStageMap(agg.ActivePRsByStage)

	hist := a.HistogramSnapshot()
	agg.AvgPRTimeToMerge = avgDuration(hist.PRTimeToMerge)
	agg.AvgCIWaitDuration = avgDuration(hist.CIWaitDurations)
	agg.AvgExecutionDuration = avgDuration(hist.ExecutionDurations)

	// Recompute success rate over the merged counters (TASK-392 semantics:
	// rate_limited is excluded from the denominator).
	total := int64(0)
	for k, v := range agg.IssuesProcessed {
		if k == "rate_limited" {
			continue
		}
		total += v
	}
	if total > 0 {
		agg.SuccessRate = float64(agg.IssuesProcessed["success"]) / float64(total)
	}
	if agg.IssuesAttempted > 0 {
		agg.IssueLevelSuccessRate = float64(agg.IssuesShipped) / float64(agg.IssuesAttempted)
	}

	return agg
}

// HistogramSnapshot returns the concatenated raw duration samples across
// every source.
func (a *AggregateMetrics) HistogramSnapshot() HistogramData {
	var out HistogramData
	for _, m := range a.sources {
		if m == nil {
			continue
		}
		h := m.HistogramSnapshot()
		out.PRTimeToMerge = append(out.PRTimeToMerge, h.PRTimeToMerge...)
		out.CIWaitDurations = append(out.CIWaitDurations, h.CIWaitDurations...)
		out.ExecutionDurations = append(out.ExecutionDurations, h.ExecutionDurations...)
		out.TimeToPRDurations = append(out.TimeToPRDurations, h.TimeToPRDurations...)
		out.QueueWaitDurations = append(out.QueueWaitDurations, h.QueueWaitDurations...)
		out.ApprovalWaitDurations = append(out.ApprovalWaitDurations, h.ApprovalWaitDurations...)
	}
	return out
}
