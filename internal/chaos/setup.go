// Package chaos provides fault injection testing infrastructure.
// It allows testing system behavior under adverse conditions like
// network failures, API errors, timeouts, and resource contention.
package chaos

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

// Common errors for chaos testing
var (
	ErrInjectedFault    = errors.New("injected fault")
	ErrInjectedTimeout  = errors.New("injected timeout")
	ErrInjected500      = errors.New("injected server error")
	ErrInjectedRateLimit = errors.New("injected rate limit")
)

// FaultType represents the type of fault to inject
type FaultType int

const (
	FaultNone FaultType = iota
	FaultError500
	FaultTimeout
	FaultRateLimit
	FaultConnectionReset
	FaultSlowResponse
	FaultIntermittent
)

// FaultConfig configures fault injection behavior
type FaultConfig struct {
	// Type of fault to inject
	Type FaultType

	// Probability of fault occurring (0.0 to 1.0)
	// For FaultIntermittent, this controls failure rate
	Probability float64

	// Delay before responding (for FaultSlowResponse)
	Delay time.Duration

	// Number of failures before recovery (for FaultIntermittent)
	FailuresBeforeRecovery int

	// Rate limit reset time (for FaultRateLimit)
	RateLimitResetAfter time.Duration

	// Custom error message
	ErrorMessage string
}

// DefaultFaultConfig returns sensible defaults for fault injection
func DefaultFaultConfig() *FaultConfig {
	return &FaultConfig{
		Type:                   FaultNone,
		Probability:            1.0,
		Delay:                  5 * time.Second,
		FailuresBeforeRecovery: 3,
		RateLimitResetAfter:    1 * time.Hour,
		ErrorMessage:           "chaos fault injected",
	}
}

// FaultInjector manages fault injection state
type FaultInjector struct {
	config     *FaultConfig
	mu         sync.RWMutex
	callCount  atomic.Int64
	failCount  atomic.Int64
	enabled    atomic.Bool
	rng        *rand.Rand
}

// NewFaultInjector creates a new fault injector
func NewFaultInjector(config *FaultConfig) *FaultInjector {
	if config == nil {
		config = DefaultFaultConfig()
	}
	fi := &FaultInjector{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	fi.enabled.Store(true)
	return fi
}

// Enable enables fault injection
func (fi *FaultInjector) Enable() {
	fi.enabled.Store(true)
}

// Disable disables fault injection
func (fi *FaultInjector) Disable() {
	fi.enabled.Store(false)
}

// IsEnabled returns whether fault injection is enabled
func (fi *FaultInjector) IsEnabled() bool {
	return fi.enabled.Load()
}

// SetConfig updates the fault configuration
func (fi *FaultInjector) SetConfig(config *FaultConfig) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.config = config
}

// GetConfig returns the current fault configuration
func (fi *FaultInjector) GetConfig() *FaultConfig {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	return fi.config
}

// Stats returns fault injection statistics
func (fi *FaultInjector) Stats() (calls, faults int64) {
	return fi.callCount.Load(), fi.failCount.Load()
}

// Reset resets the fault injector state
func (fi *FaultInjector) Reset() {
	fi.callCount.Store(0)
	fi.failCount.Store(0)
}

// ShouldFail determines if this call should fail based on config
func (fi *FaultInjector) ShouldFail() bool {
	if !fi.enabled.Load() {
		return false
	}

	fi.callCount.Add(1)

	fi.mu.RLock()
	config := fi.config
	fi.mu.RUnlock()

	switch config.Type {
	case FaultNone:
		return false

	case FaultIntermittent:
		// Fail for first N calls, then succeed
		if int(fi.failCount.Load()) < config.FailuresBeforeRecovery {
			fi.failCount.Add(1)
			return true
		}
		return false

	default:
		// Probability-based failure
		if fi.rng.Float64() < config.Probability {
			fi.failCount.Add(1)
			return true
		}
		return false
	}
}

// InjectFault applies the configured fault and returns an error if applicable
func (fi *FaultInjector) InjectFault(ctx context.Context) error {
	if !fi.ShouldFail() {
		return nil
	}

	fi.mu.RLock()
	config := fi.config
	fi.mu.RUnlock()

	switch config.Type {
	case FaultError500:
		return ErrInjected500

	case FaultTimeout:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(config.Delay):
			return ErrInjectedTimeout
		}

	case FaultRateLimit:
		return ErrInjectedRateLimit

	case FaultConnectionReset:
		return ErrInjectedFault

	case FaultSlowResponse:
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(config.Delay):
			return nil // Slow but succeeds
		}

	case FaultIntermittent:
		return ErrInjectedFault

	default:
		return nil
	}
}

// ChaosHTTPServer creates an HTTP server that injects faults
type ChaosHTTPServer struct {
	server   *httptest.Server
	injector *FaultInjector
	handler  http.Handler
}

// NewChaosHTTPServer creates a new chaos HTTP server wrapping an existing handler
func NewChaosHTTPServer(handler http.Handler, config *FaultConfig) *ChaosHTTPServer {
	injector := NewFaultInjector(config)

	chaosHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if injector.ShouldFail() {
			cfg := injector.GetConfig()
			switch cfg.Type {
			case FaultError500:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error": "` + cfg.ErrorMessage + `"}`))
				return

			case FaultTimeout:
				// Simulate timeout by sleeping longer than client timeout
				time.Sleep(cfg.Delay)
				w.WriteHeader(http.StatusGatewayTimeout)
				return

			case FaultRateLimit:
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", time.Now().Add(cfg.RateLimitResetAfter).Format(time.RFC3339))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error": "rate limit exceeded"}`))
				return

			case FaultSlowResponse:
				time.Sleep(cfg.Delay)
				// Continue to normal handler after delay

			case FaultConnectionReset:
				// Close connection abruptly by hijacking
				hijacker, ok := w.(http.Hijacker)
				if ok {
					conn, _, _ := hijacker.Hijack()
					if conn != nil {
						_ = conn.Close()
					}
				}
				return
			}
		}
		// Normal handling
		handler.ServeHTTP(w, r)
	})

	server := httptest.NewServer(chaosHandler)

	return &ChaosHTTPServer{
		server:   server,
		injector: injector,
		handler:  chaosHandler,
	}
}

// URL returns the test server URL
func (s *ChaosHTTPServer) URL() string {
	return s.server.URL
}

// Close shuts down the server
func (s *ChaosHTTPServer) Close() {
	s.server.Close()
}

// Injector returns the fault injector for configuration
func (s *ChaosHTTPServer) Injector() *FaultInjector {
	return s.injector
}

// ProcessWatchdog monitors a process and kills it if it hangs
type ProcessWatchdog struct {
	timeout   time.Duration
	checkInterval time.Duration
	onTimeout func(ctx context.Context) error
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	running   bool
}

// NewProcessWatchdog creates a watchdog with the given timeout
func NewProcessWatchdog(timeout, checkInterval time.Duration) *ProcessWatchdog {
	return &ProcessWatchdog{
		timeout:       timeout,
		checkInterval: checkInterval,
		done:          make(chan struct{}),
	}
}

// SetTimeoutHandler sets the function to call when timeout is reached
func (w *ProcessWatchdog) SetTimeoutHandler(handler func(ctx context.Context) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onTimeout = handler
}

// Start begins the watchdog timer
func (w *ProcessWatchdog) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	w.mu.Unlock()

	go func() {
		defer close(w.done)
		timer := time.NewTimer(w.timeout)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w.mu.Lock()
			handler := w.onTimeout
			w.mu.Unlock()
			if handler != nil {
				_ = handler(ctx)
			}
		}
	}()
}

// Stop cancels the watchdog timer
func (w *ProcessWatchdog) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	if w.cancel != nil {
		w.cancel()
	}
	w.mu.Unlock()
	<-w.done
}

// Reset restarts the watchdog timer
func (w *ProcessWatchdog) Reset(ctx context.Context) {
	w.Stop()
	w.Start(ctx)
}

// SQLiteLockSimulator simulates database lock contention
type SQLiteLockSimulator struct {
	mu            sync.Mutex
	lockDuration  time.Duration
	locked        atomic.Bool
	waiters       atomic.Int64
	maxWaitTime   time.Duration
}

// NewSQLiteLockSimulator creates a lock simulator
func NewSQLiteLockSimulator(lockDuration, maxWaitTime time.Duration) *SQLiteLockSimulator {
	return &SQLiteLockSimulator{
		lockDuration: lockDuration,
		maxWaitTime:  maxWaitTime,
	}
}

// Lock simulates acquiring a database lock with contention
func (s *SQLiteLockSimulator) Lock(ctx context.Context) error {
	s.waiters.Add(1)
	defer s.waiters.Add(-1)

	deadline := time.Now().Add(s.maxWaitTime)

	for {
		if time.Now().After(deadline) {
			return errors.New("database is locked: timeout exceeded")
		}

		s.mu.Lock()
		if !s.locked.Load() {
			s.locked.Store(true)
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()

		// Wait a bit before retrying
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Unlock releases the simulated lock
func (s *SQLiteLockSimulator) Unlock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locked.Store(false)
}

// SimulateContention holds a lock for the configured duration
func (s *SQLiteLockSimulator) SimulateContention(ctx context.Context) error {
	if err := s.Lock(ctx); err != nil {
		return err
	}
	defer s.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(s.lockDuration):
		return nil
	}
}

// WaiterCount returns the number of goroutines waiting for the lock
func (s *SQLiteLockSimulator) WaiterCount() int64 {
	return s.waiters.Load()
}

// RetryTracker tracks retry behavior for verification
type RetryTracker struct {
	mu       sync.Mutex
	attempts []time.Time
	delays   []time.Duration
}

// NewRetryTracker creates a new retry tracker
func NewRetryTracker() *RetryTracker {
	return &RetryTracker{
		attempts: make([]time.Time, 0),
		delays:   make([]time.Duration, 0),
	}
}

// RecordAttempt records a retry attempt
func (t *RetryTracker) RecordAttempt() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if len(t.attempts) > 0 {
		lastAttempt := t.attempts[len(t.attempts)-1]
		t.delays = append(t.delays, now.Sub(lastAttempt))
	}
	t.attempts = append(t.attempts, now)
}

// AttemptCount returns the number of attempts
func (t *RetryTracker) AttemptCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.attempts)
}

// Delays returns the delays between attempts
func (t *RetryTracker) Delays() []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]time.Duration, len(t.delays))
	copy(result, t.delays)
	return result
}

// VerifyExponentialBackoff checks if delays follow exponential backoff
// Returns true if each delay is >= previous delay * factor (within tolerance)
func (t *RetryTracker) VerifyExponentialBackoff(factor, tolerance float64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.delays) < 2 {
		return true // Not enough data to verify
	}

	for i := 1; i < len(t.delays); i++ {
		expected := float64(t.delays[i-1]) * factor
		actual := float64(t.delays[i])

		// Allow for jitter: actual should be at least (factor - tolerance) * previous
		minExpected := expected * (1 - tolerance)
		if actual < minExpected {
			return false
		}
	}
	return true
}

// Reset clears all recorded data
func (t *RetryTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts = make([]time.Time, 0)
	t.delays = make([]time.Duration, 0)
}
