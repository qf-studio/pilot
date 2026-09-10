package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestReconcileOrphanPRs_ClosedOriginSkipRevalidatedOnCadence_ReopenedIssueRegistered
// is the GH-5430 regression test for reconciler fix #3 (PR #5427 follow-up
// gap #3): closedOriginSkip (GH-5425) memoizes a skip decision forever —
// pruneClosedOriginSkip only drops it once the PR itself closes — so a PR
// memoized while its origin issue was closed stayed skipped even after a
// human reopened that issue to continue the work by hand. Every
// closedOriginRecheckCadence ticks, reconcileOrphanPRs must re-fetch the
// origin issue for each memoized PR and drop the memo (and register the PR,
// in the same tick) if the issue is open again.
func TestReconcileOrphanPRs_ClosedOriginSkipRevalidatedOnCadence_ReopenedIssueRegistered(t *testing.T) {
	var issueCalls int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			prs := []*github.PullRequest{
				{
					Number:  42,
					HTMLURL: "https://github.com/owner/repo/pull/42",
					Head:    github.PRRef{Ref: "pilot/GH-100", SHA: "abc1234"},
					Base:    github.PRRef{Ref: "main"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case "/repos/owner/repo/issues/100":
			n := atomic.AddInt64(&issueCalls, 1)
			state := github.StateOpen
			if n == 1 {
				// First check (the memoizing tick): origin issue is closed.
				state = github.StateClosed
			}
			// Every subsequent check (the cadence revalidation, and the
			// same-tick registration check that follows it): reopened.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(&github.Issue{Number: 100, State: state})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Tick 1: origin issue 100 is closed, no fix-issue record exists —
	// reconciler memoizes the skip and does not register PR 42.
	c.reconcileOrphanPRs(context.Background())

	if _, ok := c.GetPRState(42); ok {
		t.Fatal("PR 42 was registered on the memoizing tick, want it skipped (origin issue closed)")
	}
	if _, ok := c.closedOriginSkipDecision(42); !ok {
		t.Fatal("closedOriginSkip has no memo for PR 42 after the memoizing tick")
	}

	// Fast-forward to just before the next cadence boundary without actually
	// running closedOriginRecheckCadence-1 ticks (white-box: same package).
	c.mu.Lock()
	c.reconcileTick = closedOriginRecheckCadence - 1
	c.mu.Unlock()

	// Tick 30: crosses the cadence boundary. revalidateClosedOriginSkip
	// re-fetches issue 100 (now open), drops the memo, and the main loop —
	// running in this same call — finds no memo, re-checks GitHub directly,
	// and registers PR 42 under the now-open origin issue.
	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("reconciler did not register PR 42 after its origin issue was reopened on the cadence tick")
	}
	if pr.IssueNumber != 100 {
		t.Errorf("IssueNumber = %d, want 100", pr.IssueNumber)
	}
	if _, ok := c.closedOriginSkipDecision(42); ok {
		t.Error("closedOriginSkip still has a memo for PR 42 after it was registered, want it cleared")
	}
}

// TestReconcileOrphanPRs_ClosedOriginSkipNotRevalidatedOffCadence pins that
// the memo survives ticks that don't land on closedOriginRecheckCadence — the
// whole point of memoizing in the first place (GH-5425) is to avoid the
// GetIssue+Warn cost on every tick, and GH-5430's periodic revalidation must
// not silently turn back into a per-tick check.
func TestReconcileOrphanPRs_ClosedOriginSkipNotRevalidatedOffCadence(t *testing.T) {
	var issueCalls int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			prs := []*github.PullRequest{
				{
					Number:  42,
					HTMLURL: "https://github.com/owner/repo/pull/42",
					Head:    github.PRRef{Ref: "pilot/GH-100", SHA: "abc1234"},
					Base:    github.PRRef{Ref: "main"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case "/repos/owner/repo/issues/100":
			atomic.AddInt64(&issueCalls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(&github.Issue{Number: 100, State: github.StateClosed})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Tick 1: memoizes the skip (1 GetIssue call).
	c.reconcileOrphanPRs(context.Background())
	// Tick 2: off-cadence — must be served entirely from the memo.
	c.reconcileOrphanPRs(context.Background())

	if calls := atomic.LoadInt64(&issueCalls); calls != 1 {
		t.Errorf("GetIssue was called %d times across 2 off-cadence ticks, want 1 (second tick must be served from the memo)", calls)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Fatal("PR 42 was registered on an off-cadence tick, want it to stay skipped")
	}
}
