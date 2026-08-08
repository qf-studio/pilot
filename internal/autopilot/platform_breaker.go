package autopilot

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// GH-4791 (TASK-458 part 1): during the 2026-08-06 GitHub Actions outage the
// daemon acted on false CI signals for ~50 minutes — closing a correct PR,
// spawning junk fix issues, burning retries — until a human stopped it. The
// existing circuit breaker is deliberately per-PR ("so one bad PR doesn't
// block others", see prFailureState above) and each PR individually looked
// like a normal failure. The missing signal is correlation: several
// unrelated PRs failing within minutes is a platform event, not N
// independent regressions. PlatformBreaker adds that signal.
//
// Part 1 scope: correlation + suppression of destructive actions only. Part
// 2 (deferred) adds the external githubstatus.com probe, admission pause,
// and held-PR re-drive on close.

// DefaultPlatformBreakerMinCorrelatedPRs, DefaultPlatformBreakerCorrelationWindow,
// and DefaultPlatformBreakerQuietPeriod are GH-4791's part-1 defaults, used
// whenever the corresponding PlatformBreakerConfig field is zero/unset.
const (
	// DefaultPlatformBreakerMinCorrelatedPRs distinct PRs observing an
	// infra-or-unknown-class CI failure inside the correlation window opens
	// the breaker.
	DefaultPlatformBreakerMinCorrelatedPRs = 3
	// DefaultPlatformBreakerCorrelationWindow is how far back distinct-PR
	// observations count toward the threshold above.
	DefaultPlatformBreakerCorrelationWindow = 15 * time.Minute
	// DefaultPlatformBreakerQuietPeriod is how long the breaker must see no
	// new infra/unknown-class CI failure before it closes again.
	DefaultPlatformBreakerQuietPeriod = 20 * time.Minute
	// DefaultPlatformBreakerProbeInterval (GH-4792, TASK-458 part 2) is how
	// often the periodic monitor probes githubstatus.com and re-evaluates
	// the time-based close condition while the breaker is open.
	DefaultPlatformBreakerProbeInterval = 5 * time.Minute
)

// platformFailureObservation is one CI-failure observation relevant to
// cross-PR correlation: an infra- or unknown-classified failure on a
// specific PR at a specific time. Only these two classes feed the
// correlation signal — a genuine code-classified failure is never platform
// evidence (classifyPRFailure's own fail-safe-to-code default already
// requires positive evidence before naming a failure "code").
type platformFailureObservation struct {
	key string // distinct-PR identity for correlation, "owner/repo#123"
	at  time.Time
}

// PlatformBreaker correlates CI-failure observations across PRs (and repos)
// to detect a platform-wide outage the per-PR circuit breaker cannot see by
// construction — it is deliberately scoped to one PR at a time so a bad PR
// never blocks others. Shape mirrors internal/ghbudget.Tracker
// (Observe-fed process-wide state, exactly-one log per state transition):
// that package exists for the same underlying reason — a signal only
// visible in aggregate across every controller/repo in the daemon, not from
// any single one.
//
// One PlatformBreaker is constructed once in cmd/pilot/main.go and shared by
// every autopilot.Controller via WithPlatformBreaker, exactly like
// ghbudget.Tracker/WithRateBudget — GitHub Actions outages are not scoped to
// one repo, so a per-controller instance would miss correlation across
// repos entirely.
//
// A nil *PlatformBreaker is a byte-identical no-op: Observe always reports
// closed, so disabled-by-config call sites need no separate branch. Safe
// for concurrent use.
type PlatformBreaker struct {
	log *slog.Logger

	minDistinctPRs    int
	correlationWindow time.Duration
	quietPeriod       time.Duration

	// now returns the current time; overridable in tests. A nil now uses time.Now.
	now func() time.Time

	mu           sync.Mutex
	observations []platformFailureObservation // pruned to correlationWindow on every relevant Observe
	open         bool
	lastInfraAt  time.Time
	correlated   map[string]bool // PR keys observed during the current (or most recently closed) open episode
}

// NewPlatformBreaker constructs a PlatformBreaker. minDistinctPRs <= 0 uses
// DefaultPlatformBreakerMinCorrelatedPRs; correlationWindow/quietPeriod <= 0
// use their respective defaults. A nil log uses slog.Default().
func NewPlatformBreaker(minDistinctPRs int, correlationWindow, quietPeriod time.Duration, log *slog.Logger) *PlatformBreaker {
	if minDistinctPRs <= 0 {
		minDistinctPRs = DefaultPlatformBreakerMinCorrelatedPRs
	}
	if correlationWindow <= 0 {
		correlationWindow = DefaultPlatformBreakerCorrelationWindow
	}
	if quietPeriod <= 0 {
		quietPeriod = DefaultPlatformBreakerQuietPeriod
	}
	if log == nil {
		log = slog.Default()
	}
	return &PlatformBreaker{
		log:               log.With("component", "platform_breaker"),
		minDistinctPRs:    minDistinctPRs,
		correlationWindow: correlationWindow,
		quietPeriod:       quietPeriod,
	}
}

// PlatformBreakerResult is the outcome of one Observe call.
type PlatformBreakerResult struct {
	// Open reports whether the breaker is open after this observation —
	// callers gate destructive CI-failure actions on this.
	Open bool
	// JustOpened/JustClosed report whether THIS call caused the transition.
	// The tracker's own mutex guarantees at most one Observe call across
	// every controller sharing it ever sees one of these true for a given
	// episode, so that one caller alone can safely fire a once-per-transition
	// alert with no separate dedup guard needed at the call site.
	JustOpened bool
	JustClosed bool
	// CorrelatedPRs is the sorted, deduplicated list of "owner/repo#N" PR
	// refs behind the transition: the distinct PRs that opened it
	// (JustOpened), or every distinct PR observed across the whole episode
	// (JustClosed).
	CorrelatedPRs []string
}

// Observe records one CI-failure observation and reports the breaker's
// resulting state. pr/repo/class identify the observation; only
// infra-or-unknown-class observations feed the correlation signal — a
// code-classified failure carries none, but the breaker's CURRENT
// open/closed state (from prior infra/unknown observations) is still
// evaluated and returned, so a code-classified failure on some other PR is
// correctly suppressed while a platform outage is already confirmed open.
//
// A nil receiver always reports closed (Open: false, no transition) — the
// disabled-by-config no-op.
func (b *PlatformBreaker) Observe(pr int, repo string, class FailureClass) PlatformBreakerResult {
	if b == nil {
		return PlatformBreakerResult{}
	}

	now := time.Now
	if b.now != nil {
		now = b.now
	}
	t := now()

	b.mu.Lock()
	defer b.mu.Unlock()

	// Time-based close check runs first, against state as of BEFORE this
	// observation: an observation landing exactly at (or after) the quiet
	// deadline must not itself count as "still quiet" and keep a stale
	// episode open. If this observation is itself relevant, it is free to
	// open a brand-new episode below in the same call.
	result := b.closeIfQuietLocked(t)

	if relevant := class.IsInfra() || class == FailureClassUnknown; relevant {
		key := platformBreakerKey(repo, pr)
		b.lastInfraAt = t
		b.observations = append(b.observations, platformFailureObservation{key: key, at: t})
		b.pruneLocked(t)

		if b.open {
			if b.correlated == nil {
				b.correlated = make(map[string]bool)
			}
			b.correlated[key] = true
		} else {
			distinct := b.distinctKeysLocked()
			if len(distinct) >= b.minDistinctPRs {
				b.open = true
				b.correlated = distinct
				result.JustOpened = true
				result.CorrelatedPRs = sortedKeys(distinct)
				b.log.Warn("platform-outage breaker opened — correlated infra/unknown-class CI failures across distinct PRs, suspected platform outage",
					"distinct_prs", len(distinct),
					"min_distinct_prs", b.minDistinctPRs,
					"correlation_window", b.correlationWindow,
					"correlated_prs", result.CorrelatedPRs,
				)
			}
		}
	}

	result.Open = b.open
	return result
}

// IsOpen reports the breaker's current state without recording an
// observation. Time-based close is only ever evaluated as a side effect of
// Observe, so a long-idle breaker (no CI failures anywhere to call Observe
// with) may report a stale "open" here until the next Observe call — which
// is harmless, since nothing is being suppressed if no PR is going through
// handleCIFailed either. A nil receiver always reports false.
func (b *PlatformBreaker) IsOpen() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

// MinCorrelatedPRs, CorrelationWindow, and QuietPeriod expose the resolved
// (post-default) construction settings — GH-4814: cmd/pilot/main.go logs
// these once at startup so an operator can confirm the breaker is armed and
// with which thresholds without tracing the config decode path by hand.
// Immutable after NewPlatformBreaker, so no locking is needed. A nil
// receiver returns the zero value.
func (b *PlatformBreaker) MinCorrelatedPRs() int {
	if b == nil {
		return 0
	}
	return b.minDistinctPRs
}

// CorrelationWindow returns the resolved correlation window (see
// MinCorrelatedPRs).
func (b *PlatformBreaker) CorrelationWindow() time.Duration {
	if b == nil {
		return 0
	}
	return b.correlationWindow
}

// QuietPeriod returns the resolved quiet period (see MinCorrelatedPRs).
func (b *PlatformBreaker) QuietPeriod() time.Duration {
	if b == nil {
		return 0
	}
	return b.quietPeriod
}

// EvaluateClose runs the same time-based close check Observe applies as a
// side effect, standalone. GH-4792 (TASK-458 part 2): unlike Observe, this
// is not gated behind a CI-failure event — the periodic breaker monitor
// calls it on a timer so a held episode closes (and held PRs get re-driven)
// even during a quiet spell with no CI activity anywhere to trigger Observe.
// A nil receiver always reports closed with no transition.
func (b *PlatformBreaker) EvaluateClose() PlatformBreakerResult {
	if b == nil {
		return PlatformBreakerResult{}
	}
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	result := b.closeIfQuietLocked(now())
	result.Open = b.open
	return result
}

// closeIfQuietLocked applies the time-based close check against state as of
// t and returns the (possibly just-closed) result with Open left for the
// caller to set. Must be called with mu held.
func (b *PlatformBreaker) closeIfQuietLocked(t time.Time) PlatformBreakerResult {
	var result PlatformBreakerResult
	if b.open && t.Sub(b.lastInfraAt) >= b.quietPeriod {
		result.JustClosed = true
		result.CorrelatedPRs = sortedKeys(b.correlated)
		b.open = false
		b.observations = nil
		b.correlated = nil
		b.log.Info("platform-outage breaker closed — quiet period elapsed with no new infra/unknown-class CI failure",
			"quiet_period", b.quietPeriod,
			"correlated_prs", result.CorrelatedPRs,
		)
	}
	return result
}

// pruneLocked drops observations older than correlationWindow relative to
// t. Must be called with mu held.
func (b *PlatformBreaker) pruneLocked(t time.Time) {
	cutoff := t.Add(-b.correlationWindow)
	i := 0
	for _, o := range b.observations {
		if o.at.After(cutoff) {
			b.observations[i] = o
			i++
		}
	}
	b.observations = b.observations[:i]
}

// distinctKeysLocked returns the set of distinct PR keys currently within
// the correlation window. Must be called with mu held, after pruneLocked.
func (b *PlatformBreaker) distinctKeysLocked() map[string]bool {
	out := make(map[string]bool, len(b.observations))
	for _, o := range b.observations {
		out[o.key] = true
	}
	return out
}

// platformBreakerKey builds the distinct-PR identity used for correlation —
// repo is included (not just the PR number) since one PlatformBreaker is
// shared across every repo's controller.
func platformBreakerKey(repo string, pr int) string {
	return fmt.Sprintf("%s#%d", repo, pr)
}

// sortedKeys returns the sorted keys of a string-set map, for a
// deterministic alert/log PR list.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
