package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleCIFailed_SelfCloseMarker_PersistedBeforeTailPersist is the
// GH-5361 regression for a gap left by GH-5351: markSelfClosed only stamped
// prState.SelfClosedFixIssue in memory — the marker was not durably saved
// until ProcessPR's tail persistPRState call (~L2913), which runs only after
// handleCIFailed returns. A daemon death in that window (after the marker is
// stamped and the PR closed on GitHub, but before the tail persist runs)
// would lose the marker entirely: on restart, checkExternalMergeOrClose
// would read GitHub's close back as external and run the destructive
// relabel/branch-delete path the marker exists to prevent (the exact
// pilot-console #275 incident GH-5351 fixed for the non-restart case).
//
// This test calls handleCIFailed directly and deliberately never invokes any
// tail persistPRState — simulating the daemon dying the instant
// handleCIFailed returns — then reads the marker back from a real,
// file-backed state store to prove handleCIFailed itself persists it rather
// than depending on the caller.
func TestHandleCIFailed_SelfCloseMarker_PersistedBeforeTailPersist(t *testing.T) {
	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/gh5361sha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 401, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/401/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 5362}))
		case r.URL.Path == "/repos/owner/repo/pulls/5360" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	tmpDir, err := os.MkdirTemp("", "gh5361-selfclose-persist-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	memStore, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = memStore.Close() }()

	stateStore, err := NewStateStore(memStore.DB())
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))
	c.SetStateStore(stateStore)

	prState := &PRState{
		PRNumber:    5360,
		IssueNumber: 5359,
		HeadSHA:     "gh5361sha1",
		Stage:       StageCIFailed,
	}

	// Deliberately no tail persistPRState call here — simulates the daemon
	// dying the moment handleCIFailed returns.
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if prState.SelfClosedFixIssue != 5362 {
		t.Fatalf("prState.SelfClosedFixIssue = %d, want 5362", prState.SelfClosedFixIssue)
	}

	row, err := stateStore.GetPRState("owner/repo", 5360)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row == nil {
		t.Fatal("state store has no row for PR 5360 after handleCIFailed — self-close marker was never persisted, only held in memory")
	}
	if row.SelfClosedFixIssue != 5362 {
		t.Fatalf("persisted row SelfClosedFixIssue = %d, want 5362 — marker did not survive a simulated daemon death before the tail persist", row.SelfClosedFixIssue)
	}
}
