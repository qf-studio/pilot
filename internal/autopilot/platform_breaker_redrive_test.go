package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestReDriveBreakerHeldPRs_RevivesHeldPR covers the GH-4792 acceptance
// criterion "held PRs re-driven exactly once on close": a PR parked by the
// platform-outage breaker (StageFailed + BreakerHoldActive, see
// handleCIFailed) must re-enter StageWaitingCI, have BreakerHoldActive
// cleared, BreakerReadoptCount incremented exactly once, and get a
// re-adoption PR comment — the first time ReDriveBreakerHeldPRs runs after
// the breaker closes.
func TestReDriveBreakerHeldPRs_RevivesHeldPR(t *testing.T) {
	var mu sync.Mutex
	var commentPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues/80/comments" && r.Method == http.MethodPost {
			mu.Lock()
			commentPosts++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:          80,
		IssueNumber:       80,
		BranchName:        "pilot/GH-80",
		HeadSHA:           "sha-held",
		Stage:             StageFailed,
		Error:             "platform-outage breaker open — holding PR",
		BreakerHoldActive: true,
	}
	c.mu.Lock()
	c.activePRs[80] = prState
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should be cleared after re-drive")
	}
	if prState.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount = %d, want 1", prState.BreakerReadoptCount)
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty after re-drive", prState.Error)
	}
	if prState.CIWaitStartedAt.IsZero() {
		t.Error("CIWaitStartedAt should be reset for fresh CI monitoring")
	}
	mu.Lock()
	got := commentPosts
	mu.Unlock()
	if got != 1 {
		t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", got)
	}

	// GH-4792: calling ReDriveBreakerHeldPRs again (e.g. a second monitor
	// tick, or the breaker opening and closing again shortly after) must be
	// a no-op for this PR now — it is no longer StageFailed/BreakerHoldActive,
	// so "re-driven exactly once" holds even under a redundant call.
	c.ReDriveBreakerHeldPRs(context.Background())
	if prState.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount after second ReDriveBreakerHeldPRs call = %d, want unchanged 1", prState.BreakerReadoptCount)
	}
	mu.Lock()
	got = commentPosts
	mu.Unlock()
	if got != 1 {
		t.Errorf("PR comment calls after second call = %d, want unchanged 1", got)
	}
}

// TestReDriveBreakerHeldPRs_IgnoresPRsNotBreakerHeld covers the negative
// case: a StageFailed PR held for any OTHER reason (e.g. a rebase hold, or a
// plain terminal failure with no BreakerHoldActive) must not be touched by
// ReDriveBreakerHeldPRs — only the platform-breaker's own hold flag is in
// scope here, not every StageFailed PR in the fleet.
func TestReDriveBreakerHeldPRs_IgnoresPRsNotBreakerHeld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	rebaseHeld := &PRState{
		PRNumber:         81,
		Stage:            StageFailed,
		RebaseHoldActive: true,
		HeadSHA:          "sha-rebase",
	}
	plainFailed := &PRState{
		PRNumber: 82,
		Stage:    StageFailed,
		HeadSHA:  "sha-plain",
	}
	c.mu.Lock()
	c.activePRs[81] = rebaseHeld
	c.activePRs[82] = plainFailed
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if rebaseHeld.Stage != StageFailed || !rebaseHeld.RebaseHoldActive {
		t.Errorf("rebase-held PR was touched: Stage=%v RebaseHoldActive=%v", rebaseHeld.Stage, rebaseHeld.RebaseHoldActive)
	}
	if plainFailed.Stage != StageFailed {
		t.Errorf("plain-failed PR was touched: Stage=%v", plainFailed.Stage)
	}
}

// TestReDriveBreakerHeldPRs_CapReached covers the re-adoption cap: once
// BreakerReadoptCount reaches maxBreakerReadoptAttempts, a further breaker
// close must NOT trigger another revival — the PR stays parked for a human
// even though the breaker itself has closed again, so a PR whose own
// perpetual failure keeps re-opening the breaker can't ping-pong forever.
func TestReDriveBreakerHeldPRs_CapReached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:            83,
		IssueNumber:         83,
		Stage:               StageFailed,
		BreakerHoldActive:   true,
		BreakerReadoptCount: maxBreakerReadoptAttempts,
		HeadSHA:             "sha-at-cap",
	}
	c.mu.Lock()
	c.activePRs[83] = prState
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (cap reached, must stay parked)", prState.Stage)
	}
	if !prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should remain true — cap reached, PR stays parked")
	}
	if prState.BreakerReadoptCount != maxBreakerReadoptAttempts {
		t.Errorf("BreakerReadoptCount = %d, want unchanged %d", prState.BreakerReadoptCount, maxBreakerReadoptAttempts)
	}
}

// TestReDriveBreakerHeldPRs_MultiplePRs verifies the fan-out across every
// currently-held PR in one controller's activePRs map, mirroring the
// periodic monitor calling this once per controller.
func TestReDriveBreakerHeldPRs_MultiplePRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	held := []*PRState{
		{PRNumber: 90, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-90"},
		{PRNumber: 91, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-91"},
		{PRNumber: 92, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-92"},
	}
	c.mu.Lock()
	for _, pr := range held {
		c.activePRs[pr.PRNumber] = pr
	}
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	for _, pr := range held {
		if pr.Stage != StageWaitingCI {
			t.Errorf("pr %d Stage = %v, want StageWaitingCI", pr.PRNumber, pr.Stage)
		}
		if pr.BreakerHoldActive {
			t.Errorf("pr %d BreakerHoldActive should be cleared", pr.PRNumber)
		}
		if pr.BreakerReadoptCount != 1 {
			t.Errorf("pr %d BreakerReadoptCount = %d, want 1", pr.PRNumber, pr.BreakerReadoptCount)
		}
	}
}
