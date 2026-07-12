package autopilot

import (
	"fmt"

	"github.com/qf-studio/pilot/internal/memory"
)

// RefreshQueueDepth sets the pilot_queue_depth gauge from the store's
// current queued/pending execution count (GH-4246). SetQueueDepth had zero
// production callers before this — pilot_queue_depth read a constant 0
// regardless of actual queue depth. Call periodically (the dashboard's 2s
// refresh loop) so the gauge tracks the DB in near-real-time.
func RefreshQueueDepth(store *memory.Store, metrics *Metrics) error {
	if store == nil || metrics == nil {
		return nil
	}
	n, err := store.CountQueuedTasks()
	if err != nil {
		return fmt.Errorf("refresh queue depth: %w", err)
	}
	metrics.SetQueueDepth(n)
	return nil
}
