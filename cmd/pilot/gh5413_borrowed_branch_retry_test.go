package main

import (
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestQueueRetryIfRateLimited_SetsFromPRFromAutopilotMetaFooter is a
// regression test for GH-5413's root cause: QueueRetryIfRateLimited
// previously constructed the queued retry task without ever populating
// FromPR, so pendingTask.Task.FromPR was always 0 downstream — githubRetryOnPRCreated
// (and any future consumer) had no way to distinguish a borrowed-branch fix
// dispatch from a mis-paired event once a rate-limited task was replayed.
// This asserts the queued task's FromPR is recovered from the issue body's
// autopilot-meta footer, mirroring how handleGithubIssueEventSDK resolves it
// on the primary (non-retry) path.
func TestQueueRetryIfRateLimited_SetsFromPRFromAutopilotMetaFooter(t *testing.T) {
	scheduler := executor.NewScheduler(executor.DefaultSchedulerConfig(), nil)
	s := sdkRateLimitScheduler{scheduler: scheduler}

	body := "Please fix the failing check.\n\n<!-- autopilot-meta branch:pilot/GH-10 pr:5361 -->"
	errText := "You've hit your limit \u00b7 resets 6am (UTC)"

	if !s.QueueRetryIfRateLimited("GH-20", "Fix issue", body, errText) {
		t.Fatal("QueueRetryIfRateLimited() = false, want true for a recognized rate-limit error")
	}

	pending := scheduler.Queue().List()
	if len(pending) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(pending))
	}
	if pending[0].Task.FromPR != 5361 {
		t.Errorf("queued task.FromPR = %d, want 5361 (parsed from the autopilot-meta footer's pr: field)", pending[0].Task.FromPR)
	}
}

// TestQueueRetryIfRateLimited_NoFooter_FromPRStaysZero is the negative
// control: an issue body without an autopilot-meta footer (the common case —
// most issues aren't fix/revision issues) must not spuriously set FromPR.
func TestQueueRetryIfRateLimited_NoFooter_FromPRStaysZero(t *testing.T) {
	scheduler := executor.NewScheduler(executor.DefaultSchedulerConfig(), nil)
	s := sdkRateLimitScheduler{scheduler: scheduler}

	body := "Please implement this feature."
	errText := "You've hit your limit \u00b7 resets 6am (UTC)"

	if !s.QueueRetryIfRateLimited("GH-20", "Feature", body, errText) {
		t.Fatal("QueueRetryIfRateLimited() = false, want true for a recognized rate-limit error")
	}

	pending := scheduler.Queue().List()
	if len(pending) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(pending))
	}
	if pending[0].Task.FromPR != 0 {
		t.Errorf("queued task.FromPR = %d, want 0 with no autopilot-meta footer in body", pending[0].Task.FromPR)
	}
}

// TestGithubRetryOnPRCreated_BorrowedBranch_RegistersUnderFixIssue is the
// poller-level test for GH-5413 (PR #5412 follow-up): githubRetryOnPRCreated
// is the exact function githubRetryCallback — the callback production wires
// into rateLimitScheduler.SetRetryCallback at cmd/pilot/poller_github.go —
// invokes once a rate-limited retry produces a PR. It is extracted (mirroring
// the GH-4211 lesson: test the handler production wires up, not a
// lower-level controller method called directly) so this exact routing
// decision can be driven without also running the full dispatcher/runner
// pipeline through handleGithubIssueEventSDK just to get a *sdkcore.IssueResult.
//
// A fix issue F (here 20) that was dispatched onto its origin PR's branch
// (resolveAutopilotFixBranch) carries FromPR > 0 on its retried task. The
// issue actually fetched by GetIssue during retry is F itself (issueNumber =
// 20), but the branch the resulting PR lands on is named after the ORIGIN
// issue (10) — plain OnPRCreated's GH-5409 guard would silently drop
// registration in that case. This asserts the PR ends up tracked under F,
// with the borrowed branch name preserved.
func TestGithubRetryOnPRCreated_BorrowedBranch_RegistersUnderFixIssue(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	const originIssue = 10
	const fixIssue = 20
	result := &sdkcore.IssueResult{
		Success:    true,
		PRNumber:   99,
		PRURL:      "https://github.com/owner/repo/pull/99",
		HeadSHA:    "deadbeef",
		BranchName: "pilot/GH-10", // borrowed origin branch, not pilot/GH-20
	}

	githubRetryOnPRCreated(ctrl, result, fixIssue, "", originIssue)

	prs := ctrl.GetActivePRs()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR registered, got %d", len(prs))
	}
	state, ok := ctrl.GetPRState(99)
	if !ok {
		t.Fatal("PR 99 should be registered")
	}
	if state.IssueNumber != fixIssue {
		t.Errorf("IssueNumber = %d, want %d (fix issue, not origin issue %d)", state.IssueNumber, fixIssue, originIssue)
	}
	if state.BranchName != "pilot/GH-10" {
		t.Errorf("BranchName = %q, want %q (borrowed origin branch preserved unchanged)", state.BranchName, "pilot/GH-10")
	}
}

// TestGithubRetryOnPRCreated_NoFromPR_MismatchedBranchIsBlocked is the
// GH-5413 regression companion: a retried task WITHOUT the borrowed-branch
// signal (fromPR == 0) whose branch still doesn't match issueNumber must
// still hit the GH-5409 guard inside plain OnPRCreated and register nothing —
// githubRetryOnPRCreated must not itself become a bypass for the mis-paired-
// event case the guard was written to block.
func TestGithubRetryOnPRCreated_NoFromPR_MismatchedBranchIsBlocked(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	result := &sdkcore.IssueResult{
		Success:    true,
		PRNumber:   99,
		PRURL:      "https://github.com/owner/repo/pull/99",
		HeadSHA:    "deadbeef",
		BranchName: "pilot/GH-10",
	}

	// fromPR = 0: no borrowed-branch signal, but issueNumber (99) still
	// mismatches the branch-encoded issue (10).
	githubRetryOnPRCreated(ctrl, result, 99, "", 0)

	prs := ctrl.GetActivePRs()
	if len(prs) != 0 {
		t.Fatalf("expected 0 PRs registered for a mismatched branch without the borrowed-branch signal, got %d", len(prs))
	}
	if _, ok := ctrl.GetPRState(99); ok {
		t.Error("PR 99 should not be registered when branch/issueNumber mismatch and fromPR == 0")
	}
}

// TestGithubRetryOnPRCreated_NilResult_NoOp guards the nil-safety of the
// extracted routing function against the common "no PR yet" retry result
// (e.g. Skipped, or execution failed before pushing a branch).
func TestGithubRetryOnPRCreated_NilResult_NoOp(t *testing.T) {
	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	cfg := autopilot.DefaultConfig()
	ctrl := autopilot.NewController(cfg, ghClient, nil, "owner", "repo")

	githubRetryOnPRCreated(ctrl, nil, 20, "", 10)
	githubRetryOnPRCreated(ctrl, &sdkcore.IssueResult{Success: true}, 20, "", 10)

	if prs := ctrl.GetActivePRs(); len(prs) != 0 {
		t.Fatalf("expected 0 PRs registered for a nil/PR-less result, got %d", len(prs))
	}
}
