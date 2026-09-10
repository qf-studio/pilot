package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestGithubOnPRCreatedHandler_BorrowedBranch_RegistersUnderFixIssue is the
// GH-5421 regression test for the PRIMARY SDK OnPRCreated path (as opposed to
// the rate-limit retry path GH-5413/PR#5416 already fixed). Mirrors the
// GH-4211 lesson (poller_github_gh4211_test.go): drive the exact function
// production wires into pollerDeps.OnPRCreated, not a lower-level controller
// method called directly.
//
// A fix issue (task GH-200) dispatched onto its origin PR's branch
// (resolveAutopilotFixBranch, cmd/pilot/handlers.go) records that borrowed
// branch via RecordBorrowedBranch. When the SDK poller later fires
// OnPRCreated for the resulting PR — with IssueID "200" (the fix issue) and
// BranchName "pilot/GH-100" (the borrowed origin branch) — the handler must
// route through OnPRCreatedForFixIssue so the PR is tracked under the fix
// issue with the borrowed branch name preserved, instead of being silently
// dropped by Controller.OnPRCreated's branch/issue guard (GH-5409).
func TestGithubOnPRCreatedHandler_BorrowedBranch_RegistersUnderFixIssue(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	runner := &executor.Runner{}
	runner.RecordBorrowedBranch("GH-200", "pilot/GH-100", 5361)

	// This is the exact function production wires into pollerDeps.OnPRCreated
	// (poller_github.go's startGithubSDKPollerForRepo), not a re-implementation.
	handler := githubOnPRCreatedHandler(ctrl, runner)
	handler(sdkcore.PRCreatedEvent{
		PRNumber:   99,
		PRURL:      "https://github.com/owner/repo/pull/99",
		IssueID:    "200",
		HeadSHA:    "deadbeef",
		BranchName: "pilot/GH-100",
	})

	state, ok := ctrl.GetPRState(99)
	if !ok {
		t.Fatal("PR 99 should be registered")
	}
	if state.IssueNumber != 200 {
		t.Errorf("IssueNumber = %d, want 200 (fix issue, not the branch-derived origin issue 100)", state.IssueNumber)
	}
	if state.BranchName != "pilot/GH-100" {
		t.Errorf("BranchName = %q, want %q (borrowed origin branch preserved unchanged)", state.BranchName, "pilot/GH-100")
	}
}

// TestGithubOnPRCreatedHandler_NoBorrowedRecord_GuardStillHolds is the
// negative control: without a matching RecordBorrowedBranch entry (the
// common case — most issues aren't fix/revision issues on a borrowed
// branch), a branch/issue mismatch must still hit Controller.OnPRCreated's
// GH-5409 guard and register nothing. This pins that adding the borrowed-
// branch lookup does not turn into a blanket bypass of that guard.
func TestGithubOnPRCreatedHandler_NoBorrowedRecord_GuardStillHolds(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	runner := &executor.Runner{} // no RecordBorrowedBranch call

	handler := githubOnPRCreatedHandler(ctrl, runner)
	handler(sdkcore.PRCreatedEvent{
		PRNumber:   99,
		PRURL:      "https://github.com/owner/repo/pull/99",
		IssueID:    "200",
		HeadSHA:    "deadbeef",
		BranchName: "pilot/GH-100", // mismatches issue 200, no borrowed record to justify it
	})

	if prs := ctrl.GetActivePRs(); len(prs) != 0 {
		t.Fatalf("expected 0 PRs registered without a borrowed-branch record, got %d", len(prs))
	}
	if _, ok := ctrl.GetPRState(99); ok {
		t.Error("PR 99 should not be registered when branch/issue mismatch and no borrowed-branch record exists")
	}
}

// TestGithubOnPRCreatedHandler_NilBorrowedReader_FallsBackToPlainOnPRCreated
// guards the nil-safety of the borrowed reader parameter — production code
// that hasn't wired a Runner (or an older test) must keep working exactly as
// before GH-5421.
func TestGithubOnPRCreatedHandler_NilBorrowedReader_FallsBackToPlainOnPRCreated(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	handler := githubOnPRCreatedHandler(ctrl, nil)
	handler(sdkcore.PRCreatedEvent{
		PRNumber:   4212,
		PRURL:      "https://github.com/owner/repo/pull/4212",
		IssueID:    "4211",
		HeadSHA:    "abc1234",
		BranchName: "pilot/GH-4211",
	})

	state, ok := ctrl.GetPRState(4212)
	if !ok {
		t.Fatal("PR 4212 should be registered via the plain OnPRCreated fallback")
	}
	if state.IssueNumber != 4211 {
		t.Errorf("IssueNumber = %d, want 4211", state.IssueNumber)
	}
}
