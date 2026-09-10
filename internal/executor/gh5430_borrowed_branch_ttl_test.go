package executor

import (
	"testing"
	"time"
)

// TestTouchBorrowedBranchStart_RearmsExpiredEntry is the GH-5430 regression
// test for reconciler fix #2 (PR #5427 follow-up): RecordBorrowedBranch runs
// at dispatch time, before the task is admitted into the queue, so a fix task
// that waits in the queue — or itself runs — for more than borrowedBranchTTL
// would reach the PR-created hook with an entry the TTL already treats as
// expired. executeWithOptions calls touchBorrowedBranchStart at the
// queued→running transition to re-stamp recordedAt to that moment instead,
// so a recordedAt older than the TTL at dispatch time still resolves as long
// as the running-start touch itself is within the window.
func TestTouchBorrowedBranchStart_RearmsExpiredEntry(t *testing.T) {
	r := NewRunner()
	r.RecordBorrowedBranch("GH-300", "pilot/GH-103", 5364)

	// Backdate recordedAt past borrowedBranchTTL, simulating a task that sat
	// in the queue (or ran) long enough that the TTL clock, if left running
	// from dispatch time, would already consider the entry expired.
	r.mu.Lock()
	rec := r.borrowedBranch["GH-300"]
	rec.recordedAt = time.Now().Add(-borrowedBranchTTL - time.Hour)
	r.borrowedBranch["GH-300"] = rec
	r.mu.Unlock()

	// The queued→running transition re-arms the TTL from this moment.
	r.touchBorrowedBranchStart("GH-300")

	branch, fromPR, ok := r.BorrowedBranch("GH-300")
	if !ok {
		t.Fatal("BorrowedBranch(\"GH-300\") = not found after touchBorrowedBranchStart re-armed a pre-expired entry, want it to still resolve")
	}
	if branch != "pilot/GH-103" || fromPR != 5364 {
		t.Errorf("BorrowedBranch(\"GH-300\") = (%q, %d), want (%q, %d)", branch, fromPR, "pilot/GH-103", 5364)
	}

	if fixIssue, ok := r.FixIssueForBranch("pilot/GH-103"); !ok || fixIssue != 300 {
		t.Errorf("FixIssueForBranch(\"pilot/GH-103\") = (%d, %v), want (300, true) after the running-start touch re-armed the TTL", fixIssue, ok)
	}
}

// TestTouchBorrowedBranchStart_NoEntry pins that touching an unrecorded task
// ID is a harmless no-op — the common case for tasks that never borrowed a
// branch (GH-5430).
func TestTouchBorrowedBranchStart_NoEntry(t *testing.T) {
	r := NewRunner()
	r.touchBorrowedBranchStart("GH-999")

	if _, _, ok := r.BorrowedBranch("GH-999"); ok {
		t.Error("BorrowedBranch(\"GH-999\") = found after touching an unrecorded task ID, want not-found")
	}
}
