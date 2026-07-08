package autopilot

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// MetricsPersister periodically saves metrics snapshots to SQLite for history.
type MetricsPersister struct {
	controller *Controller
	store      *memory.Store
	interval   time.Duration
	retention  time.Duration // How long to keep snapshots
	log        *slog.Logger
}

// NewMetricsPersister creates a new MetricsPersister.
// Saves snapshots every 5 minutes and retains 7 days of history.
func NewMetricsPersister(controller *Controller, store *memory.Store) *MetricsPersister {
	return &MetricsPersister{
		controller: controller,
		store:      store,
		interval:   5 * time.Minute,
		retention:  7 * 24 * time.Hour,
		log:        slog.Default().With("component", "metrics-persister"),
	}
}

// Run starts the persister loop.
func (mp *MetricsPersister) Run(ctx context.Context) {
	if mp.store == nil {
		mp.log.Debug("no store configured, metrics persistence disabled")
		return
	}

	ticker := time.NewTicker(mp.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final snapshot on shutdown
			mp.persist()
			return
		case <-ticker.C:
			mp.refreshIssueLevelCounts()
			mp.persist()
			mp.prune()
		}
	}
}

// refreshIssueLevelCounts re-queries the store for unique-issue attempt/ship
// counts and updates the gauge. Unlike the Counter-backed metrics (tokens,
// cost, executions), this is a point-in-time re-derivation each cycle rather
// than an incremental hydration, since dedupe-by-task_id can't be computed
// from deltas alone. TASK-392.
func (mp *MetricsPersister) refreshIssueLevelCounts() {
	counts, err := mp.store.GetIssueLevelCounts("")
	if err != nil {
		mp.log.Warn("failed to refresh issue-level counts", slog.Any("error", err))
		return
	}
	mp.controller.Metrics().SetIssueLevelCounts(int64(counts.Shipped), int64(counts.Attempted))
}

func (mp *MetricsPersister) persist() {
	snap := mp.controller.Metrics().Snapshot()

	// Sum up API errors total
	var apiErrorsTotal int64
	for _, count := range snap.APIErrors {
		apiErrorsTotal += count
	}

	// Convert tokenKey map to string-keyed map for storage (key: "model|direction").
	tokensConsumed := make(map[string]int64, len(snap.TokensConsumed))
	for k, v := range snap.TokensConsumed {
		tokensConsumed[k.Model+"|"+k.Direction] = v
	}

	// Convert execKey map to string-keyed map for storage (key: "model|result").
	executionsByResult := make(map[string]int64, len(snap.ExecutionsByResult))
	for k, v := range snap.ExecutionsByResult {
		executionsByResult[k.Model+"|"+k.Result] = v
	}

	row := &memory.AutopilotMetricsRow{
		SnapshotAt:          snap.SnapshotAt,
		IssuesSuccess:       int(snap.IssuesProcessed["success"]),
		IssuesFailed:        int(snap.IssuesProcessed["failed"]),
		IssuesRateLimited:   int(snap.IssuesProcessed["rate_limited"]),
		PRsMerged:           int(snap.PRsMerged),
		PRsFailed:           int(snap.PRsFailed),
		PRsConflicting:      int(snap.PRsConflicting),
		CircuitBreakerTrips: int(snap.CircuitBreakerTrips),
		APIErrorsTotal:      int(apiErrorsTotal),
		APIErrorRate:        snap.APIErrorRate,
		QueueDepth:          snap.QueueDepth,
		FailedQueueDepth:    snap.FailedQueueDepth,
		ActivePRs:           snap.TotalActivePRs,
		SuccessRate:         snap.SuccessRate,
		AvgCIWaitMs:         snap.AvgCIWaitDuration.Milliseconds(),
		AvgMergeTimeMs:      snap.AvgPRTimeToMerge.Milliseconds(),
		AvgExecutionMs:      snap.AvgExecutionDuration.Milliseconds(),
		TokensConsumed:      tokensConsumed,
		ExecutionCostUSD:    snap.ExecutionCostUSD,
		ExecutionsByResult:  executionsByResult,
	}

	if err := mp.store.SaveAutopilotMetrics(row); err != nil {
		mp.log.Warn("failed to persist autopilot metrics", slog.Any("error", err))
	}
}

func (mp *MetricsPersister) prune() {
	deleted, err := mp.store.PruneAutopilotMetrics(mp.retention)
	if err != nil {
		mp.log.Warn("failed to prune autopilot metrics", slog.Any("error", err))
	} else if deleted > 0 {
		mp.log.Debug("pruned old autopilot metrics", slog.Int64("deleted", deleted))
	}

	deletedLogs, err := mp.store.PruneExecutionLogs(mp.retention)
	if err != nil {
		mp.log.Warn("failed to prune execution logs", slog.Any("error", err))
	} else if deletedLogs > 0 {
		mp.log.Debug("pruned old execution logs", slog.Int64("deleted", deletedLogs))
	}
}
