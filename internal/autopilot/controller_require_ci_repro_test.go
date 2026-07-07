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

// GH-3994 subtask 1 originally pinned the bug: with release.require_ci: true,
// both polled-merge detection paths (the GH-411 external-merge hijack in
// checkExternalMergeOrClose and the scan-recovery path in
// ScanRecentlyMergedPRs) set StageReleasing directly without ever calling
// CheckCI(mainSHA) or routing through StagePostMergeCI. RequireCI was only
// consulted by handleMerged's SkipPostMergeCI fast path (controller.go
// ~line 1933) — these two sites never read it.
//
// GH-3994 subtask 5 fixed both sites to route through StagePostMergeCI when
// require_ci: true, per the decision in
// .agent/knowledge/memories/decisions/decision_require_ci_polled_merge_paths.md.
// These tests now pin the FIXED behavior in lockstep with that change.

// TestCheckExternalMergeOrClose_RequireCITrue_RoutesThroughPostMergeCI_GH3994
// verifies the GH-411 hijack fix: an externally merged PR with
// require_ci: true is routed to StagePostMergeCI (with PostMergeSHA seeded
// from the already-known merge commit SHA) instead of jumping straight to
// StageReleasing, and a subsequent tick actually polls CheckCI before any
// release decision is made.
func TestCheckExternalMergeOrClose_RequireCITrue_RoutesThroughPostMergeCI_GH3994(t *testing.T) {
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

	// checkExternalMergeOrClose still returns false ("continue processing")
	// when it triggers a release, rather than draining the PR immediately.
	if resolved {
		t.Fatal("checkExternalMergeOrClose reported the PR as fully resolved/drained; expected it to hand off to the release path (resolved=false)")
	}

	if prState.Stage != StagePostMergeCI {
		t.Fatalf("FIX NOT APPLIED: prState.Stage = %v, want StagePostMergeCI (require_ci: true must route the external-merge hijack through the post-merge CI gate)", prState.Stage)
	}

	if prState.PostMergeSHA != "merge-sha-42" {
		t.Fatalf("FIX NOT APPLIED: prState.PostMergeSHA = %q, want the merge commit SHA %q seeded directly", prState.PostMergeSHA, "merge-sha-42")
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 0 {
		t.Errorf("checkExternalMergeOrClose itself must not call CheckCI synchronously; got %d call(s) — CI polling belongs to the subsequent StagePostMergeCI tick", got)
	}

	// Drive one StagePostMergeCI tick and confirm CI is now actually polled
	// before any release decision — this is the behavior that was missing.
	prState.mu.Lock()
	err := c.handlePostMergeCI(context.Background(), prState)
	prState.mu.Unlock()
	if err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 1 {
		t.Errorf("FIX NOT APPLIED: check-runs (CheckCI) was called %d time(s) after the StagePostMergeCI tick; want 1 — require_ci: true must poll CI before releasing", got)
	}

	if prState.Stage == StageReleasing {
		t.Fatal("FIX NOT APPLIED: prState.Stage reached StageReleasing after a single tick with no resolved CI checks; must wait for CheckCI to report success")
	}
}

// TestScanRecentlyMergedPRs_RequireCITrue_RoutesThroughPostMergeCI_GH3994
// verifies the scan-recovery fix: ScanRecentlyMergedPRs registers a merged
// PR at StagePostMergeCI (not StageReleasing, and without the previously
// hardcoded CIStatus: CISuccess lie) when require_ci: true is configured.
func TestScanRecentlyMergedPRs_RequireCITrue_RoutesThroughPostMergeCI_GH3994(t *testing.T) {
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

	if pr.Stage != StagePostMergeCI {
		t.Fatalf("FIX NOT APPLIED: PR 42 stage = %v, want StagePostMergeCI (require_ci: true must route scan-recovery through the post-merge CI gate)", pr.Stage)
	}
	if pr.CIStatus == CISuccess {
		t.Error("FIX NOT APPLIED: PR 42 CIStatus = CISuccess hardcoded without ever checking CI; require_ci: true must not lie about CI status")
	}
	if pr.PostMergeSHA != "merge-sha-42" {
		t.Fatalf("FIX NOT APPLIED: PR 42 PostMergeSHA = %q, want the merge commit SHA %q seeded directly", pr.PostMergeSHA, "merge-sha-42")
	}

	if got := atomic.LoadInt64(&checkRunsCalls); got != 0 {
		t.Errorf("ScanRecentlyMergedPRs itself must not call CheckCI synchronously; got %d call(s) — CI polling belongs to the subsequent StagePostMergeCI tick", got)
	}
}
