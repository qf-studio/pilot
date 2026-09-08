package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleReviewRequested_SelfCloseMarker_PersistedBeforeTailPersist is the
// GH-5377 regression: PR #5356 (issues #5351/#5361/#5374) persisted the
// self-close marker immediately after markSelfClosed on the CI-fix path
// (handleCIFailed, ~L3869) so a daemon death in that window can't lose it.
// The review-revision path added by GH-5362 and adapted in #5356
// (handleReviewRequested) called markSelfClosed and then ClosePullRequest
// with no persist in between, relying entirely on ProcessPR's tail
// persistPRState call. A daemon death between those two calls lost the
// marker: on restart, checkExternalMergeOrClose would read the own close as
// external and run the destructive relabel/branch-delete path the marker
// exists to prevent (the exact pilot-console #275 incident shape).
//
// This test calls handleReviewRequested directly and deliberately never
// invokes any tail persistPRState — simulating the daemon dying the instant
// handleReviewRequested returns — then reads the marker back from a real,
// file-backed state store to prove handleReviewRequested itself persists it
// rather than depending on the caller. Mirrors
// TestHandleCIFailed_SelfCloseMarker_PersistedBeforeTailPersist
// (gh5361_selfclose_persist_test.go) for the review path.
func TestHandleReviewRequested_SelfCloseMarker_PersistedBeforeTailPersist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/91/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/91/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 701}))
		case r.URL.Path == "/repos/owner/repo/pulls/91" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 91, State: "closed"}))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "gh5377-review-persist-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	memStore, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = memStore.Close() }()

	stateStore, err := NewStateStore(memStore.DB())
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	prState := &PRState{
		PRNumber:    91,
		IssueNumber: 40,
		BranchName:  "pilot/GH-40",
		Stage:       StageReviewRequested,
	}
	c.mu.Lock()
	c.activePRs[91] = prState
	c.mu.Unlock()

	// Deliberately no tail persistPRState call here — simulates the daemon
	// dying the moment handleReviewRequested returns.
	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if prState.SelfClosedFixIssue != 701 {
		t.Fatalf("prState.SelfClosedFixIssue = %d, want 701", prState.SelfClosedFixIssue)
	}

	row, err := stateStore.GetPRState("owner/repo", 91)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row == nil {
		t.Fatal("state store has no row for PR 91 after handleReviewRequested — self-close marker was never persisted, only held in memory")
	}
	if row.SelfClosedFixIssue != 701 {
		t.Fatalf("persisted row SelfClosedFixIssue = %d, want 701 — marker did not survive a simulated daemon death before the tail persist", row.SelfClosedFixIssue)
	}
}
