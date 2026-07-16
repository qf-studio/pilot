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
