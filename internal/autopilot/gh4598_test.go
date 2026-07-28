package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestRestartReplay_ParkedPR_StaysParkedAndDoesNotReComment is the GH-4598
// regression test. It simulates a full daemon restart for a PR that got
// parked (GH-4596/#4602: approval required, no approval channel wired) on
// the pre-restart controller:
//
//  1. Controller A parks PR #57 via submitAsyncApprovalRequest (one comment
//     POST, Parked=true, Stage stays StageAwaitApproval) and persists it —
//     mirroring ProcessPR's unconditional persistPRState call.
//  2. Controller B ("after restart") loads the same on-disk store fresh via
//     RestoreState and re-ticks the PR through the ordinary ProcessPR
//     dispatch (Stage-based, exactly what the poller does).
//
// Before GH-4598, Parked/EscalationReason were in-memory only, so step 2
// would rehydrate Parked=false — the tick would then treat the still-true
// misconfig as brand new, re-log the WARN and call postMisconfigComment
// again (a wasted GitHub round-trip even though the marker check itself
// stopped the actual duplicate comment). It must now recognize the PR as
// already parked and stay quiet: no second comment POST, and the PR must
// never transition to StageFailed on replay.
func TestRestartReplay_ParkedPR_StaysParkedAndDoesNotReComment(t *testing.T) {
	commentPosts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			commentPosts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage

	store := newTestStateStore(t)

	// Step 1: controller A parks the PR (approvalMgr nil -> misconfig branch).
	controllerA := NewController(cfg, ghClient, nil, "owner", "repo")
	controllerA.SetStateStore(store)

	prState := &PRState{
		PRNumber:         57,
		PRURL:            "https://github.com/owner/repo/pull/57",
		IssueNumber:      22,
		BranchName:       "pilot/GH-22",
		HeadSHA:          "sha57",
		Stage:            StageAwaitApproval,
		EscalationReason: "PR adds 656 net lines (> 500 threshold)",
	}
	controllerA.mu.Lock()
	controllerA.activePRs[57] = prState
	controllerA.mu.Unlock()

	if err := controllerA.ProcessPR(context.Background(), 57, nil); err != nil {
		t.Fatalf("controller A ProcessPR: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Fatalf("after tick 1: Stage = %v, want StageAwaitApproval", prState.Stage)
	}
	if !prState.Parked {
		t.Fatalf("after tick 1: Parked = false, want true")
	}
	if commentPosts != 1 {
		t.Fatalf("after tick 1: comment POSTs = %d, want 1", commentPosts)
	}

	// Step 2: "restart" — a brand new controller loads the same on-disk store.
	controllerB := NewController(cfg, ghClient, nil, "owner", "repo")
	controllerB.SetStateStore(store)

	restored, err := controllerB.RestoreState()
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if restored != 1 {
		t.Fatalf("RestoreState restored %d PRs, want 1", restored)
	}

	controllerB.mu.RLock()
	restoredPR, ok := controllerB.activePRs[57]
	controllerB.mu.RUnlock()
	if !ok {
		t.Fatalf("PR 57 not present in controller B's activePRs after RestoreState")
	}
	if restoredPR.Stage != StageAwaitApproval {
		t.Fatalf("restored Stage = %v, want StageAwaitApproval (not re-classified as failed)", restoredPR.Stage)
	}
	if !restoredPR.Parked {
		t.Fatalf("restored Parked = false, want true — restart must rehydrate the parked flag")
	}
	if restoredPR.EscalationReason != prState.EscalationReason {
		t.Fatalf("restored EscalationReason = %q, want %q", restoredPR.EscalationReason, prState.EscalationReason)
	}

	// First post-restart tick (the poller's replay of this PR) must stay
	// parked quietly: no re-alert, no re-comment, no failed transition.
	if err := controllerB.ProcessPR(context.Background(), 57, nil); err != nil {
		t.Fatalf("controller B ProcessPR (post-restart tick): %v", err)
	}
	if restoredPR.Stage != StageAwaitApproval {
		t.Errorf("after restart replay tick: Stage = %v, want StageAwaitApproval", restoredPR.Stage)
	}
	if commentPosts != 1 {
		t.Errorf("after restart replay tick: comment POSTs = %d, want still 1 (idempotent across restart)", commentPosts)
	}
}
