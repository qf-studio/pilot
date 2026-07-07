package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-3994 subtask 1: reproduction tests pinning the CURRENT (buggy) behavior —
// with release.require_ci: true configured, both polled-merge detection paths
// (the GH-411 external-merge hijack in checkExternalMergeOrClose and the
// scan-recovery path in ScanRecentlyMergedPRs) set StageReleasing directly
// without ever calling CheckCI(mainSHA) or routing through StagePostMergeCI.
// RequireCI is only consulted by handleMerged's SkipPostMergeCI fast path
// (controller.go ~line 1933) — these two sites never read it.
//
// These tests are expected to keep PASSING until a later GH-3994 subtask
// fixes checkExternalMergeOrClose/ScanRecentlyMergedPRs to honor require_ci;
// at that point they document behavior that must change and should be
// updated alongside the fix.

// TestCheckExternalMergeOrClose_RequireCITrue_ReleasesWithoutCICheck_GH3994
// reproduces the GH-411 hijack bug: an externally merged PR with
// require_ci: true still jumps straight to StageReleasing with zero
// CheckCI/check-runs calls.
func TestCheckExternalMergeOrClose_RequireCITrue_ReleasesWithoutCICheck_GH3994(t *testing.T) {
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
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: true, TagPrefix: "v"}

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

	// checkExternalMergeOrClose returns false ("continue processing") when it
	// triggers a release, rather than draining the PR immediately.
	if resolved {
		t.Fatal("checkExternalMergeOrClose reported the PR as fully resolved/drained; expected it to hand off to the release path (resolved=false)")
	}

	if prState.Stage != StageReleasing {
		t.Fatalf("BUG NOT REPRODUCED: prState.Stage = %v, want StageReleasing (GH-3994 expects the current buggy short-circuit)", prState.Stage)
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 0 {
		t.Errorf("BUG NOT REPRODUCED: check-runs (CheckCI) was called %d time(s); GH-3994 bug is that require_ci=true is never consulted on this path, so CheckCI must be called 0 times", got)
	}
}

// TestScanRecentlyMergedPRs_RequireCITrue_ReleasesWithoutCICheck_GH3994
// reproduces the scan-recovery bug: ScanRecentlyMergedPRs registers a merged
// PR directly at StageReleasing (with a hardcoded CIStatus: CISuccess) even
// when require_ci: true is configured, without ever calling CheckCI.
func TestScanRecentlyMergedPRs_RequireCITrue_ReleasesWithoutCICheck_GH3994(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var checkRunsCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			atomic.AddInt64(&checkRunsCalls, 1)
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			prs := []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha42"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			}
			out := make([]*github.PullRequest, len(prs))
			for i := range prs {
				out[i] = &prs[i]
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/tags"):
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
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: true, TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 should be registered in activePRs")
	}

	if pr.Stage != StageReleasing {
		t.Fatalf("BUG NOT REPRODUCED: PR 42 stage = %v, want StageReleasing (GH-3994 expects the current buggy short-circuit)", pr.Stage)
	}
	if pr.CIStatus != CISuccess {
		t.Errorf("BUG NOT REPRODUCED: PR 42 CIStatus = %v, want CISuccess hardcoded by the scan-recovery path without ever checking CI", pr.CIStatus)
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 0 {
		t.Errorf("BUG NOT REPRODUCED: check-runs (CheckCI) was called %d time(s); GH-3994 bug is that require_ci=true is never consulted on this path, so CheckCI must be called 0 times", got)
	}
}
