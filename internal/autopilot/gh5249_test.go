package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HandleCIFailed_BoardSyncSkipsFailColumnOnHandoff is GH-5249's
// regression guard for the GH-1870 board sync inside handleCIFailed: once a
// fix issue has been spawned (spawnFailureIssue set prState.TerminalLabel to
// github.LabelSuperseded), this is a healthy hand-off, not a failure — the
// board card must not be moved to the fail column, mirroring the GH-5247
// split already applied to c.metrics/c.monitor.Fail for this same branch.
// Before this fix the card moved to Failed even on the routine-revision path
// TestGH5247_HandleCIFailed_Classification proves is not a pipeline defect.
func TestController_HandleCIFailed_BoardSyncSkipsFailColumnOnHandoff(t *testing.T) {
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1:1: some lint error (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh5249hoff/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 701, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/701/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/54002":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 54002, Body: "no-meta"}))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 54003}))
		case r.URL.Path == "/repos/owner/repo/pulls/54001" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	mock := &mockBoardSyncer{}
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))

	prState := &PRState{
		PRNumber:    54001,
		IssueNumber: 54002,
		IssueNodeID: "IssueNodeID_gh5249hoff",
		HeadSHA:     "gh5249hoff",
		Stage:       StageCIFailed,
	}
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed: %v", err)
	}

	if prState.TerminalLabel != github.LabelSuperseded {
		t.Fatalf("TerminalLabel = %q, want %q (precondition: this must be the healthy hand-off branch)",
			prState.TerminalLabel, github.LabelSuperseded)
	}
	for _, call := range mock.calls {
		if call.statusName == "Failed" {
			t.Errorf("board sync moved card to Failed on a healthy hand-off, calls=%+v", mock.calls)
		}
	}
}

// TestController_NotifyExternalClose_BoardSyncSkipsFailColumnOnSupersededClose
// is GH-5249's regression guard for the GH-4475 board sync in the general
// external-close path (checkExternalMergeOrClose -> notifyExternalClose):
// when the close is a supersededClose (prState.TerminalLabel already
// github.LabelSuperseded — e.g. set earlier by spawnFailureIssue/
// spawnReviewIssue), the card must not move to the fail column, mirroring
// TestController_CheckExternalClose_BoardSync's genuine-failure case, which
// still expects "Failed".
func TestController_NotifyExternalClose_BoardSyncSkipsFailColumnOnSupersededClose(t *testing.T) {
	const issueNodeID = "IssueNodeID_gh5249superseded"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/issues/10":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"node_id": issueNodeID})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	mock := &mockBoardSyncer{}
	c := NewController(cfg, ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", issueNodeID)
	// OnPRCreated itself fires a reviewStatus sync — reset so we only observe
	// the external-close sync under test.
	mock.calls = nil

	// GH-4570: back-date CreatedAt past externalCloseGraceWindow so a single
	// closed read is trusted. GH-5249: pre-mark TerminalLabel as it would be
	// by the time a real supersededClose reaches this poll tick (set earlier
	// by spawnFailureIssue/spawnReviewIssue the moment the fix/revision issue
	// was created, well before the PR close is ever observed here).
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Now().Add(-10 * time.Minute)
	c.activePRs[42].TerminalLabel = github.LabelSuperseded
	c.mu.Unlock()

	c.processAllPRs(context.Background())

	for _, call := range mock.calls {
		if call.statusName == "Failed" {
			t.Errorf("board sync moved card to Failed on a supersededClose, calls=%+v", mock.calls)
		}
	}
}
