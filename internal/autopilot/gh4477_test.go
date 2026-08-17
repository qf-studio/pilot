package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleMerging_LateCIFailure_RescindsApproval is the GH-4477 regression
// test: once handleCIPassed transitions a PR to StageAwaitApproval,
// ci_status is frozen at "success" and never rechecked. If a check
// subsequently flips to failure (re-run, late-reporting check) while the PR
// sits in the (now-approved) StageMerging stage, the frozen ci_status must
// not be trusted — the PR nearly merged red via #4466 (2026-07-19).
//
// This asserts the merge chokepoint (handleMerging) re-validates CI live: on
// a since-regressed check it must not call the merge API, must un-freeze
// ci_status, must regress the stage instead of merging, and must clear/cancel
// the stale approval so a future re-escalation can't fast-track past a fresh
// approval gate on the old "approved" decision.
func TestHandleMerging_LateCIFailure_RescindsApproval(t *testing.T) {
	var mergeCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha77/check-runs" && r.Method == http.MethodGet:
			// Live re-check now reports a failing check run, even though
			// prState.CIStatus is still frozen at CISuccess from the earlier
			// handleCIPassed transition.
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "test", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/77/merge" && r.Method == http.MethodPut:
			mergeCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"merged": true})

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	mgr := approval.NewManager(nil)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	c.mu.Lock()
	c.activePRs[77] = &PRState{
		PRNumber:            77,
		PRURL:               "https://github.com/owner/repo/pull/77",
		IssueNumber:         30,
		BranchName:          "pilot/GH-30",
		HeadSHA:             "sha77",
		Stage:               StageMerging,
		CIStatus:            CISuccess, // frozen "success" from handleCIPassed
		EscalationReason:    "environments.prod.require_approval=true",
		ApprovalRequestID:   "req-77",
		ApprovalDecision:    string(approval.DecisionApproved),
		ApprovalRequestedAt: time.Now().Add(-5 * time.Minute),
		CreatedAt:           time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 77, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled {
		t.Error("merge API was called — a red PR must never be merged on stale ci_status")
	}

	pr, ok := c.GetPRState(77)
	if !ok {
		t.Fatal("PR state missing after ProcessPR")
	}
	if pr.Stage != StageCIFailed {
		t.Errorf("Stage = %s, want %s (stage must regress on late CI failure)", pr.Stage, StageCIFailed)
	}
	if pr.CIStatus != CIFailure {
		t.Errorf("CIStatus = %s, want %s (frozen success must be un-frozen)", pr.CIStatus, CIFailure)
	}
	if pr.ApprovalRequestID != "" {
		t.Errorf("ApprovalRequestID = %q, want empty (stale approval request must be cancelled)", pr.ApprovalRequestID)
	}
	if pr.ApprovalDecision != "" {
		t.Errorf("ApprovalDecision = %q, want empty (stale approved decision must not fast-track a future re-approval)", pr.ApprovalDecision)
	}
}

// TestHandleMerging_CIStillPassing_ProceedsToMerge is the control case for
// GH-4477: when the live re-check still reports success, handleMerging must
// proceed to merge as before — the new re-validation must not block a
// legitimately green PR.
func TestHandleMerging_CIStillPassing_ProceedsToMerge(t *testing.T) {
	var mergeCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha88/check-runs" && r.Method == http.MethodGet:
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "test", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/88/merge" && r.Method == http.MethodPut:
			mergeCalled = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]bool{"merged": true})

		case r.URL.Path == "/repos/owner/repo/issues/40/labels" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[88] = &PRState{
		PRNumber:     88,
		PRURL:        "https://github.com/owner/repo/pull/88",
		IssueNumber:  40,
		BranchName:   "pilot/GH-40",
		HeadSHA:      "sha88",
		Stage:        StageMerging,
		CIStatus:     CISuccess,
		CreatedAt:    time.Now(),
		TargetBranch: "main",
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 88, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !mergeCalled {
		t.Error("merge API was not called — a still-green PR must proceed to merge")
	}

	pr, ok := c.GetPRState(88)
	if !ok {
		t.Fatal("PR state missing after ProcessPR")
	}
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
}
