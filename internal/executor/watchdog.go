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
func (r *Runner) runStallWatchdog(
	taskID string,
	lastEventAt *atomic.Int64,
	stallDetected *atomic.Bool,
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
