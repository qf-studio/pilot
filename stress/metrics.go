// Package stress provides stress testing utilities for Pilot.
package stress

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics collects stress test measurements.
type Metrics struct {
	// Issue processing
	IssuesProcessed   int64
	IssuesSucceeded   int64
	IssuesFailed      int64
	ProcessingTimeSum int64 // nanoseconds

	// Concurrency
	PeakGoroutines    int
	CurrentGoroutines int
	PeakConcurrent    int64
	currentConcurrent int64

	// Memory
	InitialMemory uint64
	PeakMemory    uint64
	FinalMemory   uint64

	// Timing
	StartTime time.Time
	EndTime   time.Time

	mu sync.Mutex
}

// NewMetrics creates a new metrics collector with initial memory snapshot.
func NewMetrics() *Metrics {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return &Metrics{
		InitialMemory:     memStats.Alloc,
		PeakMemory:        memStats.Alloc,
		StartTime:         time.Now(),
		PeakGoroutines:    runtime.NumGoroutine(),
		CurrentGoroutines: runtime.NumGoroutine(),
	}
}

// RecordIssueStart marks the beginning of issue processing and tracks concurrency.
func (m *Metrics) RecordIssueStart() {
	current := atomic.AddInt64(&m.currentConcurrent, 1)

	m.mu.Lock()
	if current > m.PeakConcurrent {
		m.PeakConcurrent = current
	}
	m.mu.Unlock()
}

// RecordIssueComplete marks successful completion of an issue.
func (m *Metrics) RecordIssueComplete(duration time.Duration) {
	atomic.AddInt64(&m.IssuesProcessed, 1)
	atomic.AddInt64(&m.IssuesSucceeded, 1)
	atomic.AddInt64(&m.ProcessingTimeSum, int64(duration))
	atomic.AddInt64(&m.currentConcurrent, -1)
}

// RecordIssueFailed marks failed issue processing.
func (m *Metrics) RecordIssueFailed(duration time.Duration) {
	atomic.AddInt64(&m.IssuesProcessed, 1)
	atomic.AddInt64(&m.IssuesFailed, 1)
	atomic.AddInt64(&m.ProcessingTimeSum, int64(duration))
	atomic.AddInt64(&m.currentConcurrent, -1)
}

// SampleMemoryAndGoroutines takes a snapshot of memory and goroutine count.
// Call periodically during the test.
func (m *Metrics) SampleMemoryAndGoroutines() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	goroutines := runtime.NumGoroutine()

	m.mu.Lock()
	defer m.mu.Unlock()

	if memStats.Alloc > m.PeakMemory {
		m.PeakMemory = memStats.Alloc
	}
	if goroutines > m.PeakGoroutines {
		m.PeakGoroutines = goroutines
	}
	m.CurrentGoroutines = goroutines
}

// Finalize captures final metrics snapshot.
func (m *Metrics) Finalize() {
	m.EndTime = time.Now()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	m.mu.Lock()
	m.FinalMemory = memStats.Alloc
	m.CurrentGoroutines = runtime.NumGoroutine()
	m.mu.Unlock()
}

// Duration returns the total test duration.
func (m *Metrics) Duration() time.Duration {
	if m.EndTime.IsZero() {
		return time.Since(m.StartTime)
	}
	return m.EndTime.Sub(m.StartTime)
}

// IssuesPerMinute returns the processing rate.
func (m *Metrics) IssuesPerMinute() float64 {
	duration := m.Duration()
	if duration == 0 {
		return 0
	}
	processed := atomic.LoadInt64(&m.IssuesProcessed)
	return float64(processed) / duration.Minutes()
}

// AverageProcessingTime returns average time per issue.
func (m *Metrics) AverageProcessingTime() time.Duration {
	processed := atomic.LoadInt64(&m.IssuesProcessed)
	if processed == 0 {
		return 0
	}
	sum := atomic.LoadInt64(&m.ProcessingTimeSum)
	return time.Duration(sum / processed)
}

// MemoryGrowth returns bytes allocated since start.
func (m *Metrics) MemoryGrowth() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.FinalMemory > m.InitialMemory {
		return int64(m.FinalMemory - m.InitialMemory)
	}
	return 0
}

// GoroutineGrowth returns goroutines added since start.
func (m *Metrics) GoroutineGrowth() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.CurrentGoroutines - runtime.NumGoroutine()
}

// GetPeakMemory returns the peak memory usage (thread-safe).
func (m *Metrics) GetPeakMemory() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.PeakMemory
}

// GetCurrentGoroutines returns current goroutine count (thread-safe).
func (m *Metrics) GetCurrentGoroutines() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CurrentGoroutines
}

// GetPeakGoroutines returns peak goroutine count (thread-safe).
func (m *Metrics) GetPeakGoroutines() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.PeakGoroutines
}
