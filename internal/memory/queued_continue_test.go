package memory

import (
	"testing"
	"time"
)

// TestGetQueuedTasksForProject_ContinueBranch pins GH-6281: the worker rebuilds a
// queued task from GetQueuedTasksForProject, so the pre-PR hold continuation SHA
// and the inherited task-branch head saved with the row must come back with it.
func TestGetQueuedTasksForProject_ContinueBranch(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	now := time.Now()
	rows := []*Execution{
		{ID: "q-hold", TaskID: "GH-6272", ProjectPath: "/p", Status: "queued", CreatedAt: now,
			TaskBranch: "pilot/GH-6272", ContinueBranchSHA: "b57d141c0ffee0123456789abcdef0123456789a"},
		{ID: "q-stale", TaskID: "GH-6267", ProjectPath: "/p", Status: "pending", CreatedAt: now.Add(time.Second),
			TaskBranch: "pilot/GH-6267"},
		{ID: "q-plain", TaskID: "GH-1", ProjectPath: "/p", Status: "queued", CreatedAt: now.Add(2 * time.Second)},
		{ID: "q-other", TaskID: "GH-2", ProjectPath: "/other", Status: "queued", ContinueBranchSHA: "ffffffffffff"},
	}
	for _, r := range rows {
		if err := store.SaveExecution(r); err != nil {
			t.Fatalf("SaveExecution(%s): %v", r.ID, err)
		}
	}

	// The dispatcher stamps the inherited head after the row is saved.
	if err := store.SetExecutionInheritedHead("q-stale", "deadbeefcafe"); err != nil {
		t.Fatalf("SetExecutionInheritedHead: %v", err)
	}

	got, err := store.GetQueuedTasksForProject("/p", 10)
	if err != nil {
		t.Fatalf("GetQueuedTasksForProject: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want the 3 queued/pending rows of /p", len(got))
	}
	want := map[string][2]string{ // id -> {continue_branch_sha, inherited_head}
		"q-hold":  {"b57d141c0ffee0123456789abcdef0123456789a", ""},
		"q-stale": {"", "deadbeefcafe"},
		"q-plain": {"", ""},
	}
	for _, e := range got {
		w, ok := want[e.ID]
		if !ok {
			t.Errorf("unexpected row %s", e.ID)
			continue
		}
		if e.ContinueBranchSHA != w[0] || e.InheritedHead != w[1] {
			t.Errorf("%s: (ContinueBranchSHA %q, InheritedHead %q), want (%q, %q)",
				e.ID, e.ContinueBranchSHA, e.InheritedHead, w[0], w[1])
		}
	}
}

// TestReclassifyLegacyOutcomes_KeepsNonFastForwardFailed pins GH-6281 item 5: a
// non-fast-forward rejection against the task branch is deterministic. The
// startup backfill must not move it to "infra" (re-pickable) just because the
// error also reads "push failed".
func TestReclassifyLegacyOutcomes_KeepsNonFastForwardFailed(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	nonFF := "push failed after 3 attempt(s): push rejected, non-fast-forward against task branch pilot/GH-6272: " +
		"remote head 3d1fb36 is not a SHA this run pushed (local HEAD 67b1ffd); re-running from origin/main cannot fast-forward it — recovery: branch=pilot/GH-6272 sha=67b1ffd"
	rows := []*Execution{
		{ID: "nonff", TaskID: "GH-6272", ProjectPath: "/p", Status: "failed", Error: nonFF},
		{ID: "plain", TaskID: "GH-12", ProjectPath: "/p", Status: "failed", Error: "push failed after 3 attempt(s): connection reset — recovery: branch=pilot/GH-12 sha=abc"},
	}
	for _, r := range rows {
		if err := store.SaveExecution(r); err != nil {
			t.Fatalf("SaveExecution(%s): %v", r.ID, err)
		}
	}
	if err := store.reclassifyLegacyOutcomes(); err != nil {
		t.Fatalf("reclassifyLegacyOutcomes: %v", err)
	}
	for id, want := range map[string]string{"nonff": "failed", "plain": "infra"} {
		got, err := store.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if got.Status != want {
			t.Errorf("%s status = %q, want %q", id, got.Status, want)
		}
	}
}
