package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleReviewRequested_SelfClose_MarksOriginalSuperseded mirrors
// TestHandleCIFailed_HandOff_MarksOriginalSuperseded for the review-revision
// rung (GH-5741): the original task is recorded superseded by the revision
// issue at the self-close, not left to be demoted to failed and re-picked.
func TestHandleReviewRequested_SelfClose_MarksOriginalSuperseded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/92/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/92/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 702}))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithProjectPath("/proj/pilot"))
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{PRNumber: 92, IssueNumber: 41, BranchName: "pilot/GH-41", Stage: StageReviewRequested}
	c.mu.Lock()
	c.activePRs[92] = prState
	c.mu.Unlock()

	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("handleReviewRequested: %v", err)
	}
	if prState.SelfClosedFixIssue != 702 {
		t.Fatalf("SelfClosedFixIssue = %d, want 702", prState.SelfClosedFixIssue)
	}

	want := reclassifyCall{TaskID: "GH-41", ProjectPath: "/proj/pilot", Reason: "continued by fix issue #702"}
	if len(evalMock.reclassifiedSuperseded) != 1 || evalMock.reclassifiedSuperseded[0] != want {
		t.Errorf("ReclassifyCompletionAsSuperseded calls = %+v, want exactly [%+v]", evalMock.reclassifiedSuperseded, want)
	}
}

// TestHandleCIFailed_SelfClose_OriginalHasTerminalCompletion is the GH-5741
// chain test against a real store: a completed-with-PR original, CI-fail →
// self-close → the StageFailed ledger transition recordExecutionEvent runs.
// The original's row must end up superseded (not failed) so the dispatcher's
// HasTerminalCompletion gate counts it done and nextRetryGeneration grants no
// re-pick — the GH-5731 re-pick loop.
func TestHandleCIFailed_SelfClose_OriginalHasTerminalCompletion(t *testing.T) {
	const projectPath = "/proj/pilot"
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-57412", TaskID: "GH-57412", ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/owner/repo/pull/57411", CommitSHA: "abc1234",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	server := newHandOffCIServer(t, "gh5741chain", 57411, 57412, 57413)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithProjectPath(projectPath))
	// memoryStore only: evalStore is wired just when learning is enabled, and
	// the hand-off write must not depend on it.
	c.SetMemoryStore(store)

	prState := &PRState{PRNumber: 57411, IssueNumber: 57412, HeadSHA: "gh5741chain", Stage: StageCIFailed}
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed: %v", err)
	}
	// ProcessPR runs this on the StageCIFailed → StageFailed transition and
	// demotes completed rows to failed (GH-5067).
	c.recordExecutionEvent(prState, StageCIFailed, memory.StageFailed, prState.Error)

	status, err := store.GetExecutionStatusByTaskID("GH-57412", projectPath)
	if err != nil {
		t.Fatalf("GetExecutionStatusByTaskID: %v", err)
	}
	if status != "superseded" {
		t.Errorf("original row status = %q, want superseded", status)
	}
	done, err := store.HasTerminalCompletion("GH-57412", projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if !done {
		t.Error("HasTerminalCompletion = false for the self-closed original; the dispatcher would re-pick it into the fix run's branch")
	}
}
