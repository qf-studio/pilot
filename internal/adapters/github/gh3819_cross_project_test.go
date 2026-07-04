package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newGH3819ProjectServer simulates a single-repo GitHub project with one open,
// unlabeled-status issue at the given number. Mirrors the mock pattern used by
// TestPoller_UnmarkProcessed_PermanentFailureRetainsMarker (poller_gh3270_test.go):
// "/search/issues" reports no results, every other path returns the issue.
func newGH3819ProjectServer(t *testing.T, issueNumber int) *httptest.Server {
	t.Helper()
	issue := &github.Issue{
		Number:    issueNumber,
		Title:     fmt.Sprintf("GH-%d fix something", issueNumber),
		State:     "open",
		Labels:    []github.Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/search/issues" {
			_, _ = fmt.Fprintf(w, `{"total_count":0}`)
			return
		}
		_ = json.NewEncoder(w).Encode([]*github.Issue{issue})
	}))
	t.Cleanup(server.Close)
	return server
}

// TestCrossProjectDedup_SameIssueNumberProcessedIndependently guards GH-3819:
// two Pilot projects (different repos) sharing one autopilot.StateStore must
// dispatch/skip their own issue #N independently. Before the fix, adapter_processed
// was keyed on (adapter, issue_id) only, so marking issue #5 done in one repo
// silently clobbered — or was clobbered by — the other repo's row for the same
// issue_id, corrupting both repos' dedup state.
func TestCrossProjectDedup_SameIssueNumberProcessedIndependently(t *testing.T) {
	store, err := autopilot.NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}

	// Project A already processed issue #5 in its own repo before this test's
	// pollers start — simulates a prior poll cycle.
	if err := store.Mark("github", "org/repo-a", "5"); err != nil {
		t.Fatalf("seed Mark(repo-a): %v", err)
	}

	serverA := newGH3819ProjectServer(t, 5)
	serverB := newGH3819ProjectServer(t, 5)

	var dispatchedA, dispatchedB atomic.Bool
	dispatchedBCh := make(chan struct{})

	pollerA, err := github.NewPoller(
		github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverA.URL),
		"org/repo-a", "pilot", time.Hour,
		github.WithOnIssueWithResult(func(ctx context.Context, i *github.Issue) (*github.IssueResult, error) {
			dispatchedA.Store(true)
			return &github.IssueResult{Success: true, PRNumber: 100, PRURL: "https://example.invalid/pr/100"}, nil
		}),
		github.WithProcessedStore(store),
		github.WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller(repo-a): %v", err)
	}

	pollerB, err := github.NewPoller(
		github.NewClientWithBaseURL(testutil.FakeGitHubToken, serverB.URL),
		"org/repo-b", "pilot", time.Hour,
		github.WithOnIssueWithResult(func(ctx context.Context, i *github.Issue) (*github.IssueResult, error) {
			dispatchedB.Store(true)
			close(dispatchedBCh)
			return &github.IssueResult{Success: true, PRNumber: 200, PRURL: "https://example.invalid/pr/200"}, nil
		}),
		github.WithProcessedStore(store),
		github.WithMaxConcurrent(1),
	)
	if err != nil {
		t.Fatalf("NewPoller(repo-b): %v", err)
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	go func() { pollerA.Start(ctxA); close(doneA) }()
	go func() { pollerB.Start(ctxB); close(doneB) }()

	select {
	case <-dispatchedBCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for repo-b to dispatch its own issue #5")
	}
	pollerB.WaitForActive()

	cancelA()
	cancelB()
	<-doneA
	<-doneB

	if dispatchedA.Load() {
		t.Error("repo-a re-dispatched issue #5 — should have stayed skipped (already processed, within grace period)")
	}
	if !dispatchedB.Load() {
		t.Error("repo-b never dispatched its own issue #5 — should be independent of repo-a's processed state")
	}

	// Store-level cross-check: dispatching repo-b's issue #5 must not disturb
	// repo-a's row (the GH-3819 collision would clobber whichever repo's row
	// was written most recently for the shared issue_id).
	okA, err := store.IsProcessed("github", "org/repo-a", "5")
	if err != nil {
		t.Fatalf("IsProcessed(repo-a): %v", err)
	}
	if !okA {
		t.Error("repo-a's issue #5 should still be marked processed after repo-b marked its own issue #5")
	}
	okB, err := store.IsProcessed("github", "org/repo-b", "5")
	if err != nil {
		t.Fatalf("IsProcessed(repo-b): %v", err)
	}
	if !okB {
		t.Error("repo-b's issue #5 should be marked processed after its own dispatch")
	}
}
