package executor

import (
	"context"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// RetryStrategy defines how to retry based on error type.
// GH-920: Smarter retry strategies per error type for improved reliability.
type RetryStrategy struct {
	// MaxAttempts is the maximum number of retry attempts (0 = no retry)
	MaxAttempts int `yaml:"max_attempts"`

	// InitialBackoff is the wait time before the first retry
	InitialBackoff time.Duration `yaml:"initial_backoff"`

	// BackoffMultiplier increases the backoff after each retry (e.g., 2.0 doubles it)
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`

	// ExtendTimeout extends the execution timeout on retry (for timeout errors)
	ExtendTimeout bool `yaml:"extend_timeout,omitempty"`

	// TimeoutMultiplier is how much to extend the timeout (default: 1.5)
	TimeoutMultiplier float64 `yaml:"timeout_multiplier,omitempty"`
}

// RetryConfig holds error-type-specific retry strategies.
//
// Example YAML configuration:
//
//	executor:
//	  retry:
//	    enabled: true
//	    rate_limit:
//	      max_attempts: 3
//	      initial_backoff: 30s
//	      backoff_multiplier: 2.0
//	    api_error:
//	      max_attempts: 3
//	      initial_backoff: 5s
//	      backoff_multiplier: 2.0
//	    timeout:
//	      max_attempts: 2
//	      extend_timeout: true
//	      timeout_multiplier: 1.5
type RetryConfig struct {
	// Enabled controls whether smart retry is active
	Enabled bool `yaml:"enabled"`

	// RateLimit strategy for rate limit errors (30s, 60s, 120s backoff)
	RateLimit *RetryStrategy `yaml:"rate_limit,omitempty"`

	// APIError strategy for API errors (5s, 10s, 20s backoff)
	APIError *RetryStrategy `yaml:"api_error,omitempty"`

	// Timeout strategy for timeout errors (retry with extended timeout)
	Timeout *RetryStrategy `yaml:"timeout,omitempty"`

	// Note: invalid_config has no entry = fail fast (no retry)
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		Enabled: false, // Disabled by default, opt-in
		RateLimit: &RetryStrategy{
			MaxAttempts:       3,
			InitialBackoff:    30 * time.Second,
			BackoffMultiplier: 2.0, // 30s, 60s, 120s
		},
		APIError: &RetryStrategy{
			MaxAttempts:       3,
			InitialBackoff:    5 * time.Second,
			BackoffMultiplier: 2.0, // 5s, 10s, 20s
		},
		Timeout: &RetryStrategy{
			MaxAttempts:       2, // Retry once
			ExtendTimeout:     true,
			TimeoutMultiplier: 1.5,
		},
	}
}

// RetryDecision contains the result of a retry evaluation.
type RetryDecision struct {
	// ShouldRetry indicates whether to retry
	ShouldRetry bool

	// BackoffDuration is how long to wait before retrying
	BackoffDuration time.Duration

	// ExtendedTimeout is the new timeout if ExtendTimeout is true
	ExtendedTimeout time.Duration

	// Reason explains the decision
	Reason string
}

// Retrier evaluates whether to retry based on error type.
type Retrier struct {
	config *RetryConfig
	log    *slog.Logger
}

// NewRetrier creates a new Retrier with the given configuration.
func NewRetrier(config *RetryConfig) *Retrier {
	if config == nil {
		config = DefaultRetryConfig()
	}
	return &Retrier{
		config: config,
		log:    logging.WithComponent("executor.retry"),
	}
}

// Evaluate determines whether to retry based on error type and attempt count.
func (r *Retrier) Evaluate(err error, attempt int, originalTimeout time.Duration) RetryDecision {
	if !r.config.Enabled {
		return RetryDecision{
			ShouldRetry: false,
			Reason:      "retry disabled",
		}
	}

	ccErr, ok := err.(*ClaudeCodeError)
	if !ok {
		return RetryDecision{
			ShouldRetry: false,
			Reason:      "not a classified Claude Code error",
		}
	}

	var strategy *RetryStrategy
	var errorName string

	switch ccErr.Type {
	case ErrorTypeRateLimit:
		strategy = r.config.RateLimit
		errorName = "rate_limit"
	case ErrorTypeAPIError:
		strategy = r.config.APIError
		errorName = "api_error"
	case ErrorTypeTimeout:
		strategy = r.config.Timeout
		errorName = "timeout"
	case ErrorTypeInvalidConfig:
		// Invalid config should never be retried - fail fast
		return RetryDecision{
			ShouldRetry: false,
			Reason:      "invalid_config errors are not retryable (fail fast)",
		}
	default:
		// Unknown errors use default behavior (no smart retry)
		return RetryDecision{
			ShouldRetry: false,
			Reason:      "unknown error type - using default behavior",
		}
	}

	if strategy == nil {
		return RetryDecision{
			ShouldRetry: false,
			Reason:      errorName + " retry strategy not configured",
		}
	}

	if attempt >= strategy.MaxAttempts {
		return RetryDecision{
			ShouldRetry: false,
			Reason:      errorName + " max attempts reached",
		}
	}

	// Calculate backoff: initial * multiplier^attempt
	backoff := strategy.InitialBackoff
	for i := 0; i < attempt; i++ {
		backoff = time.Duration(float64(backoff) * strategy.BackoffMultiplier)
	}

	decision := RetryDecision{
		ShouldRetry:     true,
		BackoffDuration: backoff,
		Reason:          errorName + " retry scheduled",
	}

	// Handle timeout extension
	if strategy.ExtendTimeout && originalTimeout > 0 {
		multiplier := strategy.TimeoutMultiplier
		if multiplier == 0 {
			multiplier = 1.5 // Default
		}
		decision.ExtendedTimeout = time.Duration(float64(originalTimeout) * multiplier)
	}

	r.log.Info("Retry decision",
		slog.String("error_type", errorName),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", strategy.MaxAttempts),
		slog.Duration("backoff", backoff),
		slog.Duration("extended_timeout", decision.ExtendedTimeout),
	)

	return decision
}

// Sleep waits for the backoff duration, respecting context cancellation.
func (r *Retrier) Sleep(ctx context.Context, duration time.Duration) error {
	r.log.Debug("Waiting before retry", slog.Duration("duration", duration))

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(duration):
		return nil
	}
}
