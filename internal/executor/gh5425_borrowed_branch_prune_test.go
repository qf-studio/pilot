package executor

import (
	"testing"
	"time"
)

// TestFixIssueForBranch_ConsumesEntryOnMatch is the GH-5425 regression test
// for the registry's reverse-index pruning: FixIssueForBranch's own
// successful lookup is treated as "the recording task's PR-created hook has
// fired" (see that method's doc for why the alternative — clearing on the
// task's own terminal-state signal — can't work, since that signal always
// fires before the hook does). The entry must therefore be gone immediately
// after one successful match, so a later, unrelated PR pushed to the same
// borrowed branch is never mis-attributed to the now-consumed fix issue.
func TestFixIssueForBorrowedBranch_ConsumesEntryOnMatch(t *testing.T) {
	r := NewRunner()
	r.RecordBorrowedBranch("GH-200", "pilot/GH-100", 5361)

	fixIssue, ok := r.FixIssueForBranch("pilot/GH-100")
	if !ok {
		t.Fatal("FixIssueForBranch(\"pilot/GH-100\") = not found on first (hook-fired) lookup, want fix issue 200")
	}
	if fixIssue != 200 {
		t.Errorf("fixIssue = %d, want 200", fixIssue)
	}

	// The recording task's execution has, from this consumer's perspective,
	// now finished and the hook has fired — a second lookup for the same
	// branch must report not-found rather than repeating the same match.
	if fixIssue, ok := r.FixIssueForBranch("pilot/GH-100"); ok {
		t.Errorf("FixIssueForBranch(\"pilot/GH-100\") = (%d, true) on second lookup, want not-found (entry must be consumed after first match)", fixIssue)
	}

	// The forward record must be gone too, not just the reverse index.
	if branch, fromPR, ok := r.BorrowedBranch("GH-200"); ok {
		t.Errorf("BorrowedBranch(\"GH-200\") = (%q, %d, true) after FixIssueForBranch consumed the entry, want not-found", branch, fromPR)
	}
}

// TestFixIssueForBranch_ExpiresUnconsumedEntryAfterTTL is the GH-5425
// regression test for the registry's TTL fallback: a task that borrows a
// branch but never gets a PR-created hook to consume the record (declined,
// failed, or the hook simply never fires) must not leave the mapping around
// forever — that staleness is exactly what let a later, unrelated PR on the
// same branch get mis-attributed to a long-dead fix issue (GH-5425).
func TestFixIssueForBorrowedBranch_ExpiresUnconsumedEntryAfterTTL(t *testing.T) {
	r := NewRunner()
	r.RecordBorrowedBranch("GH-201", "pilot/GH-101", 5362)

	// Backdate the entry past borrowedBranchTTL without ever consuming it,
	// simulating a task that ended without its PR-created hook ever firing.
	r.mu.Lock()
	rec := r.borrowedBranch["GH-201"]
	rec.recordedAt = time.Now().Add(-borrowedBranchTTL - time.Minute)
	r.borrowedBranch["GH-201"] = rec
	r.mu.Unlock()

	if fixIssue, ok := r.FixIssueForBranch("pilot/GH-101"); ok {
		t.Errorf("FixIssueForBranch(\"pilot/GH-101\") = (%d, true) for an entry older than borrowedBranchTTL, want not-found", fixIssue)
	}

	r.mu.Lock()
	_, stillPresent := r.borrowedBranch["GH-201"]
	_, reverseStillPresent := r.borrowedBranchByBranch["pilot/GH-101"]
	r.mu.Unlock()
	if stillPresent || reverseStillPresent {
		t.Error("expired entry was reported not-found but not pruned from the registry")
	}
}

// TestBorrowedBranch_NonDestructivePeek pins that BorrowedBranch — the
// primary SDK-hook consumer, unlike the reconciler's FixIssueForBranch — does
// NOT delete the entry on a successful read. The SDK poller can redeliver
// the same PRCreatedEvent, and a destructive read here would break that
// retry by silently falling through to the plain OnPRCreated path on
// redelivery (GH-5425).
func TestBorrowedBranch_NonDestructivePeek(t *testing.T) {
	r := NewRunner()
	r.RecordBorrowedBranch("GH-202", "pilot/GH-102", 5363)

	if _, _, ok := r.BorrowedBranch("GH-202"); !ok {
		t.Fatal("BorrowedBranch(\"GH-202\") = not found on first read, want a match")
	}
	branch, fromPR, ok := r.BorrowedBranch("GH-202")
	if !ok {
		t.Fatal("BorrowedBranch(\"GH-202\") = not found on second (redelivery) read, want the entry to survive a non-destructive peek")
	}
	if branch != "pilot/GH-102" || fromPR != 5363 {
		t.Errorf("BorrowedBranch(\"GH-202\") = (%q, %d), want (%q, %d)", branch, fromPR, "pilot/GH-102", 5363)
	}
}
