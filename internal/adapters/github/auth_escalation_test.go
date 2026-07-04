package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
)

// fakeAlertProcessor implements executor.AlertEventProcessor for GH-3839 tests.
type fakeAlertProcessor struct {
	mu     sync.Mutex
	events []executor.AlertEvent
}

func (f *fakeAlertProcessor) ProcessEvent(e executor.AlertEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeAlertProcessor) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakeAlertProcessor) last() executor.AlertEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[len(f.events)-1]
}

// TestPoller_AuthFailureEscalation_AlertsAfterThreshold simulates a dead
// GitHub token (persistent 401) and asserts the poller escalates only once
// the consecutive-failure count reaches the configured threshold, naming the
// token source in the emitted alert (GH-3839).
func TestPoller_AuthFailureEscalation_AlertsAfterThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	alertProc := &fakeAlertProcessor{}
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithAlertProcessor(alertProc),
		WithTokenSource("env GITHUB_TOKEN"),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	for i := 0; i < 2; i++ {
		poller.checkForNewIssues(context.Background())
		if got := alertProc.count(); got != 0 {
			t.Fatalf("after %d consecutive auth failure(s): alert fired early (count=%d)", i+1, got)
		}
	}

	poller.checkForNewIssues(context.Background())
	if got := alertProc.count(); got != 1 {
		t.Fatalf("after 3rd consecutive auth failure: got %d alerts, want 1", got)
	}

	ev := alertProc.last()
	if ev.Type != executor.AlertEventTypeConfigError {
		t.Errorf("alert type = %v, want AlertEventTypeConfigError", ev.Type)
	}
	if ev.Metadata["token_source"] != "env GITHUB_TOKEN" {
		t.Errorf("token_source metadata = %q, want %q", ev.Metadata["token_source"], "env GITHUB_TOKEN")
	}
	if ev.Metadata["consecutive_failures"] != "3" {
		t.Errorf("consecutive_failures metadata = %q, want %q", ev.Metadata["consecutive_failures"], "3")
	}
}

// TestPoller_AuthFailureEscalation_TransientErrorsDontTrip asserts that
// non-auth transient errors (5xx) neither increment the auth-failure counter
// nor fire an alert, however many times they occur (GH-3839).
func TestPoller_AuthFailureEscalation_TransientErrorsDontTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	alertProc := &fakeAlertProcessor{}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithAlertProcessor(alertProc))

	for i := 0; i < 5; i++ {
		poller.checkForNewIssues(context.Background())
	}

	if got := alertProc.count(); got != 0 {
		t.Errorf("transient 500s fired %d alerts, want 0", got)
	}
	if got := poller.consecutiveAuthFailures.Load(); got != 0 {
		t.Errorf("consecutiveAuthFailures = %d, want 0 (500s are not auth errors)", got)
	}
}

// TestPoller_AuthFailureEscalation_RateLimitedDoesNotTrip asserts that a
// rate-limited 403 (the existing #3798 backoff path) never increments the
// auth-failure counter, even repeated many times.
func TestPoller_AuthFailureEscalation_RateLimitedDoesNotTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	alertProc := &fakeAlertProcessor{}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithAlertProcessor(alertProc))

	for i := 0; i < 5; i++ {
		poller.checkForNewIssues(context.Background())
	}

	if got := alertProc.count(); got != 0 {
		t.Errorf("rate-limited 403s fired %d alerts, want 0", got)
	}
	if got := poller.consecutiveAuthFailures.Load(); got != 0 {
		t.Errorf("consecutiveAuthFailures = %d, want 0 (rate-limit 403s stay on the #3798 backoff path)", got)
	}
}

// TestPoller_AuthFailureEscalation_ResetsOnSuccess asserts a successful fetch
// clears the streak, so a fresh run of consecutive failures is required
// before escalating again (GH-3839).
func TestPoller_AuthFailureEscalation_ResetsOnSuccess(t *testing.T) {
	var authMode atomic.Bool
	authMode.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authMode.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	alertProc := &fakeAlertProcessor{}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithAlertProcessor(alertProc))

	poller.checkForNewIssues(context.Background())
	poller.checkForNewIssues(context.Background())
	if got := poller.consecutiveAuthFailures.Load(); got != 2 {
		t.Fatalf("after 2 auth failures, counter = %d, want 2", got)
	}

	authMode.Store(false)
	poller.checkForNewIssues(context.Background())
	if got := poller.consecutiveAuthFailures.Load(); got != 0 {
		t.Fatalf("after a successful fetch, counter = %d, want 0", got)
	}

	authMode.Store(true)
	poller.checkForNewIssues(context.Background())
	poller.checkForNewIssues(context.Background())
	if got := alertProc.count(); got != 0 {
		t.Fatalf("after reset, 2 failures should not yet re-trigger; got %d alerts", got)
	}
	poller.checkForNewIssues(context.Background())
	if got := alertProc.count(); got != 1 {
		t.Errorf("a fresh 3-streak after reset should fire exactly 1 alert; got %d", got)
	}
}

// TestPoller_RecoverOrphanedIssues_AuthFailureEscalates asserts the startup
// orphan-recovery fetch path also escalates on a dead token (GH-3839).
func TestPoller_RecoverOrphanedIssues_AuthFailureEscalates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	alertProc := &fakeAlertProcessor{}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithAlertProcessor(alertProc))

	for i := 0; i < 3; i++ {
		poller.recoverOrphanedIssues(context.Background())
	}

	if got := alertProc.count(); got != 1 {
		t.Errorf("recoverOrphanedIssues: got %d alerts after 3 consecutive 401s, want 1", got)
	}
}

// TestIsAuthFetchError covers the auth-vs-transient classification directly.
func TestIsAuthFetchError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"AuthError (401)", &AuthError{Message: "bad creds"}, true},
		{"RateLimitError (403 rate-limited)", &RateLimitError{StatusCode: 403, Message: "rate limit exceeded"}, false},
		{"RateLimitError (429)", &RateLimitError{StatusCode: 429, Message: "too many requests"}, false},
		{"generic 403 (non-rate-limit forbidden)", errors.New("API error (status 403): insufficient scope"), true},
		{"generic 500", errors.New("API error (status 500): internal error"), false},
		{"network error", errors.New("dial tcp: connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthFetchError(tt.err); got != tt.want {
				t.Errorf("isAuthFetchError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
