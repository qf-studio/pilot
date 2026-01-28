// Package webhooks provides outbound HTTP webhook delivery for Pilot events.
// It supports HMAC signature verification, retry with exponential backoff,
// and event filtering per webhook endpoint.
package webhooks

import (
	"time"
)

// EventType represents the type of event that can be sent via webhook.
type EventType string

const (
	// Task lifecycle events
	EventTaskStarted   EventType = "task.started"
	EventTaskProgress  EventType = "task.progress"
	EventTaskCompleted EventType = "task.completed"
	EventTaskFailed    EventType = "task.failed"

	// PR events
	EventPRCreated EventType = "pr.created"

	// Budget events
	EventBudgetWarning EventType = "budget.warning"
)

// AllEventTypes returns all supported event types.
func AllEventTypes() []EventType {
	return []EventType{
		EventTaskStarted,
		EventTaskProgress,
		EventTaskCompleted,
		EventTaskFailed,
		EventPRCreated,
		EventBudgetWarning,
	}
}

// Config holds configuration for outbound webhooks.
type Config struct {
	// Enabled controls whether webhooks are active
	Enabled bool `yaml:"enabled"`

	// Endpoints is the list of webhook endpoints to deliver events to
	Endpoints []*EndpointConfig `yaml:"endpoints"`

	// Defaults applied to all endpoints unless overridden
	Defaults *EndpointDefaults `yaml:"defaults,omitempty"`
}

// EndpointConfig defines a single webhook endpoint.
type EndpointConfig struct {
	// ID is a unique identifier for this endpoint (auto-generated if empty)
	ID string `yaml:"id,omitempty"`

	// Name is a human-readable name for this endpoint
	Name string `yaml:"name"`

	// URL is the destination URL for webhook delivery
	URL string `yaml:"url"`

	// Secret is used for HMAC-SHA256 signature generation
	// Can use environment variable syntax: $WEBHOOK_SECRET
	Secret string `yaml:"secret"`

	// Events is the list of event types this endpoint subscribes to
	// Empty means all events
	Events []EventType `yaml:"events,omitempty"`

	// Enabled controls whether this endpoint is active
	Enabled bool `yaml:"enabled"`

	// Timeout for HTTP requests (default: 30s)
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// Retry configuration
	Retry *RetryConfig `yaml:"retry,omitempty"`

	// Headers to include in webhook requests
	Headers map[string]string `yaml:"headers,omitempty"`
}

// EndpointDefaults holds default values for webhook endpoints.
type EndpointDefaults struct {
	Timeout time.Duration `yaml:"timeout"`
	Retry   *RetryConfig  `yaml:"retry,omitempty"`
}

// RetryConfig defines retry behavior for failed webhook deliveries.
type RetryConfig struct {
	// MaxAttempts is the maximum number of delivery attempts (default: 3)
	MaxAttempts int `yaml:"max_attempts"`

	// InitialDelay is the delay before the first retry (default: 1s)
	InitialDelay time.Duration `yaml:"initial_delay"`

	// MaxDelay is the maximum delay between retries (default: 60s)
	MaxDelay time.Duration `yaml:"max_delay"`

	// Multiplier is the exponential backoff multiplier (default: 2.0)
	Multiplier float64 `yaml:"multiplier"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Enabled:   false,
		Endpoints: []*EndpointConfig{},
		Defaults: &EndpointDefaults{
			Timeout: 30 * time.Second,
			Retry:   DefaultRetryConfig(),
		},
	}
}

// DefaultRetryConfig returns default retry settings.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Second,
		MaxDelay:     60 * time.Second,
		Multiplier:   2.0,
	}
}

// SubscribesTo returns true if the endpoint subscribes to the given event type.
func (e *EndpointConfig) SubscribesTo(eventType EventType) bool {
	if len(e.Events) == 0 {
		return true // Empty means all events
	}
	for _, et := range e.Events {
		if et == eventType {
			return true
		}
	}
	return false
}

// GetTimeout returns the effective timeout for this endpoint.
func (e *EndpointConfig) GetTimeout(defaults *EndpointDefaults) time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	if defaults != nil && defaults.Timeout > 0 {
		return defaults.Timeout
	}
	return 30 * time.Second
}

// GetRetry returns the effective retry config for this endpoint.
func (e *EndpointConfig) GetRetry(defaults *EndpointDefaults) *RetryConfig {
	if e.Retry != nil {
		return e.Retry
	}
	if defaults != nil && defaults.Retry != nil {
		return defaults.Retry
	}
	return DefaultRetryConfig()
}
