package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ProcessPR_StackedSuperset_UnparksAndResumesOnceBlockingPRGone
// is the GH-5032 regression: a PR parked for the stacked-superset reason
// (GH-5027/GH-5031, parkForStackedSuperset — reproduced here directly on
// PRState the same way GH-4909's base-mismatch resume test does, since
// Parked/EscalationReason persist across ticks via the state store, GH-4598,
// regardless of which prior tick or process set them) must resume and merge
// on the very next tick once the PR it was stacked on is no longer an open
// ancestor — mirroring GH-4911's base-mismatch un-park path exactly, with no
// manual `gh pr merge`.
func TestController_ProcessPR_StackedSuperset_UnparksAndResumesOnceBlockingPRGone(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var (
		mergeCalled      atomic.Int32
		labelRemoveCalls atomic.Int32
	)

	const labelPath = "/repos/owner/repo/issues/174/labels"
	const labelRemovePath = labelPath + "/autopilot/parked-awaiting-approval"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == labelRemovePath && r.Method == http.MethodDelete:
			labelRemoveCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == labelPath && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.SetAlertsEngine(&fakeAlertSink{})

	c.mu.Lock()
	// PR#16 (the PR #17 was stacked on) has already merged and is no longer
	// tracked as an active/open PR — detectStackedSuperset only ever
	// compares against c.activePRs, so removing it here is the same signal
	// a real merge produces (removePR drops the entry from activePRs).
	c.activePRs[17] = &PRState{
		PRNumber:         17,
		IssueNumber:      174,
		BranchName:       "pilot/GH-17",
		HeadSHA:          stackedSHA,
		TargetBranch:     "main",
		Stage:            StageMerging,
		Parked:           true,
		EscalationReason: "stacked on open PR: PR #17 is stacked on open PR #16 — merge that first",
		CreatedAt:        time.Now(),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: stackedSHA},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(ctx, 17, ghPR); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 1 {
		t.Errorf("merge called %d times, want 1 — PR#17 must resume auto-merge without manual intervention once PR#16 is gone", mergeCalled.Load())
	}

	pr, ok := c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
	if pr.Parked {
		t.Error("Parked should be false after the stacked-superset block resolved and the PR merged")
	}
	if pr.EscalationReason != "" {
		t.Errorf("EscalationReason = %q, want empty after un-park", pr.EscalationReason)
	}
	if labelRemoveCalls.Load() != 1 {
		t.Errorf("parked-awaiting-approval label removal calls = %d, want 1", labelRemoveCalls.Load())
	}
}

// TestController_ProcessPR_StackedSuperset_StaysParkedWhileBlockingPRStillOpen
// covers the negative case: PR#17 is still parked and PR#16 (the PR it is
// stacked on) is still open/tracked — detectStackedSuperset must still
// report the stacking, so the un-park check must leave the PR parked and
// must NOT attempt a merge.
func TestController_ProcessPR_StackedSuperset_StaysParkedWhileBlockingPRStillOpen(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")
	baseSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var mergeCalled atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.SetAlertsEngine(&fakeAlertSink{})

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI,
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:         17,
		IssueNumber:      174,
		BranchName:       "pilot/GH-17",
		HeadSHA:          stackedSHA,
		TargetBranch:     "main",
		Stage:            StageMerging,
		Parked:           true,
		EscalationReason: "stacked on open PR: PR #17 is stacked on open PR #16 — merge that first",
		CreatedAt:        time.Now(),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: stackedSHA},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(ctx, 17, ghPR); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled.Load() != 0 {
		t.Errorf("merge called %d times, want 0 — PR#17 must stay held while PR#16 is still open", mergeCalled.Load())
	}

	pr, ok := c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (must not advance while still stacked)", pr.Stage, StageMerging)
	}
	if !pr.Parked {
		t.Error("Parked should remain true while PR#16 is still open")
	}
	if !strings.HasPrefix(pr.EscalationReason, stackedSupersetReasonPrefix) {
		t.Errorf("EscalationReason = %q, want it to still carry the stacked-superset reason", pr.EscalationReason)
	}
}
