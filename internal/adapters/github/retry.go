package github

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RetryOptions configures retry behavior
type RetryOptions struct {
	MaxRetries int           // Maximum number of retries (default: 3)
	BaseDelay  time.Duration // Initial delay between retries (default: 1s)
	MaxDelay   time.Duration // Maximum delay between retries (default: 30s)
}

// DefaultRetryOptions returns sensible defaults for retry behavior
func DefaultRetryOptions() RetryOptions {
	return RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
	}
}

// WithRetry executes an operation with exponential backoff retry.
// It respects context cancellation and GitHub's Retry-After header.
func WithRetry[T any](ctx context.Context, op func() (T, error), opts RetryOptions) (T, error) {
	var result T
	var lastErr error

	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		result, lastErr = op()
		if lastErr == nil {
			return result, nil
		}

		// Don't retry non-retryable errors
		if !isRetryableError(lastErr) {
			return result, lastErr
		}

		// Don't retry if we've exhausted retries
		if attempt >= opts.MaxRetries {
			return result, lastErr
		}

		// Calculate delay with exponential backoff: 1s, 2s, 4s, 8s...
		delay := opts.BaseDelay * time.Duration(1<<uint(attempt))
		if delay > opts.MaxDelay {
			delay = opts.MaxDelay
		}

		// Check for Retry-After header in rate limit errors
		if retryAfter := extractRetryAfter(lastErr); retryAfter > 0 {
			delay = retryAfter
		}

		// Wait with context cancellation support
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
			// Continue to next retry attempt
		}
	}

	return result, lastErr
}

// WithRetryVoid is like WithRetry but for operations that don't return a value.
func WithRetryVoid(ctx context.Context, op func() error, opts RetryOptions) error {
	_, err := WithRetry(ctx, func() (struct{}, error) {
		return struct{}{}, op()
	}, opts)
	return err
}

// isRetryableError determines if an error is transient and should be retried.
// Returns true for:
// - 429 Too Many Requests (rate limiting)
// - 500, 502, 503, 504 (server errors)
// - Network/connection errors
// Returns false for:
// - 400 Bad Request
// - 401 Unauthorized
// - 403 Forbidden (non-rate-limit)
// - 404 Not Found
// - 422 Unprocessable Entity
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Check for retryable HTTP status codes
	retryableStatuses := []string{
		"status 429", // Rate limited
		"status 500", // Internal Server Error
		"status 502", // Bad Gateway
		"status 503", // Service Unavailable
		"status 504", // Gateway Timeout
	}

	for _, status := range retryableStatuses {
		if strings.Contains(errStr, status) {
			return true
		}
	}

	// Check for network errors (these don't have HTTP status)
	networkErrors := []string{
		"connection refused",
		"connection reset",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"context deadline exceeded",
		"dial tcp",
	}

	errLower := strings.ToLower(errStr)
	for _, netErr := range networkErrors {
		if strings.Contains(errLower, netErr) {
			return true
		}
	}

	return false
}

// extractRetryAfter extracts the Retry-After duration from a rate limit error.
// GitHub includes this header in 429 responses indicating when the client can retry.
// Returns 0 if no Retry-After information is found.
func extractRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}

	errStr := err.Error()

	// GitHub API sometimes includes retry-after info in error response
	// Look for patterns like "retry after X seconds" or "Retry-After: X"
	patterns := []string{
		`retry.after[:\s]+(\d+)`,
		`Retry-After[:\s]+(\d+)`,
		`rate.limit.*?(\d+)\s*seconds?`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile("(?i)" + pattern)
		matches := re.FindStringSubmatch(errStr)
		if len(matches) > 1 {
			if seconds, parseErr := strconv.Atoi(matches[1]); parseErr == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}

	// Default for 429 without explicit retry-after: wait 60 seconds
	// GitHub's default rate limit window is 1 minute for unauthenticated requests
	if strings.Contains(errStr, "status 429") {
		return 60 * time.Second
	}

	return 0
}
