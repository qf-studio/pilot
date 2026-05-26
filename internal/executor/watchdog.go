package executor

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

const defaultStallWatchdogInterval = 30 * time.Second

// runStallWatchdog ticks every watchdogInterval and cancels stallCancel if no
// backend event has been seen for longer than stallTimeout. Sets stallDetected
// before canceling so callers can distinguish a stall from other cancellations.
// Returns when done is closed (i.e., the execution has ended).
func (r *Runner) runStallWatchdog(
	taskID string,
	lastEventAt *atomic.Int64,
	stallDetected *atomic.Bool,
	stallTimeout time.Duration,
	done <-chan struct{},
	stallCancel context.CancelFunc,
) {
	ticker := time.NewTicker(defaultStallWatchdogInterval)
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
