package executor

import (
	"context"
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

// TestExecuteWithOptions_TouchesBorrowedBranchStart is the GH-5432 wiring
// pin: TestTouchBorrowedBranchStart_RearmsExpiredEntry above exercises
// touchBorrowedBranchStart directly, so it would keep passing even if the
// r.touchBorrowedBranchStart(task.ID) call were deleted from
// executeWithOptions (runner.go) — the exact PR #5431 mutation-testing gap
// this test closes. This one drives the real Runner.Execute -> executeWithOptions
// path (mirroring the setupPRGuardRepo/mockFixedBackend pattern used by the
// GH-5359 tests) for a task whose borrowed-branch entry was recorded more
// than borrowedBranchTTL ago, then asserts the entry still resolves once
// execution has started — which is only true if executeWithOptions actually
// calls touchBorrowedBranchStart at the queued→running transition.
func TestExecuteWithOptions_TouchesBorrowedBranchStart(t *testing.T) {
	const taskID = "GH-5432"
	const branch = "pilot/GH-5432-branch"
	// No additional commit: the PR guard's fast confirmed-empty no_op path
	// resolves without shelling out to gh, keeping this test hermetic while
	// still exercising the real executeWithOptions entry sequence.
	dir := setupPRGuardRepo(t, branch, false)

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "analysis complete"},
	}
	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	runner.RecordBorrowedBranch(taskID, "pilot/GH-100", 5364)
	// Backdate recordedAt past borrowedBranchTTL, as if this task's own
	// dispatch-time record sat unrefreshed for longer than the TTL window.
	runner.mu.Lock()
	rec := runner.borrowedBranch[taskID]
	rec.recordedAt = time.Now().Add(-borrowedBranchTTL - time.Hour)
	runner.borrowedBranch[taskID] = rec
	runner.mu.Unlock()

	task := &Task{
		ID:          taskID,
		Title:       "test: pin touchBorrowedBranchStart wiring in executeWithOptions",
		Description: "GH-5432 regression: deleting the touch call must fail this test",
		ProjectPath: dir,
		Branch:      branch,
		CreatePR:    true,
	}

	if _, err := runner.Execute(context.Background(), task); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	gotBranch, fromPR, ok := runner.BorrowedBranch(taskID)
	if !ok {
		t.Fatal("BorrowedBranch(taskID) = not found after Execute() ran a pre-expired entry through it — executeWithOptions did not re-stamp recordedAt via touchBorrowedBranchStart")
	}
	if gotBranch != "pilot/GH-100" || fromPR != 5364 {
		t.Errorf("BorrowedBranch(taskID) = (%q, %d), want (%q, %d)", gotBranch, fromPR, "pilot/GH-100", 5364)
	}
}
