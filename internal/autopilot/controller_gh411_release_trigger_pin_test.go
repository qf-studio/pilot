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

// GH-3994 subtask 3: pins the CURRENT GH-411 external-merge release trigger
// semantics for the require_ci: false (default-preserving) path, independent
// of whatever shape the require_ci: true fix (subtasks 4/5) ends up taking.
//
// checkExternalMergeOrClose's release branch must keep doing exactly this
// when require_ci is false/unset:
//   - update prState.HeadSHA to the GitHub-reported merge commit SHA
//   - set prState.Stage = StageReleasing directly (no CI poll)
//   - report resolved=false (hand off to the release path, PR stays tracked)
//   - never call CheckCI/check-runs
//
// Unlike TestCheckExternalMergeOrClose_RequireCITrue_ReleasesWithoutCICheck_GH3994
// (controller_require_ci_repro_test.go), which pins the BUG and is expected to
// start failing once the require_ci: true fix lands, THIS test must keep
// passing unmodified through that fix — it is the safety net proving the fix
// didn't regress the require_ci: false majority case.
func TestCheckExternalMergeOrClose_RequireCIFalse_ReleasesImmediately_PinsCurrentSemantics_GH3994(t *testing.T) {
	var checkRunsCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			atomic.AddInt64(&checkRunsCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: false, TagPrefix: "v"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		HeadSHA:  "abc123",
		Stage:    StageMerging,
	}
	c.mu.Lock()
	c.activePRs[42] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number:         42,
		State:          "closed",
		Merged:         true,
		HTMLURL:        "https://github.com/owner/repo/pull/42",
		MergeCommitSHA: "merge-sha-42",
	}

	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
	prState.mu.Unlock()

	if resolved {
		t.Fatal("checkExternalMergeOrClose reported the PR as fully resolved/drained; expected it to hand off to the release path (resolved=false)")
	}

	if prState.Stage != StageReleasing {
		t.Fatalf("REGRESSION: prState.Stage = %v, want StageReleasing (require_ci: false must keep releasing immediately on external merge)", prState.Stage)
	}

	if prState.HeadSHA != "merge-sha-42" {
		t.Fatalf("REGRESSION: prState.HeadSHA = %q, want the merge commit SHA %q", prState.HeadSHA, "merge-sha-42")
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 0 {
		t.Errorf("REGRESSION: check-runs (CheckCI) was called %d time(s); require_ci: false must never poll CI on this path", got)
	}

	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("REGRESSION: PR 42 should remain tracked in activePRs, handed off to the release path")
	}
}
