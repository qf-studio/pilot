package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ReconcileOrphanPRs_ClosedOriginSkip_MemoizedAcrossTicks is
// the GH-5425 regression test for reconciler cost #1 (PR #5423 follow-up): a
// PR whose origin issue is closed with no fix-issue record stays untracked
// forever, so before this fix every 60s reconciler tick repeated the same
// GetIssue call and the same Warn log for it — roughly 1,440 of each per day
// for one wedged PR. The first tick must still call GetIssue and Warn once;
// every later tick must skip the GetIssue call entirely and log at Debug.
func TestController_ReconcileOrphanPRs_ClosedOriginSkip_MemoizedAcrossTicks(t *testing.T) {
	var issueRequests int32
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
			atomic.AddInt32(&issueRequests, 1)
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
	var logBuf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Tick 1: no memo yet — must call GetIssue and Warn.
	c.reconcileOrphanPRs(context.Background())
	if n := atomic.LoadInt32(&issueRequests); n != 1 {
		t.Fatalf("GetIssue calls after tick 1 = %d, want 1", n)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Fatal("PR 42 must not be registered — origin issue is closed with no fix-issue record")
	}
	warnCount := strings.Count(logBuf.String(), "level=WARN")
	if warnCount != 1 {
		t.Fatalf("Warn log lines after tick 1 = %d, want 1; log:\n%s", warnCount, logBuf.String())
	}

	// Tick 2 (and a 3rd, for good measure): the skip decision is memoized —
	// no additional GetIssue call, no additional Warn, only a Debug log.
	c.reconcileOrphanPRs(context.Background())
	c.reconcileOrphanPRs(context.Background())
	if n := atomic.LoadInt32(&issueRequests); n != 1 {
		t.Errorf("GetIssue calls after 3 ticks = %d, want 1 (memoized on ticks 2-3)", n)
	}
	if warnCount := strings.Count(logBuf.String(), "level=WARN"); warnCount != 1 {
		t.Errorf("Warn log lines after 3 ticks = %d, want 1 (still just tick 1's)", warnCount)
	}
	if debugCount := strings.Count(logBuf.String(), "level=DEBUG"); debugCount != 2 {
		t.Errorf("Debug log lines after 3 ticks = %d, want 2 (ticks 2 and 3)", debugCount)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR 42 must still not be registered after memoized ticks")
	}
}

// TestController_ReconcileOrphanPRs_ClosedOriginSkip_EvictedWhenPRCloses is
// the GH-5425 regression test for reconciler cost #1's bound: a memoized skip
// decision must not survive the PR itself closing, or the map backing it
// would grow forever across the life of the repo. Once the PR no longer
// appears in the open-PR list, its memo entry must be pruned.
func TestController_ReconcileOrphanPRs_ClosedOriginSkip_EvictedWhenPRCloses(t *testing.T) {
	var prOpen atomic.Bool
	prOpen.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			var prs []*github.PullRequest
			if prOpen.Load() {
				prs = []*github.PullRequest{
					{
						Number:  42,
						HTMLURL: "https://github.com/owner/repo/pull/42",
						Head:    github.PRRef{Ref: "pilot/GH-100", SHA: "abc1234"},
						Base:    github.PRRef{Ref: "main"},
					},
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case "/repos/owner/repo/issues/100":
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

	// Tick 1: PR open, origin closed, no fix record — memoized skip.
	c.reconcileOrphanPRs(context.Background())
	c.mu.RLock()
	_, memoized := c.closedOriginSkip[42]
	c.mu.RUnlock()
	if !memoized {
		t.Fatal("expected PR 42's closed-origin skip to be memoized after tick 1")
	}

	// PR 42 closes (no longer in the open-PR list).
	prOpen.Store(false)
	c.reconcileOrphanPRs(context.Background())

	c.mu.RLock()
	_, stillMemoized := c.closedOriginSkip[42]
	mapLen := len(c.closedOriginSkip)
	c.mu.RUnlock()
	if stillMemoized {
		t.Error("PR 42's skip memo must be evicted once the PR is no longer open, so the map does not grow forever")
	}
	if mapLen != 0 {
		t.Errorf("closedOriginSkip has %d entries after the only memoized PR closed, want 0", mapLen)
	}
}
