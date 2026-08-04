package executor

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// runStallWatchdog ticks at policy.StallWatchdogInterval and cancels
// stallCancel if no backend event has been seen for longer than
// policy.StallTimeout. Sets stallDetected before canceling so callers can
// distinguish a stall from other cancellations. Returns when done is closed
// (i.e., the execution has ended).
//
// GH-4715: policy is resolved once per task (ResolveLivenessPolicy, in
// liveness_policy.go) and threaded through by the caller — this function
// owns no silence-threshold constants of its own, so it can never drift
// from the heartbeat monitor's hard-kill floor the way the two mechanisms
// did before GH-4695 hand-resynced them.
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
	policy LivenessPolicy,
	done <-chan struct{},
	stallCancel context.CancelFunc,
) {
	ticker := time.NewTicker(policy.StallWatchdogInterval)
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
			if idle > policy.StallTimeout {
				r.log.Warn("Stall watchdog: no event activity, terminating session",
					slog.String("task_id", taskID),
					slog.Duration("idle", idle),
					slog.Duration("stall_timeout", policy.StallTimeout),
				)
				stallDetected.Store(true)
				stallCancel()
				return
			}
		}
	}
}
