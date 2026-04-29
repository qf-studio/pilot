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

// GH-2432: Retry counter is persisted via labels (pilot-retry-1, pilot-retry-2,
// pilot-retry-exhausted) so the budget survives `pilot start` restarts.

// First retry on a fresh pilot-retry-ready issue should add pilot-retry-1.
func TestPoller_RetryLabel_FirstAttemptAddsRetry1(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}}, CreatedAt: now.Add(-1 * time.Hour)},
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
		if l == LabelRetry1 {
			foundRetry1 = true
		}
	}
	if !foundRetry1 {
		t.Errorf("expected pilot-retry-1 to be added, got labels=%v", added)
	}
}

// pilot-retry-2 + pilot-retry-ready → escalate to pilot-retry-exhausted, no dispatch.
func TestPoller_RetryLabel_Retry2EscalatesToExhausted(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}, {Name: LabelRetry2}}, CreatedAt: now.Add(-1 * time.Hour)},
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
		if l == LabelRetryExhausted {
			foundExhausted = true
		}
	}
	if !foundExhausted {
		t.Errorf("expected pilot-retry-exhausted to be stamped on #42, got %v", added)
	}
}

// pilot-retry-exhausted is terminal — never dispatched.
func TestPoller_RetryLabel_ExhaustedIsTerminal(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Exhausted", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}, {Name: LabelRetryExhausted}}, CreatedAt: now.Add(-1 * time.Hour)},
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

// RetryStateLabels exposes the canonical list for cleanup on merge.
func TestRetryStateLabels_OrderingAndCompleteness(t *testing.T) {
	want := []string{LabelRetry1, LabelRetry2, LabelRetryExhausted}
	if len(RetryStateLabels) != len(want) {
		t.Fatalf("RetryStateLabels len = %d, want %d", len(RetryStateLabels), len(want))
	}
	for i, l := range want {
		if RetryStateLabels[i] != l {
			t.Errorf("RetryStateLabels[%d] = %q, want %q", i, RetryStateLabels[i], l)
		}
	}
}
