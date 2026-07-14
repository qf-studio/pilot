package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// releaseCyclesServer builds a fake GitHub server covering everything
// ScanRecentlyMergedPRs + handleReleasing need to drive a merged PR all the
// way through tag creation, plus a GetIssue route serving fake issues by
// number (for heldByScope / maybeCloseParentIssue). gitRefPosts counts
// POST /git/refs calls so tests can assert a hold never creates a tag.
func releaseCyclesServer(t *testing.T, issues map[int]scopeMembershipFakeIssue, gitRefPosts, issueGets *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			atomic.AddInt64(issueGets, 0) // no-op, kept for symmetry
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
			atomic.AddInt64(issueGets, 1)
			var num int
			_, _ = fmtSscanIssueNum(r.URL.Path, &num)
			issue, ok := issues[num]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			labels := make([]github.Label, 0, len(issue.labels))
			for _, name := range issue.labels {
				labels = append(labels, github.Label{Name: name})
			}
			state := issue.state
			if state == "" {
				state = "open"
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: num, Title: issue.title, Body: issue.body, State: state, Labels: labels})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case strings.Contains(r.URL.Path, "/branches/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// fmtSscanIssueNum extracts the trailing /issues/<N> number from a path.
func fmtSscanIssueNum(path string, num *int) (int, error) {
	const marker = "/issues/"
	idx := strings.LastIndex(path, marker)
	if idx < 0 {
		return 0, nil
	}
	rest := path[idx+len(marker):]
	n := 0
	for _, ch := range rest {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	*num = n
	return n, nil
}

// TestScanRecentlyMergedPRs_HoldsUnderScopeClose covers the "mixed mode" case:
// a scope-member PR (open epic parent) is held (never registered at
// StageReleasing), while a standalone PR in the same scan releases per-merge
// as today (GH-3989).
func TestScanRecentlyMergedPRs_HoldsUnderScopeClose(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var gitRefPosts, issueGets int64
	server := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{
		100: {title: "member", body: "Parent: GH-1"},
		1:   {title: "epic", state: "open"},
		200: {title: "standalone"},
	}, &gitRefPosts, &issueGets)
	defer server.Close()

	// Override the default pulls route with the specific PR fixtures for this test.
	prs := []github.PullRequest{
		{
			Number:         42,
			Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha42"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/42",
			Title:          "feat(member): scoped work",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "merge-sha-42",
		},
		{
			Number:         43,
			Head:           github.PRRef{Ref: "pilot/GH-200", SHA: "sha43"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/43",
			Title:          "fix(standalone): unrelated bug",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "merge-sha-43",
		},
	}
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") && !strings.Contains(r.URL.Path, "/commits") {
			out := make([]*github.PullRequest, len(prs))
			for i := range prs {
				out[i] = &prs[i]
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		server.Config.Handler.ServeHTTP(w, r)
	}))
	defer server2.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server2.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if _, ok := c.GetPRState(42); ok {
		t.Error("PR 42 (scope member, open epic) should be held — not registered in activePRs")
	}

	standalone, ok := c.GetPRState(43)
	if !ok {
		t.Fatal("PR 43 (standalone) should be registered at StageReleasing")
	}
	if standalone.Stage != StageReleasing {
		t.Errorf("PR 43 stage = %v, want StageReleasing", standalone.Stage)
	}

	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count after scan = %d, want 0 (tag creation happens later in handleReleasing)", got)
	}

	// Drive the standalone PR through handleReleasing directly (mirrors the
	// controller's main loop dispatching StageReleasing) and confirm it tags.
	if err := c.handleReleasing(context.Background(), standalone); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count after handleReleasing = %d, want 1 (standalone PR must still tag)", got)
	}
}

// TestScanRecentlyMergedPRs_HoldsUnderSchedule verifies Trigger "on_schedule"
// holds every merged PR unconditionally, without ever consulting
// heldByScope/GetIssue (GH-3989).
func TestScanRecentlyMergedPRs_HoldsUnderSchedule(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var gitRefPosts, issueGets int64
	base := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{}, &gitRefPosts, &issueGets)
	defer base.Close()

	prs := []github.PullRequest{
		{
			Number:         50,
			Head:           github.PRRef{Ref: "pilot/GH-300", SHA: "sha50"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/50",
			Title:          "feat: scheduled work",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "merge-sha-50",
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") && !strings.Contains(r.URL.Path, "/commits") {
			out := make([]*github.PullRequest, len(prs))
			for i := range prs {
				out[i] = &prs[i]
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_schedule", Schedule: "0 21 * * FRI", TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if _, ok := c.GetPRState(50); ok {
		t.Error("PR 50 should be held under on_schedule — not registered in activePRs")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count = %d, want 0", got)
	}
	if got := atomic.LoadInt64(&issueGets); got != 0 {
		t.Errorf("GetIssue call count = %d, want 0 (on_schedule never consults scope membership)", got)
	}
}

// TestCheckExternalMergeOrClose_HoldsUnderScopeClose verifies the GH-411
// external-merge hijack holds scope members: the PR still drains via the
// existing removePR path (label/close bookkeeping unaffected), no tag is
// created, and a one-time "held for scope release" comment is posted.
func TestCheckExternalMergeOrClose_HoldsUnderScopeClose(t *testing.T) {
	var gitRefPosts, issueGets int64
	var commentPosts int64
	var commentBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:         42,
				State:          "closed",
				Merged:         true,
				HTMLURL:        "https://github.com/owner/repo/pull/42",
				MergeCommitSHA: "merge-sha-42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10/comments" && r.Method == http.MethodPost:
			atomic.AddInt64(&commentPosts, 1)
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			commentBodies = append(commentBodies, body.Body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		case r.URL.Path == "/repos/owner/repo/issues/10":
			atomic.AddInt64(&issueGets, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: 10,
				Title:  "member issue",
				State:  "open",
				Labels: []github.Label{{Name: "scope:billing"}},
			})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(&gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	c.processAllPRs(context.Background())

	if _, ok := c.GetPRState(42); ok {
		t.Error("held PR should still be drained from tracking (existing removePR path)")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count = %d, want 0 (held PR must not tag)", got)
	}
	// GH-2297's merge-completion comment also posts here, so assert on
	// content rather than a raw count: exactly one comment must carry the
	// scope-hold breadcrumb.
	if got := atomic.LoadInt64(&commentPosts); got == 0 {
		t.Fatal("expected at least one comment to be posted")
	}
	holdComments := 0
	for _, body := range commentBodies {
		if strings.Contains(body, "held for scope release label:billing") {
			holdComments++
		}
	}
	if holdComments != 1 {
		t.Errorf("scope-hold comment count = %d, want exactly 1 (bodies: %v)", holdComments, commentBodies)
	}
}

// TestHandleMerged_SkipPostMergeCI_HoldsUnderScopeClose covers the dev-env
// fast path (SkipPostMergeCI + RequireCI=false): a scope-member merge is
// held (drained without releasing) instead of jumping straight to
// StageReleasing (GH-3989).
func TestHandleMerged_SkipPostMergeCI_HoldsUnderScopeClose(t *testing.T) {
	var issueGets int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues/10":
			atomic.AddInt64(&issueGets, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: 10,
				Title:  "member issue",
				State:  "open",
				Labels: []github.Label{{Name: "scope:billing"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // SkipPostMergeCI: true
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", RequireCI: false}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 77, IssueNumber: 10, Stage: StageMerged}
	c.mu.Lock()
	c.activePRs[77] = prState
	c.mu.Unlock()

	if err := c.handleMerged(context.Background(), prState); err != nil {
		t.Fatalf("handleMerged() error = %v", err)
	}

	if prState.Stage == StageReleasing {
		t.Error("held PR must not advance to StageReleasing")
	}
	if _, ok := c.GetPRState(77); ok {
		t.Error("held PR should be drained from tracking")
	}
	if got := atomic.LoadInt64(&issueGets); got == 0 {
		t.Error("expected at least one GetIssue call to evaluate scope membership")
	}
}

// TestHandlePostMergeCI_HoldsUnderScopeClose covers the post-merge CI success
// branch: a scope-member merge is held (drained) rather than advancing to
// StageReleasing once CI passes (GH-3989).
func TestHandlePostMergeCI_HoldsUnderScopeClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"commit": map[string]string{"sha": "mainsha42"}})
		case "/repos/owner/repo/commits/mainsha42/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		case "/repos/owner/repo/issues/10":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: 10,
				Title:  "member issue",
				State:  "open",
				Labels: []github.Label{{Name: "scope:billing"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // SkipPostMergeCI: false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 88, IssueNumber: 10, Stage: StagePostMergeCI}
	c.mu.Lock()
	c.activePRs[88] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if prState.Stage == StageReleasing {
		t.Error("held PR must not advance to StageReleasing")
	}
	if _, ok := c.GetPRState(88); ok {
		t.Error("held PR should be drained from tracking")
	}
}

// TestReleaseTrigger_OnMerge_BackwardCompatSnapshot pins Trigger "on_merge"
// (absent/default) to byte-identical behavior: releaseActionFor always
// releases immediately and never calls GetIssue, matching pre-GH-3989
// on-merge semantics exactly (zero new API calls).
func TestReleaseTrigger_OnMerge_BackwardCompatSnapshot(t *testing.T) {
	var issueGets int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") {
			atomic.AddInt64(&issueGets, 1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	action, scopeKey, scopeTitle := c.releaseActionFor(context.Background(), 10)
	if action != releaseActionRelease {
		t.Errorf("action = %v, want releaseActionRelease", action)
	}
	if scopeKey != "" || scopeTitle != "" {
		t.Errorf("scopeKey/scopeTitle = %q/%q, want empty for on_merge", scopeKey, scopeTitle)
	}
	if got := atomic.LoadInt64(&issueGets); got != 0 {
		t.Errorf("GetIssue call count = %d, want 0 (on_merge must never consult scope membership)", got)
	}
}

// externalMergeCIServer builds a fake GitHub server for the checkExternalMergeOrClose
// require_ci tests below: GET the PR as merged, serve issue/label/comment bookkeeping
// endpoints, count check-runs polls against mainSHA (so tests can assert whether CI
// was consulted before releasing), and serve the tag/compare/git-refs endpoints
// handleReleasing needs so tests can tick one step further and assert a tag is cut
// against mergeSHA (GH-4146) instead of stopping at StageReleasing. gitRefPosts may
// be nil for tests that never drive that far.
func externalMergeCIServer(t *testing.T, prNumber int, mergeSHA, mainSHA string, checkRunPolls, gitRefPosts *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == fmt.Sprintf("/repos/owner/repo/pulls/%d", prNumber):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:         prNumber,
				State:          "closed",
				Merged:         true,
				HTMLURL:        fmt.Sprintf("https://github.com/owner/repo/pull/%d", prNumber),
				MergeCommitSHA: mergeSHA,
			})
		case r.URL.Path == "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": mainSHA}})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			if checkRunPolls != nil {
				atomic.AddInt64(checkRunPolls, 1)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/") && strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 10, Title: "issue", State: "open"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: add a thing")})
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ahead"})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			if gitRefPosts != nil {
				atomic.AddInt64(gitRefPosts, 1)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestCheckExternalMergeOrClose_RequireCIFalse_DirectRelease is the GH-3994
// regression pin: with require_ci false (the pre-fix default for a bare
// ReleaseConfig literal), an externally merged PR must still jump straight to
// StageReleasing at the merge commit SHA, with zero check-runs polls. This is
// the exact GH-411 external-merge semantics that must survive the fix.
func TestCheckExternalMergeOrClose_RequireCIFalse_DirectRelease(t *testing.T) {
	var checkRunPolls int64
	server := externalMergeCIServer(t, 42, "merge-sha-42", "mainsha", &checkRunPolls, nil)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: false}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 10, Stage: StageWaitingCI}
	c.mu.Lock()
	c.activePRs[42] = prState
	c.mu.Unlock()

	ctx := context.Background()
	ghPR, err := ghClient.GetPullRequest(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}

	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()

	if resolved {
		t.Fatal("checkExternalMergeOrClose should return false (continue processing to release), not remove the PR")
	}
	if prState.Stage != StageReleasing {
		t.Errorf("Stage = %v, want StageReleasing (direct jump, require_ci false)", prState.Stage)
	}
	if prState.HeadSHA != "merge-sha-42" {
		t.Errorf("HeadSHA = %q, want merge commit sha", prState.HeadSHA)
	}
	if got := atomic.LoadInt64(&checkRunPolls); got != 0 {
		t.Errorf("check-runs poll count = %d, want 0 (require_ci false must never consult CI)", got)
	}
}

// TestCheckExternalMergeOrClose_RequireCITrue_RoutesToPostMergeCI verifies the
// GH-3994 fix: with require_ci true, an externally merged PR is routed through
// StagePostMergeCI (PostMergeSHA = merge commit SHA, PostMergeCIStartedAt set)
// instead of hijacking straight to StageReleasing. Then drives it forward via
// handlePostMergeCI to confirm CI success reaches StageReleasing (same path as
// the pre-existing handleMerged flow) — the fix reuses the existing gate,
// it doesn't invent a new one. Finally ticks one step further into
// handleReleasing (GH-4146) to confirm the tag is actually cut against the
// merge SHA — the pre-fix bug left HeadSHA at its stale pre-merge value here,
// which always compared "diverged" against main and escalated to StageFailed.
func TestCheckExternalMergeOrClose_RequireCITrue_RoutesToPostMergeCI(t *testing.T) {
	var checkRunPolls, gitRefPosts int64
	server := externalMergeCIServer(t, 43, "merge-sha-43", "mainsha", &checkRunPolls, &gitRefPosts)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // SkipPostMergeCI: false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: true}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 43, IssueNumber: 10, Stage: StageWaitingCI}
	c.mu.Lock()
	c.activePRs[43] = prState
	c.mu.Unlock()

	ctx := context.Background()
	ghPR, err := ghClient.GetPullRequest(ctx, "owner", "repo", 43)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}

	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()

	if resolved {
		t.Fatal("checkExternalMergeOrClose should return false (continue processing), not remove the PR")
	}
	if prState.Stage != StagePostMergeCI {
		t.Fatalf("Stage = %v, want StagePostMergeCI (require_ci true must gate the external-merge hijack)", prState.Stage)
	}
	if prState.PostMergeSHA != "merge-sha-43" {
		t.Errorf("PostMergeSHA = %q, want merge commit sha", prState.PostMergeSHA)
	}
	if prState.PostMergeCIStartedAt.IsZero() {
		t.Error("PostMergeCIStartedAt should be set once StagePostMergeCI begins")
	}
	if got := atomic.LoadInt64(&checkRunPolls); got != 0 {
		t.Errorf("check-runs poll count = %d, want 0 (CI is polled by handlePostMergeCI, not the hijack itself)", got)
	}

	// Next tick: this PR must NOT be re-hijacked by checkExternalMergeOrClose —
	// handlePostMergeCI now owns it.
	prState.mu.Lock()
	resolvedAgain := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()
	if resolvedAgain {
		t.Fatal("checkExternalMergeOrClose should return false on a PostMergeCI-stage PR (guard), not remove it")
	}
	if prState.Stage != StagePostMergeCI {
		t.Fatalf("Stage = %v after a second tick, want StagePostMergeCI unchanged (no re-hijack)", prState.Stage)
	}

	// Drive it forward: CI succeeds -> StageReleasing (existing StagePostMergeCI semantics, reused).
	if err := c.handlePostMergeCI(ctx, prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}
	if prState.Stage != StageReleasing {
		t.Errorf("Stage = %v after CI success, want StageReleasing", prState.Stage)
	}
	if got := atomic.LoadInt64(&checkRunPolls); got != 1 {
		t.Errorf("check-runs poll count = %d, want 1 (handlePostMergeCI must consult CI before releasing)", got)
	}

	// Drive it one step further: StageReleasing -> tag cut against the merge
	// SHA (GH-4146). Pre-fix, HeadSHA was never resynced from PostMergeSHA for
	// a plain (non-scope) PR, so this always escalated to StageFailed instead.
	if err := c.handleReleasing(ctx, prState); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}
	if prState.Stage == StageFailed {
		t.Fatal("Stage = StageFailed after handleReleasing — HeadSHA was not resynced from PostMergeSHA")
	}
	if prState.HeadSHA != "merge-sha-43" {
		t.Errorf("HeadSHA = %q, want resynced to PostMergeSHA merge-sha-43", prState.HeadSHA)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count = %d, want 1 (tag must be created against the merge SHA)", got)
	}
}

// TestScanRecentlyMergedPRs_RequireCI_RoutesToPostMergeCI verifies GH-3994's
// scan-recovery fix: with require_ci true, a merged Pilot PR discovered by the
// periodic scan (e.g. after a daemon restart) is registered at StagePostMergeCI
// with PostMergeSHA set to the merge commit SHA, not StageReleasing with an
// assumed CISuccess.
func TestScanRecentlyMergedPRs_RequireCI_RoutesToPostMergeCI(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var gitRefPosts, issueGets int64
	base := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{}, &gitRefPosts, &issueGets)
	defer base.Close()

	prs := []github.PullRequest{
		{
			Number:         60,
			Head:           github.PRRef{Ref: "pilot/GH-400", SHA: "sha60"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/60",
			Title:          "feat: needs post-merge CI",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "merge-sha-60",
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") && !strings.Contains(r.URL.Path, "/commits") {
			out := make([]*github.PullRequest, len(prs))
			for i := range prs {
				out[i] = &prs[i]
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
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

	prState, ok := c.GetPRState(60)
	if !ok {
		t.Fatal("PR 60 should be registered (require_ci must not skip tracking)")
	}
	if prState.Stage != StagePostMergeCI {
		t.Errorf("Stage = %v, want StagePostMergeCI (require_ci true must gate scan-recovery)", prState.Stage)
	}
	if prState.PostMergeSHA != "merge-sha-60" {
		t.Errorf("PostMergeSHA = %q, want merge commit sha", prState.PostMergeSHA)
	}
	if prState.CIStatus == CISuccess {
		t.Error("CIStatus must not be pre-set to CISuccess when require_ci is true — CI hasn't run yet")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count = %d, want 0 (must not tag before post-merge CI completes)", got)
	}

	// Drive it forward: CI succeeds -> StageReleasing -> tag cut against the
	// merge SHA (GH-4146). The scan-recovery HeadSHA is the pre-merge branch
	// head ("sha60"), which never converges with main after a squash merge;
	// pre-fix, handleReleasing never resynced it from PostMergeSHA for a plain
	// PR, so this always escalated to StageFailed instead of releasing.
	ctx := context.Background()
	if err := c.handlePostMergeCI(ctx, prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}
	if prState.Stage != StageReleasing {
		t.Fatalf("Stage = %v after CI success, want StageReleasing", prState.Stage)
	}
	if err := c.handleReleasing(ctx, prState); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}
	if prState.Stage == StageFailed {
		t.Fatal("Stage = StageFailed after handleReleasing — HeadSHA was not resynced from PostMergeSHA")
	}
	if prState.HeadSHA != "merge-sha-60" {
		t.Errorf("HeadSHA = %q, want resynced to PostMergeSHA merge-sha-60", prState.HeadSHA)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count = %d, want 1 (tag must be created against the merge SHA)", got)
	}
}

// TestScanRecentlyMergedPRs_SkipsPersistedFailed verifies GH-4312: a merged PR
// persisted at StageFailed (e.g. a prior post-merge CI failure) must not be
// re-registered by the scan. RestoreState intentionally excludes StageFailed
// rows from activePRs (controller.go:945), so after a simulated daemon
// restart (row persisted, activePRs empty) the scan's in-memory
// "already tracked" gate alone cannot catch it — only the state-store lookup
// added alongside this test can.
func TestScanRecentlyMergedPRs_SkipsPersistedFailed(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var gitRefPosts, issueGets int64
	base := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{}, &gitRefPosts, &issueGets)
	defer base.Close()

	prs := []github.PullRequest{
		{
			Number:         70,
			Head:           github.PRRef{Ref: "pilot/GH-500", SHA: "sha70"},
			Base:           github.PRRef{Ref: "main"},
			HTMLURL:        "https://github.com/owner/repo/pull/70",
			Title:          "fix: previously failed post-merge CI",
			Merged:         true,
			MergedAt:       recentMergedAt,
			MergeCommitSHA: "merge-sha-70",
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") && !strings.Contains(r.URL.Path, "/commits") {
			out := make([]*github.PullRequest, len(prs))
			for i := range prs {
				out[i] = &prs[i]
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: true, TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	// Simulate a daemon restart: the PR was previously driven to StageFailed by
	// handlePostMergeCI's CIFailure branch and persisted, but RestoreState does
	// not reload 'failed' rows into activePRs.
	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 70,
		PRURL:    "https://github.com/owner/repo/pull/70",
		Stage:    StageFailed,
		Error:    "post-merge CI failed at merge-sh",
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if _, ok := c.GetPRState(70); ok {
		t.Error("PR 70 is persisted at StageFailed and must not be re-registered by the scan")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count = %d, want 0 (a failed PR must never be tagged)", got)
	}
}

// TestCheckExternalMergeOrClose_StageGuards is a table-driven pin of every
// early-return guard in checkExternalMergeOrClose: a scope-release carrier,
// a PostMergeCI-stage PR (GH-3994), and a Releasing-stage PR (GH-4124) must
// all bounce off the hijack unresolved (return false) and stay tracked —
// none of them may be handed to removePR before their owning handler
// (handlePostMergeCI / handleReleasing) has run.
func TestCheckExternalMergeOrClose_StageGuards(t *testing.T) {
	tests := []struct {
		name     string
		stage    PRStage
		scopeKey string
	}{
		{name: "scope carrier guard", scopeKey: "epic:1", stage: StageMerging},
		{name: "post_merge_ci guard (GH-3994)", stage: StagePostMergeCI},
		{name: "releasing guard (GH-4124)", stage: StageReleasing},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prNumber := 100 + i
			prState := &PRState{PRNumber: prNumber, IssueNumber: 10, Stage: tt.stage, ScopeKey: tt.scopeKey}
			c.mu.Lock()
			c.activePRs[prNumber] = prState
			c.mu.Unlock()

			ghPR := &github.PullRequest{Number: prNumber, Merged: true, State: "closed"}

			prState.mu.Lock()
			resolved := c.checkExternalMergeOrClose(context.Background(), prState, ghPR)
			prState.mu.Unlock()

			if resolved {
				t.Errorf("checkExternalMergeOrClose returned true, want false (must not drain a %s PR)", tt.name)
			}
			if _, ok := c.GetPRState(prNumber); !ok {
				t.Errorf("PR should still be tracked after checkExternalMergeOrClose (removePR must not run for %s)", tt.name)
			}
		})
	}
}

// TestCheckExternalMergeOrClose_StageReleasing_NotDrained is the GH-4124
// regression pin: a merged, non-scope PR already at StageReleasing must be
// left alone by the external-merge drain — it is owned by handleReleasing's
// own tick. Without the StageReleasing guard, execution falls through to
// removePR because the GH-411 release-trigger block above only fires when
// Stage != StageReleasing, so the PR is silently drained and no tag is ever
// cut. This test drives both halves: checkExternalMergeOrClose must return
// false without removing the PR, and the subsequent ProcessPR dispatch to
// handleReleasing must then cut exactly one tag.
func TestCheckExternalMergeOrClose_StageReleasing_NotDrained(t *testing.T) {
	var gitRefPosts, issueGets int64
	server := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{
		10: {title: "issue", state: "open"},
	}, &gitRefPosts, &issueGets)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 10, Stage: StageReleasing, HeadSHA: "merge-sha-42"}
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

	ctx := context.Background()

	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()

	if resolved {
		t.Fatal("checkExternalMergeOrClose should return false (must not drain a StageReleasing PR), not remove it")
	}
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should still be tracked after checkExternalMergeOrClose — the StageReleasing guard must not call removePR")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count after checkExternalMergeOrClose = %d, want 0 (no tag yet)", got)
	}

	// The main loop dispatches StageReleasing PRs to handleReleasing via
	// ProcessPR. Confirm that path now runs and cuts a tag exactly once.
	if err := c.ProcessPR(ctx, 42, ghPR); err != nil {
		t.Fatalf("ProcessPR() error = %v", err)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count after ProcessPR = %d, want 1 (handleReleasing must cut the tag)", got)
	}
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be drained from tracking after a successful release")
	}
}

// TestCheckExternalMergeOrClose_RequireCITrue_FullLoop_CutsTagWithoutDraining
// is the GH-4124 end-to-end regression: a require_ci merged PR routed
// external-merge -> post_merge_ci -> (CI success) -> releasing must reach
// handleReleasing and cut a tag on the very next tick, instead of being
// drained by checkExternalMergeOrClose before handleReleasing ever runs
// (the wedge that silently blocked every require_ci on_merge release since
// GH-3994).
func TestCheckExternalMergeOrClose_RequireCITrue_FullLoop_CutsTagWithoutDraining(t *testing.T) {
	var gitRefPosts, issueGets int64
	base := releaseCyclesServer(t, map[int]scopeMembershipFakeIssue{
		10: {title: "issue", state: "open"},
	}, &gitRefPosts, &issueGets)
	defer base.Close()

	var checkRunPolls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			atomic.AddInt64(&checkRunPolls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
			return
		}
		base.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // SkipPostMergeCI: false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", RequireCI: true, TagPrefix: "v"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 44, IssueNumber: 10, Stage: StageWaitingCI}
	c.mu.Lock()
	c.activePRs[44] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number:         44,
		State:          "closed",
		Merged:         true,
		HTMLURL:        "https://github.com/owner/repo/pull/44",
		MergeCommitSHA: "merge-sha-44",
	}
	ctx := context.Background()

	// Tick 1: external-merge hijack routes to StagePostMergeCI (RequireCI true).
	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()
	if resolved {
		t.Fatal("checkExternalMergeOrClose should return false on tick 1 (routes to StagePostMergeCI)")
	}
	if prState.Stage != StagePostMergeCI {
		t.Fatalf("Stage after tick 1 = %v, want StagePostMergeCI", prState.Stage)
	}

	// Tick 2: handlePostMergeCI observes CI success and advances to StageReleasing
	// (mirrors the main loop dispatching StagePostMergeCI via ProcessPR).
	if err := c.ProcessPR(ctx, 44, ghPR); err != nil {
		t.Fatalf("ProcessPR() (post_merge_ci tick) error = %v", err)
	}
	if prState.Stage != StageReleasing {
		t.Fatalf("Stage after tick 2 = %v, want StageReleasing", prState.Stage)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count after tick 2 = %d, want 0 (not releasing yet)", got)
	}

	// Tick 3: this is the exact GH-4124 wedge point. Before the fix,
	// checkExternalMergeOrClose ran again ahead of the stage dispatch on every
	// poll and drained the PR here because it has no StageReleasing guard —
	// handleReleasing never ran, and the PR looped post_merge_ci -> releasing
	// -> drained until it aged out with no tag ever cut.
	prState.mu.Lock()
	resolvedTick3 := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()
	if resolvedTick3 {
		t.Fatal("checkExternalMergeOrClose should return false on tick 3 (StageReleasing guard) — this is the GH-4124 wedge")
	}
	if _, ok := c.GetPRState(44); !ok {
		t.Fatal("PR should still be tracked after tick 3 — must not be drained before handleReleasing runs")
	}

	// Tick 3 continued: the main loop's stage dispatch now reaches
	// handleReleasing and cuts the tag within this same releasing tick.
	if err := c.ProcessPR(ctx, 44, ghPR); err != nil {
		t.Fatalf("ProcessPR() (releasing tick) error = %v", err)
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count after releasing tick = %d, want 1 (handleReleasing must cut the tag)", got)
	}
	if _, ok := c.GetPRState(44); ok {
		t.Error("PR should be drained from tracking after a successful release")
	}
}
