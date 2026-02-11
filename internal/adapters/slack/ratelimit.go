package slack

import (
	"sync"
	"time"
)

// RateLimitConfig holds rate limiting configuration for Slack.
type RateLimitConfig struct {
	Enabled           bool `yaml:"enabled"`
	MessagesPerMinute int  `yaml:"messages_per_minute"` // Max messages per minute (default: 20)
	TasksPerHour      int  `yaml:"tasks_per_hour"`      // Max task executions per hour (default: 10)
	BurstSize         int  `yaml:"burst_size"`          // Burst allowance (default: 5)
}

// DefaultRateLimitConfig returns default rate limit configuration.
func DefaultRateLimitConfig() *RateLimitConfig {
	return &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 20,
		TasksPerHour:      10,
		BurstSize:         5,
	}
}

// RateLimiter implements per-user/channel token bucket rate limiting.
type RateLimiter struct {
	config  *RateLimitConfig
	buckets map[string]*tokenBucket
	mu      sync.Mutex
}

// tokenBucket tracks rate limits for a single user/channel.
type tokenBucket struct {
	messageTokens   float64
	taskTokens      float64
	lastRefill      time.Time
	messageRate     float64 // tokens per second
	taskRate        float64 // tokens per second
	maxMessageBurst int
	maxTaskBurst    int
}

// NewRateLimiter creates a new rate limiter with the given configuration.
func NewRateLimiter(config *RateLimitConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimitConfig()
	}
	return &RateLimiter{
		config:  config,
		buckets: make(map[string]*tokenBucket),
	}
}

// AllowMessage checks if a message is allowed for the given channel/user ID.
// Returns true if allowed, false if rate limited.
func (r *RateLimiter) AllowMessage(channelID string) bool {
	if !r.config.Enabled {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bucket := r.getOrCreateBucket(channelID)
	bucket.refill()

	if bucket.messageTokens >= 1 {
		bucket.messageTokens--
		return true
	}
	return false
}

// AllowTask checks if a task execution is allowed for the given channel/user ID.
// Returns true if allowed, false if rate limited.
func (r *RateLimiter) AllowTask(channelID string) bool {
	if !r.config.Enabled {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bucket := r.getOrCreateBucket(channelID)
	bucket.refill()

	if bucket.taskTokens >= 1 {
		bucket.taskTokens--
		return true
	}
	return false
}

// getOrCreateBucket returns the token bucket for a channel ID, creating if needed.
func (r *RateLimiter) getOrCreateBucket(channelID string) *tokenBucket {
	bucket, exists := r.buckets[channelID]
	if !exists {
		maxMessageBurst := r.config.MessagesPerMinute
		if r.config.BurstSize > 0 && r.config.BurstSize < maxMessageBurst {
			maxMessageBurst = r.config.BurstSize
		}

		maxTaskBurst := r.config.TasksPerHour
		if r.config.BurstSize > 0 && r.config.BurstSize < maxTaskBurst {
			maxTaskBurst = r.config.BurstSize
		}

		bucket = &tokenBucket{
			messageTokens:   float64(maxMessageBurst), // Start with burst capacity
			taskTokens:      float64(maxTaskBurst),
			lastRefill:      time.Now(),
			messageRate:     float64(r.config.MessagesPerMinute) / 60.0, // per second
			taskRate:        float64(r.config.TasksPerHour) / 3600.0,    // per second
			maxMessageBurst: maxMessageBurst,
			maxTaskBurst:    maxTaskBurst,
		}
		r.buckets[channelID] = bucket
	}
	return bucket
}

// refill adds tokens based on elapsed time.
func (b *tokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.lastRefill = now

	// Add tokens based on elapsed time
	b.messageTokens += elapsed * b.messageRate
	if b.messageTokens > float64(b.maxMessageBurst) {
		b.messageTokens = float64(b.maxMessageBurst)
	}

	b.taskTokens += elapsed * b.taskRate
	if b.taskTokens > float64(b.maxTaskBurst) {
		b.taskTokens = float64(b.maxTaskBurst)
	}
}

// GetRemainingMessages returns the number of messages remaining in the rate limit.
func (r *RateLimiter) GetRemainingMessages(channelID string) int {
	if !r.config.Enabled {
		return -1 // Unlimited
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bucket := r.getOrCreateBucket(channelID)
	bucket.refill()
	return int(bucket.messageTokens)
}

// GetRemainingTasks returns the number of task executions remaining in the rate limit.
func (r *RateLimiter) GetRemainingTasks(channelID string) int {
	if !r.config.Enabled {
		return -1 // Unlimited
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	bucket := r.getOrCreateBucket(channelID)
	bucket.refill()
	return int(bucket.taskTokens)
}

// Cleanup removes buckets that haven't been used recently.
// Call periodically (e.g., every hour) to prevent memory leaks.
func (r *RateLimiter) Cleanup(maxAge time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for channelID, bucket := range r.buckets {
		if bucket.lastRefill.Before(cutoff) {
			delete(r.buckets, channelID)
		}
	}
}
