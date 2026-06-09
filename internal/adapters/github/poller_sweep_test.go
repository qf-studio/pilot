package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// sweepTestServer returns an httptest server that serves the given issues for
// GET /issues and records pilot-in-progress DELETEs into removed.
func sweepTestServer(t *testing.T, issues []*Issue, removed *[]int, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/repos/owner/repo/issues" {
				_ = json.NewEncoder(w).Encode(issues)
			}
		case http.MethodDelete:
			// Path: /repos/owner/repo/issues/{n}/labels/pilot-in-progress
			var n int
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d/labels/"+LabelInProgress, &n); err == nil {
				mu.Lock()
				*removed = append(*removed, n)
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// TASK-354: a stranded in-progress issue (goroutine gone, no terminal label) is
// swept — label removed AND re-armed for pickup.
func TestPoller_SweepStranded_RemovesAndRearms(t *testing.T) {
	issues := []*Issue{
		{Number: 123, Title: "Stranded", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}},
	}
	removed := []int{}
	var mu sync.Mutex
	server := sweepTestServer(t, issues, &removed, &mu)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	poller.markProcessed(123) // simulate retained marker from a no_op

	poller.sweepStrandedIssues(context.Background())

	mu.Lock()
	gotRemoved := append([]int(nil), removed...)
	mu.Unlock()
	if len(gotRemoved) != 1 || gotRemoved[0] != 123 {
		t.Fatalf("expected pilot-in-progress removed from #123, got %v", gotRemoved)
	}
	poller.mu.RLock()
	_, stillProcessed := poller.processed[123]
	poller.mu.RUnlock()
	if stillProcessed {
		t.Error("expected #123 to be re-armed (unmarkProcessed) when no terminal label present")
	}
}

// TASK-354: an in-flight issue is never swept (live execution protected).
func TestPoller_SweepStranded_SkipsInFlight(t *testing.T) {
	issues := []*Issue{
		{Number: 123, Title: "Running", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}},
	}
	removed := []int{}
	var mu sync.Mutex
	server := sweepTestServer(t, issues, &removed, &mu)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	poller.markInFlight(123) // live execution

	poller.sweepStrandedIssues(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(removed) != 0 {
		t.Errorf("expected no label removal for in-flight issue, got %v", removed)
	}
}

// TASK-354: an in-progress + terminal-label contradiction is cleaned up (label
// removed) but NOT re-armed — a deterministically-failed issue must not re-loop.
func TestPoller_SweepStranded_TerminalLabelNoRearm(t *testing.T) {
	issues := []*Issue{
		{Number: 123, Title: "Blocked leftover", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}, {Name: LabelBlocked}}},
	}
	removed := []int{}
	var mu sync.Mutex
	server := sweepTestServer(t, issues, &removed, &mu)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	poller.markProcessed(123)

	poller.sweepStrandedIssues(context.Background())

	mu.Lock()
	gotRemoved := append([]int(nil), removed...)
	mu.Unlock()
	if len(gotRemoved) != 1 || gotRemoved[0] != 123 {
		t.Fatalf("expected pilot-in-progress cleaned up on #123, got %v", gotRemoved)
	}
	poller.mu.RLock()
	_, stillProcessed := poller.processed[123]
	poller.mu.RUnlock()
	if !stillProcessed {
		t.Error("expected #123 to stay processed (no re-arm) when a terminal label is present")
	}
}
