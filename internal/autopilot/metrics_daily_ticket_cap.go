package autopilot

import (
	"fmt"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// RefreshDailyTicketCapDispatchedToday sets the
// pilot_daily_ticket_cap_dispatched_today gauge from the ledger: distinct
// tickets started in the org-local calendar day containing now, counted by the
// same query ExecutionLifecycle.Begin holds against (GH-5808). loc is the
// executor.daily_ticket_cap_timezone zone. Call periodically next to
// RefreshQueueDepth.
func RefreshDailyTicketCapDispatchedToday(store *memory.Store, metrics *Metrics, loc *time.Location, now time.Time) error {
	if store == nil || metrics == nil {
		return nil
	}
	start, end := memory.LocalDayWindow(now, loc)
	n, err := store.CountDistinctTasksInWindow(start, end)
	if err != nil {
		return fmt.Errorf("refresh daily ticket cap gauge: %w", err)
	}
	metrics.SetDailyTicketCapDispatchedToday(n)
	return nil
}
