package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetrier_Evaluate_RateLimit(t *testing.T) {
	config := &RetryConfig{
		Enabled: true,
		RateLimit: &RetryStrategy{
			MaxAttempts:       3,
			InitialBackoff:    30 * time.Second,
			BackoffMultiplier: 2.0,
		},
	}
	retrier := NewRetrier(config)

	err := &ClaudeCodeError{Type: ErrorTypeRateLimit, Message: "rate limit"}

	// First attempt should retry with 30s backoff
	decision := retrier.Evaluate(err, 0, 10*time.Minute)
	if !decision.ShouldRetry {
		t.Error("Expected ShouldRetry=true for first rate limit attempt")
	}
	if decision.BackoffDuration != 30*time.Second {
		t.Errorf("Expected 30s backoff, got %v", decision.BackoffDuration)
	}

	// Second attempt should retry with 60s backoff
	decision = retrier.Evaluate(err, 1, 10*time.Minute)
	if !decision.ShouldRetry {
		t.Error("Expected ShouldRetry=true for second rate limit attempt")
	}
	if decision.BackoffDuration != 60*time.Second {
		t.Errorf("Expected 60s backoff, got %v", decision.BackoffDuration)
	}

	// Third attempt (attempt=2) should retry with 120s backoff
	decision = retrier.Evaluate(err, 2, 10*time.Minute)
	if !decision.ShouldRetry {
		t.Error("Expected ShouldRetry=true for third rate limit attempt")
	}
	if decision.BackoffDuration != 120*time.Second {
		t.Errorf("Expected 120s backoff, got %v", decision.BackoffDuration)
	}

	// Fourth attempt (attempt=3) should NOT retry - max reached
	decision = retrier.Evaluate(err, 3, 10*time.Minute)
	if decision.ShouldRetry {
		t.Error("Expected ShouldRetry=false after max attempts")
	}
}

func TestRetrier_Evaluate_APIError(t *testing.T) {
	config := &RetryConfig{
		Enabled: true,
		APIError: &RetryStrategy{
			MaxAttempts:       3,
			InitialBackoff:    5 * time.Second,
			BackoffMultiplier: 2.0,
		},
	}
	retrier := NewRetrier(config)

	err := &ClaudeCodeError{Type: ErrorTypeAPIError, Message: "api error"}

	decision := retrier.Evaluate(err, 0, 10*time.Minute)
	if !decision.ShouldRetry {
		t.Error("Expected ShouldRetry=true for API error")
	}
	if decision.BackoffDuration != 5*time.Second {
		t.Errorf("Expected 5s backoff, got %v", decision.BackoffDuration)
	}
}

func TestRetrier_Evaluate_Timeout(t *testing.T) {
	config := &RetryConfig{
		Enabled: true,
		Timeout: &RetryStrategy{
			MaxAttempts:       2,
			ExtendTimeout:     true,
			TimeoutMultiplier: 1.5,
		},
	}
	retrier := NewRetrier(config)

	err := &ClaudeCodeError{Type: ErrorTypeTimeout, Message: "timeout"}
	originalTimeout := 10 * time.Minute

	decision := retrier.Evaluate(err, 0, originalTimeout)
	if !decision.ShouldRetry {
		t.Error("Expected ShouldRetry=true for timeout")
	}
	if decision.ExtendedTimeout != 15*time.Minute {
		t.Errorf("Expected 15m extended timeout, got %v", decision.ExtendedTimeout)
	}

	// Second attempt should NOT retry - max reached
	decision = retrier.Evaluate(err, 2, originalTimeout)
	if decision.ShouldRetry {
		t.Error("Expected ShouldRetry=false after max attempts")
	}
}

func TestRetrier_Evaluate_InvalidConfig_FailFast(t *testing.T) {
	config := &RetryConfig{
		Enabled: true,
		// No InvalidConfig strategy = fail fast
	}
	retrier := NewRetrier(config)

	err := &ClaudeCodeError{Type: ErrorTypeInvalidConfig, Message: "invalid config"}

	decision := retrier.Evaluate(err, 0, 10*time.Minute)
	if decision.ShouldRetry {
		t.Error("Expected ShouldRetry=false for invalid config (fail fast)")
	}
	if decision.Reason != "invalid_config errors are not retryable (fail fast)" {
		t.Errorf("Unexpected reason: %s", decision.Reason)
	}
}

func TestRetrier_Evaluate_Disabled(t *testing.T) {
	config := &RetryConfig{
		Enabled: false, // Disabled
		RateLimit: &RetryStrategy{
			MaxAttempts: 3,
		},
	}
	retrier := NewRetrier(config)

	err := &ClaudeCodeError{Type: ErrorTypeRateLimit, Message: "rate limit"}

	decision := retrier.Evaluate(err, 0, 10*time.Minute)
	if decision.ShouldRetry {
		t.Error("Expected ShouldRetry=false when retry is disabled")
	}
}

func TestRetrier_Evaluate_NonClaudeCodeError(t *testing.T) {
	config := &RetryConfig{
		Enabled: true,
		RateLimit: &RetryStrategy{
			MaxAttempts: 3,
		},
	}
	retrier := NewRetrier(config)

	err := errors.New("some generic error")

	decision := retrier.Evaluate(err, 0, 10*time.Minute)
	if decision.ShouldRetry {
		t.Error("Expected ShouldRetry=false for non-ClaudeCodeError")
	}
}

func TestRetrier_Sleep_ContextCancellation(t *testing.T) {
	retrier := NewRetrier(DefaultRetryConfig())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := retrier.Sleep(ctx, 10*time.Second)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestRetrier_Sleep_Success(t *testing.T) {
	retrier := NewRetrier(DefaultRetryConfig())

	ctx := context.Background()
	start := time.Now()

	err := retrier.Sleep(ctx, 50*time.Millisecond)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Errorf("Sleep was too short: %v", elapsed)
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	config := DefaultRetryConfig()

	if config.Enabled {
		t.Error("Expected Enabled=false by default")
	}
	if config.RateLimit == nil {
		t.Error("Expected RateLimit to be configured")
	}
	if config.APIError == nil {
		t.Error("Expected APIError to be configured")
	}
	if config.Timeout == nil {
		t.Error("Expected Timeout to be configured")
	}
}
