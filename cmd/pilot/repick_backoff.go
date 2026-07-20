package main

import (
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// GH-4376: the poller's label-removed retry heuristic (external studio-sdk
// dependency) re-admits an open issue on every poll tick with no memory of
// prior drops — evidenced by GH-91 (COMPLETED terminal execution, no status
// labels) generating a "dispatch claim lost" drop 191x in one afternoon,
// ~30s apart forever. Since that admission loop lives outside this repo, the
// throttle has to live on our side of the shared handler chokepoint
// (handleIssueGeneric): a per-issue exponential backoff that suppresses
// repeated dispatch attempts after a dropped pickup, independent of whatever
// the poller decides to re-offer next tick.
const (
	repickBackoffBaseInterval = 30 * time.Second
	// repickBackoffMaxShift caps backoff growth at base * 2^5 = base * 32.
	repickBackoffMaxShift      = 5
	repickBackoffWarnThreshold = 5 // consecutive drops before escalating to WARN

	// repickLoopBreakerThreshold is the consecutive-drop count (GH-4469) at
	// which handleIssueGeneric fires a single WARNING alert naming the task,
	// on top of the ordinary Warn-level log escalation at
	// repickBackoffWarnThreshold. By this point the exponential backoff has
	// already been capped (repickBackoffMaxShift) for several drops, so the
	// task is being silently re-offered every ~16 minutes rather than every
	// 30s — still worth paging an operator about, since GH-4391 accumulated
	// this pattern for two days before anyone noticed.
	repickLoopBreakerThreshold = 10
)

// repickBackoffEntry tracks one task_id/project_path pair's cooldown state.
type repickBackoffEntry struct {
	consecutiveDrops int
	nextAllowedAt    time.Time
	// gateLogged records whether the current backoff window has already
	// emitted its once-per-window DEBUG "gated" log line (GH-4469 deliverable
	// 2) — set by gateStatus, cleared once nextAllowedAt passes so the next
	// window logs exactly once again.
	gateLogged bool
}

// repickBackoffPersister durably mirrors the tracker's in-memory entries
// (GH-4394). Implemented by *executor.Dispatcher, which proxies to the
// store's repick_backoff table. A nil persister (e.g. in the tracker's own
// unit tests, or before the dispatcher is wired) makes the tracker behave
// exactly as it did pre-GH-4394: pure in-memory, reset on restart.
type repickBackoffPersister interface {
	RepickBackoffState(key string) (consecutiveDrops int, nextAllowedAt time.Time, found bool, err error)
	SetRepickBackoffState(key string, consecutiveDrops int, nextAllowedAt time.Time) error
	ClearRepickBackoffState(key string) error
}

// repickBackoffTracker throttles repeated dispatch attempts for the same task
// after a dropped pickup (claim lost, or a completed-but-open issue the
// poller re-admitted despite terminal ledger evidence). Backoff grows
// exponentially per consecutive drop and is capped at
// repickBackoffBaseInterval * 2^repickBackoffMaxShift.
//
// GH-4394: state is mirrored to persist (when wired) so a daemon restart or
// a shadow-DB split-brain doesn't silently reset a task's cooldown to zero
// mid-storm — the in-memory map remains the hot-path cache, but the durable
// copy is what a fresh process (or a fresh check after the first process
// crashed) rehydrates from.
type repickBackoffTracker struct {
	mu      sync.Mutex
	entries map[string]*repickBackoffEntry
	persist repickBackoffPersister
}

func newRepickBackoffTracker() *repickBackoffTracker {
	return &repickBackoffTracker{entries: make(map[string]*repickBackoffEntry)}
}

// setPersister wires (or rewires) the durable backing store. Safe to call
// repeatedly — handleIssueGeneric calls it on every invocation with the
// current deps.Dispatcher, which is idempotent for the common case (the same
// dispatcher instance for the process lifetime) and correctly picks up a new
// dispatcher in tests that construct a fresh one per test.
func (t *repickBackoffTracker) setPersister(p repickBackoffPersister) {
	t.mu.Lock()
	t.persist = p
	t.mu.Unlock()
}

// hydrate loads key's persisted state into the in-memory cache on first
// touch (e.g. right after a restart, before this process has recorded any
// drop of its own for key). Must be called with t.mu held.
func (t *repickBackoffTracker) hydrateLocked(key string) *repickBackoffEntry {
	if t.persist == nil {
		return nil
	}
	consecutiveDrops, nextAllowedAt, found, err := t.persist.RepickBackoffState(key)
	if err != nil {
		logging.WithComponent("dispatch").Warn("failed to load persisted repick backoff state",
			slog.String("key", key), slog.Any("error", err))
		return nil
	}
	if !found {
		return nil
	}
	e := &repickBackoffEntry{consecutiveDrops: consecutiveDrops, nextAllowedAt: nextAllowedAt}
	t.entries[key] = e
	return e
}

// allow reports whether a dispatch attempt for key may proceed now — false
// while key is still within its backoff window from a prior drop.
func (t *repickBackoffTracker) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		e = t.hydrateLocked(key)
		if e == nil {
			return true
		}
	}
	return !time.Now().Before(e.nextAllowedAt)
}

// gateStatus reports whether key is currently within its backoff window
// (gated) and, if so, whether the caller should emit its once-per-window
// DEBUG log line (GH-4469 deliverable 2) — true only the first time
// gateStatus observes this window as gated, false on every subsequent poll
// tick until the window expires and a new one begins.
func (t *repickBackoffTracker) gateStatus(key string) (gated bool, shouldLog bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		e = t.hydrateLocked(key)
		if e == nil {
			return false, false
		}
	}
	if time.Now().Before(e.nextAllowedAt) {
		shouldLog = !e.gateLogged
		e.gateLogged = true
		return true, shouldLog
	}
	e.gateLogged = false
	return false, false
}

// recordDrop registers a dropped pickup for key, extending its backoff window
// exponentially (capped), and returns the new consecutive-drop count so the
// caller can decide whether to escalate its log level / fire a metric.
func (t *repickBackoffTracker) recordDrop(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		e = t.hydrateLocked(key)
		if e == nil {
			e = &repickBackoffEntry{}
			t.entries[key] = e
		}
	}
	e.consecutiveDrops++
	shift := e.consecutiveDrops - 1
	if shift > repickBackoffMaxShift {
		shift = repickBackoffMaxShift
	}
	e.nextAllowedAt = time.Now().Add(repickBackoffBaseInterval * time.Duration(uint64(1)<<uint(shift)))
	if t.persist != nil {
		if err := t.persist.SetRepickBackoffState(key, e.consecutiveDrops, e.nextAllowedAt); err != nil {
			logging.WithComponent("dispatch").Warn("failed to persist repick backoff state",
				slog.String("key", key), slog.Any("error", err))
		}
	}
	return e.consecutiveDrops
}

// recordSuccess clears any backoff state for key once a dispatch actually
// proceeds (a fresh execution was queued) — the next drop, if any, starts a
// fresh backoff sequence rather than continuing to escalate.
func (t *repickBackoffTracker) recordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
	if t.persist != nil {
		if err := t.persist.ClearRepickBackoffState(key); err != nil {
			logging.WithComponent("dispatch").Warn("failed to clear persisted repick backoff state",
				slog.String("key", key), slog.Any("error", err))
		}
	}
}

// repickBackoff is the process-wide tracker consulted by handleIssueGeneric.
// A single shared instance is safe: keys are namespaced by project path, so
// tasks from different adapters/projects never collide.
var repickBackoff = newRepickBackoffTracker()

// repickBackoffKey namespaces backoff state by project path + task ID —
// task_id alone is not unique across projects (GH-4276).
func repickBackoffKey(projectPath, taskID string) string {
	return projectPath + "|" + taskID
}
