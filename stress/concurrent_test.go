package stress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestStress_20ConcurrentIssues verifies that Pilot can handle 20 concurrent issues
// without deadlocks, with proper semaphore enforcement, and stable resource usage.
func TestStress_20ConcurrentIssues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	const (
		numIssues       = 20
		maxConcurrent   = 5
		processingDelay = 50 * time.Millisecond // Simulate work
		testTimeout     = 2 * time.Minute
	)

	// Track concurrent execution to verify semaphore
	var (
		currentlyProcessing int64
		peakConcurrent      int64
		processedCount      int64
		processedSet        = make(map[int]bool)
		mu                  sync.Mutex
	)

	// Create mock issues - they get pilot-done label after processing
	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Stress Test Issue %d", i+1),
			Body:      fmt.Sprintf("Issue body for stress test %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-time.Duration(numIssues-i) * time.Hour), // Oldest first
		}
	}

	// Mock GitHub API server - returns issues with updated labels
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			// Return issues with updated status labels
			mu.Lock()
			responseIssues := make([]*github.Issue, 0, numIssues)
			for _, issue := range issues {
				issueCopy := *issue
				if processedSet[issue.Number] {
					// Add pilot-done label to processed issues
					issueCopy.Labels = []github.Label{{Name: "pilot"}, {Name: github.LabelDone}}
				}
				responseIssues = append(responseIssues, &issueCopy)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(responseIssues)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Create poller with concurrency limit
	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		100*time.Millisecond,
		github.WithMaxConcurrent(maxConcurrent),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			// Track concurrent processing
			current := atomic.AddInt64(&currentlyProcessing, 1)
			mu.Lock()
			if current > peakConcurrent {
				peakConcurrent = current
			}
			mu.Unlock()

			// Simulate processing work
			time.Sleep(processingDelay)

			atomic.AddInt64(&currentlyProcessing, -1)

			// Track unique issues processed
			mu.Lock()
			if !processedSet[issue.Number] {
				processedSet[issue.Number] = true
				atomic.AddInt64(&processedCount, 1)
			}
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	// Start metrics collection
	metrics := NewMetrics()
	done := make(chan struct{})

	// Sample metrics periodically
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				metrics.SampleMemoryAndGoroutines()
			}
		}
	}()

	// Run the poller
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go poller.Start(ctx)

	// Wait for all issues to be processed
	deadline := time.After(testTimeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("Test timed out after %v. Processed: %d/%d issues",
				testTimeout, atomic.LoadInt64(&processedCount), numIssues)
		case <-ticker.C:
			if atomic.LoadInt64(&processedCount) >= int64(numIssues) {
				goto done
			}
		}
	}

done:
	close(done)
	cancel()
	poller.WaitForActive()
	metrics.Finalize()

	// Verify all issues were processed
	finalProcessed := atomic.LoadInt64(&processedCount)
	if finalProcessed != int64(numIssues) {
		t.Errorf("Processed %d issues, want %d", finalProcessed, numIssues)
	}

	// Verify semaphore correctly limited concurrency
	mu.Lock()
	actualPeak := peakConcurrent
	mu.Unlock()

	if actualPeak > int64(maxConcurrent) {
		t.Errorf("Peak concurrent %d exceeded max_concurrent %d - semaphore not working",
			actualPeak, maxConcurrent)
	}
	if actualPeak == 0 {
		t.Error("Peak concurrent was 0 - no concurrent processing occurred")
	}

	t.Logf("Stress test results:")
	t.Logf("  Issues processed: %d/%d", finalProcessed, numIssues)
	t.Logf("  Peak concurrent: %d (limit: %d)", actualPeak, maxConcurrent)
	t.Logf("  Duration: %v", metrics.Duration())
	t.Logf("  Rate: %.1f issues/minute", metrics.IssuesPerMinute())
	t.Logf("  Peak goroutines: %d", metrics.PeakGoroutines)
	t.Logf("  Memory growth: %d bytes", metrics.MemoryGrowth())
}

// TestStress_SemaphoreEnforcement specifically verifies the semaphore limits parallelism.
func TestStress_SemaphoreEnforcement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	const (
		numIssues     = 30
		maxConcurrent = 3
		holdTime      = 100 * time.Millisecond
	)

	var (
		currentlyProcessing int64
		violations          int64
	)

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Issue %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		50*time.Millisecond,
		github.WithMaxConcurrent(maxConcurrent),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			current := atomic.AddInt64(&currentlyProcessing, 1)

			// Check for semaphore violation
			if current > int64(maxConcurrent) {
				atomic.AddInt64(&violations, 1)
			}

			// Hold the semaphore slot
			time.Sleep(holdTime)

			atomic.AddInt64(&currentlyProcessing, -1)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go poller.Start(ctx)

	// Wait for completion
	time.Sleep(time.Duration(numIssues/maxConcurrent+1) * holdTime * 2)

	cancel()
	poller.WaitForActive()

	if v := atomic.LoadInt64(&violations); v > 0 {
		t.Errorf("Semaphore violated %d times - concurrency exceeded limit", v)
	}
}

// TestStress_NoDeadlock verifies no deadlock occurs under concurrent load.
func TestStress_NoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	const (
		numIssues     = 50
		maxConcurrent = 10
		timeout       = 30 * time.Second
	)

	var (
		processedSet = make(map[int]bool)
		mu           sync.Mutex
	)

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Issue %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return issues with pilot-done label if already processed
		mu.Lock()
		responseIssues := make([]*github.Issue, 0, numIssues)
		for _, issue := range issues {
			issueCopy := *issue
			if processedSet[issue.Number] {
				issueCopy.Labels = []github.Label{{Name: "pilot"}, {Name: github.LabelDone}}
			}
			responseIssues = append(responseIssues, &issueCopy)
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(responseIssues)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		10*time.Millisecond,
		github.WithMaxConcurrent(maxConcurrent),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			// Minimal processing to maximize throughput
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			processedSet[issue.Number] = true
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	completed := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(completed)
	}()

	// Wait for all unique issues to be processed
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			cancel()
			poller.WaitForActive()
			mu.Lock()
			processed := len(processedSet)
			mu.Unlock()
			if processed < numIssues {
				t.Errorf("Possible deadlock: only processed %d/%d issues in %v",
					processed, numIssues, timeout)
			}
			return
		default:
			mu.Lock()
			processed := len(processedSet)
			mu.Unlock()
			if processed >= numIssues {
				goto done
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

done:
	cancel()
	poller.WaitForActive()

	mu.Lock()
	processed := len(processedSet)
	mu.Unlock()
	if processed != numIssues {
		t.Errorf("Processed %d/%d issues", processed, numIssues)
	}
}

// TestStress_GoroutineStability verifies goroutine count stabilizes after processing.
func TestStress_GoroutineStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	// Force GC and get baseline
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	const (
		numIssues     = 25
		maxConcurrent = 5
	)

	var processedCount int64

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Issue %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute),
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		50*time.Millisecond,
		github.WithMaxConcurrent(maxConcurrent),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&processedCount, 1)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	go poller.Start(ctx)

	// Wait for all issues to process
	for atomic.LoadInt64(&processedCount) < int64(numIssues) {
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	poller.WaitForActive()

	// Allow goroutines to clean up
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	finalGoroutines := runtime.NumGoroutine()

	// Allow some tolerance (test runner goroutines, etc.)
	tolerance := 5
	if finalGoroutines > baselineGoroutines+tolerance {
		t.Errorf("Goroutine leak: baseline=%d, final=%d, leaked=%d",
			baselineGoroutines, finalGoroutines, finalGoroutines-baselineGoroutines)
	}

	t.Logf("Goroutines: baseline=%d, final=%d", baselineGoroutines, finalGoroutines)
}

// TestStress_RapidCancellation tests graceful shutdown under cancellation.
func TestStress_RapidCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	const numIssues = 100

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Issue %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now(),
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		10*time.Millisecond,
		github.WithMaxConcurrent(10),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			// Long-running task
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return nil
			}
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Let some issues start processing
	time.Sleep(100 * time.Millisecond)

	// Cancel abruptly
	cancel()

	// Should complete within reasonable time
	select {
	case <-done:
		// Good - stopped gracefully
	case <-time.After(10 * time.Second):
		t.Error("Poller did not stop gracefully after cancellation")
	}

	poller.WaitForActive()
}
