package autopilot

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-6276: a PR that merges after a failed auto-rebase (human pushes the merge
// commit, autopilot re-adopts the PR, the PR merges) must flip the latest
// 'failed' execution row of its task to 'completed' — and nothing else.
func TestCheckExternalMergeOrClose_ReconcileFailedExecution(t *testing.T) {
	const (
		projectPath = "/work/repo"
		prURL       = "https://github.com/owner/repo/pull/76"
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/71/comments" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	base := time.Now().Add(-time.Hour)
	for _, e := range []*memory.Execution{
		{ID: "old-gen", TaskID: "GH-71", ProjectPath: projectPath, Status: "failed", Error: "older generation", CreatedAt: base},
		{ID: "latest", TaskID: "GH-71", ProjectPath: projectPath, Status: "failed", Error: "auto-rebase failed", CreatedAt: base.Add(time.Minute)},
		{ID: "other-task", TaskID: "GH-72", ProjectPath: projectPath, Status: "failed", Error: "unrelated", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "other-project", TaskID: "GH-71", ProjectPath: "/work/other", Status: "failed", Error: "other repo", CreatedAt: base.Add(3 * time.Minute)},
	} {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithLogger(logger), WithProjectPath(projectPath))
	c.SetAlertsEngine(&fakeAlertSink{})
	c.SetEvalStore(store)

	prState := &PRState{
		PRNumber:    76,
		PRURL:       prURL,
		IssueNumber: 71,
		BranchName:  "pilot/GH-71",
		Stage:       StageWaitingCI,
	}
	c.mu.Lock()
	c.activePRs[76] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 76, State: "closed", Merged: true, Base: github.PRRef{Ref: "main"}}
	prState.mu.Lock()
	c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
	prState.mu.Unlock()

	status := func(id string) (string, string) {
		t.Helper()
		got, err := store.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution %s: %v", id, err)
		}
		return got.Status, got.PRUrl
	}

	if st, pr := status("latest"); st != "completed" || pr != prURL {
		t.Errorf("latest row = status %q pr_url %q, want completed %q", st, pr, prURL)
	}
	for _, id := range []string{"old-gen", "other-task", "other-project"} {
		if st, _ := status(id); st != "failed" {
			t.Errorf("row %s status = %q, want failed (must not be touched)", id, st)
		}
	}
	if n := strings.Count(logs.String(), "execution reconciled after merge"); n != 1 {
		t.Errorf("Info line 'execution reconciled after merge' logged %d times, want 1; logs:\n%s", n, logs.String())
	}
}

// TestReconcileExecutionAfterMerge_NoStoreOrIssue covers the no-op guards.
func TestReconcileExecutionAfterMerge_NoStoreOrIssue(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")
	c.reconcileExecutionAfterMerge(71, "https://github.com/owner/repo/pull/1") // nil store: no panic

	mock := &mockEvalStore{}
	c.SetEvalStore(mock)
	c.reconcileExecutionAfterMerge(0, "https://github.com/owner/repo/pull/1")
	if len(mock.reconciled) != 0 {
		t.Errorf("issue 0 reconciled %d rows, want 0", len(mock.reconciled))
	}
	c.reconcileExecutionAfterMerge(71, "https://github.com/owner/repo/pull/1")
	if len(mock.reconciled) != 1 || mock.reconciled[0].TaskID != "GH-71" {
		t.Errorf("reconciled = %+v, want one GH-71 call", mock.reconciled)
	}
}
