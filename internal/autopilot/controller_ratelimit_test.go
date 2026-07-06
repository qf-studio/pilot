package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ProcessAllPRs_RateLimitBackoff verifies that a GitHub
// primary-rate-limit response from GetPullRequest opens a cooldown window
// instead of being retried on every tick. GH-3784: without this, a sustained
// rate-limit window left approved, green PRs unmerged for over an hour
// because every tick re-fetched every tracked PR and burned the little quota
// headroom that existed.
func TestController_ProcessAllPRs_RateLimitBackoff(t *testing.T) {
	var pullRequestCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/42" {
			atomic.AddInt32(&pullRequestCalls, 1)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 123."}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		IssueNumber: 100,
		BranchName:  "pilot/GH-100",
		HeadSHA:     "abc1234",
		Stage:       StageWaitingCI,
	}
	c.mu.Unlock()

	if c.rateLimitCooldownActive() {
		t.Fatal("cooldown should not be active before any request")
	}

	c.processAllPRs(context.Background())

	if calls := atomic.LoadInt32(&pullRequestCalls); calls != 1 {
		t.Fatalf("expected 1 GetPullRequest call on first tick, got %d", calls)
	}
	if !c.rateLimitCooldownActive() {
		t.Fatal("expected rate-limit cooldown to be active after a 403 rate-limit response")
	}

	// A second tick while the cooldown is active must not re-hit the API.
	c.processAllPRs(context.Background())
	if calls := atomic.LoadInt32(&pullRequestCalls); calls != 1 {
		t.Fatalf("expected no additional GetPullRequest calls during cooldown, got %d total", calls)
	}
}

// TestController_ReconcileOrphanPRs_RateLimitBackoff verifies that a
// rate-limit response from ListPullRequests opens the same cooldown window
// the processAllPRs loop honors, so the periodic orphan-PR sweep also stops
// hammering the API during a sustained outage.
func TestController_ReconcileOrphanPRs_RateLimitBackoff(t *testing.T) {
	var listCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			atomic.AddInt32(&listCalls, 1)
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 123."}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.reconcileOrphanPRs(context.Background())
	if calls := atomic.LoadInt32(&listCalls); calls != 1 {
		t.Fatalf("expected 1 ListPullRequests call on first sweep, got %d", calls)
	}
	if !c.rateLimitCooldownActive() {
		t.Fatal("expected rate-limit cooldown to be active after a 403 rate-limit response")
	}

	c.reconcileOrphanPRs(context.Background())
	if calls := atomic.LoadInt32(&listCalls); calls != 1 {
		t.Fatalf("expected no additional ListPullRequests calls during cooldown, got %d total", calls)
	}
}

// TestController_EnterRateLimitCooldown_Bounds verifies the cooldown window
// is floored and capped so a missing Retry-After doesn't hot-loop and a
// runaway header value doesn't stall the controller indefinitely.
func TestController_EnterRateLimitCooldown_Bounds(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	if got := c.enterRateLimitCooldown(0); got != 30*time.Second {
		t.Errorf("floor: enterRateLimitCooldown(0) = %v, want 30s", got)
	}

	c2 := NewController(cfg, ghClient, nil, "owner", "repo")
	if got := c2.enterRateLimitCooldown(2 * time.Hour); got != 20*time.Minute {
		t.Errorf("cap: enterRateLimitCooldown(2h) = %v, want 20m", got)
	}
}
