package autopilot

import (
	"sync"
	"time"
)

// Metrics collects autopilot operational metrics.
// All methods are goroutine-safe.
type Metrics struct {
	mu sync.RWMutex

	// Counters
	IssuesProcessed     map[string]int64 // result → count (success, failed, rate_limited)
	PRsMerged           int64
	PRsFailed           int64
	PRsConflicting      int64
	CircuitBreakerTrips int64
	APIErrors           map[string]int64 // endpoint → count
	LabelCleanups       map[string]int64 // label → count

	// Gauges (point-in-time values)
	ActivePRsByStage map[PRStage]int
	QueueDepth       int // issues with `pilot` label, no `pilot-in-progress`
	FailedQueueDepth int // issues with `pilot-failed`

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
		IssuesProcessed:    make(map[string]int64),
		APIErrors:          make(map[string]int64),
		LabelCleanups:      make(map[string]int64),
		ActivePRsByStage:   make(map[PRStage]int),
		PRTimeToMerge:      make([]time.Duration, 0, 100),
		CIWaitDurations:    make([]time.Duration, 0, 100),
		ExecutionDurations: make([]time.Duration, 0, 100),
		apiErrorTimes:      make([]time.Time, 0, 100),
		maxSamples:         1000,
	}
}

// --- Counter increments ---

// RecordIssueProcessed increments the processed issue counter by result.
func (m *Metrics) RecordIssueProcessed(result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.IssuesProcessed[result]++
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
		IssuesProcessed:      copyStringIntMap(m.IssuesProcessed),
		PRsMerged:            m.PRsMerged,
		PRsFailed:            m.PRsFailed,
		PRsConflicting:       m.PRsConflicting,
		CircuitBreakerTrips:  m.CircuitBreakerTrips,
		APIErrors:            copyStringIntMap(m.APIErrors),
		LabelCleanups:        copyStringIntMap(m.LabelCleanups),
		ActivePRsByStage:     copyStageIntMap(m.ActivePRsByStage),
		QueueDepth:           m.QueueDepth,
		FailedQueueDepth:     m.FailedQueueDepth,
		TotalActivePRs:       sumStageMap(m.ActivePRsByStage),
		AvgPRTimeToMerge:     avgDuration(m.PRTimeToMerge),
		AvgCIWaitDuration:    avgDuration(m.CIWaitDurations),
		AvgExecutionDuration: avgDuration(m.ExecutionDurations),
		APIErrorRate:         m.apiErrorRate(),
		SnapshotAt:           time.Now(),
	}

	// Calculate success rate
	total := int64(0)
	for _, v := range m.IssuesProcessed {
		total += v
	}
	if total > 0 {
		snap.SuccessRate = float64(m.IssuesProcessed["success"]) / float64(total)
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
	IssuesProcessed     map[string]int64
	PRsMerged           int64
	PRsFailed           int64
	PRsConflicting      int64
	CircuitBreakerTrips int64
	APIErrors           map[string]int64
	LabelCleanups       map[string]int64

	// Gauges
	ActivePRsByStage map[PRStage]int
	TotalActivePRs   int
	QueueDepth       int
	FailedQueueDepth int

	// Computed summaries
	SuccessRate          float64
	AvgPRTimeToMerge     time.Duration
	AvgCIWaitDuration    time.Duration
	AvgExecutionDuration time.Duration
	APIErrorRate         float64 // errors per minute (5m window)

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
