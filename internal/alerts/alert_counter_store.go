package alerts

import "github.com/qf-studio/pilot/internal/memory"

// AlertCounterStore persists the last-seen value of level-triggered
// stats-event counters (e.g. circuit_breaker_trips) across restarts, so
// Engine can fire on an increase since the checkpoint instead of on the
// counter simply being nonzero (GH-5209). Optional: an Engine constructed
// without one (WithAlertCounterStore never called) keeps checkpoint state
// only in memory — a restart forgets it and treats the first post-restart
// observation as a fresh baseline, same as before this store existed.
//
// *memory.Store satisfies this interface directly — same optional-store
// shape as ActiveAlertStore.
type AlertCounterStore interface {
	UpsertAlertCounter(ruleName, source string, value int) error
	LoadAlertCounters() ([]*memory.AlertCounter, error)
}

// Compile-time assertion that *memory.Store satisfies AlertCounterStore.
var _ AlertCounterStore = (*memory.Store)(nil)
