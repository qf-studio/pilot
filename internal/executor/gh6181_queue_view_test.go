package executor

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestDispatcher_GetQueuedTaskIDs pins GH-6181: queued and pending rows are
// reported (any project, no worker needed); running and completed rows are not.
func TestDispatcher_GetQueuedTaskIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	d := NewDispatcher(store, NewRunner(), nil)

	for id, status := range map[string]string{"GH-1": "queued", "GH-2": "pending", "GH-3": "running", "GH-4": "completed"} {
		if err := store.SaveExecution(&memory.Execution{ID: "exec-" + id, TaskID: id, ProjectPath: "/proj-" + id, Status: status, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	got := d.GetQueuedTaskIDs()
	sort.Strings(got)
	if fmt.Sprint(got) != "[GH-1 GH-2]" {
		t.Errorf("GetQueuedTaskIDs = %v, want [GH-1 GH-2]", got)
	}
}

// TestRunner_BorrowedBranches pins GH-6181: a non-destructive listing that
// skips entries past borrowedBranchTTL and survives repeated calls.
func TestRunner_BorrowedBranches(t *testing.T) {
	r := NewRunner()
	r.RecordBorrowedBranch("GH-200", "pilot/GH-100", 5)
	r.RecordBorrowedBranch("GH-201", "pilot/GH-101", 6)
	r.mu.Lock()
	rec := r.borrowedBranch["GH-201"]
	rec.recordedAt = time.Now().Add(-borrowedBranchTTL - time.Minute)
	r.borrowedBranch["GH-201"] = rec
	r.mu.Unlock()

	for i := 0; i < 2; i++ {
		if got := r.BorrowedBranches(); fmt.Sprint(got) != "[pilot/GH-100]" {
			t.Fatalf("call %d: BorrowedBranches = %v, want [pilot/GH-100]", i, got)
		}
	}
	if _, ok := r.FixIssueForBranch("pilot/GH-100"); !ok {
		t.Error("listing consumed the registry entry")
	}
}
