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

	return nil
}
