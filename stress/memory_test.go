package stress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestMemory_NoUnboundedGrowth verifies memory doesn't grow unbounded during processing.
func TestMemory_NoUnboundedGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}

	const (
		numIssues     = 100
		maxConcurrent = 10
		// Allow 50MB growth max - generous for test overhead
		maxGrowthBytes = 50 * 1024 * 1024
	)

	// Force GC and baseline
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var initialMem runtime.MemStats
	runtime.ReadMemStats(&initialMem)

	var processedCount int64

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Memory Test Issue %d", i+1),
			Body:      fmt.Sprintf("Body with some content for issue %d to simulate realistic payload size", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-time.Duration(numIssues-i) * time.Minute),
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
			// Simulate some work that allocates memory
			_ = make([]byte, 1024) // 1KB allocation per issue
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&processedCount, 1)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	// Track memory during processing
	metrics := NewMetrics()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go poller.Start(ctx)

	// Sample memory periodically
	done := make(chan struct{})
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

	// Wait for processing
	for atomic.LoadInt64(&processedCount) < int64(numIssues) {
		time.Sleep(50 * time.Millisecond)
	}

	close(done)
	cancel()
	poller.WaitForActive()

	// Force GC to get accurate final memory
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)

	memGrowth := int64(finalMem.Alloc) - int64(initialMem.Alloc)

	t.Logf("Memory stats:")
	t.Logf("  Initial: %d MB", initialMem.Alloc/1024/1024)
	t.Logf("  Peak: %d MB", metrics.GetPeakMemory()/1024/1024)
	t.Logf("  Final: %d MB", finalMem.Alloc/1024/1024)
	t.Logf("  Growth: %d bytes", memGrowth)
	t.Logf("  Issues processed: %d", atomic.LoadInt64(&processedCount))

	// Memory growth should be bounded
	if memGrowth > maxGrowthBytes {
		t.Errorf("Memory grew by %d bytes (%.2f MB), exceeds limit of %d bytes",
			memGrowth, float64(memGrowth)/1024/1024, maxGrowthBytes)
	}
}

// TestMemory_ProcessedMapGrowth verifies the processed map doesn't leak.
func TestMemory_ProcessedMapGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}

	const numIssues = 1000

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

	var processedCount int64

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		10*time.Millisecond,
		github.WithMaxConcurrent(20),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			atomic.AddInt64(&processedCount, 1)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go poller.Start(ctx)

	// Wait for all to be processed
	for atomic.LoadInt64(&processedCount) < int64(numIssues) {
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	poller.WaitForActive()

	// Verify processed count matches
	if count := poller.ProcessedCount(); count != numIssues {
		t.Errorf("ProcessedCount() = %d, want %d", count, numIssues)
	}

	// Reset should clear processed map
	poller.Reset()
	if count := poller.ProcessedCount(); count != 0 {
		t.Errorf("ProcessedCount() after Reset() = %d, want 0", count)
	}
}

// TestMemory_LargePayloads tests handling of issues with large bodies.
func TestMemory_LargePayloads(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}

	const (
		numIssues     = 20
		bodySize      = 100 * 1024 // 100KB per issue body
		maxConcurrent = 5
	)

	// Generate large issue bodies
	largeBody := make([]byte, bodySize)
	for i := range largeBody {
		largeBody[i] = byte('a' + i%26)
	}
	bodyStr := string(largeBody)

	issues := make([]*github.Issue, numIssues)
	for i := 0; i < numIssues; i++ {
		issues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Large Issue %d", i+1),
			Body:      bodyStr,
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

	var processedCount int64

	poller, err := github.NewPoller(
		client,
		"owner/repo",
		"pilot",
		100*time.Millisecond,
		github.WithMaxConcurrent(maxConcurrent),
		github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
			// Verify body is intact
			if len(issue.Body) != bodySize {
				return fmt.Errorf("body size mismatch: got %d, want %d", len(issue.Body), bodySize)
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&processedCount, 1)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create poller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go poller.Start(ctx)

	for atomic.LoadInt64(&processedCount) < int64(numIssues) {
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	poller.WaitForActive()

	processed := atomic.LoadInt64(&processedCount)
	if processed != int64(numIssues) {
		t.Errorf("Processed %d issues, want %d", processed, numIssues)
	}

	t.Logf("Successfully processed %d issues with %dKB payloads each", numIssues, bodySize/1024)
}

// TestMemory_RepeatedStartStop tests for memory leaks across multiple start/stop cycles.
func TestMemory_RepeatedStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}

	const (
		numCycles      = 10
		issuesPerCycle = 10
	)

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var initialMem runtime.MemStats
	runtime.ReadMemStats(&initialMem)

	baseIssues := make([]*github.Issue, issuesPerCycle)
	for i := 0; i < issuesPerCycle; i++ {
		baseIssues[i] = &github.Issue{
			Number:    i + 1,
			Title:     fmt.Sprintf("Issue %d", i+1),
			State:     "open",
			Labels:    []github.Label{{Name: "pilot"}},
			CreatedAt: time.Now(),
		}
	}

	for cycle := 0; cycle < numCycles; cycle++ {
		// Create fresh issues for each cycle
		issues := make([]*github.Issue, issuesPerCycle)
		for i := 0; i < issuesPerCycle; i++ {
			issues[i] = &github.Issue{
				Number:    (cycle * issuesPerCycle) + i + 1,
				Title:     fmt.Sprintf("Cycle %d Issue %d", cycle+1, i+1),
				State:     "open",
				Labels:    []github.Label{{Name: "pilot"}},
				CreatedAt: time.Now(),
			}
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(issues)
		}))

		client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

		var processedCount int64

		poller, err := github.NewPoller(
			client,
			"owner/repo",
			"pilot",
			10*time.Millisecond,
			github.WithMaxConcurrent(5),
			github.WithOnIssue(func(ctx context.Context, issue *github.Issue) error {
				atomic.AddInt64(&processedCount, 1)
				return nil
			}),
		)
		if err != nil {
			t.Fatalf("Cycle %d: failed to create poller: %v", cycle, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		go poller.Start(ctx)

		// Wait for processing
		deadline := time.After(5 * time.Second)
		for atomic.LoadInt64(&processedCount) < int64(issuesPerCycle) {
			select {
			case <-deadline:
				t.Fatalf("Cycle %d timed out", cycle)
			default:
				time.Sleep(10 * time.Millisecond)
			}
		}

		cancel()
		poller.WaitForActive()
		server.Close()

		// Force GC between cycles
		runtime.GC()
	}

	// Final cleanup
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)

	// Allow 10MB growth for test overhead
	maxGrowth := uint64(10 * 1024 * 1024)
	if finalMem.Alloc > initialMem.Alloc+maxGrowth {
		t.Errorf("Memory leak detected: initial=%dMB, final=%dMB, growth=%dMB",
			initialMem.Alloc/1024/1024, finalMem.Alloc/1024/1024,
			(finalMem.Alloc-initialMem.Alloc)/1024/1024)
	}

	t.Logf("Completed %d cycles, memory: initial=%dMB, final=%dMB",
		numCycles, initialMem.Alloc/1024/1024, finalMem.Alloc/1024/1024)
}
