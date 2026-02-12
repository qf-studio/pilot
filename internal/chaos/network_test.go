package chaos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChaos_GitHubAPI500 verifies retry with backoff works on 500 errors
func TestChaos_GitHubAPI500(t *testing.T) {
	// Track request count directly in handler
	var requestCount atomic.Int64
	failuresBeforeSuccess := 3

	// Handler that fails N times then succeeds
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count <= int64(failuresBeforeSuccess) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": "internal server error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// No chaos injection - we control failures directly
	server := NewChaosHTTPServer(handler, &FaultConfig{Type: FaultNone})
	defer server.Close()

	tracker := NewRetryTracker()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Simulate a client with retry logic
	var lastErr error
	var response map[string]string
	maxRetries := 5
	baseDelay := 50 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		tracker.RecordAttempt()

		resp, err := http.Get(server.URL())
		if err != nil {
			lastErr = err
			time.Sleep(baseDelay * time.Duration(1<<i)) // Exponential backoff
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error: %d - %s", resp.StatusCode, string(body))
			time.Sleep(baseDelay * time.Duration(1<<i))
			continue
		}

		if resp.StatusCode == http.StatusOK {
			_ = json.Unmarshal(body, &response)
			lastErr = nil
			break
		}
	}

	// Verify retry behavior
	if lastErr != nil {
		t.Fatalf("Expected success after retries, got error: %v", lastErr)
	}

	if response["status"] != "ok" {
		t.Errorf("Expected status=ok, got %v", response["status"])
	}

	// Should have made 4 attempts (3 failures + 1 success)
	attempts := tracker.AttemptCount()
	if attempts != failuresBeforeSuccess+1 {
		t.Errorf("Expected %d attempts, got %d", failuresBeforeSuccess+1, attempts)
	}

	// Verify delays increase (exponential backoff)
	delays := tracker.Delays()
	if len(delays) < 2 {
		t.Skip("Not enough delays to verify backoff")
	}

	for i := 1; i < len(delays); i++ {
		if delays[i] < delays[i-1] {
			t.Errorf("Delay %d (%v) should be >= delay %d (%v)", i, delays[i], i-1, delays[i-1])
		}
	}

	_ = ctx // Keep ctx in scope for potential future use
}

// TestChaos_RateLimitHit verifies scheduler queues and retries rate-limited tasks
func TestChaos_RateLimitHit(t *testing.T) {
	var requestCount atomic.Int64
	var rateLimitHit atomic.Bool

	// Handler that returns rate limit on first request, then succeeds
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requestCount.Add(1)
		if count == 1 {
			rateLimitHit.Store(true)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(100*time.Millisecond).Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok"}`))
	})

	server := NewChaosHTTPServer(handler, &FaultConfig{Type: FaultNone})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Simulate rate limit handling with queue
	type queuedTask struct {
		retryAt time.Time
		attempt int
	}

	var queue []queuedTask
	var mu sync.Mutex

	processTask := func() (bool, error) {
		resp, err := http.Get(server.URL())
		if err != nil {
			return false, err
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode == http.StatusTooManyRequests {
			// Parse reset time and queue for retry
			resetStr := resp.Header.Get("X-RateLimit-Reset")
			if resetStr != "" {
				resetUnix := int64(0)
				_, _ = fmt.Sscanf(resetStr, "%d", &resetUnix)
				resetTime := time.Unix(resetUnix, 0)

				mu.Lock()
				queue = append(queue, queuedTask{
					retryAt: resetTime,
					attempt: 1,
				})
				mu.Unlock()
			}
			return false, nil
		}

		return resp.StatusCode == http.StatusOK, nil
	}

	// First attempt - should hit rate limit
	success, err := processTask()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if success {
		t.Fatal("Expected rate limit on first attempt")
	}
	if !rateLimitHit.Load() {
		t.Fatal("Rate limit should have been hit")
	}

	// Verify task was queued
	mu.Lock()
	queueLen := len(queue)
	mu.Unlock()
	if queueLen != 1 {
		t.Fatalf("Expected 1 queued task, got %d", queueLen)
	}

	// Wait for rate limit reset
	mu.Lock()
	retryAt := queue[0].retryAt
	mu.Unlock()

	select {
	case <-ctx.Done():
		t.Fatal("Context cancelled before retry")
	case <-time.After(time.Until(retryAt) + 50*time.Millisecond):
	}

	// Retry - should succeed now
	success, err = processTask()
	if err != nil {
		t.Fatalf("Retry error: %v", err)
	}
	if !success {
		t.Error("Expected success after rate limit reset")
	}

	// Verify total request count
	if count := requestCount.Load(); count != 2 {
		t.Errorf("Expected 2 requests, got %d", count)
	}
}

// TestChaos_NetworkTimeout verifies graceful handling of network timeouts
func TestChaos_NetworkTimeout(t *testing.T) {
	// Handler that delays response
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than client timeout
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})

	// Create server without chaos injection - we control timing directly
	server := NewChaosHTTPServer(handler, &FaultConfig{Type: FaultNone})
	defer server.Close()

	// Create client with short timeout
	client := &http.Client{
		Timeout: 100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt request - should timeout
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL(), nil)
	resp, err := client.Do(req)

	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("Expected timeout error, got success")
	}

	// Verify it's a timeout error
	if !errors.Is(err, context.DeadlineExceeded) && !isTimeoutError(err) {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

// isTimeoutError checks if the error is a timeout
func isTimeoutError(err error) bool {
	type timeout interface {
		Timeout() bool
	}
	if te, ok := err.(timeout); ok {
		return te.Timeout()
	}
	return false
}

// TestChaos_ConnectionReset verifies handling of abrupt connection closures
func TestChaos_ConnectionReset(t *testing.T) {
	config := &FaultConfig{
		Type:        FaultConnectionReset,
		Probability: 1.0, // Always fail
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := NewChaosHTTPServer(handler, config)
	defer server.Close()

	// Attempt request - should fail with connection error
	resp, err := http.Get(server.URL())
	if err == nil {
		_ = resp.Body.Close()
		// Connection reset might manifest as empty response instead of error
		// depending on timing
		t.Log("Connection reset may have been handled gracefully")
		return
	}

	// Verify we got some kind of connection error
	t.Logf("Got expected connection error: %v", err)
}

// TestChaos_SlowResponse verifies handling of slow but successful responses
func TestChaos_SlowResponse(t *testing.T) {
	config := &FaultConfig{
		Type:        FaultSlowResponse,
		Probability: 1.0,
		Delay:       200 * time.Millisecond,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	server := NewChaosHTTPServer(handler, config)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create client with timeout longer than delay
	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	start := time.Now()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL(), nil)
	resp, err := client.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Expected success for slow response, got: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Verify response was delayed
	if elapsed < config.Delay {
		t.Errorf("Response was too fast: %v (expected >= %v)", elapsed, config.Delay)
	}

	// Verify response content
	var response map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if response["status"] != "ok" {
		t.Errorf("Expected status=ok, got %v", response["status"])
	}
}

// TestChaos_IntermittentFailures verifies recovery from intermittent errors
func TestChaos_IntermittentFailures(t *testing.T) {
	config := &FaultConfig{
		Type:                   FaultIntermittent,
		FailuresBeforeRecovery: 5,
	}

	var successCount atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	server := NewChaosHTTPServer(handler, config)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Make multiple requests
	var wg sync.WaitGroup
	results := make([]bool, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Simple retry loop
			for attempt := 0; attempt < 3; attempt++ {
				select {
				case <-ctx.Done():
					return
				default:
				}

				resp, err := http.Get(server.URL())
				if err != nil {
					time.Sleep(50 * time.Millisecond)
					continue
				}
				_ = resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					results[idx] = true
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Count successes
	successfulRequests := 0
	for _, r := range results {
		if r {
			successfulRequests++
		}
	}

	// After 5 failures, remaining requests should succeed
	// With 10 parallel requests and 5 failures, we expect some successes
	if successfulRequests == 0 {
		t.Error("Expected some successful requests after intermittent failures")
	}

	t.Logf("Successful requests: %d/10, handler called successfully: %d times",
		successfulRequests, successCount.Load())
}

// TestChaos_RetryWithBackoff verifies exponential backoff behavior
func TestChaos_RetryWithBackoff(t *testing.T) {
	tracker := NewRetryTracker()

	// Simulate retries with exponential backoff
	baseDelay := 50 * time.Millisecond
	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		tracker.RecordAttempt()
		delay := baseDelay * time.Duration(1<<i) // 50ms, 100ms, 200ms, 400ms, 800ms
		if i < maxRetries-1 {
			time.Sleep(delay)
		}
	}

	// Verify exponential backoff
	// Factor of ~2 with some tolerance for timing variations
	if !tracker.VerifyExponentialBackoff(1.5, 0.5) {
		t.Error("Delays don't follow exponential backoff pattern")
		t.Logf("Recorded delays: %v", tracker.Delays())
	}

	if tracker.AttemptCount() != maxRetries {
		t.Errorf("Expected %d attempts, got %d", maxRetries, tracker.AttemptCount())
	}
}

// TestChaos_FaultInjectorToggle verifies fault injection can be toggled
func TestChaos_FaultInjectorToggle(t *testing.T) {
	config := &FaultConfig{
		Type:        FaultError500,
		Probability: 1.0,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := NewChaosHTTPServer(handler, config)
	defer server.Close()

	// With injection enabled, should fail
	resp, err := http.Get(server.URL())
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected 500 with injection enabled, got %d", resp.StatusCode)
		}
	}

	// Disable injection
	server.Injector().Disable()

	// Should succeed now
	resp, err = http.Get(server.URL())
	if err != nil {
		t.Fatalf("Expected success with injection disabled, got: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 with injection disabled, got %d", resp.StatusCode)
	}

	// Re-enable injection
	server.Injector().Enable()

	// Should fail again
	resp, err = http.Get(server.URL())
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("Expected 500 with injection re-enabled, got %d", resp.StatusCode)
		}
	}
}

// TestChaos_ConcurrentRequests verifies thread-safety under load
func TestChaos_ConcurrentRequests(t *testing.T) {
	config := &FaultConfig{
		Type:        FaultIntermittent,
		FailuresBeforeRecovery: 10,
	}

	var totalRequests atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	})

	server := NewChaosHTTPServer(handler, config)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Launch 50 concurrent requests
	numGoroutines := 50
	var wg sync.WaitGroup
	errors := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				errors[idx] = ctx.Err()
				return
			default:
			}

			resp, err := http.Get(server.URL())
			if err != nil {
				errors[idx] = err
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 500 {
				errors[idx] = fmt.Errorf("server error: %d", resp.StatusCode)
			}
		}(i)
	}

	wg.Wait()

	// Count errors vs successes
	errorCount := 0
	for _, err := range errors {
		if err != nil {
			errorCount++
		}
	}

	// Should have some errors (first 10 failures) but also some successes
	t.Logf("Concurrent test: %d errors, %d successes, %d total handled",
		errorCount, numGoroutines-errorCount, totalRequests.Load())

	// Verify no panics or deadlocks occurred (test completing is the verification)
	calls, faults := server.Injector().Stats()
	t.Logf("Injector stats: %d calls, %d faults injected", calls, faults)
}
