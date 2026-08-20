package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestUnwindGithubStartedLabel_GH5028 reproduces the GH-5028 label-loss
// shape end to end at the behavioral level: an issue is queued and picked
// (it carries "pilot"), notifyTaskStartedSDK applies "pilot-in-progress",
// and the dispatch pickup that follows is then dropped (repick backoff /
// claim lost) with no execution ever landing — zero PR, zero ledger
// progress. That is exactly the live incident: issue queued 16:35Z, then
// vanished from the poller's queue with "pilot" gone, sitting invisible
// until an operator manually restored the label.
//
// The unwind that fires in that case must remove only the label
// notifyTaskStartedSDK actually applied (githubSDK.LabelInProgress), never
// the poller's own trigger label ("pilot") — removing the trigger label is
// what stranded the issue. Before the GH-5042 fix, the unwind call in
// handleGithubIssueEventSDK removed `pilotLabel` (the trigger label)
// instead of githubSDK.LabelInProgress, so this test fails against that
// code: the DELETE it observes targets "pilot", not "pilot-in-progress".
func TestUnwindGithubStartedLabel_GH5028(t *testing.T) {
	var mu sync.Mutex
	var removed []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if parts := strings.SplitN(r.URL.Path, "/labels/", 2); len(parts) == 2 {
				mu.Lock()
				removed = append(removed, parts[1])
				mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	client := githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Simulates the queued issue's pilot label already being present, and
	// notifyTaskStartedSDK having already applied pilot-in-progress before
	// the dispatch pickup was dropped.
	if err := unwindGithubStartedLabel(context.Background(), client, "owner", "repo", 5028); err != nil {
		t.Fatalf("unwindGithubStartedLabel returned unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	for _, l := range removed {
		if l == "pilot" {
			t.Fatalf("unwind must never remove the trigger label %q — a queued-but-never-dispatched issue must stay pollable (GH-5028), removed=%v", "pilot", removed)
		}
	}

	found := false
	for _, l := range removed {
		if l == githubSDK.LabelInProgress {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the unwind to remove %q, got removed=%v", githubSDK.LabelInProgress, removed)
	}
}
