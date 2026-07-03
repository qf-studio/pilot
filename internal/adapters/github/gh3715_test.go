package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// GH-3715: pilot-failed retry counter is persisted via labels (pilot-failed-retry-1,
// pilot-failed-retry-2, pilot-failed-retry-exhausted) so the budget survives
// `pilot start` restarts, mirroring the GH-2432 pilot-retry-ready mechanism.

// First retry on a fresh pilot-failed issue should add pilot-failed-retry-1.
func TestPoller_FailedRetryLabel_FirstAttemptAddsRetry1(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	var addedLabels atomic.Value
	addedLabels.Store([]string(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			prev, _ := addedLabels.Load().([]string)
			addedLabels.Store(append(prev, body.Labels...))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			return
		case r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithRetryGracePeriod(0))

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil || issue.Number != 42 {
		t.Fatalf("expected #42 to be dispatched, got %v", issue)
	}

	added, _ := addedLabels.Load().([]string)
	foundRetry1 := false
	for _, l := range added {
		if l == LabelFailedRetry1 {
			foundRetry1 = true
		}
	}
	if !foundRetry1 {
		t.Errorf("expected pilot-failed-retry-1 to be added, got labels=%v", added)
	}
}

// pilot-failed-retry-2 + pilot-failed → escalate to pilot-failed-retry-exhausted, no dispatch.
func TestPoller_FailedRetryLabel_Retry2EscalatesToExhausted(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelFailedRetry2}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	var addedLabels atomic.Value
	addedLabels.Store([]string(nil))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			prev, _ := addedLabels.Load().([]string)
			addedLabels.Store(append(prev, body.Labels...))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			return
		case r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithRetryGracePeriod(0))

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("expected #43 to be dispatched (skipping exhausted #42)")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43", issue.Number)
	}

	added, _ := addedLabels.Load().([]string)
	foundExhausted := false
	for _, l := range added {
		if l == LabelFailedRetryExhausted {
			foundExhausted = true
		}
	}
	if !foundExhausted {
		t.Errorf("expected pilot-failed-retry-exhausted to be stamped on #42, got %v", added)
	}
}

// pilot-failed-retry-exhausted is terminal — never dispatched.
func TestPoller_FailedRetryLabel_ExhaustedIsTerminal(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Exhausted", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelFailedRetryExhausted}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithRetryGracePeriod(0))

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil || issue.Number != 43 {
		t.Errorf("expected #43, got %v", issue)
	}
}

// GH-3715 acceptance: the failed-retry count must survive a poller restart.
// Because the counter lives in GitHub labels rather than the in-memory
// failedRetryCount map, a brand-new Poller instance (simulating a daemon
// restart) built against the same issue state must still honor the
// exhausted/limit label — the previous in-memory map would have reset to
// zero on reconstruction and allowed indefinite retries.
func TestPoller_FailedRetryLabel_SurvivesPollerReconstruction(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelFailedRetry2}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/42/labels"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
			return
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			return
		case r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Simulate a crash/restart: this is a fresh Poller with a zero-value
	// failedRetryCount map, exactly as would happen after `pilot start`
	// restarts. The label state (pilot-failed-retry-2) is what must carry
	// the retry budget across the restart.
	restarted, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithRetryGracePeriod(0))
	if got := restarted.failedRetryCount[42]; got != 0 {
		t.Fatalf("sanity check failed: fresh poller's in-memory counter should start at 0, got %d", got)
	}

	// The next retry attempt on #42 escalates it to exhausted rather than
	// granting a 3rd retry — proving the label, not the reset in-memory map,
	// is the source of truth.
	issue, err := restarted.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil || issue.Number != 43 {
		t.Fatalf("expected #43 to be dispatched (restart must not reset #42's retry budget), got %v", issue)
	}
}

// FailedRetryStateLabels exposes the canonical list for cleanup on merge.
func TestFailedRetryStateLabels_OrderingAndCompleteness(t *testing.T) {
	want := []string{LabelFailedRetry1, LabelFailedRetry2, LabelFailedRetryExhausted}
	if len(FailedRetryStateLabels) != len(want) {
		t.Fatalf("FailedRetryStateLabels len = %d, want %d", len(FailedRetryStateLabels), len(want))
	}
	for i, l := range want {
		if FailedRetryStateLabels[i] != l {
			t.Errorf("FailedRetryStateLabels[%d] = %q, want %q", i, FailedRetryStateLabels[i], l)
		}
	}
}
