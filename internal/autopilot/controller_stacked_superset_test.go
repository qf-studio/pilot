package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_DetectStackedSuperset_LocalGit_Descendant reproduces the
// GH-5027/GH-5029 shape via the local-git-first detection path: PR#17's
// branch was built on top of PR#16's still-open branch (one extra commit),
// both targeting main. detectStackedSuperset, called for PR#17, must report
// PR#16 as the PR it is stacked on.
func TestController_DetectStackedSuperset_LocalGit_Descendant(t *testing.T) {
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

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected GitHub API call on the local-git-first path: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

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
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[17])
	if err != nil {
		t.Fatalf("detectStackedSuperset: %v", err)
	}
	if stackedOn == nil {
		t.Fatal("expected PR#17 to be detected as stacked on PR#16")
	}
	if stackedOn.PRNumber != 16 {
		t.Fatalf("stackedOn.PRNumber = %d, want 16", stackedOn.PRNumber)
	}

	// Symmetric case (GH-5027 requirement 5): PR#16 is the BASE of the
	// stack — its head is an ANCESTOR, not a descendant, of PR#17's head —
	// so detecting from PR#16's side must report no stacking.
	stackedOn, err = c.detectStackedSuperset(ctx, c.activePRs[16])
	if err != nil {
		t.Fatalf("detectStackedSuperset (base direction): %v", err)
	}
	if stackedOn != nil {
		t.Fatalf("expected PR#16 (the stack's base) to merge unhindered, but got stacked on PR#%d", stackedOn.PRNumber)
	}
}

// TestController_DetectStackedSuperset_LocalGit_Unrelated covers two open
// PRs with disjoint ancestry off main — neither should be flagged.
func TestController_DetectStackedSuperset_LocalGit_Unrelated(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-20")
	writeFixtureFile(t, local, "twenty.txt", "from GH-20\n")
	runFixtureGit(t, local, "add", "twenty.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-20 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-20")
	sha20 := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "main")
	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-21")
	writeFixtureFile(t, local, "twentyone.txt", "from GH-21\n")
	runFixtureGit(t, local, "add", "twentyone.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-21 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-21")
	sha21 := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://unused.invalid")
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[20] = &PRState{PRNumber: 20, BranchName: "pilot/GH-20", HeadSHA: sha20, TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.activePRs[21] = &PRState{PRNumber: 21, BranchName: "pilot/GH-21", HeadSHA: sha21, TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.mu.Unlock()

	if stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[20]); err != nil || stackedOn != nil {
		t.Fatalf("PR#20: got stackedOn=%v err=%v, want (nil, nil)", stackedOn, err)
	}
	if stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[21]); err != nil || stackedOn != nil {
		t.Fatalf("PR#21: got stackedOn=%v err=%v, want (nil, nil)", stackedOn, err)
	}
}

// TestController_DetectStackedSuperset_NoOtherOpenPRs covers GH-5027
// requirement 4: with no other open autopilot PRs tracked, the check must
// be a no-op (and, on the local-git path, must not even attempt a git
// fetch — there is nothing to compare against).
func TestController_DetectStackedSuperset_NoOtherOpenPRs(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://unused.invalid")
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	c.mu.Lock()
	c.activePRs[30] = &PRState{PRNumber: 30, BranchName: "pilot/GH-30", HeadSHA: "deadbeef", TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.mu.Unlock()

	stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[30])
	if err != nil {
		t.Fatalf("detectStackedSuperset: %v", err)
	}
	if stackedOn != nil {
		t.Fatalf("expected no stacking with no other open PRs, got PR#%d", stackedOn.PRNumber)
	}
}

// TestController_DetectStackedSuperset_NonDefaultBase covers the
// documented precondition: a PR whose own TargetBranch is not the default
// branch is out of scope for this check (GH-4872's parkForBaseMismatch
// already handles that shape) — detectStackedSuperset must no-op rather
// than run the (moot) ancestry probe.
func TestController_DetectStackedSuperset_NonDefaultBase(t *testing.T) {
	ctx := context.Background()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://unused.invalid")
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	c.activePRs[40] = &PRState{PRNumber: 40, BranchName: "pilot/GH-40", HeadSHA: "aaaa", TargetBranch: "pilot/GH-39", Stage: StageMerging, CreatedAt: time.Now()}
	c.activePRs[39] = &PRState{PRNumber: 39, BranchName: "pilot/GH-39", HeadSHA: "bbbb", TargetBranch: "main", Stage: StageWaitingCI, CreatedAt: time.Now()}
	c.mu.Unlock()

	stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[40])
	if err != nil {
		t.Fatalf("detectStackedSuperset: %v", err)
	}
	if stackedOn != nil {
		t.Fatalf("expected no-op for a non-default-base PR, got stacked on PR#%d", stackedOn.PRNumber)
	}
}

// TestController_HeadIsStrictDescendant_FallsBackToCompareAPI covers the
// documented fallback: with no local clone available (c.projectPath ==
// ""), headIsStrictDescendant must use the GitHub compare API instead of
// erroring out.
func TestController_HeadIsStrictDescendant_FallsBackToCompareAPI(t *testing.T) {
	ctx := context.Background()

	var comparedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/compare/") {
			comparedPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ahead"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	// No WithProjectPath — forces the fallback.
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	isDescendant, err := c.headIsStrictDescendant(ctx, "pilot/GH-17", "stackedsha", "pilot/GH-16", "basesha")
	if err != nil {
		t.Fatalf("headIsStrictDescendant: %v", err)
	}
	if !isDescendant {
		t.Fatal("expected compare-API fallback to report a strict descendant for status=ahead")
	}
	wantPath := "/repos/owner/repo/compare/basesha...stackedsha"
	if comparedPath != wantPath {
		t.Fatalf("compare path = %q, want %q", comparedPath, wantPath)
	}
}

// TestController_HeadIsStrictDescendant_CompareAPIIdenticalIsNotDescendant
// verifies "identical" (base and head are the same commit) is NOT treated
// as a strict descendant via the fallback path, mirroring the local-git
// same-commit short-circuit.
func TestController_HeadIsStrictDescendant_CompareAPIIdenticalIsNotDescendant(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"identical"}`))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	isDescendant, err := c.headIsStrictDescendant(ctx, "pilot/GH-17", "samesha-on-both", "pilot/GH-16", "othersha")
	if err != nil {
		t.Fatalf("headIsStrictDescendant: %v", err)
	}
	if isDescendant {
		t.Fatal("expected status=identical to NOT be reported as a strict descendant")
	}
}
