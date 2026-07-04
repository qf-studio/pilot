package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestController_OnPRCreated_DuplicateRegistrationIsNoOp is a regression test for
// GH-3828 (defect D16): the orphan-reconciler and the normal poller OnPRCreated
// callback can both observe the same PR as "untracked" and both call
// OnPRCreated for it, because each caller's tracked-check and its registration
// call are separate lock acquisitions. Before the fix, the second call
// silently overwrote the first's *PRState with a brand-new struct reset to
// StagePRCreated, discarding any progress (e.g. a submitted approval request)
// and restarting the whole register→CI→escalate cycle a second time.
func TestController_OnPRCreated_DuplicateRegistrationIsNoOp(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// First registration (e.g. the normal poller's OnPRCreated callback).
	c.OnPRCreated(3808, "https://github.com/owner/repo/pull/3808", 100, "sha1", "pilot/GH-100", "")

	first, ok := c.GetPRState(3808)
	if !ok {
		t.Fatal("PR 3808 not tracked after first OnPRCreated")
	}

	// Advance state past creation, as ProcessPR would have done by the time
	// the reconciler's delayed registration lands.
	first.mu.Lock()
	first.Stage = StageWaitingCI
	first.ApprovalRequestID = "req-already-pending"
	first.mu.Unlock()

	// Second registration for the SAME PR (e.g. the orphan-reconciler, which
	// snapshotted "untracked" before the callback above ran, then calls
	// OnPRCreated after losing the race).
	c.OnPRCreated(3808, "https://github.com/owner/repo/pull/3808", 100, "sha1-stale", "pilot/GH-100", "")

	second, ok := c.GetPRState(3808)
	if !ok {
		t.Fatal("PR 3808 no longer tracked after duplicate OnPRCreated")
	}

	if second != first {
		t.Fatal("duplicate OnPRCreated replaced the tracked *PRState — progress (stage, approval request) was discarded")
	}

	second.mu.Lock()
	stage := second.Stage
	approvalID := second.ApprovalRequestID
	second.mu.Unlock()

	if stage != StageWaitingCI {
		t.Errorf("Stage = %q after duplicate registration, want %q (must not reset to StagePRCreated)", stage, StageWaitingCI)
	}
	if approvalID != "req-already-pending" {
		t.Errorf("ApprovalRequestID = %q after duplicate registration, want unchanged %q (must not spawn a sibling request)", approvalID, "req-already-pending")
	}

	activePRs := c.GetActivePRs()
	count := 0
	for _, pr := range activePRs {
		if pr.PRNumber == 3808 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("activePRs contains %d entries for PR 3808, want 1", count)
	}

	snap := c.metrics.Snapshot()
	if snap.DuplicateRegistrationsSkipped != 1 {
		t.Errorf("DuplicateRegistrationsSkipped = %d, want 1", snap.DuplicateRegistrationsSkipped)
	}
}

// TestController_OnPRCreated_ConcurrentRace drives the orphan-reconciler and
// the OnPRCreated callback path concurrently against the same PR number under
// -race. Exactly one call must win the registration; every other call must
// no-op rather than overwrite the winner's PRState.
func TestController_OnPRCreated_ConcurrentRace(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	const callers = 20
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			c.OnPRCreated(3820, "https://github.com/owner/repo/pull/3820", 200, "sha", "pilot/GH-200", "")
		}()
	}
	wg.Wait()

	activePRs := c.GetActivePRs()
	count := 0
	for _, pr := range activePRs {
		if pr.PRNumber == 3820 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("activePRs contains %d entries for PR 3820 after concurrent registration, want 1", count)
	}

	snap := c.metrics.Snapshot()
	if snap.DuplicateRegistrationsSkipped != callers-1 {
		t.Errorf("DuplicateRegistrationsSkipped = %d, want %d (one winner, rest skipped)", snap.DuplicateRegistrationsSkipped, callers-1)
	}
}

// TestController_OnPRCreated_DuplicateDoesNotDuplicateMergedComment simulates
// the reconciler racing in AFTER a PR has already been merged and the
// completion comment posted. Before the fix, the duplicate registration would
// reset Stage to StagePRCreated, and a subsequent processing pass would drive
// the PR through the whole pipeline again — including a second "PR merged"
// comment on the linked issue (observed on #3792 the night this defect was
// found).
func TestController_OnPRCreated_DuplicateDoesNotDuplicateMergedComment(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 3808, 100, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(3808, "https://github.com/owner/repo/pull/3808", 100, "abc1234", "pilot/GH-100", "")
	prState, _ := c.GetPRState(3808)
	prState.Stage = StageMerging

	ctx := context.Background()
	if err := c.handleMerging(ctx, prState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("comment count after merge = %d, want 1", commentCount)
	}
	if prState.Stage != StageMerged {
		t.Fatalf("Stage after merge = %q, want %q", prState.Stage, StageMerged)
	}

	// Reconciler arrives late and re-registers the same PR.
	c.OnPRCreated(3808, "https://github.com/owner/repo/pull/3808", 100, "abc1234", "pilot/GH-100", "")

	after, _ := c.GetPRState(3808)
	if after != prState {
		t.Fatal("duplicate OnPRCreated replaced the merged PRState")
	}
	after.mu.Lock()
	stage := after.Stage
	after.mu.Unlock()
	if stage != StageMerged {
		t.Errorf("Stage = %q after duplicate registration, want %q (must not restart the pipeline)", stage, StageMerged)
	}
	if commentCount != 1 {
		t.Errorf("comment count after duplicate registration = %d, want 1 (no duplicate merge comment)", commentCount)
	}
}

// TestController_OnPRCreated_DuplicateDoesNotDuplicateApprovalRequest simulates
// the reconciler racing in AFTER an approval request has already been
// submitted for the PR. The duplicate registration must preserve the pending
// request rather than clobbering the PRState and letting handleAwaitApproval
// submit a sibling request on the next tick.
func TestController_OnPRCreated_DuplicateDoesNotDuplicateApprovalRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	mgr := approval.NewManager(&approval.Config{
		Enabled:        true,
		DefaultTimeout: 0,
		PreMerge: &approval.StageConfig{
			Enabled: true,
		},
	})

	c := NewController(cfg, ghClient, mgr, "owner", "repo")
	c.OnPRCreated(3820, "https://github.com/owner/repo/pull/3820", 200, "sha", "pilot/GH-200", "")
	prState, _ := c.GetPRState(3820)
	prState.mu.Lock()
	prState.Stage = StageAwaitApproval
	prState.mu.Unlock()

	ctx := context.Background()
	if err := c.handleAwaitApproval(ctx, prState); err != nil {
		t.Fatalf("handleAwaitApproval returned error: %v", err)
	}
	prState.mu.Lock()
	firstRequestID := prState.ApprovalRequestID
	prState.mu.Unlock()
	if firstRequestID == "" {
		t.Fatal("expected an approval request ID to be set after first handleAwaitApproval tick")
	}

	// Reconciler arrives late and re-registers the same PR while approval is pending.
	c.OnPRCreated(3820, "https://github.com/owner/repo/pull/3820", 200, "sha", "pilot/GH-200", "")

	after, _ := c.GetPRState(3820)
	if after != prState {
		t.Fatal("duplicate OnPRCreated replaced the awaiting-approval PRState")
	}
	after.mu.Lock()
	stage := after.Stage
	requestID := after.ApprovalRequestID
	after.mu.Unlock()
	if stage != StageAwaitApproval {
		t.Errorf("Stage = %q after duplicate registration, want %q", stage, StageAwaitApproval)
	}
	if requestID != firstRequestID {
		t.Errorf("ApprovalRequestID changed from %q to %q after duplicate registration — a sibling request was created", firstRequestID, requestID)
	}

	// A subsequent tick must take the "still waiting" path, not resubmit.
	if err := c.handleAwaitApproval(ctx, prState); err != nil {
		t.Fatalf("second handleAwaitApproval returned error: %v", err)
	}
	prState.mu.Lock()
	finalRequestID := prState.ApprovalRequestID
	prState.mu.Unlock()
	if finalRequestID != firstRequestID {
		t.Errorf("ApprovalRequestID changed to %q after a follow-up tick, want unchanged %q (single request)", finalRequestID, firstRequestID)
	}
}
