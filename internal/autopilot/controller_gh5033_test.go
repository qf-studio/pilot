package autopilot

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_HeadIsStrictDescendant_LocalFailure_FallsBackToCompareAPI
// covers the "local git worktree failure" half of GH-5033: when the local
// ancestry check errors (here, `git fetch origin <branch>` fails because the
// branch was never pushed to the fixture's origin), headIsStrictDescendant
// must fall back to the GitHub compare API rather than surfacing the local
// error directly — mirroring
// TestController_HeadIsStrictDescendant_FallsBackToCompareAPI (which covers
// the "no local clone at all" case), this covers "local clone present but
// the check itself failed".
func TestController_HeadIsStrictDescendant_LocalFailure_FallsBackToCompareAPI(t *testing.T) {
	local := newFixtureRepo(t)
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

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	var logBuf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	// Neither branch was ever pushed to the fixture's origin, so the local
	// `git fetch origin <branch>` leg fails — this is the "local git
	// worktree failure" shape, as opposed to the no-project-path shape
	// already covered elsewhere.
	isDescendant, err := c.headIsStrictDescendant(ctx, "pilot/GH-ghost-17", "stackedsha", "pilot/GH-ghost-16", "basesha")
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
	if !strings.Contains(logBuf.String(), "local ancestry check failed, falling back to GitHub compare API") {
		t.Errorf("expected a logged warning for the local-git failure, got:\n%s", logBuf.String())
	}
}

// TestController_HeadIsStrictDescendant_BothPathsFail_ReturnsError is the
// GH-5033 core case: local git fails (unpushed branches) AND the GitHub
// compare API fallback also fails (500). headIsStrictDescendant must return
// a non-nil error rather than silently resolving to "not a descendant" —
// the caller (detectStackedSuperset / handleMerging) is responsible for
// failing open on that error, not this function.
func TestController_HeadIsStrictDescendant_BothPathsFail_ReturnsError(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))

	_, err := c.headIsStrictDescendant(ctx, "pilot/GH-ghost-17", "stackedsha", "pilot/GH-ghost-16", "basesha")
	if err == nil {
		t.Fatal("expected an error when both the local check and the compare-API fallback fail")
	}
}

// TestController_DetectStackedSuperset_AncestryError_Propagates verifies
// detectStackedSuperset surfaces a non-nil error (rather than swallowing it
// as "not stacked") when the underlying ancestry check fails for a
// candidate — the wrapped error names both PR numbers involved.
func TestController_DetectStackedSuperset_AncestryError_Propagates(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"internal error"}`))
	}))
	defer server.Close()

	// No WithProjectPath — forces straight to the (failing) compare API.
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[16] = &PRState{PRNumber: 16, BranchName: "pilot/GH-16", HeadSHA: "basesha", TargetBranch: "main", Stage: StageWaitingCI, CreatedAt: time.Now()}
	c.activePRs[17] = &PRState{PRNumber: 17, BranchName: "pilot/GH-17", HeadSHA: "stackedsha", TargetBranch: "main", Stage: StageMerging, CreatedAt: time.Now()}
	c.mu.Unlock()

	stackedOn, err := c.detectStackedSuperset(ctx, c.activePRs[17])
	if err == nil {
		t.Fatal("expected detectStackedSuperset to propagate the ancestry-check error")
	}
	if stackedOn != nil {
		t.Fatalf("expected nil result alongside the error, got PR#%d", stackedOn.PRNumber)
	}
	if !strings.Contains(err.Error(), "pr #17") || !strings.Contains(err.Error(), "pr #16") {
		t.Errorf("error = %q, want it to name both PR#17 and PR#16", err.Error())
	}
}

// TestController_HandleMerging_StackedSupersetCheckError_FailsOpen is the
// GH-5033 end-to-end regression: with the ancestry probe unable to resolve
// (no local clone, and the GitHub compare API failing), driving a PR through
// handleMerging via ProcessPR must NOT be blocked by the stacked-superset
// guard — it must log a warning and proceed to merge exactly as if no other
// open PR existed. A broken probe must never wedge merging.
func TestController_HandleMerging_StackedSupersetCheckError_FailsOpen(t *testing.T) {
	ctx := context.Background()

	var mergeCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/compare/"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/issues/17/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	// No WithProjectPath: forces the ancestry probe straight onto the
	// (failing) compare API for every candidate — the "GitHub compare API
	// failure" shape from the GH-5033 problem statement.
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	var logBuf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      "basesha",
		TargetBranch: "main",
		Stage:        StageWaitingCI,
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		IssueNumber:  117,
		BranchName:   "pilot/GH-17",
		HeadSHA:      "stackedsha",
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: "stackedsha"},
		Base:   github.PRRef{Ref: "main"},
	}
	if err := c.ProcessPR(ctx, 17, ghPR); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}

	if mergeCalled.Load() != 1 {
		t.Errorf("MergePR called %d times, want 1 — a broken ancestry probe must fail open and let the merge proceed", mergeCalled.Load())
	}
	pr, ok := c.GetPRState(17)
	if ok && pr.Parked {
		t.Errorf("PR should not be parked on a detection error — got Parked=true, reason=%q", pr.EscalationReason)
	}
	if !strings.Contains(logBuf.String(), "stacked-superset ancestry check failed, proceeding with merge (fail-open)") {
		t.Errorf("expected a logged fail-open warning, got:\n%s", logBuf.String())
	}
}
