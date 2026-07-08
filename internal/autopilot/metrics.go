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
	IssuesProcessed       map[string]int64 // result → count (success, failed, rate_limited)
	PRsMerged             int64
	PRsFailed             int64
	PRsConflicting        int64
	CircuitBreakerTrips   int64
	APIErrors             map[string]int64 // endpoint → count
	LabelCleanups         map[string]int64 // label → count
	ApprovalPersistMisses map[string]int64 // kind → count (request_id, decision)
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

	// Issue-level outcome gauges (TASK-392): unique task_id counts, deduped
	// across retry attempts. Contrast with IssuesProcessed, which is
	// per-attempt (a task retried twice before shipping contributes 3 events).
	IssuesShipped   int64 // distinct task_id that reached status='completed'
	IssuesAttempted int64 // distinct task_id with at least one execution

	// Histograms (stored as recent samples for summary stats)
	PRTimeToMerge      []time.Duration
	CIWaitDurations    []time.Duration
	ExecutionDurations []time.Duration

	// Timestamps for rate calculation
	apiErrorTimes []time.Time

	// Maximum samples to keep for histograms
	maxSamples int
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	return &Metrics{
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
		PRTimeToMerge:              make([]time.Duration, 0, 100),
		CIWaitDurations:            make([]time.Duration, 0, 100),
		ExecutionDurations:         make([]time.Duration, 0, 100),
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

// RecordPRMerged increments the merged PR counter.
func (m *Metrics) RecordPRMerged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsMerged++
}

// RecordPRFailed increments the failed PR counter.
func (m *Metrics) RecordPRFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsFailed++
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

// HydratePRsMerged adds a store-lifetime baseline to the merged-PR counter.
// See HydrateExecutions (GH-4093, restores pilot_prs_merged_total across
// restarts from the durable execution_events ledger).
func (m *Metrics) HydratePRsMerged(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsMerged += n
}

// HydratePRsFailed adds a store-lifetime baseline to the failed-PR counter.
// See HydrateExecutions (GH-4093).
func (m *Metrics) HydratePRsFailed(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PRsFailed += n
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

// SetIssueLevelCounts updates the issue-level outcome gauges from a
// store.IssueLevelCounts query (unique task_id, deduped across retries).
// TASK-392.
func (m *Metrics) SetIssueLevelCounts(shipped, attempted int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesShipped = shipped
	m.IssuesAttempted = attempted
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

// --- Read accessors ---

// Snapshot returns a point-in-time copy of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snap := MetricsSnapshot{
		IssuesProcessed:               copyStringIntMap(m.IssuesProcessed),
		PRsMerged:                     m.PRsMerged,
		PRsFailed:                     m.PRsFailed,
		PRsConflicting:                m.PRsConflicting,
		CircuitBreakerTrips:           m.CircuitBreakerTrips,
		APIErrors:                     copyStringIntMap(m.APIErrors),
		LabelCleanups:                 copyStringIntMap(m.LabelCleanups),
		ApprovalPersistMisses:         copyStringIntMap(m.ApprovalPersistMisses),
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
		IssuesShipped:                 m.IssuesShipped,
		IssuesAttempted:               m.IssuesAttempted,
		TotalActivePRs:                sumStageMap(m.ActivePRsByStage),
		AvgPRTimeToMerge:              avgDuration(m.PRTimeToMerge),
		AvgCIWaitDuration:             avgDuration(m.CIWaitDurations),
		AvgExecutionDuration:          avgDuration(m.ExecutionDurations),
		APIErrorRate:                  m.apiErrorRate(),
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
	IssuesProcessed       map[string]int64
	PRsMerged             int64
	PRsFailed             int64
	PRsConflicting        int64
	CircuitBreakerTrips   int64
	APIErrors             map[string]int64
	LabelCleanups         map[string]int64
	ApprovalPersistMisses map[string]int64
	TokensConsumed        map[tokenKey]int64
	ExecutionCostUSD      map[string]float64
	ExecutionsByResult    map[execKey]int64

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
	IssuesShipped    int64 // TASK-392: distinct task_id reaching 'completed'
	IssuesAttempted  int64 // TASK-392: distinct task_id with any execution

	// Computed summaries
	SuccessRate           float64 // per-attempt; excludes rate_limited (TASK-392)
	IssueLevelSuccessRate float64 // unique-issue, deduped across retries (TASK-392)
	AvgPRTimeToMerge      time.Duration
	AvgCIWaitDuration     time.Duration
	AvgExecutionDuration  time.Duration
	APIErrorRate          float64 // errors per minute (5m window)

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
}

// HistogramSnapshot returns a copy of raw histogram samples.
func (m *Metrics) HistogramSnapshot() HistogramData {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return HistogramData{
		PRTimeToMerge:      copyDurations(m.PRTimeToMerge),
		CIWaitDurations:    copyDurations(m.CIWaitDurations),
		ExecutionDurations: copyDurations(m.ExecutionDurations),
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
