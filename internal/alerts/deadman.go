package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// DefaultDeadManFailureThreshold mirrors judgeFailureStreakAlertThreshold
// (cmd/pilot/poller_github.go, GH-4669) and repickLoopBreakerThreshold
// (cmd/pilot/repick_backoff.go, GH-4391): the consecutive-failure count a
// dead-man tracker default fires at, once. Callers that need a different
// cadence can pass their own threshold to RegisterDeadManTracker — this is
// only the shared default for new registrations that have no prior tuning.
const DefaultDeadManFailureThreshold = 10

// DefaultDeadManWindow is the default zero-attempts staleness horizon (see
// DeadManTracker.Stale): a tracked seam that hasn't recorded a single
// attempt in this long has gone quiet, mirroring retryTrackerTTL's 24h
// idle-eviction window elsewhere in this package.
const DefaultDeadManWindow = 24 * time.Hour

// EventTypeDeadManStreak carries a DeadManTracker's threshold-reached signal
// through the engine's normal event queue (priority-aware, dedupe-aware) so
// a new registration reuses the same delivery path instead of hand-rolling
// its own Event{Type: ...} + handler pair. The rule to fire is selected by
// event.Metadata["alert_type"], set by DeadManTracker.RecordFailure. This is
// the tracker's default event type; a registration can override it (see
// WithDeadManEventType) to route into a pre-existing dedicated handler
// instead, as the intent-judge migration does to keep
// EventTypeIntentJudgeFailureStreak's emission unchanged.
const EventTypeDeadManStreak EventType = "dead_man_streak"

// EventTypeDeadManAttempt/Success/Failure relay a DeadManTracker's
// Record{Attempt,Success,Failure} calls across the executor/alerts package
// boundary. internal/executor cannot import internal/alerts directly (this
// package already imports executor, for ExecutionLifecycle — see
// engine.go), so a seam inside the executor (e.g. runSelfReview) cannot hold
// a live *DeadManTracker reference. Instead it emits one of these through
// its own AlertEventProcessor/AlertEvent mirror types (mirrors every other
// AlertEventType in internal/executor/alerts.go), which
// alerts.EngineAdapter.ProcessEvent forwards here unchanged (string-typed
// EventType/AlertEventType cast). event.Metadata["tracker"] selects which
// registered tracker to route the call to; a lookup miss (tracker not yet
// registered) is a silent no-op, matching every other nil-safe guard in this
// package.
const (
	EventTypeDeadManAttempt EventType = "dead_man_attempt"
	EventTypeDeadManSuccess EventType = "dead_man_success"
	EventTypeDeadManFailure EventType = "dead_man_failure"
)

// DeadManTracker is a reusable liveness primitive for the "silent death"
// incident class TASK-441 L2 (GH-4709) generalizes: a seam that produces
// zero errors not because it's healthy, but because it fails open without
// ever surfacing an error (GH-4669, intent judge: 17 days, 4,321
// invocations) or because nothing calls it at all (GH-4687, label
// lifecycle: 19 days; GH-4702, self-review: months). Absence of errors is
// not liveness — a tracker counts attempts and successes separately, not
// just failures, so a subsystem wired to nothing is itself detectable
// (memory: poller-labels-removed-log-means-never-applied).
//
// A tracker fires its registered AlertType exactly once when its consecutive
// failure streak reaches threshold (>=, gated by a fired-once flag rather
// than an equality check — GH-4866: a tracker that starts counting mid-streak,
// e.g. after a process restart or a threshold lowered below an
// already-elevated streak, would otherwise sail past an exact-match
// threshold and never fire at all) and resets the streak, and the fired
// flag, on the next success — a dead-open seam pages once per streak, not on
// every subsequent failure, and pages again if it dies again after
// recovering.
type DeadManTracker struct {
	engine    *Engine
	name      string
	alertType AlertType
	eventType EventType
	threshold int
	window    time.Duration

	mu                  sync.Mutex
	attempts            uint64
	successes           uint64
	consecutiveFailures int
	fired               bool
	lastAttemptAt       time.Time
}

// DeadManTrackerOption configures optional DeadManTracker behavior.
type DeadManTrackerOption func(*DeadManTracker)

// WithDeadManEventType overrides the EventType queued when a tracker's
// failure streak reaches threshold (default EventTypeDeadManStreak, routed
// through the generic handleDeadManStreak). Used by migrations that need to
// keep queuing a pre-existing, dedicated EventType/handler pair unchanged —
// e.g. the intent-judge migration keeps emitting
// EventTypeIntentJudgeFailureStreak (GH-4669's original, ops-watched alert
// contract, handled by the unchanged handleIntentJudgeFailureStreak) rather
// than switching to the new generic path.
func WithDeadManEventType(t EventType) DeadManTrackerOption {
	return func(tr *DeadManTracker) { tr.eventType = t }
}

// NewDeadManTracker constructs a standalone tracker not registered in any
// Engine's registry. engine may be nil (e.g. a call site with no alerts
// engine wired at all) — counting (RecordAttempt/RecordSuccess/RecordFailure)
// always works; only threshold-reached alert delivery no-ops when engine is
// nil, mirroring every other nil-engine/nil-store guard in this package
// (e.g. Engine.lifecycle, WithExecutionLifecycle).
//
// Most callers should prefer Engine.RegisterDeadManTracker, which memoizes
// by name so multiple call sites (or repeated construction — e.g. a
// per-repo wrapper rebuilt on each poll cycle) share one set of counters
// instead of resetting them.
func NewDeadManTracker(engine *Engine, name string, alertType AlertType, threshold int, window time.Duration, opts ...DeadManTrackerOption) *DeadManTracker {
	t := &DeadManTracker{
		engine:    engine,
		name:      name,
		alertType: alertType,
		eventType: EventTypeDeadManStreak,
		threshold: threshold,
		window:    window,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// RegisterDeadManTracker returns the engine-scoped tracker for name,
// creating and memoizing it on first call — a second registration under the
// same name returns the original tracker (and its accumulated counters)
// rather than resetting it, so a call site constructed fresh per
// request/repo/reconfigure doesn't need to separately memoize the tracker
// itself. Nil-safe: a nil *Engine (no alerts engine wired) returns a
// standalone, unregistered tracker via NewDeadManTracker — counting still
// works, delivery no-ops.
func (e *Engine) RegisterDeadManTracker(name string, alertType AlertType, threshold int, window time.Duration, opts ...DeadManTrackerOption) *DeadManTracker {
	if e == nil {
		return NewDeadManTracker(nil, name, alertType, threshold, window, opts...)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.deadManTrackers == nil {
		e.deadManTrackers = make(map[string]*DeadManTracker)
	}
	if t, ok := e.deadManTrackers[name]; ok {
		return t
	}
	t := NewDeadManTracker(e, name, alertType, threshold, window, opts...)
	e.deadManTrackers[name] = t
	return t
}

// namedDeadManTracker looks up a registered tracker by name for the
// executor-relay event handlers (handleDeadManAttempt/Success/Failure). A
// miss (unregistered name) returns nil — the caller no-ops, since a tracker
// must be registered once at startup (see e.g.
// registerSelfReviewDeadManTracker) before any relayed event can reach it.
func (e *Engine) namedDeadManTracker(name string) *DeadManTracker {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.deadManTrackers[name]
}

// RecordAttempt marks that the tracked operation was invoked, independent of
// outcome. Call once per invocation, before the outcome is known, so
// Attempts() reflects "did we even try" — the signal a wired-to-nothing seam
// (zero attempts despite tasks flowing) can never fake by only counting
// errors.
func (t *DeadManTracker) RecordAttempt() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.attempts++
	t.lastAttemptAt = time.Now()
	t.mu.Unlock()
}

// RecordSuccess resets the consecutive-failure streak (and the fired-once
// gate — see RecordFailure) so failures before and after an intervening
// success don't compound toward the alert threshold, and a seam that dies
// again after recovering pages again instead of staying silenced by its
// first streak's fired flag. Increments the success counter exposed by
// Successes().
func (t *DeadManTracker) RecordSuccess() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.successes++
	t.consecutiveFailures = 0
	t.fired = false
	t.mu.Unlock()
}

// RecordFailure increments the consecutive-failure streak and, at the
// threshold crossing, logs an unconditional WARN — greppable (`alerts.deadman`
// component) whether or not an alerts engine is even wired up, since GH-4866
// found the daemon can silently run without a single dead-man rule
// registered at all. The crossing also fires exactly one alert event per
// streak, gated by a fired-once flag rather than an equality check (see the
// DeadManTracker doc comment for why) so a seam that never recovers pages
// once instead of on every failure thereafter, and pages again on the next
// streak past threshold after an intervening RecordSuccess. Both the WARN
// log and the alert event share the same shouldFire gate, so the log line is
// always present when (and only when) an alert would have fired had an
// engine been wired. extraMetadata is merged into the fired event alongside
// the tracker's own name/consecutive_failures (e.g. repo, task_id).
func (t *DeadManTracker) RecordFailure(extraMetadata map[string]string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.consecutiveFailures++
	consecutive := t.consecutiveFailures
	shouldFire := consecutive >= t.threshold && !t.fired
	if shouldFire {
		t.fired = true
	}
	t.mu.Unlock()

	if !shouldFire {
		return
	}

	logging.WithComponent("alerts.deadman").Warn("dead-man tracker reached failure threshold",
		slog.String("tracker", t.name),
		slog.String("alert_type", string(t.alertType)),
		slog.Int("consecutive_failures", consecutive),
		slog.Int("threshold", t.threshold),
	)

	if t.engine == nil {
		return
	}

	metadata := map[string]string{
		"tracker":              t.name,
		"alert_type":           string(t.alertType),
		"consecutive_failures": strconv.Itoa(consecutive),
	}
	for k, v := range extraMetadata {
		metadata[k] = v
	}

	t.engine.ProcessEvent(Event{
		Type:      t.eventType,
		Metadata:  metadata,
		Timestamp: time.Now(),
	})
}

// Attempts returns the total number of RecordAttempt calls.
func (t *DeadManTracker) Attempts() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// Successes returns the total number of RecordSuccess calls.
func (t *DeadManTracker) Successes() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.successes
}

// ConsecutiveFailures returns the current unbroken failure streak.
func (t *DeadManTracker) ConsecutiveFailures() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.consecutiveFailures
}

// Stale reports the zero-attempts half of the silent-death class (GH-4687,
// GH-4702): true if the tracker has never recorded an attempt, or if its
// last attempt is older than its registered window — either shape means the
// tracked seam has gone quiet, which a pure failure/success counter can
// never observe (a seam nothing calls produces no failures either). A
// non-positive window disables the elapsed-time check (returns false once at
// least one attempt has been recorded).
func (t *DeadManTracker) Stale(now time.Time) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.attempts == 0 {
		return true
	}
	if t.window <= 0 {
		return false
	}
	return now.Sub(t.lastAttemptAt) > t.window
}

// Name returns the tracker's registered name.
func (t *DeadManTracker) Name() string {
	if t == nil {
		return ""
	}
	return t.name
}

// handleDeadManAttempt/Success/Failure route an executor-relayed liveness
// signal (EventTypeDeadManAttempt/Success/Failure) to its named tracker.
func (e *Engine) handleDeadManAttempt(event Event) {
	e.namedDeadManTracker(event.Metadata["tracker"]).RecordAttempt()
}

func (e *Engine) handleDeadManSuccess(event Event) {
	e.namedDeadManTracker(event.Metadata["tracker"]).RecordSuccess()
}

func (e *Engine) handleDeadManFailure(event Event) {
	e.namedDeadManTracker(event.Metadata["tracker"]).RecordFailure(event.Metadata)
}

// handleDeadManStreak routes a DeadManTracker's threshold-reached signal
// (the generic EventTypeDeadManStreak path — see WithDeadManEventType for
// the override used to keep a pre-existing dedicated event/handler pair
// unchanged) to its registered AlertRule. Every generic dead-man tracker
// shares this single handler — rule selection is by
// event.Metadata["alert_type"] rather than a per-tracker switch case, since
// RecordFailure already gated on the exact threshold before emitting
// (mirroring handleIntentJudgeFailureStreak et al: no Condition-based
// counting happens here).
func (e *Engine) handleDeadManStreak(ctx context.Context, event Event) {
	alertType := AlertType(event.Metadata["alert_type"])
	matched := false
	for _, rule := range e.config.Rules {
		if rule.Type != alertType {
			continue
		}
		matched = true
		if !rule.Enabled || !e.shouldFire(rule) {
			continue
		}
		message := fmt.Sprintf("Dead-man tracker %q has failed %s consecutive times without a success",
			event.Metadata["tracker"], event.Metadata["consecutive_failures"])
		alert := e.createAlert(rule, event, message)
		e.fireAlert(ctx, rule, alert)
	}
	// RecordFailure already logs an unconditional WARN at the threshold
	// crossing (greppable regardless of rule coverage), so this only needs
	// to flag the additional, more specific failure mode: the streak
	// crossed threshold but no configured rule can ever deliver it as a
	// channel alert — the same class of gap FromConfigAlerts' default-rule
	// union (GH-4866) closes for a fresh/persisted config, surfaced here as
	// a runtime backstop for any rule set that still slips through it.
	if !matched {
		e.logger.Warn("dead-man streak event has no matching rule — alert dropped silently",
			slog.String("tracker", event.Metadata["tracker"]),
			slog.String("alert_type", string(alertType)),
			slog.String("consecutive_failures", event.Metadata["consecutive_failures"]))
	}
}
