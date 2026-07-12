package autopilot

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/memory"
)

// HydrateFromStore restores Prometheus counter baselines from the store's
// lifetime execution history into metrics, so external dashboards match
// Pilot's store-backed totals across daemon restarts instead of observing a
// reset-to-zero (GH-4041). Counters stay true Counters — this adds the
// lifetime baseline on top of the freshly constructed, all-zero in-memory
// Metrics, it does not replace or reset anything.
//
// Must be called once at startup, after metrics are registered and before
// the HTTP server starts serving /metrics. Callers should treat a non-nil
// error as fatal — starting with silent zero baselines defeats the point.
//
// pilot_success_rate is a gauge derived from IssuesProcessed at scrape time
// (see Metrics.Snapshot), so hydrating IssuesProcessed here fixes it for
// free — no separate wiring needed.
func HydrateFromStore(ctx context.Context, store *memory.Store, metrics *Metrics) error {
	if store == nil || metrics == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	baselines, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: %w", err)
	}
	for key, n := range baselines.TokensByModelDirection {
		metrics.RecordTokens(key.Model, key.Direction, n)
	}
	for model, cost := range baselines.CostByModel {
		metrics.RecordCost(model, cost)
	}
	for key, n := range baselines.ExecutionsByModelResult {
		metrics.HydrateExecutions(key.Model, key.Result, n)
	}

	// TASK-392: hydrate the same three keys the live path ever records
	// (RecordIssueProcessed call sites only ever pass success/failed/
	// rate_limited). taskCounts.Failed already counts genuine failures only
	// (status='failed') — unlike Total-Succeeded-RateLimited, it does not
	// pull in declined/no_op/stalled/infra/skipped, so hydration no longer
	// mislabels non-failure terminal outcomes as "failed".
	taskCounts, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime task counts: %w", err)
	}
	metrics.HydrateIssuesProcessed("success", int64(taskCounts.Succeeded))
	metrics.HydrateIssuesProcessed("rate_limited", int64(taskCounts.RateLimited))
	metrics.HydrateIssuesProcessed("failed", int64(taskCounts.Failed))

	issueLevel, err := store.GetIssueLevelCounts("")
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: issue-level counts: %w", err)
	}
	metrics.SetIssueLevelCounts(int64(issueLevel.Shipped), int64(issueLevel.Attempted))

	// GH-4121: pilot_prs_merged_total / pilot_prs_failed_total from the
	// executions table (all-time), not the execution_events ledger — the
	// ledger only goes back to its TASK-379/GH-3844 introduction and
	// undercounted these two counters ~20x against every other lifetime
	// counter on this same Metrics (issues_shipped, cost, tokens), which all
	// hydrate all-time from executions. Must land on this same
	// designated-owner Metrics (see AggregateMetrics doc comment) exactly
	// once — hydrating any other controller's Metrics here would double
	// count in the fleet-wide aggregate.
	prCounters, err := store.GetLifetimePRCountersFromExecutions("")
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime PR counters: %w", err)
	}
	metrics.HydratePRsMerged(prCounters.Merged)
	metrics.HydratePRsFailed(prCounters.Failed)

	// GH-4134: pilot_ci_runs_total{result} from the execution_events ledger —
	// unlike PRsMerged/PRsFailed there is no pre-ledger source to fall back to
	// (CI verdicts were never persisted anywhere before this ledger existed),
	// so this baseline only covers CI verdicts recorded since the ledger's
	// TASK-379/GH-3844 introduction. Must land on this same designated-owner
	// Metrics exactly once, same reasoning as the PR counters above.
	ciRunCounters, err := store.GetLifetimeCIRunCounters()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime CI run counters: %w", err)
	}
	metrics.HydrateCIRun("pass", ciRunCounters.Pass)
	metrics.HydrateCIRun("fail", ciRunCounters.Fail)

	// pilot_pr_time_to_merge_seconds: derivable from execution_events
	// (pr_created -> merged deltas), so hydrate it too rather than leaving it
	// session-scoped.
	timeToMerge, err := store.GetLifetimePRTimeToMerge()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime PR time-to-merge: %w", err)
	}
	for _, d := range timeToMerge {
		metrics.RecordPRTimeToMerge(d)
	}

	// GH-4211: the TASK-393/GH-4128 throughput histograms (pilot_time_to_pr_seconds,
	// pilot_queue_wait_seconds, pilot_approval_wait_seconds) reset to zero on every
	// restart same as pilot_pr_time_to_merge_seconds above — reconstruct them from
	// the ledger/executions table so a daily self-upgrade restart doesn't wipe the
	// throughput view. Each Record* call self-caps at Metrics.maxSamples, and the
	// three Get* queries return samples oldest-first, so the cap keeps the most
	// recent N regardless of how many lifetime rows exist.
	timeToPR, err := store.GetLifetimeTimeToPR()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime time-to-PR: %w", err)
	}
	for _, d := range timeToPR {
		metrics.RecordTimeToPR(d)
	}

	queueWait, err := store.GetLifetimeQueueWait()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime queue wait: %w", err)
	}
	for _, d := range queueWait {
		metrics.RecordQueueWaitDuration(d)
	}

	approvalWait, err := store.GetLifetimeApprovalWait()
	if err != nil {
		return fmt.Errorf("hydrate metrics from store: lifetime approval wait: %w", err)
	}
	for _, d := range approvalWait {
		metrics.RecordApprovalWaitDuration(d)
	}

	// The remaining counters are intentionally NOT hydrated here — they have no
	// durable per-event source to hydrate from (unlike execution_events, which
	// is an append-only ledger keyed off executions.id) and reset to zero on
	// every restart by design:
	//   - pilot_prs_conflicting_total (RecordPRConflicting): no execution_events
	//     stage exists for "conflicting" — handleMergeConflict does not log to
	//     the ledger, only to the in-memory counter (GH-4069).
	//   - pilot_circuit_breaker_trips_total, pilot_api_errors_total,
	//     pilot_label_cleanups_total, pilot_approval_persist_misses_total,
	//     pilot_poller_skipped_total, pilot_poller_dispatched_total,
	//     pilot_poller_deferred_scope_overlap_total, pilot_panics_total: pure
	//     in-memory operational/diagnostic counters with no matching table or
	//     ledger row anywhere in the store. The periodic autopilot_metrics
	//     snapshot table (SaveAutopilotMetrics) records point-in-time values of
	//     these but is not append-only across restarts — its "latest row"
	//     reflects the session since the previous restart, not lifetime, so
	//     using it as a hydration baseline would silently drop everything
	//     between the last snapshot write and the actual restart. Treated as
	//     reset-on-restart by design; see FEATURE-MATRIX.md.
	return nil
}
