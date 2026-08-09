package main

import (
	"strings"
	"testing"
)

// GH-4817 (TASK-459 Phase 3, Tasks 3/4/7): source-level guards proving the
// two GH-3053 "no commit, no PR" demotion sites consult
// executor.IsNoArtifactExplainedOutcome before flipping success to failure.
// Both handleGitlabIssueWithResult and the CLI's newGitHubRunCmd RunE
// closure build real GitHub/GitLab clients and drive runner.Execute end to
// end, so — mirroring the existing source-inspection pattern for
// handleGithubIssueEventSDK (see TestGithubHandlerSDK_NotifyTaskStartedWired) —
// these assert against the literal guard clause rather than standing up a
// full mock pipeline.

// TestGitlabHandler_NoArtifactDemotionConsultsOutcome guards the GitLab
// GH-3053 site (handlers.go, handleGitlabIssueWithResult): a no_op or
// terminal-by-design (superseded/canceled) outcome must not be demoted to
// issueResult.Success = false just because no commit/PR was produced.
func TestGitlabHandler_NoArtifactDemotionConsultsOutcome(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGitlabIssueWithResult(")

	idx := strings.Index(body, "issueResult.Success = false")
	if idx < 0 {
		t.Fatal("expected handleGitlabIssueWithResult to still demote issueResult.Success on a genuine no-artifact completion")
	}

	// The guarding "if" must be the nearest preceding one and must reference
	// both the terminal-by-design check and the outcome-classification helper.
	preamble := body[:idx]
	ifIdx := strings.LastIndex(preamble, "if !hr.IsTerminalByDesign()")
	if ifIdx < 0 {
		t.Fatal("expected `if !hr.IsTerminalByDesign() && !executor.IsNoArtifactExplainedOutcome(...)` immediately guarding the Success demotion")
	}
	guardClause := preamble[ifIdx:]
	if !strings.Contains(guardClause, "executor.IsNoArtifactExplainedOutcome(hr.Result.Outcome)") {
		t.Error("the demotion guard must also consult executor.IsNoArtifactExplainedOutcome(hr.Result.Outcome), not just IsTerminalByDesign()")
	}
}

// TestCLIRunCmd_NoArtifactCheckConsultsOutcome guards the CLI's GH-3053 site
// (commands.go, newGitHubRunCmd's RunE closure): a no_op or terminal-by-design
// outcome must not be treated as an execution failure just because no
// commit/PR was produced.
func TestCLIRunCmd_NoArtifactCheckConsultsOutcome(t *testing.T) {
	body := githubFuncBody(t, "commands.go", "func newGitHubRunCmd() *cobra.Command {")

	idx := strings.Index(body, "!result.IsEpic && result.CommitSHA == \"\" && result.PRUrl == \"\"")
	if idx < 0 {
		t.Fatal("expected the GH-3053 no-artifact condition in newGitHubRunCmd's RunE closure")
	}

	line := body[idx : idx+strings.Index(body[idx:], "\n")]
	if !strings.Contains(line, "!executor.IsNoArtifactExplainedOutcome(result.Outcome)") {
		t.Errorf("the CLI no-artifact condition must also consult !executor.IsNoArtifactExplainedOutcome(result.Outcome), got: %s", line)
	}
}
