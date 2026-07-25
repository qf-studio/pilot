package main

import (
	"testing"
	"time"
)

// TestRepickBackoffTracker_AllowsFirstAttempt verifies a key with no prior
// drop is never throttled.
func TestRepickBackoffTracker_AllowsFirstAttempt(t *testing.T) {
	tr := newRepickBackoffTracker()
	if !tr.allow("proj|GH-1") {
		t.Error("expected a never-seen key to be allowed")
	}
}

// TestRepickBackoffTracker_DropBlocksUntilWindowElapses is the GH-4376
// regression test: a dropped pickup must not immediately re-allow another
// attempt — the whole point is to stop a same-key retry every ~30s poll tick.
func TestRepickBackoffTracker_DropBlocksUntilWindowElapses(t *testing.T) {
	tr := newRepickBackoffTracker()
	key := "proj|GH-91"

	got := tr.recordDrop(key)
	if got != 1 {
		t.Fatalf("expected consecutive drop count 1, got %d", got)
	}
	if tr.allow(key) {
		t.Error("expected key to be blocked immediately after a drop")
	}
}

// TestRepickBackoffTracker_GrowsExponentiallyAndCaps verifies the backoff
// window grows with each consecutive drop and is capped at
// repickBackoffBaseInterval * 2^repickBackoffMaxShift (base * 32). Wait
// durations are derived from each recordDrop call's own "before" timestamp
// (not compared wall-clock-to-wall-clock across iterations) so the
// assertions aren't sensitive to nanosecond-level scheduling jitter once the
// cap is reached.
func TestRepickBackoffTracker_GrowsExponentiallyAndCaps(t *testing.T) {
	tr := newRepickBackoffTracker()
	key := "proj|GH-4372"
	capDuration := repickBackoffBaseInterval * (1 << repickBackoffMaxShift)

	waitFor := func(dropNum int) time.Duration {
		before := time.Now()
		tr.recordDrop(key)
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if got := tr.entries[key].consecutiveDrops; got != dropNum {
			t.Fatalf("drop %d: expected consecutiveDrops=%d, got %d", dropNum, dropNum, got)
		}
		return tr.entries[key].nextAllowedAt.Sub(before)
	}

	// Below the cap, each successive drop's backoff must be strictly longer
	// than the previous — derived from the same recordDrop's own reference
	// timestamp so there's no cross-call jitter to account for.
	prevWait := waitFor(1)
	for i := 2; i <= repickBackoffMaxShift+1; i++ {
		wait := waitFor(i)
		if wait <= prevWait {
			t.Fatalf("drop %d: backoff %v is not longer than drop %d's %v — expected exponential growth below the cap", i, wait, i-1, prevWait)
		}
		prevWait = wait
	}
	if prevWait < capDuration-time.Second || prevWait > capDuration+time.Second {
		t.Fatalf("drop %d: expected backoff at the cap (%v), got %v", repickBackoffMaxShift+1, capDuration, prevWait)
	}

	// Beyond the cap, further drops must not exceed it.
	for i := repickBackoffMaxShift + 2; i <= repickBackoffMaxShift+4; i++ {
		wait := waitFor(i)
		if wait > capDuration+time.Second {
			t.Fatalf("drop %d: backoff %v exceeds cap %v", i, wait, capDuration)
		}
	}
}

// TestRepickBackoffTracker_SuccessResetsState verifies a successful dispatch
// clears backoff state so the next drop starts a fresh sequence rather than
// continuing to escalate.
func TestRepickBackoffTracker_SuccessResetsState(t *testing.T) {
	tr := newRepickBackoffTracker()
	key := "proj|GH-4370"

	tr.recordDrop(key)
	tr.recordDrop(key)
	tr.recordSuccess(key)

	if !tr.allow(key) {
		t.Error("expected key to be allowed immediately after recordSuccess clears state")
	}

	got := tr.recordDrop(key)
	if got != 1 {
		t.Errorf("expected consecutive drop count to restart at 1 after reset, got %d", got)
	}
}

// TestRepickBackoffKey_NamespacesByProjectPath verifies task IDs are not
// unique across projects (GH-4276 precedent) so the key must include the
// project path.
func TestRepickBackoffKey_NamespacesByProjectPath(t *testing.T) {
	a := repickBackoffKey("/repo/a", "GH-1")
	b := repickBackoffKey("/repo/b", "GH-1")
	if a == b {
		t.Errorf("expected distinct keys for the same task_id under different project paths, got %q for both", a)
	}
}

// TestRepickBackoffKey_FormatMatchesDispatcherPackage is the GH-4394 subtask
// 4 regression test for the "one shared per-task backoff" invariant across
// packages. cmd/pilot and internal/executor each define their own
// repickBackoffKey (duplicated rather than imported, since internal/executor
// cannot import cmd/pilot — see dispatcher.go's repickBackoffKey doc comment)
// but both MUST read/write the exact same repick_backoff store row for a
// given (projectPath, taskID), or the poller's outer backoff gate
// (handleIssueGeneric) and the dispatcher's terminal-claim re-pick gate
// (beginWithGenerationRetry) would silently throttle two different keys —
// each seeing an empty/reset backoff the other has already grown, exactly
// the kind of gap GH-85 fell into. Pinning this package's key to the literal
// "projectPath|taskID" format means any future edit to the separator or
// field order here, without an identical edit on the internal/executor side
// (internal/executor/dispatcher_test.go has the matching pin), fails this
// test immediately instead of silently splitting the shared backoff in two.
func TestRepickBackoffKey_FormatMatchesDispatcherPackage(t *testing.T) {
	got := repickBackoffKey("/repo/a", "GH-85")
	want := "/repo/a|GH-85"
	if got != want {
		t.Errorf("repickBackoffKey format changed: got %q, want %q — internal/executor's repickBackoffKey must be updated identically or the shared backoff store silently splits into two divergent keys", got, want)
	}
}

// fakeRepickBackoffPersister is an in-memory stand-in for
// *executor.Dispatcher's store-backed RepickBackoffState/
// SetRepickBackoffState/ClearRepickBackoffState trio, used to verify the
// tracker's persistence wiring without standing up a real SQLite store.
type fakeRepickBackoffPersister struct {
	consecutiveDrops int
	nextAllowedAt    time.Time
	found            bool

	// claimLostDrops (GH-4540/TASK-421) backs ClaimLostDropCount/
	// SetClaimLostBackoff — deliberately a separate counter from
	// consecutiveDrops above, mirroring the real store's separate
	// claim_lost_drops column that must never perturb consecutive_drops.
	// nextAllowedAt/found above are shared, matching the real
	// repick_backoff row: both a genuine drop and a claim-lost drop grow the
	// same cooldown window.
	claimLostDrops int
	claimLostFound bool
}

func (f *fakeRepickBackoffPersister) RepickBackoffState(key string) (int, time.Time, bool, error) {
	return f.consecutiveDrops, f.nextAllowedAt, f.found, nil
}

func (f *fakeRepickBackoffPersister) SetRepickBackoffState(key string, consecutiveDrops int, nextAllowedAt time.Time) error {
	f.consecutiveDrops = consecutiveDrops
	f.nextAllowedAt = nextAllowedAt
	f.found = true
	return nil
}

func (f *fakeRepickBackoffPersister) ClearRepickBackoffState(key string) error {
	f.consecutiveDrops = 0
	f.nextAllowedAt = time.Time{}
	f.found = false
	f.claimLostDrops = 0
	f.claimLostFound = false
	return nil
}

func (f *fakeRepickBackoffPersister) ClaimLostDropCount(key string) (int, bool, error) {
	return f.claimLostDrops, f.claimLostFound, nil
}

func (f *fakeRepickBackoffPersister) SetClaimLostBackoff(key string, claimLostDrops int, nextAllowedAt time.Time) error {
	f.claimLostDrops = claimLostDrops
	f.claimLostFound = true
	f.nextAllowedAt = nextAllowedAt
	f.found = true
	return nil
}

// TestRepickBackoffTracker_RecordDropPersistsState is the GH-4394 regression
// test: recordDrop must mirror its computed cooldown to the wired persister,
// not just the in-process map — the whole point being that a second
// process/restart reading only the persister still sees the throttle.
func TestRepickBackoffTracker_RecordDropPersistsState(t *testing.T) {
	tr := newRepickBackoffTracker()
	persist := &fakeRepickBackoffPersister{}
	tr.setPersister(persist)

	key := "proj|GH-85"
	got := tr.recordDrop(key)
	if got != 1 {
		t.Fatalf("expected consecutive drop count 1, got %d", got)
	}
	if !persist.found {
		t.Fatal("expected recordDrop to persist state via the wired persister")
	}
	if persist.consecutiveDrops != 1 {
		t.Errorf("expected persisted consecutive_drops=1, got %d", persist.consecutiveDrops)
	}
	if persist.nextAllowedAt.IsZero() {
		t.Error("expected persisted next_allowed_at to be set")
	}
}

// TestRepickBackoffTracker_HydratesFromPersisterAfterRestart is the GH-4394
// core regression test: a FRESH tracker (simulating a daemon restart, where
// the in-memory map starts empty) wired to a persister that already holds a
// prior drop's cooldown must honor that cooldown immediately — not treat the
// key as never-seen just because this process's own map is empty.
func TestRepickBackoffTracker_HydratesFromPersisterAfterRestart(t *testing.T) {
	key := "proj|GH-85"
	persist := &fakeRepickBackoffPersister{
		consecutiveDrops: 4,
		nextAllowedAt:    time.Now().Add(5 * time.Minute),
		found:            true,
	}

	// Fresh tracker, as if the process just restarted.
	tr := newRepickBackoffTracker()
	tr.setPersister(persist)

	if tr.allow(key) {
		t.Fatal("expected a restarted tracker to honor the persisted cooldown instead of allowing immediately")
	}

	// A further drop must continue escalating from the persisted count (5th
	// drop), not restart at 1 — this is the "no growth across a restart"
	// symptom from the GH-4394 incident report (GH-85 retried 5x in ~15min
	// with no backoff growth).
	got := tr.recordDrop(key)
	if got != 5 {
		t.Errorf("expected consecutive drop count to continue from persisted state (5), got %d", got)
	}
}

// TestRepickBackoffTracker_GateStatus_LogsOncePerWindow is the GH-4469
// deliverable-2 regression test: gateStatus must report shouldLog=true only
// on the FIRST observation of a given backoff window, so
// terminalCompletionChecker.HasCompletedExecution logs its DEBUG "gated" line
// once per window instead of once per ~30s poll tick for the entire
// (potentially 16-minute) cooldown.
func TestRepickBackoffTracker_GateStatus_LogsOncePerWindow(t *testing.T) {
	tr := newRepickBackoffTracker()
	key := "proj|GH-4391"

	// No drop recorded yet — not gated.
	if gated, shouldLog := tr.gateStatus(key); gated || shouldLog {
		t.Fatalf("expected a never-seen key to be ungated with no log, got gated=%v shouldLog=%v", gated, shouldLog)
	}

	tr.recordDrop(key)

	gated, shouldLog := tr.gateStatus(key)
	if !gated || !shouldLog {
		t.Fatalf("expected first gateStatus check after a drop to be gated=true shouldLog=true, got gated=%v shouldLog=%v", gated, shouldLog)
	}

	// Subsequent checks within the same window must not re-log.
	for i := 0; i < 3; i++ {
		gated, shouldLog = tr.gateStatus(key)
		if !gated {
			t.Fatalf("check %d: expected key to remain gated within its backoff window", i)
		}
		if shouldLog {
			t.Fatalf("check %d: expected shouldLog=false for repeat checks within the same window", i)
		}
	}
}

// TestRepickBackoffTracker_GateStatus_ResetsLogFlagOnNewWindow verifies that
// once a backoff window expires, the next drop's new window logs once again
// rather than staying permanently silenced from the prior window.
func TestRepickBackoffTracker_GateStatus_ResetsLogFlagOnNewWindow(t *testing.T) {
	tr := newRepickBackoffTracker()
	key := "proj|GH-4391b"

	tr.recordDrop(key)
	if gated, shouldLog := tr.gateStatus(key); !gated || !shouldLog {
		t.Fatalf("expected first window to be gated+logged, got gated=%v shouldLog=%v", gated, shouldLog)
	}

	// Force the window to have already elapsed.
	tr.mu.Lock()
	tr.entries[key].nextAllowedAt = time.Now().Add(-time.Second)
	tr.mu.Unlock()

	if gated, shouldLog := tr.gateStatus(key); gated || shouldLog {
		t.Fatalf("expected an elapsed window to report ungated with no log, got gated=%v shouldLog=%v", gated, shouldLog)
	}

	// A fresh drop starts a new window that must log once again.
	tr.recordDrop(key)
	if gated, shouldLog := tr.gateStatus(key); !gated || !shouldLog {
		t.Fatalf("expected the new window to be gated+logged again, got gated=%v shouldLog=%v", gated, shouldLog)
	}
}

// TestRepickBackoffTracker_GateStatus_HydratesFromPersister verifies a fresh
// tracker (simulating a restart) whose map has never seen the key still
// correctly reports it as gated when the persister holds a live cooldown —
// mirroring TestRepickBackoffTracker_HydratesFromPersisterAfterRestart but
// for the gate-check path terminalCompletionChecker uses.
func TestRepickBackoffTracker_GateStatus_HydratesFromPersister(t *testing.T) {
	key := "proj|GH-4391c"
	persist := &fakeRepickBackoffPersister{
		consecutiveDrops: 2,
		nextAllowedAt:    time.Now().Add(5 * time.Minute),
		found:            true,
	}

	tr := newRepickBackoffTracker()
	tr.setPersister(persist)

	gated, shouldLog := tr.gateStatus(key)
	if !gated || !shouldLog {
		t.Fatalf("expected a restarted tracker to hydrate the persisted cooldown as gated+logged, got gated=%v shouldLog=%v", gated, shouldLog)
	}
}

// TestRepickBackoffTracker_RecordSuccessClearsPersister verifies
// recordSuccess clears the persister's state, not just the in-memory map —
// otherwise a restart right after a successful dispatch would rehydrate the
// stale pre-success cooldown.
func TestRepickBackoffTracker_RecordSuccessClearsPersister(t *testing.T) {
	tr := newRepickBackoffTracker()
	persist := &fakeRepickBackoffPersister{}
	tr.setPersister(persist)

	key := "proj|GH-4370"
	tr.recordDrop(key)
	tr.recordDrop(key)
	tr.recordSuccess(key)

	if persist.found {
		t.Error("expected recordSuccess to clear the persister's state")
	}
}
