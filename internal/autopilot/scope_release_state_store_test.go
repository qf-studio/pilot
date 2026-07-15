package autopilot

import (
	"sync"
	"testing"
)

func TestStateStore_EnqueueScopeRelease_Idempotent(t *testing.T) {
	store := newTestStateStore(t)

	// EnqueueScopeRelease is a dumb CRUD layer — dedupe/sort is the controller
	// wrapper's job (dedupeSortInts), so members are stored in the given order.
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "Checkout epic", []int{5, 10, 20}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	// Second call for the same scope key must be a no-op (INSERT OR IGNORE).
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "different title", []int{99}); err != nil {
		t.Fatalf("EnqueueScopeRelease (second call) failed: %v", err)
	}

	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row == nil {
		t.Fatal("GetScopeRelease returned nil")
	}
	if row.ScopeTitle != "Checkout epic" {
		t.Errorf("ScopeTitle = %q, want %q (second enqueue must not clobber)", row.ScopeTitle, "Checkout epic")
	}
	if row.State != "pending" {
		t.Errorf("State = %q, want pending", row.State)
	}
	if row.AnchorPR != 20 {
		t.Errorf("AnchorPR = %d, want 20 (highest member)", row.AnchorPR)
	}
	wantMembers := []int{5, 10, 20}
	if len(row.MemberPRs) != len(wantMembers) {
		t.Fatalf("MemberPRs = %v, want %v", row.MemberPRs, wantMembers)
	}
	for i, m := range wantMembers {
		if row.MemberPRs[i] != m {
			t.Errorf("MemberPRs[%d] = %d, want %d", i, row.MemberPRs[i], m)
		}
	}
}

func TestStateStore_ClaimScopeRelease_ExactlyOneWinner(t *testing.T) {
	store := newTestStateStore(t)
	// A SQLite ":memory:" DB is per-connection: without capping the pool to a
	// single connection, concurrent goroutines below can each open a fresh
	// connection backed by its own empty in-memory database, missing the
	// schema entirely. Real usage opens one long-lived *sql.DB per daemon, so
	// this only matters for the concurrent-access shape this test exercises.
	store.db.SetMaxOpenConns(1)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{1, 2, 3}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := store.ClaimScopeRelease("owner/repo", "epic:1")
			if err != nil {
				t.Errorf("ClaimScopeRelease failed: %v", err)
				return
			}
			results[i] = claimed
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, r := range results {
		if r {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1 (results: %v)", winners, results)
	}

	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "releasing" {
		t.Errorf("State = %q, want releasing", row.State)
	}
}

func TestStateStore_MarkScopeReleaseDone(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := store.ClaimScopeRelease("owner/repo", "epic:1"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}
	if err := store.MarkScopeReleaseDone("owner/repo", "epic:1", "v1.2.0", "deadbeef"); err != nil {
		t.Fatalf("MarkScopeReleaseDone failed: %v", err)
	}

	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "done" {
		t.Errorf("State = %q, want done", row.State)
	}
	if row.Tag != "v1.2.0" {
		t.Errorf("Tag = %q, want v1.2.0", row.Tag)
	}
	if row.FinalSHA != "deadbeef" {
		t.Errorf("FinalSHA = %q, want deadbeef", row.FinalSHA)
	}
}

func TestStateStore_MarkScopeReleasePending_AttemptsIncrement(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := store.ClaimScopeRelease("owner/repo", "epic:1"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}

	// incrementAttempts=false: crash-recovery re-drive, attempts unchanged.
	if err := store.MarkScopeReleasePending("owner/repo", "epic:1", false, ""); err != nil {
		t.Fatalf("MarkScopeReleasePending(false) failed: %v", err)
	}
	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "pending" || row.Attempts != 0 {
		t.Errorf("state/attempts = %q/%d, want pending/0", row.State, row.Attempts)
	}

	// incrementAttempts=true: genuine carrier failure, attempts bumps and
	// last_failed_sha is recorded.
	if err := store.MarkScopeReleasePending("owner/repo", "epic:1", true, "redsha1"); err != nil {
		t.Fatalf("MarkScopeReleasePending(true) failed: %v", err)
	}
	row, err = store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", row.Attempts)
	}
	if row.LastFailedSHA != "redsha1" {
		t.Errorf("LastFailedSHA = %q, want redsha1", row.LastFailedSHA)
	}
}

func TestStateStore_MarkScopeReleaseFailed(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if err := store.MarkScopeReleaseFailed("owner/repo", "epic:1"); err != nil {
		t.Fatalf("MarkScopeReleaseFailed failed: %v", err)
	}
	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "failed" {
		t.Errorf("State = %q, want failed", row.State)
	}

	// A failed scope must not be claimable again.
	claimed, err := store.ClaimScopeRelease("owner/repo", "epic:1")
	if err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}
	if claimed {
		t.Error("claimed a failed scope release row, want not claimable")
	}
}

func TestStateStore_ListScopeReleases(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic1", []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if err := store.EnqueueScopeRelease("owner/repo", "epic:2", "epic2", []int{2}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if err := store.EnqueueScopeRelease("owner/other-repo", "epic:1", "other epic", []int{3}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := store.ClaimScopeRelease("owner/repo", "epic:2"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}

	pending, err := store.ListScopeReleases("owner/repo", "pending")
	if err != nil {
		t.Fatalf("ListScopeReleases failed: %v", err)
	}
	if len(pending) != 1 || pending[0].ScopeKey != "epic:1" {
		t.Errorf("pending rows = %+v, want exactly [epic:1]", pending)
	}

	releasing, err := store.ListScopeReleases("owner/repo", "releasing")
	if err != nil {
		t.Fatalf("ListScopeReleases failed: %v", err)
	}
	if len(releasing) != 1 || releasing[0].ScopeKey != "epic:2" {
		t.Errorf("releasing rows = %+v, want exactly [epic:2]", releasing)
	}
}

func TestStateStore_ScopeMemberPending(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{10, 20}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	pending, err := store.ScopeMemberPending("owner/repo", 10)
	if err != nil {
		t.Fatalf("ScopeMemberPending failed: %v", err)
	}
	if !pending {
		t.Error("ScopeMemberPending(10) = false, want true (member of pending scope)")
	}

	pending, err = store.ScopeMemberPending("owner/repo", 30)
	if err != nil {
		t.Fatalf("ScopeMemberPending failed: %v", err)
	}
	if pending {
		t.Error("ScopeMemberPending(30) = true, want false (not a member)")
	}

	if err := store.MarkScopeReleaseDone("owner/repo", "epic:1", "v1.0.0", "sha"); err != nil {
		t.Fatalf("MarkScopeReleaseDone failed: %v", err)
	}
	pending, err = store.ScopeMemberPending("owner/repo", 10)
	if err != nil {
		t.Fatalf("ScopeMemberPending failed: %v", err)
	}
	if pending {
		t.Error("ScopeMemberPending(10) = true after done, want false (done is terminal, not pending/releasing)")
	}
}
