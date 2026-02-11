package github

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "success", nil
	}, DefaultRetryOptions())

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got: %s", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got: %d", calls)
	}
}

func TestWithRetry_SuccessAfterRetries(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("API error (status 503): service unavailable")
		}
		return "success", nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond, // Fast for tests
		MaxDelay:   10 * time.Millisecond,
	})

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got: %s", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got: %d", calls)
	}
}

func TestWithRetry_ExhaustsRetries(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", errors.New("API error (status 500): internal server error")
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err == nil {
		t.Error("expected error after exhausting retries")
	}
	// Initial attempt + 3 retries = 4 total calls
	if calls != 4 {
		t.Errorf("expected 4 calls (1 + 3 retries), got: %d", calls)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", errors.New("API error (status 404): not found")
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err == nil {
		t.Error("expected error for 404")
	}
	// Should not retry 404
	if calls != 1 {
		t.Errorf("expected 1 call (no retries for 404), got: %d", calls)
	}
}

func TestWithRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	// Cancel after first failure
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := WithRetry(ctx, func() (string, error) {
		calls++
		return "", errors.New("API error (status 503): service unavailable")
	}, RetryOptions{
		MaxRetries: 10,
		BaseDelay:  50 * time.Millisecond, // Long delay to allow cancellation
		MaxDelay:   100 * time.Millisecond,
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if calls > 2 {
		t.Errorf("expected at most 2 calls before cancellation, got: %d", calls)
	}
}

func TestWithRetryVoid_Success(t *testing.T) {
	calls := 0
	err := WithRetryVoid(context.Background(), func() error {
		calls++
		return nil
	}, DefaultRetryOptions())

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got: %d", calls)
	}
}

func TestWithRetryVoid_RetriesOnError(t *testing.T) {
	calls := 0
	err := WithRetryVoid(context.Background(), func() error {
		calls++
		if calls < 2 {
			return errors.New("API error (status 502): bad gateway")
		}
		return nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got: %d", calls)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil error", nil, false},
		{"429 rate limit", errors.New("API error (status 429): rate limited"), true},
		{"500 server error", errors.New("API error (status 500): internal error"), true},
		{"502 bad gateway", errors.New("API error (status 502): bad gateway"), true},
		{"503 unavailable", errors.New("API error (status 503): service unavailable"), true},
		{"504 timeout", errors.New("API error (status 504): gateway timeout"), true},
		{"400 bad request", errors.New("API error (status 400): bad request"), false},
		{"401 unauthorized", errors.New("API error (status 401): unauthorized"), false},
		{"403 forbidden", errors.New("API error (status 403): forbidden"), false},
		{"404 not found", errors.New("API error (status 404): not found"), false},
		{"422 unprocessable", errors.New("API error (status 422): unprocessable entity"), false},
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"connection reset", errors.New("connection reset by peer"), true},
		{"no such host", errors.New("dial tcp: no such host"), true},
		{"i/o timeout", errors.New("i/o timeout"), true},
		{"context deadline", errors.New("context deadline exceeded"), true},
		{"generic error", errors.New("something went wrong"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.retryable {
				t.Errorf("isRetryableError(%q) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestExtractRetryAfter(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected time.Duration
	}{
		{"nil error", nil, 0},
		{"no retry-after", errors.New("some error"), 0},
		{"429 default", errors.New("API error (status 429): rate limited"), 60 * time.Second},
		{"retry-after seconds", errors.New("retry after 30 seconds"), 30 * time.Second},
		{"Retry-After header", errors.New("Retry-After: 45"), 45 * time.Second},
		{"rate limit message", errors.New("rate limit exceeded, retry in 120 seconds"), 120 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRetryAfter(tt.err)
			if got != tt.expected {
				t.Errorf("extractRetryAfter(%q) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestDefaultRetryOptions(t *testing.T) {
	opts := DefaultRetryOptions()

	if opts.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", opts.MaxRetries)
	}
	if opts.BaseDelay != 1*time.Second {
		t.Errorf("expected BaseDelay=1s, got %v", opts.BaseDelay)
	}
	if opts.MaxDelay != 30*time.Second {
		t.Errorf("expected MaxDelay=30s, got %v", opts.MaxDelay)
	}
}

func TestWithRetry_ExponentialBackoff(t *testing.T) {
	// Test that delays increase exponentially (approximately)
	delays := []time.Duration{}
	lastCall := time.Now()

	calls := 0
	_, _ = WithRetry(context.Background(), func() (string, error) {
		now := time.Now()
		if calls > 0 {
			delays = append(delays, now.Sub(lastCall))
		}
		lastCall = now
		calls++
		if calls <= 3 {
			return "", errors.New("API error (status 500): error")
		}
		return "done", nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
	})

	if len(delays) != 3 {
		t.Fatalf("expected 3 delays, got %d", len(delays))
	}

	// Verify exponential growth: 10ms, 20ms, 40ms (with some tolerance)
	expectedDelays := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	tolerance := 5 * time.Millisecond

	for i, expected := range expectedDelays {
		if delays[i] < expected-tolerance || delays[i] > expected+2*tolerance {
			t.Errorf("delay[%d] = %v, expected ~%v (tolerance %v)", i, delays[i], expected, tolerance)
		}
	}
}

func TestWithRetry_MaxDelayRespected(t *testing.T) {
	calls := 0
	lastCall := time.Now()
	var maxDelayObserved time.Duration

	_, _ = WithRetry(context.Background(), func() (string, error) {
		now := time.Now()
		if calls > 0 {
			delay := now.Sub(lastCall)
			if delay > maxDelayObserved {
				maxDelayObserved = delay
			}
		}
		lastCall = now
		calls++
		if calls <= 5 {
			return "", errors.New("API error (status 500): error")
		}
		return "done", nil
	}, RetryOptions{
		MaxRetries: 5,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   25 * time.Millisecond, // Cap at 25ms
	})

	// Without cap: 10, 20, 40, 80, 160ms
	// With cap at 25ms: 10, 20, 25, 25, 25ms
	// Max should be around 25ms, not 160ms
	if maxDelayObserved > 35*time.Millisecond {
		t.Errorf("max delay %v exceeded expected cap of ~25ms", maxDelayObserved)
	}
}
