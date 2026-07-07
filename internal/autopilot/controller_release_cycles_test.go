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
