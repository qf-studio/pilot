package executor

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const defaultStallWatchdogInterval = 30 * time.Second

// minStallWatchdogInterval floors the derived tick interval so a tiny configured
// stall timeout can't spin the watchdog hot.
const minStallWatchdogInterval = time.Second

// highEffortStallFloor is the minimum stall timeout applied to high-effort or
// complex-lane executions (GH-4501). A single high-effort thinking turn can
// legitimately produce no complete message — and, rarely, no partial-message
// delta either — for several minutes, so the default 3m stall timeout is too
// aggressive for these lanes.
const highEffortStallFloor = 10 * time.Minute

// effortAwareStallTimeout raises configured to highEffortStallFloor for
// high-effort or complex-lane executions, unless configured (an explicit
// stall_timeout_ms) is already higher — an explicit config value always wins
// when it's the larger of the two. When configured <= 0 (stall detection
// disabled via a negative StallTimeoutMs), it is returned unchanged so the
// watchdog stays off for every lane. GH-4501.
func effortAwareStallTimeout(configured time.Duration, effort string, complexity Complexity) time.Duration {
	if configured <= 0 {
		return configured
	}
	if effort == "high" || complexity == ComplexityComplex {
		if configured < highEffortStallFloor {
			return highEffortStallFloor
		}
	}
	return configured
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

// runStallWatchdog ticks at an interval derived from stallTimeout
// (watchdogTickInterval: min(30s, stallTimeout/3), floored at 1s) and cancels
// stallCancel if no backend event has been seen for longer than stallTimeout.
// Sets stallDetected before canceling so callers can distinguish a stall from
// other cancellations. Returns when done is closed (i.e., the execution has ended).
//
// inFlightBackgroundTasks counts backend task_started events without a
// matching task_notification (e.g. a backgrounded Bash command still
// running). While it is > 0, the idle clock is suspended entirely: a session
// waiting on a legitimate long-running background task emits zero events by
// design and must not be mistaken for a dead one (GH-4357).
func (r *Runner) runStallWatchdog(
	taskID string,
	lastEventAt *atomic.Int64,
	stallDetected *atomic.Bool,
	inFlightBackgroundTasks *atomic.Int64,
	stallTimeout time.Duration,
	done <-chan struct{},
	stallCancel context.CancelFunc,
) {
	ticker := time.NewTicker(watchdogTickInterval(stallTimeout))
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if inFlightBackgroundTasks != nil && inFlightBackgroundTasks.Load() > 0 {
				continue
			}
			lastAt := time.Unix(0, lastEventAt.Load())
			idle := time.Since(lastAt)
			if idle > stallTimeout {
				r.log.Warn("Stall watchdog: no event activity, terminating session",
					slog.String("task_id", taskID),
					slog.Duration("idle", idle),
					slog.Duration("stall_timeout", stallTimeout),
				)
				stallDetected.Store(true)
				stallCancel()
				return
			}
		}
	}
}
