package autopilot

import (
	"sync"
	"time"
)

// tokenKey identifies a token bucket by model and direction (input/output/cache_read/etc).
type tokenKey struct {
	Model     string
	Direction string
}

// execKey identifies an execution bucket by model and result (success/failed/etc).
type execKey struct {
	Model  string
	Result string
}

// pollerSkipKey identifies a poller skip event by repo and reason.
type pollerSkipKey struct {
	Repo   string
	Reason string
}

// Metrics collects autopilot operational metrics.
// All methods are goroutine-safe.
type Metrics struct {
	mu sync.RWMutex

	// Counters
	IssuesProcessed map[string]int64 // result → count (success, failed, rate_limited)
	// PRsMerged/PRsFailed are pure session counters (GH-4511): they start at
	// 0 on every boot and are only ever incremented live (RecordPRMerged/
	// RecordPRFailed). They are NEVER hydrated with a store baseline —
	// doing so previously caused Prometheus counter-reset artifacts, since a
	// hydrated baseline landing below the pre-restart live value looks like
	// a reset to increase()/rate(), which then replays the entire baseline
	// as fabricated new activity. All-time totals live on the
	// PRsMergedLifetime/PRsFailedLifetime gauges below instead.
	PRsMerged      int64
	PRsFailed      int64
	PRsConflicting int64
	// PRsMergedLifetime/PRsFailedLifetime (GH-4511) are gauges: hydrated once
	// at boot from the store's lifetime baseline (HydratePRsMergedLifetime/
	// HydratePRsFailedLifetime) and bumped alongside the session counters on
	// every live RecordPRMerged/RecordPRFailed call. Gauges are immune to the
	// counter-reset misinterpretation above, so this is the correct series
	// for all-time/dashboard totals across restarts.
	PRsMergedLifetime int64
	PRsFailedLifetime int64
	// CIRuns counts distinct CI verdicts by result ("pass"/"fail"), GH-4134.
	// One entry per StageCIPassed transition or terminal handleCIFailed call —
	// a multi-iteration CI-fix cascade on one PR is several distinct verdicts
	// (one per push+CI-run), not one. Unlike PRsFailed (RecordPRFailed), which
	// also folds in approval rejections and merge/release failures, this is
	// scoped to CI outcomes only.
	CIRuns map[string]int64
	// PRFailureClasses counts terminal handleCIFailed calls by classification
	// ("code"/"infra"/"infra_billing", see FailureClass — GH-4533, extended
	// GH-4591), regardless of whether the failure resulted in an auto-retry
	// or a spawned fix issue. Distinct from CIRuns: CIRuns tracks
	// verdict/outcome ("fail"/"infra_retry"/"infra_fail"), this tracks the
	// classification the outcome was decided from.
	PRFailureClasses    map[string]int64
	CircuitBreakerTrips int64
	// RateLimitFloorEngagements counts distinct episodes (not calls) where
	// the shared GitHub rate-budget floor engaged and this controller
	// skipped a PriorityBackground scan (merged-PR scan, orphan-PR sweep) —
	// GH-4391. One increment per transition into the engaged state per
	// controller, deduped via Controller.budgetFloorSkipped, mirroring how
	// the ghbudget.Tracker itself dedupes its WARN log.
	RateLimitFloorEngagements int64
	APIErrors                 map[string]int64 // endpoint → count
	LabelCleanups             map[string]int64 // label → count
	ApprovalPersistMisses     map[string]int64 // kind → count (request_id, decision)
	// IntentJudgeFailures counts pre-flight intent-judge subprocess failures
	// by cause (context_deadline, external_sigkill, other) — GH-4377: the SDK
	// poller's pre-flight gate previously failed open on every judge crash
	// with no visible signal that the judge was effectively dead.
	IntentJudgeFailures map[string]int64
	// ApprovalSubmitFailures counts Manager.SubmitApprovalRequest errors — an
	// unregistered/misrouted approval channel, distinct from a normal timeout
	// or rejection decision (GH-4380).
	ApprovalSubmitFailures int64
	// TokensConsumed, ExecutionCostUSD, and ExecutionsByResult are persisted per
	// snapshot to SQLite (GH-2856) so historical data survives across runs.
	// However, on daemon restart the in-memory counters reset to zero and
	// re-accumulate from new executions — they are NOT restored from the latest
	// snapshot yet. Prometheus rate() queries tolerate this via reset detection.
	// TODO(GH-2836): call Metrics.RestoreFromRow on startup to resume from last snapshot.
	TokensConsumed     map[tokenKey]int64 // {model,direction} → token count
	ExecutionCostUSD   map[string]float64 // model → cumulative USD cost
	ExecutionsByResult map[execKey]int64  // {model,result} → execution count

	// Poller dispatch/skip counters (GH-3064, TASK-293)
	PollerSkipped              map[pollerSkipKey]int64 // {repo,reason} → skip count
	PollerDispatched           map[string]int64        // repo → dispatch count
	PollerDeferredScopeOverlap map[string]int64        // repo → deferred-scope-overlap count

	// Orphan PR registration counters (GH-3113, TASK-302)
	// trigger is "reconciler" or "startup_scan".
	// Sustained spikes indicate OnPRCreated is missing fires.
	OrphanPRsRegistered map[string]int64 // trigger → count

	// Duplicate registration skips (GH-3828): OnPRCreated fired twice for the
	// same PR (e.g. orphan-reconciler and the normal poller callback racing).
	// OnPRCreated has no visibility into which caller arrived first, so this
	// is a single counter rather than a per-trigger breakdown. Sustained
	// counts indicate the two paths are racing often, though each occurrence
	// is already handled safely (no-op, not a bug).
	DuplicateRegistrationsSkipped int64

	// Gauges (point-in-time values)
	ActivePRsByStage map[PRStage]int
	QueueDepth       int // issues with `pilot` label, no `pilot-in-progress`
	FailedQueueDepth int // issues with `pilot-failed`

	// UnsourcedLabeledIssues is the GH-4488 board-sourcing audit gauge: repo
	// → count of open pilot-labeled issues that project_board.source_enabled
	// board sourcing is silently ignoring (absent from the board, or in a
	// status other than source_status). Set by
	// reconcileUnsourcedBoardIssues every poll cycle; nonzero indicates a
	// board-sourced repo has labeled work the poller will never dispatch
	// until either the board or the label is fixed.
	UnsourcedLabeledIssues map[string]int64

	// Issue-level outcome gauges (TASK-392): unique task_id counts, deduped
	// across retry attempts. Contrast with IssuesProcessed, which is
	// per-attempt (a task retried twice before shipping contributes 3 events).
	IssuesShipped   int64 // distinct task_id that reached status='completed'
	IssuesAttempted int64 // distinct task_id with at least one execution

	// Issue-level outcome gauges broken out by model (GH-4483): same
	// dedupe-by-task_id semantics as IssuesShipped/IssuesAttempted above, but
	// per model — so the per-model panel can show eventual delivery next to
	// the attempt-level ExecutionsByResult signal, which is dominated by
	// mid-flight retries/rate-limit deaths and reads far lower than the
	// issue's actual eventual-success rate.
	IssuesShippedByModel   map[string]int64
	IssuesAttemptedByModel map[string]int64

	// Histograms (stored as recent samples for summary stats)
	PRTimeToMerge      []time.Duration
	CIWaitDurations    []time.Duration
	ExecutionDurations []time.Duration

	// Histograms registered by GH-4128; observed at their transitions by
	// GH-4130 (OnPRCreated, applyApprovalDecision).
	TimeToPRDurations     []time.Duration // execution started_at to pr_created
	QueueWaitDurations    []time.Duration // execution created_at to started_at
	ApprovalWaitDurations []time.Duration // awaiting_approval request to merge decision

	// Timestamps for rate calculation
	apiErrorTimes []time.Time

	// Maximum samples to keep for histograms
	maxSamples int

	// GH-4735: rolling-window headline cost/success numbers backing the
	// pilot_window_* gauges. Unlike the lifetime gauges above (hydrated once
	// at boot, then bumped incrementally on every live event), these are
	// wholesale-overwritten by SetWindowStats on a periodic ticker
	// (HydrateWindowStats/StartWindowStatsRefresher) — the underlying
	// GetWindowedStats query is too expensive to run per-scrape or per-event.
	WindowDays                int
	WindowCostUSD             float64
	WindowCostPerDeliveredUSD float64
	WindowDeliveryRate        float64
	WindowAttemptSuccessRate  float64
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
		IssuesProcessed:            make(map[string]int64),
		CIRuns:                     make(map[string]int64),
		PRFailureClasses:           make(map[string]int64),
		APIErrors:                  make(map[string]int64),
		LabelCleanups:              make(map[string]int64),
		ApprovalPersistMisses:      make(map[string]int64),
		IntentJudgeFailures:        make(map[string]int64),
		TokensConsumed:             make(map[tokenKey]int64),
		ExecutionCostUSD:           make(map[string]float64),
		ExecutionsByResult:         make(map[execKey]int64),
		PollerSkipped:              make(map[pollerSkipKey]int64),
		PollerDispatched:           make(map[string]int64),
		PollerDeferredScopeOverlap: make(map[string]int64),
		OrphanPRsRegistered:        make(map[string]int64),
		IssuesShippedByModel:       make(map[string]int64),
		IssuesAttemptedByModel:     make(map[string]int64),
		UnsourcedLabeledIssues:     make(map[string]int64),
		ActivePRsByStage:           make(map[PRStage]int),
		PRTimeToMerge:              make([]time.Duration, 0, 100),
		CIWaitDurations:            make([]time.Duration, 0, 100),
		ExecutionDurations:         make([]time.Duration, 0, 100),
		TimeToPRDurations:          make([]time.Duration, 0, 100),
		QueueWaitDurations:         make([]time.Duration, 0, 100),
		ApprovalWaitDurations:      make([]time.Duration, 0, 100),
		apiErrorTimes:              make([]time.Time, 0, 100),
		maxSamples:                 1000,
	}
}

// --- Counter increments ---

// RecordIssueProcessed increments the processed issue counter by result.
func (m *Metrics) RecordIssueProcessed(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesProcessed[result]++
}

// RecordPollerSkipped increments the poller skip counter for a given repo and reason.
func (m *Metrics) RecordPollerSkipped(repo, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PollerSkipped[pollerSkipKey{Repo: repo, Reason: reason}]++
}

// RecordPollerDispatched increments the dispatch counter for a repo.
func (m *Metrics) RecordPollerDispatched(repo string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PollerDispatched[repo]++
}

// RecordPollerDeferredScopeOverlap increments the scope-overlap deferral counter for a repo.
func (m *Metrics) RecordPollerDeferredScopeOverlap(repo string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PollerDeferredScopeOverlap[repo]++
}

// RecordOrphanPRRegistered increments the orphan-PR registration counter.
// trigger is "reconciler" (periodic loop) or "startup_scan" (ScanExistingPRs).
func (m *Metrics) RecordOrphanPRRegistered(trigger string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.OrphanPRsRegistered[trigger]++
}

// RecordDuplicateRegistrationSkipped increments the counter for a second
// OnPRCreated call landing on an already-tracked PR.
func (m *Metrics) RecordDuplicateRegistrationSkipped() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DuplicateRegistrationsSkipped++
}

// RecordPRMerged increments the merged PR session counter and its lifetime
// gauge counterpart (GH-4511).
func (m *Metrics) RecordPRMerged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsMerged++
	m.PRsMergedLifetime++
}

// RecordPRFailed increments the failed PR session counter and its lifetime
// gauge counterpart (GH-4511).
func (m *Metrics) RecordPRFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsFailed++
	m.PRsFailedLifetime++
}

// RecordCIRun increments the CI verdict counter for a given result
// ("pass"/"fail"). GH-4134: called once per distinct CI verdict — the
// StageCIPassed transition in handleWaitingCI, or a terminal handleCIFailed
// call — never per fix-iteration retry.
func (m *Metrics) RecordCIRun(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CIRuns[result]++
}

// HydrateCIRun adds a store-lifetime baseline to the CI verdict counter for a
// given result. See HydrateExecutions (GH-4134, restores pilot_ci_runs_total
// across restarts from the durable execution_events ledger).
func (m *Metrics) HydrateCIRun(result string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CIRuns[result] += n
}

// RecordPRFailedClass increments the terminal-failure classification counter
// (GH-4533). Called once per terminal handleCIFailed call, alongside
// RecordPRFailed, regardless of whether the classification triggered an
// auto-retry or a spawned fix issue.
func (m *Metrics) RecordPRFailedClass(class FailureClass) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.PRFailureClasses == nil {
		m.PRFailureClasses = make(map[string]int64)
	}
	m.PRFailureClasses[string(class)]++
}

// RecordPRConflicting increments the conflicting PR counter.
func (m *Metrics) RecordPRConflicting() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsConflicting++
}

// RecordCircuitBreakerTrip increments the circuit breaker trip counter.
func (m *Metrics) RecordCircuitBreakerTrip() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CircuitBreakerTrips++
}

// RecordRateLimitFloorEngaged increments the rate-limit budget-floor
// engagement counter (GH-4391). Call sites are expected to dedupe per
// episode themselves (see Controller.backgroundScanAllowed) — this method
// does no deduplication on its own.
func (m *Metrics) RecordRateLimitFloorEngaged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RateLimitFloorEngagements++
}

// RecordAPIError increments the API error counter for a given endpoint.
func (m *Metrics) RecordAPIError(endpoint string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.APIErrors[endpoint]++
	m.apiErrorTimes = append(m.apiErrorTimes, time.Now())
	// Trim old entries (keep last maxSamples)
	if len(m.apiErrorTimes) > m.maxSamples {
		m.apiErrorTimes = m.apiErrorTimes[len(m.apiErrorTimes)-m.maxSamples:]
	}
}

// RecordLabelCleanup increments the label cleanup counter.
func (m *Metrics) RecordLabelCleanup(label string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.LabelCleanups[label]++
}

// RecordApprovalPersistMiss increments the counter for zero-row approval UPDATE misses.
// kind is "request_id" or "decision".
func (m *Metrics) RecordApprovalPersistMiss(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ApprovalPersistMisses[kind]++
}

// RecordIntentJudgeFailure increments the pre-flight intent-judge subprocess
// failure counter for a given cause (context_deadline, external_sigkill,
// other — see executor.JudgeSubprocessError). GH-4377.
func (m *Metrics) RecordIntentJudgeFailure(cause string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IntentJudgeFailures[cause]++
}

// RecordApprovalSubmitFailure increments the counter for SubmitApprovalRequest
// errors (GH-4380).
func (m *Metrics) RecordApprovalSubmitFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ApprovalSubmitFailures++
}

// RecordTokens adds n tokens to the {model, direction} bucket.
func (m *Metrics) RecordTokens(model, direction string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TokensConsumed[tokenKey{Model: model, Direction: direction}] += n
}

// RecordCost adds costUSD to the per-model cumulative cost.
func (m *Metrics) RecordCost(model string, costUSD float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ExecutionCostUSD[model] += costUSD
}

// RecordExecution increments the {model, result} execution counter.
func (m *Metrics) RecordExecution(model, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ExecutionsByResult[execKey{Model: model, Result: result}]++
}

// HydrateExecutions adds a store-lifetime baseline to the {model, result}
// execution counter. Unlike RecordExecution (per-event, +1), this adds an
// arbitrary count — intended for one-time startup hydration from the store
// before /metrics starts serving scrapes (GH-4041), not live recording.
func (m *Metrics) HydrateExecutions(model, result string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ExecutionsByResult[execKey{Model: model, Result: result}] += n
}

// HydrateIssuesProcessed adds a store-lifetime baseline to the issues-processed
// counter. See HydrateExecutions (GH-4041).
func (m *Metrics) HydrateIssuesProcessed(result string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesProcessed[result] += n
}

// HydratePRsMergedLifetime adds a store-lifetime baseline to the merged-PR
// lifetime gauge. GH-4511: this used to add onto the live PRsMerged counter
// (see HydrateExecutions / GH-4093), which caused Prometheus counter-reset
// artifacts whenever the hydrated baseline landed below the pre-restart live
// value — increase()/rate() would then replay the whole baseline as
// fabricated new activity. PRsMerged is now session-only; the lifetime
// baseline lands exclusively on this gauge.
func (m *Metrics) HydratePRsMergedLifetime(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsMergedLifetime += n
}

// HydratePRsFailedLifetime adds a store-lifetime baseline to the failed-PR
// lifetime gauge. See HydratePRsMergedLifetime (GH-4511, GH-4093).
func (m *Metrics) HydratePRsFailedLifetime(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsFailedLifetime += n
}

// SetWindowStats overwrites the GH-4735 rolling-window cost/success gauges
// from a fresh GetWindowedStats query. Unlike the Hydrate* methods above,
// this is a plain assignment, not additive — each call replaces the
// previous window snapshot outright, since the source query is already an
// absolute point-in-time aggregate (not a per-event delta to accumulate).
func (m *Metrics) SetWindowStats(days int, totalCostUSD, costPerDelivered, deliveryRate, attemptSuccessRate float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.WindowDays = days
	m.WindowCostUSD = totalCostUSD
	m.WindowCostPerDeliveredUSD = costPerDelivered
	m.WindowDeliveryRate = deliveryRate
	m.WindowAttemptSuccessRate = attemptSuccessRate
}

// HydrateCircuitBreakerTrips sets a monotonic floor — max(recount,
// last_served) — on the circuit-breaker trip counter from the last periodic
// autopilot_metrics snapshot persisted before this restart (GH-4390).
// pilot_circuit_breaker_trips_total has no append-only per-event ledger to
// recount from (RecordCircuitBreakerTrip only ever increments an in-memory
// value), so "recount" here is always 0 and the floor reduces to
// last_served: a fresh Metrics starts at 0, and this raises it to the last
// value Prometheus actually scraped pre-restart, so a restart can only hold
// the exposed counter steady or grow it — never re-serve Prometheus a value
// below what it already observed. That's the exact shape of counter-reset
// misbehavior this closes: increase()/rate() treat any nonzero-but-lower
// value as a reset and replay the whole new value as fabricated growth
// (GH-4511 hit this same failure mode for pilot_prs_merged_total).
func (m *Metrics) HydrateCircuitBreakerTrips(lastServed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lastServed > m.CircuitBreakerTrips {
		m.CircuitBreakerTrips = lastServed
	}
}

// --- Gauge updates ---

// UpdateActivePRs recalculates active PR counts by stage from a snapshot.
func (m *Metrics) UpdateActivePRs(prs []*PRState) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset all stages
	m.ActivePRsByStage = make(map[PRStage]int)
	for _, pr := range prs {
		m.ActivePRsByStage[pr.Stage]++
	}
}

// SetQueueDepth updates the queue depth gauge.
func (m *Metrics) SetQueueDepth(depth int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.QueueDepth = depth
}

// SetFailedQueueDepth updates the failed queue depth gauge.
func (m *Metrics) SetFailedQueueDepth(depth int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailedQueueDepth = depth
}

// SetUnsourcedLabeledIssues updates the GH-4488 board-sourcing audit gauge
// for a repo. Called once per poll cycle by reconcileUnsourcedBoardIssues;
// a fresh call always overwrites the prior value (not additive), so the
// gauge tracks the live set size rather than accumulating.
func (m *Metrics) SetUnsourcedLabeledIssues(repo string, count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UnsourcedLabeledIssues == nil {
		m.UnsourcedLabeledIssues = make(map[string]int64)
	}
	m.UnsourcedLabeledIssues[repo] = count
}

// SetIssueLevelCounts updates the issue-level outcome gauges from a
// store.IssueLevelCounts query (unique task_id, deduped across retries).
// TASK-392.
func (m *Metrics) SetIssueLevelCounts(shipped, attempted int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesShipped = shipped
	m.IssuesAttempted = attempted
}

// SetIssueLevelCountsByModel replaces the per-model issue-level outcome
// gauges from a store.GetIssueLevelCountsByModel query (unique task_id,
// deduped across retries, within each model). Like SetIssueLevelCounts, this
// is a full replace each cycle rather than an increment — dedupe-by-task_id
// can't be computed from deltas alone. GH-4483.
func (m *Metrics) SetIssueLevelCountsByModel(shipped, attempted map[string]int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesShippedByModel = shipped
	m.IssuesAttemptedByModel = attempted
}

// --- Histogram recording ---

// RecordPRTimeToMerge records the duration from PR creation to merge.
func (m *Metrics) RecordPRTimeToMerge(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRTimeToMerge = append(m.PRTimeToMerge, d)
	if len(m.PRTimeToMerge) > m.maxSamples {
		m.PRTimeToMerge = m.PRTimeToMerge[len(m.PRTimeToMerge)-m.maxSamples:]
	}
}

// RecordCIWaitDuration records how long a PR waited for CI.
func (m *Metrics) RecordCIWaitDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CIWaitDurations = append(m.CIWaitDurations, d)
	if len(m.CIWaitDurations) > m.maxSamples {
		m.CIWaitDurations = m.CIWaitDurations[len(m.CIWaitDurations)-m.maxSamples:]
	}
}

// RecordExecutionDuration records task execution time.
func (m *Metrics) RecordExecutionDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ExecutionDurations = append(m.ExecutionDurations, d)
	if len(m.ExecutionDurations) > m.maxSamples {
		m.ExecutionDurations = m.ExecutionDurations[len(m.ExecutionDurations)-m.maxSamples:]
	}
}

// RecordTimeToPR records the duration from issue pickup to PR creation.
// GH-4128: no call site yet — plumbing ahead of the observer.
func (m *Metrics) RecordTimeToPR(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TimeToPRDurations = append(m.TimeToPRDurations, d)
	if len(m.TimeToPRDurations) > m.maxSamples {
		m.TimeToPRDurations = m.TimeToPRDurations[len(m.TimeToPRDurations)-m.maxSamples:]
	}
}

// RecordQueueWaitDuration records how long an issue waited in queue before pickup.
// GH-4128: no call site yet — plumbing ahead of the observer.
func (m *Metrics) RecordQueueWaitDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.QueueWaitDurations = append(m.QueueWaitDurations, d)
	if len(m.QueueWaitDurations) > m.maxSamples {
		m.QueueWaitDurations = m.QueueWaitDurations[len(m.QueueWaitDurations)-m.maxSamples:]
	}
}

// RecordApprovalWaitDuration records how long a PR waited for human approval.
// GH-4128: no call site yet — plumbing ahead of the observer.
func (m *Metrics) RecordApprovalWaitDuration(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ApprovalWaitDurations = append(m.ApprovalWaitDurations, d)
	if len(m.ApprovalWaitDurations) > m.maxSamples {
		m.ApprovalWaitDurations = m.ApprovalWaitDurations[len(m.ApprovalWaitDurations)-m.maxSamples:]
	}
}

// --- Read accessors ---

// Snapshot returns a point-in-time copy of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := MetricsSnapshot{
		IssuesProcessed:               copyStringIntMap(m.IssuesProcessed),
		PRsMerged:                     m.PRsMerged,
		PRsFailed:                     m.PRsFailed,
		PRsMergedLifetime:             m.PRsMergedLifetime,
		PRsFailedLifetime:             m.PRsFailedLifetime,
		PRsConflicting:                m.PRsConflicting,
		CIRuns:                        copyStringIntMap(m.CIRuns),
		PRFailureClasses:              copyStringIntMap(m.PRFailureClasses),
		CircuitBreakerTrips:           m.CircuitBreakerTrips,
		RateLimitFloorEngagements:     m.RateLimitFloorEngagements,
		APIErrors:                     copyStringIntMap(m.APIErrors),
		LabelCleanups:                 copyStringIntMap(m.LabelCleanups),
		ApprovalPersistMisses:         copyStringIntMap(m.ApprovalPersistMisses),
		IntentJudgeFailures:           copyStringIntMap(m.IntentJudgeFailures),
		ApprovalSubmitFailures:        m.ApprovalSubmitFailures,
		TokensConsumed:                copyTokenKeyMap(m.TokensConsumed),
		ExecutionCostUSD:              copyStringFloatMap(m.ExecutionCostUSD),
		ExecutionsByResult:            copyExecKeyMap(m.ExecutionsByResult),
		PollerSkipped:                 copyPollerSkipKeyMap(m.PollerSkipped),
		PollerDispatched:              copyStringIntMap(m.PollerDispatched),
		PollerDeferredScopeOverlap:    copyStringIntMap(m.PollerDeferredScopeOverlap),
		OrphanPRsRegistered:           copyStringIntMap(m.OrphanPRsRegistered),
		DuplicateRegistrationsSkipped: m.DuplicateRegistrationsSkipped,
		ActivePRsByStage:              copyStageIntMap(m.ActivePRsByStage),
		QueueDepth:                    m.QueueDepth,
		FailedQueueDepth:              m.FailedQueueDepth,
		UnsourcedLabeledIssues:        copyStringIntMap(m.UnsourcedLabeledIssues),
		IssuesShipped:                 m.IssuesShipped,
		IssuesAttempted:               m.IssuesAttempted,
		IssuesShippedByModel:          copyStringIntMap(m.IssuesShippedByModel),
		IssuesAttemptedByModel:        copyStringIntMap(m.IssuesAttemptedByModel),
		TotalActivePRs:                sumStageMap(m.ActivePRsByStage),
		AvgPRTimeToMerge:              avgDuration(m.PRTimeToMerge),
		AvgCIWaitDuration:             avgDuration(m.CIWaitDurations),
		AvgExecutionDuration:          avgDuration(m.ExecutionDurations),
		APIErrorRate:                  m.apiErrorRate(),
		WindowDays:                    m.WindowDays,
		WindowCostUSD:                 m.WindowCostUSD,
		WindowCostPerDeliveredUSD:     m.WindowCostPerDeliveredUSD,
		WindowDeliveryRate:            m.WindowDeliveryRate,
		WindowAttemptSuccessRate:      m.WindowAttemptSuccessRate,
		SnapshotAt:                    time.Now(),
	}

	// Calculate per-attempt success rate. rate_limited is excluded from the
	// denominator: it is a scheduling/backoff signal (we deferred the
	// attempt), not a quality outcome, so it isn't a "failure" the rate
	// should be penalized for (TASK-392). Any other keys IssuesProcessed
	// might carry (currently only success/failed/rate_limited are ever
	// recorded — see RecordIssueProcessed call sites) are included, matching
	// prior behavior for genuine outcomes.
	total := int64(0)
	for k, v := range m.IssuesProcessed {
		if k == "rate_limited" {
			continue
		}
		total += v
	}
	if total > 0 {
		snap.SuccessRate = float64(m.IssuesProcessed["success"]) / float64(total)
	}

	// Issue-level success rate: unique issues shipped / unique issues
	// attempted, deduped across retries (TASK-392). Distinct from
	// SuccessRate above, which is a per-attempt efficiency signal.
	if snap.IssuesAttempted > 0 {
		snap.IssueLevelSuccessRate = float64(snap.IssuesShipped) / float64(snap.IssuesAttempted)
	}

	return snap
}

// apiErrorRate returns errors per minute in the last 5 minutes.
// Must be called with mu held (at least RLock).
func (m *Metrics) apiErrorRate() float64 {
	cutoff := time.Now().Add(-5 * time.Minute)
	count := 0
	for _, t := range m.apiErrorTimes {
		if t.After(cutoff) {
			count++
		}
	}
	return float64(count) / 5.0 // per minute
}

// MetricsSnapshot is a read-only copy of metrics at a point in time.
type MetricsSnapshot struct {
	// Counters
	IssuesProcessed           map[string]int64
	PRsMerged                 int64
	PRsFailed                 int64
	PRsMergedLifetime         int64 // GH-4511: all-time gauge counterpart to session-only PRsMerged
	PRsFailedLifetime         int64 // GH-4511: all-time gauge counterpart to session-only PRsFailed
	PRsConflicting            int64
	CIRuns                    map[string]int64 // GH-4134: result → count (pass, fail)
	PRFailureClasses          map[string]int64 // GH-4533/GH-4591: class → count (code, infra, infra_billing)
	CircuitBreakerTrips       int64
	RateLimitFloorEngagements int64 // GH-4391: distinct floor-engagement episodes across this controller's background scans
	APIErrors                 map[string]int64
	LabelCleanups             map[string]int64
	ApprovalPersistMisses     map[string]int64
	ApprovalSubmitFailures    int64
	IntentJudgeFailures       map[string]int64 // GH-4377: cause → count
	TokensConsumed            map[tokenKey]int64
	ExecutionCostUSD          map[string]float64
	ExecutionsByResult        map[execKey]int64

	// Poller dispatch/skip counters (TASK-293)
	PollerSkipped              map[pollerSkipKey]int64
	PollerDispatched           map[string]int64
	PollerDeferredScopeOverlap map[string]int64

	// Orphan PR registration (TASK-302)
	OrphanPRsRegistered map[string]int64 // trigger → count

	// Duplicate registration skips (GH-3828)
	DuplicateRegistrationsSkipped int64

	// Gauges
	ActivePRsByStage map[PRStage]int
	TotalActivePRs   int
	QueueDepth       int
	FailedQueueDepth int

	// UnsourcedLabeledIssues is the GH-4488 board-sourcing audit gauge: repo
	// → count of open pilot-labeled issues not covered by board sourcing.
	UnsourcedLabeledIssues map[string]int64

	IssuesShipped   int64 // TASK-392: distinct task_id reaching 'completed'
	IssuesAttempted int64 // TASK-392: distinct task_id with any execution

	// Per-model issue-level gauges (GH-4483): same semantics as
	// IssuesShipped/IssuesAttempted, keyed by model.
	IssuesShippedByModel   map[string]int64
	IssuesAttemptedByModel map[string]int64

	// Computed summaries
	SuccessRate           float64 // per-attempt; excludes rate_limited (TASK-392)
	IssueLevelSuccessRate float64 // unique-issue, deduped across retries (TASK-392)
	AvgPRTimeToMerge      time.Duration
	AvgCIWaitDuration     time.Duration
	AvgExecutionDuration  time.Duration
	APIErrorRate          float64 // errors per minute (5m window)

	// GH-4735: rolling-window headline cost/success numbers (see
	// Metrics.SetWindowStats doc comment for the refresh semantics).
	WindowDays                int
	WindowCostUSD             float64
	WindowCostPerDeliveredUSD float64
	WindowDeliveryRate        float64
	WindowAttemptSuccessRate  float64

	SnapshotAt time.Time
}

// TotalIssuesProcessed returns the sum of all issue results.
func (s MetricsSnapshot) TotalIssuesProcessed() int64 {
	var total int64
	for _, v := range s.IssuesProcessed {
		total += v
	}
	return total
}

// HistogramData contains raw duration samples for histogram computation.
type HistogramData struct {
	PRTimeToMerge      []time.Duration
	CIWaitDurations    []time.Duration
	ExecutionDurations []time.Duration

	// GH-4130 observes these; see Metrics.TimeToPRDurations et al.
	TimeToPRDurations     []time.Duration
	QueueWaitDurations    []time.Duration
	ApprovalWaitDurations []time.Duration
}

// HistogramSnapshot returns a copy of raw histogram samples.
func (m *Metrics) HistogramSnapshot() HistogramData {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return HistogramData{
		PRTimeToMerge:         copyDurations(m.PRTimeToMerge),
		CIWaitDurations:       copyDurations(m.CIWaitDurations),
		ExecutionDurations:    copyDurations(m.ExecutionDurations),
		TimeToPRDurations:     copyDurations(m.TimeToPRDurations),
		QueueWaitDurations:    copyDurations(m.QueueWaitDurations),
		ApprovalWaitDurations: copyDurations(m.ApprovalWaitDurations),
	}
}

func copyDurations(src []time.Duration) []time.Duration {
	if src == nil {
		return nil
	}
	dst := make([]time.Duration, len(src))
	copy(dst, src)
	return dst
}

// --- helpers ---

func copyStringIntMap(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyStageIntMap(src map[PRStage]int) map[PRStage]int {
	dst := make(map[PRStage]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func sumStageMap(m map[PRStage]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

func avgDuration(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range samples {
		sum += d
	}
	return sum / time.Duration(len(samples))
}

func copyTokenKeyMap(src map[tokenKey]int64) map[tokenKey]int64 {
	dst := make(map[tokenKey]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyStringFloatMap(src map[string]float64) map[string]float64 {
	dst := make(map[string]float64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyExecKeyMap(src map[execKey]int64) map[execKey]int64 {
	dst := make(map[execKey]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyPollerSkipKeyMap(src map[pollerSkipKey]int64) map[pollerSkipKey]int64 {
	dst := make(map[pollerSkipKey]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
