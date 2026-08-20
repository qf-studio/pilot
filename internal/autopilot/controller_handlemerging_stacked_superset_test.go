package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_handleMerging_StackedSuperset_HoldsMerge covers GH-5030:
// handleMerging must invoke detectStackedSuperset (GH-5029) when this PR's
// base is the default branch and another open autopilot PR is tracked for
// the repo, and must hold rather than merge when the result is non-nil —
// reproducing the #5016/#5017 incident shape (PR#17 built stacked on
// still-open PR#16, both targeting main).
func TestController_handleMerging_StackedSuperset_HoldsMerge(t *testing.T) {
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

	commentCount := 0
	server := mergeMockServer(t, 17, 17, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI,
		CreatedAt:    time.Now(),
	}
	stackedState := &PRState{
		PRNumber:     17,
		IssueNumber:  17,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = stackedState
	c.mu.Unlock()

	if err := c.handleMerging(ctx, stackedState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}

	if stackedState.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (PR should be held, not advanced)", stackedState.Stage, StageMerging)
	}
	if stackedState.MergeAttempts != 0 {
		t.Errorf("MergeAttempts = %d, want 0 (merge must not be attempted while stacked)", stackedState.MergeAttempts)
	}
	if commentCount != 0 {
		t.Errorf("comment count = %d, want 0 (no completion comment while held)", commentCount)
	}
}

// TestController_handleMerging_StackedSuperset_BaseOfStackNotBlocked covers
// the GH-5027 symmetric-case requirement: the PR that is the BASE of a
// stack (i.e. an ANCESTOR of another open PR's head) must merge unhindered
// — detectStackedSuperset only holds the DESCENDANT side.
func TestController_handleMerging_StackedSuperset_BaseOfStackNotBlocked(t *testing.T) {
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

	commentCount := 0
	server := mergeMockServer(t, 16, 16, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	baseState := &PRState{
		PRNumber:     16,
		IssueNumber:  16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.activePRs[16] = baseState
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	if err := c.handleMerging(ctx, baseState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}

	if baseState.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1 (base of stack must merge, not be held)", baseState.MergeAttempts)
	}
	if commentCount != 1 {
		t.Errorf("comment count = %d, want 1 (merge completion comment expected)", commentCount)
	}
}

// TestController_handleMerging_StackedSuperset_SkippedWhenNoOtherOpenPRs
// covers GH-5027 requirement 4 at the wiring layer: with no other open
// autopilot PR tracked, handleMerging must skip the ancestry probe
// entirely (no local git / GitHub compare cost) and merge normally. Uses
// no WithProjectPath and a server that has no compare-API handler, so a
// wrongly-invoked probe would surface as a request/merge failure.
func TestController_handleMerging_StackedSuperset_SkippedWhenNoOtherOpenPRs(t *testing.T) {
	ctx := context.Background()

	commentCount := 0
	server := mergeMockServer(t, 30, 30, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	soloState := &PRState{
		PRNumber:     30,
		IssueNumber:  30,
		BranchName:   "pilot/GH-30",
		HeadSHA:      "deadbeef",
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.activePRs[30] = soloState
	c.mu.Unlock()

	if err := c.handleMerging(ctx, soloState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}

	if soloState.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1 (solo PR must merge normally)", soloState.MergeAttempts)
	}
	if commentCount != 1 {
		t.Errorf("comment count = %d, want 1", commentCount)
	}
}
