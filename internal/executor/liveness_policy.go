package executor

import "time"

// This file is the single home for every stdout-silence timing constant
// used to decide whether a Claude Code subprocess is hung (heartbeat_
// monitor.go's hard-kill) or stalled (watchdog.go's soft-stall cancel).
//
// GH-4715: these two detectors used to carry independent copies of their
// thresholds. GH-4695 had to hand-resync them after they drifted, and
// GH-4691's own out-of-scope note flagged that the next tuning PR could
// silently reintroduce the same drift. Resolving one LivenessPolicy per
// task — here — and having both detectors read only from it (never from
// their own constants) makes that drift impossible by construction: there
// is exactly one place effort/complexity feed into silence thresholds.

const (
	// defaultStallWatchdogInterval bounds how often the stall watchdog
	// polls for idleness (watchdogTickInterval below derives the actual
	// per-task interval from this, floored at minStallWatchdogInterval).
	defaultStallWatchdogInterval = 30 * time.Second

	// minStallWatchdogInterval floors the derived tick interval so a tiny
	// configured stall timeout can't spin the watchdog hot.
	minStallWatchdogInterval = time.Second

	// highEffortStallFloor is the minimum silence threshold applied to
	// high-effort or complex-lane executions (GH-4501/GH-4691) for BOTH
	// the soft-stall timeout and the hard-heartbeat floor. A single
	// high-effort thinking turn can legitimately produce no complete
	// message — and, rarely, no partial-message delta either — for
	// several minutes, so the default 3m stall timeout (and the backend's
	// flat hard-heartbeat timeout) is too aggressive for these lanes.
	highEffortStallFloor = 10 * time.Minute

	// heartbeatGraceLogInterval bounds how often heartbeat_monitor.go's
	// "heartbeat grace" INFO line repeats while a long local tool keeps
	// the stream silent — without this, a 10-minute `make test` run would
	// log once per heartbeat check tick.
	heartbeatGraceLogInterval = 2 * time.Minute
)

// LivenessPolicy bundles every resolved stdout-silence threshold for one
// task execution: the soft-stall timeout read by watchdog.go's stall
// watchdog, and the hard-kill heartbeat floor read (via ExecuteOptions) by
// backend_claudecode.go / heartbeat_monitor.go. It is resolved exactly once
// per task via ResolveLivenessPolicy and threaded through ExecuteOptions,
// so both detectors observe the same instance for a given task — a future
// tuning PR that touches one threshold and not the other can no longer
// silently reintroduce the drift GH-4695 fixed.
//
// Design decision (TASK-441 L4): this merges the *policy*, not the
// *enforcement*. The two mechanisms keep their existing, independent kill
// semantics (a hard SIGKILL vs. a context cancel) — only the thresholds
// they read are unified.
type LivenessPolicy struct {
	// StallTimeout is the effective soft-stall threshold: how long
	// watchdog.go's stall watchdog tolerates no agent event before
	// canceling the execution context. Zero disables stall detection.
	StallTimeout time.Duration

	// StallWatchdogInterval is the poll interval the stall watchdog ticks
	// at, derived from StallTimeout (watchdogTickInterval).
	StallWatchdogInterval time.Duration

	// HeartbeatFloor raises the backend's own configured/default hard
	// heartbeat timeout for this task when it's smaller — the backend
	// applies max(its own heartbeatTimeout, HeartbeatFloor). Zero means no
	// floor (the backend's own configured/default value applies
	// unchanged).
	HeartbeatFloor time.Duration
}

// ResolveLivenessPolicy computes the single LivenessPolicy for a task from
// its configured stall timeout, effort, and complexity — the ONE place
// effort/complexity feed into stdout-silence thresholds. configuredStall is
// typically Runner.effectiveStallTimeout() (BackendConfig.StallTimeoutMs
// resolved to a duration).
func ResolveLivenessPolicy(configuredStall time.Duration, effort string, complexity Complexity) LivenessPolicy {
	stallTimeout := effortAwareStallTimeout(configuredStall, effort, complexity)
	return LivenessPolicy{
		StallTimeout:          stallTimeout,
		StallWatchdogInterval: watchdogTickInterval(stallTimeout),
		HeartbeatFloor:        effortAwareHeartbeatFloor(effort, complexity),
	}
}

// effortAwareStallTimeout raises configured to highEffortStallFloor for
// high-effort or heavy-complexity (complex/epic) executions, unless
// configured (an explicit stall_timeout_ms) is already higher — an explicit
// config value always wins when it's the larger of the two. When configured
// <= 0 (stall detection disabled via a negative StallTimeoutMs), it is
// returned unchanged so the watchdog stays off for every lane. GH-4501.
//
// GH-4691: was `complexity == ComplexityComplex`, silently excluding
// ComplexityEpic — epic runs are at least as likely as complex ones to have
// long silent-stdout stretches, so they must get the same floor.
// Complexity.IsHeavy() covers both.
func effortAwareStallTimeout(configured time.Duration, effort string, complexity Complexity) time.Duration {
	if configured <= 0 {
		return configured
	}
	if effort == "high" || complexity.IsHeavy() {
		if configured < highEffortStallFloor {
			return highEffortStallFloor
		}
	}
	return configured
}

// effortAwareHeartbeatFloor derives the minimum hard-heartbeat timeout for
// high-effort or heavy-complexity (complex/epic) executions, using the same
// highEffortStallFloor threshold as effortAwareStallTimeout (GH-4691).
//
// This is intentionally independent of the *configured* stall timeout (and
// thus of whether stall detection is enabled at all): the hard heartbeat in
// backend_claudecode.go is a separate kill path from the stall watchdog
// above, and must not fire before the stall watchdog's own effort-aware
// floor would — regardless of whether that watchdog happens to be disabled
// for this run. Returns 0 (no floor) for lanes that don't qualify, letting
// the backend's default/configured heartbeat timeout stand unchanged.
func effortAwareHeartbeatFloor(effort string, complexity Complexity) time.Duration {
	if effort == "high" || complexity.IsHeavy() {
		return highEffortStallFloor
	}
	return 0
}

// watchdogTickInterval derives the watchdog poll interval from stallTimeout so a
// small configured timeout is actually honored (detection latency ≈ one tick).
// It is min(defaultStallWatchdogInterval, stallTimeout/3), floored at
// minStallWatchdogInterval. TASK-344.
func watchdogTickInterval(stallTimeout time.Duration) time.Duration {
	interval := defaultStallWatchdogInterval
	if third := stallTimeout / 3; third < interval {
		interval = third
	}
	if interval < minStallWatchdogInterval {
		interval = minStallWatchdogInterval
	}
	return interval
}
