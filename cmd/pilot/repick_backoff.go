package main

import (
	"sync"
	"time"
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
)

// repickBackoffEntry tracks one task_id/project_path pair's cooldown state.
type repickBackoffEntry struct {
	consecutiveDrops int
	nextAllowedAt    time.Time
}

// repickBackoffTracker throttles repeated dispatch attempts for the same task
// after a dropped pickup (claim lost, or a completed-but-open issue the
// poller re-admitted despite terminal ledger evidence). Backoff grows
// exponentially per consecutive drop and is capped at
// repickBackoffBaseInterval * 2^repickBackoffMaxShift.
type repickBackoffTracker struct {
	mu      sync.Mutex
	entries map[string]*repickBackoffEntry
}

func newRepickBackoffTracker() *repickBackoffTracker {
	return &repickBackoffTracker{entries: make(map[string]*repickBackoffEntry)}
}

// allow reports whether a dispatch attempt for key may proceed now — false
// while key is still within its backoff window from a prior drop.
func (t *repickBackoffTracker) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		return true
	}
	return !time.Now().Before(e.nextAllowedAt)
}

// recordDrop registers a dropped pickup for key, extending its backoff window
// exponentially (capped), and returns the new consecutive-drop count so the
// caller can decide whether to escalate its log level / fire a metric.
func (t *repickBackoffTracker) recordDrop(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.entries[key]
	if !ok {
		e = &repickBackoffEntry{}
		t.entries[key] = e
	}
	e.consecutiveDrops++
	shift := e.consecutiveDrops - 1
	if shift > repickBackoffMaxShift {
		shift = repickBackoffMaxShift
	}
	e.nextAllowedAt = time.Now().Add(repickBackoffBaseInterval * time.Duration(uint64(1)<<uint(shift)))
	return e.consecutiveDrops
}

// recordSuccess clears any backoff state for key once a dispatch actually
// proceeds (a fresh execution was queued) — the next drop, if any, starts a
// fresh backoff sequence rather than continuing to escalate.
func (t *repickBackoffTracker) recordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
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
