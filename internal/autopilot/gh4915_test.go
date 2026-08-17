package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ProcessPR_SidewaysMergeDeadEnd_NotResurrectedAcrossRestart is
// the GH-4915 regression for the PR#4912 post-merge review finding: the
// sideways-merge dead-end (GH-4911) removed the PR from activePRs and the
// state store via removePR, but ProcessPR's tail persistPRState call ran
// unconditionally afterward and re-inserted (UPSERTed) the very row that was
// just deleted, at Stage=StageMerged. RestoreState rehydrates any non-failed
// row on boot, so a daemon restart resurrected the dead-ended PR and
// handleMerged went on to deploy/release off content that never landed on
// the default branch — silently defeating the GH-4911 fix across restarts.
//
// Uses a real, file-backed SQLite state store (not a mock) shared across two
// Controller instances to simulate an actual daemon restart, mirroring
// restart_approval_integration_test.go's pattern.
func TestController_ProcessPR_SidewaysMergeDeadEnd_NotResurrectedAcrossRestart(t *testing.T) {
	var (
		mergeCalled  atomic.Int32
		deployCalled atomic.Int32
	)

	tmpDir, err := os.MkdirTemp("", "gh4915-restart-test-*")
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/92/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/92" && r.Method == http.MethodGet:
			// Post-merge re-verification: GitHub reports the PR actually
			// landed on a stacked branch, not the cached "main".
			resp := github.PullRequest{
				Number: 92,
				Head:   github.PRRef{SHA: "sha92"},
				Base:   github.PRRef{Ref: "pilot/GH-70"},
				Merged: true,
			}
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, resp)

		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, []*github.PullRequest{})

		case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
			deployCalled.Add(1)
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c1 := NewController(cfg, ghClient, nil, "owner", "repo")
	c1.SetStateStore(stateStore)
	c1.SetAlertsEngine(&fakeAlertSink{})
	// Deploy would fire on the very next tick (handleMerged) if the dead-end
	// were ever undone by a resurrection — wire it so we can assert it never
	// runs, pre- or post-restart.
	c1.deployer = NewDeployer(ghClient, "owner", "repo", &PostMergeConfig{
		Action:     "webhook",
		WebhookURL: server.URL + "/deploy",
	})

	c1.mu.Lock()
	c1.activePRs[92] = &PRState{
		PRNumber:     92,
		IssueNumber:  72,
		HeadSHA:      "sha92",
		BranchName:   "pilot/GH-92",
		Stage:        StageMerging,
		TargetBranch: "main", // passes the pre-merge guard
		CreatedAt:    time.Now(),
	}
	c1.mu.Unlock()

	ctx := context.Background()
	if err := c1.ProcessPR(ctx, 92, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 1 {
		t.Fatalf("merge called %d times, want 1", mergeCalled.Load())
	}
	if deployCalled.Load() != 0 {
		t.Errorf("deploy called %d times, want 0 — a sideways merge must never deploy", deployCalled.Load())
	}

	// The PR must be gone from in-memory tracking (removePR ran inside
	// handleMerging's mismatch path).
	if _, ok := c1.GetPRState(92); ok {
		t.Fatal("PR 92 should have been removed from tracking")
	}

	// GH-4915 core assertion: no row must land in the state store this tick.
	// Before the fix, ProcessPR's unconditional tail persistPRState re-wrote
	// the row persistRemovePR had just deleted, at Stage=StageMerged.
	row, err := stateStore.GetPRState("owner/repo", 92)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row != nil {
		t.Fatalf("state store still has a row for PR 92 after removePR: %+v — tail persist resurrected it", row)
	}

	// --- Simulated daemon restart: fresh Controller sharing only the
	// on-disk state store. ---
	c2 := NewController(cfg, ghClient, nil, "owner", "repo")
	c2.SetStateStore(stateStore)
	c2.SetAlertsEngine(&fakeAlertSink{})
	c2.deployer = NewDeployer(ghClient, "owner", "repo", &PostMergeConfig{
		Action:     "webhook",
		WebhookURL: server.URL + "/deploy",
	})

	if _, err := c2.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}

	if _, ok := c2.GetPRState(92); ok {
		t.Fatal("PR 92 must NOT be resurrected into the post-restart controller")
	}

	// A following tick must find nothing to process for PR 92 — ProcessPR
	// must reject it as untracked rather than silently re-driving it toward
	// deploy/release.
	if err := c2.ProcessPR(ctx, 92, nil); err == nil {
		t.Error("ProcessPR on the post-restart controller should error — PR 92 is no longer tracked")
	}
	if deployCalled.Load() != 0 {
		t.Errorf("deploy called %d times after restart, want 0", deployCalled.Load())
	}
}

// TestController_ProcessPR_NormalMerge_StillPersistsAcrossTick guards against
// the GH-4915 fix over-correcting: a PR that stays in activePRs after its
// handler runs (the overwhelmingly common case) must still be persisted on
// every tick exactly as before.
func TestController_ProcessPR_NormalMerge_StillPersistsAcrossTick(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gh4915-normal-persist-test-*")
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/95/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/95" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number: 95,
				Head:   github.PRRef{SHA: "sha95"},
				Base:   github.PRRef{Ref: "main"},
				Merged: true,
			}
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, resp)
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = writeJSON(w, []*github.PullRequest{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)
	c.SetAlertsEngine(&fakeAlertSink{})

	c.mu.Lock()
	c.activePRs[95] = &PRState{
		PRNumber:     95,
		IssueNumber:  75,
		HeadSHA:      "sha95",
		BranchName:   "pilot/GH-95",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 95, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	pr, ok := c.GetPRState(95)
	if !ok {
		t.Fatal("PR 95 should still be tracked")
	}
	if pr.Stage != StageMerged {
		t.Fatalf("Stage = %s, want %s", pr.Stage, StageMerged)
	}

	row, err := stateStore.GetPRState("owner/repo", 95)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row == nil {
		t.Fatal("normal merged PR must still be persisted to the state store")
	}
	if row.Stage != StageMerged {
		t.Fatalf("persisted Stage = %s, want %s", row.Stage, StageMerged)
	}
}

// TestController_HandleMerged_RefusesDeployOnNonDefaultTarget covers the
// GH-4915 belt-and-braces fence in handleMerged itself: even if a row
// somehow reaches StageMerged with a TargetBranch that isn't the resolved
// default branch, handleMerged must refuse to deploy/release and instead
// dead-end the PR via removePR — the same predicate family as handleMerging's
// pre- and post-merge guards.
func TestController_HandleMerged_RefusesDeployOnNonDefaultTarget(t *testing.T) {
	var deployCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/deploy" && r.Method == http.MethodPost:
			deployCalled.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetAlertsEngine(&fakeAlertSink{})
	c.deployer = NewDeployer(ghClient, "owner", "repo", &PostMergeConfig{
		Action:     "webhook",
		WebhookURL: server.URL + "/deploy",
	})

	prState := &PRState{
		PRNumber:     96,
		IssueNumber:  76,
		HeadSHA:      "sha96",
		BranchName:   "pilot/GH-96",
		Stage:        StageMerged,
		TargetBranch: "pilot/GH-70", // should be unreachable via handleMerging, but guard belt-and-braces anyway
		CreatedAt:    time.Now(),
	}
	c.mu.Lock()
	c.activePRs[96] = prState
	c.mu.Unlock()

	if err := c.handleMerged(context.Background(), prState); err != nil {
		t.Fatalf("handleMerged returned error: %v", err)
	}

	if deployCalled.Load() != 0 {
		t.Errorf("deploy called %d times, want 0", deployCalled.Load())
	}
	if _, ok := c.GetPRState(96); ok {
		t.Fatal("PR 96 should have been removed from tracking")
	}
}
