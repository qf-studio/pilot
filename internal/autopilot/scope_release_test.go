package autopilot

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// scopeCarrierServer builds a fake GitHub server covering everything a scope
// carrier needs to flow from StagePostMergeCI through a tagged release:
// branch/check-runs for post-merge CI, member PR commits, releases/tags for
// version resolution, and /git/refs for tag creation. gitRefPosts counts
// POST /git/refs calls.
func scopeCarrierServer(t *testing.T, memberCommits map[int][]*github.Commit, gitRefPosts *int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case r.URL.Path == "/repos/owner/repo/commits/mainsha/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			var num int
			_, _ = fmtSscanIssueNum(strings.Replace(r.URL.Path, "/pulls/", "/issues/", 1), &num)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(memberCommits[num])
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "identical"})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/releases/tags/v1.1.0"):
			// Post-tag verification (afterTagCreated, "workflow" mode) — report
			// the release as already published so the background poll goroutine
			// returns immediately instead of running past test teardown.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.1.0", HTMLURL: "https://github.com/owner/repo/releases/tag/v1.1.0"})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

func newScopeCarrierController(t *testing.T, server *httptest.Server) (*Controller, *StateStore) {
	t.Helper()
	stateStore := newTestStateStore(t)

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.Release = &ReleaseConfig{
		Enabled:          true,
		Trigger:          "on_scope_close",
		ScopeLabelPrefix: "scope:",
		TagPrefix:        "v",
		RequireCI:        true,
		VerifyRelease:    boolPtr(false),
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)
	return c, stateStore
}

// TestScopeCarrierFullFlow drives a scope-release carrier end to end: a
// pending scope row with two members claims a carrier, the carrier captures
// main's SHA, waits for post-merge CI, unions both members' commits, and cuts
// exactly one tag — leaving the scope row 'done' (GH-3990).
func TestScopeCarrierFullFlow(t *testing.T) {
	commit101 := makeCommit("fix: member one")
	commit101.SHA = "sha-101"
	commit102 := makeCommit("feat: member two")
	commit102.SHA = "sha-102"

	var gitRefPosts int64
	server := scopeCarrierServer(t, map[int][]*github.Commit{
		101: {commit101},
		102: {commit102},
	}, &gitRefPosts)
	defer server.Close()

	c, stateStore := newScopeCarrierController(t, server)

	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "Checkout epic", []int{101, 102}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	c.startPendingScopeReleases(context.Background())

	carrier, ok := c.GetPRState(102)
	if !ok {
		t.Fatal("expected carrier registered at anchor PR 102 (highest member)")
	}
	if carrier.Stage != StagePostMergeCI {
		t.Errorf("carrier stage = %v, want StagePostMergeCI", carrier.Stage)
	}
	if carrier.ScopeKey != "epic:1" {
		t.Errorf("carrier ScopeKey = %q, want epic:1", carrier.ScopeKey)
	}
	if carrier.IssueNumber != 1 {
		t.Errorf("carrier IssueNumber = %d, want 1 (parsed from epic:1)", carrier.IssueNumber)
	}

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "releasing" {
		t.Errorf("scope release state = %q, want releasing (claimed)", row.State)
	}

	// Drive post-merge CI to completion — CISuccess sends the carrier straight
	// to StageReleasing (no hold re-check).
	if err := c.handlePostMergeCI(context.Background(), carrier); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}
	if carrier.Stage != StageReleasing {
		t.Fatalf("carrier stage after CI success = %v, want StageReleasing", carrier.Stage)
	}
	if carrier.PostMergeSHA != "mainsha" {
		t.Errorf("carrier PostMergeSHA = %q, want mainsha", carrier.PostMergeSHA)
	}

	if err := c.handleReleasing(context.Background(), carrier); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}

	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count = %d, want exactly 1", got)
	}
	if carrier.HeadSHA != "mainsha" {
		t.Errorf("carrier HeadSHA = %q, want mainsha (set from PostMergeSHA)", carrier.HeadSHA)
	}
	if carrier.ReleaseVersion != "v1.1.0" {
		t.Errorf("carrier ReleaseVersion = %q, want v1.1.0 (minor bump from the feat: commit)", carrier.ReleaseVersion)
	}

	if _, ok := c.GetPRState(102); ok {
		t.Error("carrier should be drained from tracking after a successful release")
	}

	row, err = stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "done" {
		t.Errorf("scope release state = %q, want done", row.State)
	}
	if row.Tag != "v1.1.0" {
		t.Errorf("scope release tag = %q, want v1.1.0", row.Tag)
	}
}

// TestScopeCarrier_InterleavedStandaloneTag verifies that a standalone tag
// landing on (or ahead of) the carrier's final SHA does not falsely drain the
// scope release without tagging: handleReleasing must skip the
// tagCoveringCommit/GetTagForSHA drains for a scope carrier and still cut its
// own tag (GH-3990 edge case: "interleaved standalone tag").
func TestScopeCarrier_InterleavedStandaloneTag(t *testing.T) {
	var gitRefPosts int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: scoped work")})
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			// An interleaved standalone release already covers this exact SHA.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"v1.0.1","commit":{"sha":"mainsha"}}]`))
		case strings.Contains(r.URL.Path, "/branches/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "identical"})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(&gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	c, _ := newScopeCarrierController(t, server)
	carrier := &PRState{
		PRNumber:       200,
		Stage:          StageReleasing,
		PostMergeSHA:   "mainsha",
		ScopeKey:       "epic:1",
		ScopeTitle:     "epic",
		ScopeMemberPRs: []int{200},
	}
	c.mu.Lock()
	c.activePRs[200] = carrier
	c.mu.Unlock()

	if err := c.handleReleasing(context.Background(), carrier); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}

	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count = %d, want 1 (scope carrier must still tag despite the interleaved standalone tag on the same SHA)", got)
	}
}

// TestScopeCarrier_CIFailure_RequeuesForRetry covers the post-merge CI-red
// path: the carrier is drained, the scope row goes back to 'pending' with
// attempts incremented, and no tag is ever created (GH-3990).
func TestScopeCarrier_CIFailure_RequeuesForRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case r.URL.Path == "/repos/owner/repo/commits/mainsha/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "failure"}},
			})
		case r.URL.Path == "/repos/owner/repo/issues" || strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"number": 999, "id": 1})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	c, stateStore := newScopeCarrierController(t, server)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{55}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	c.startPendingScopeReleases(context.Background())

	carrier, ok := c.GetPRState(55)
	if !ok {
		t.Fatal("expected carrier registered")
	}

	if err := c.handlePostMergeCI(context.Background(), carrier); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if _, ok := c.GetPRState(55); ok {
		t.Error("carrier should be drained after CI failure")
	}

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "pending" {
		t.Errorf("scope release state = %q, want pending (re-queued for retry)", row.State)
	}
	if row.Attempts != 1 {
		t.Errorf("scope release attempts = %d, want 1", row.Attempts)
	}
}

// TestScopeCarrier_AttemptsCapEscalatesToFailedAlert verifies that once a
// scope release has failed more than maxScopeReleaseAttempts times, it is
// marked terminal-failed and a scope_release_failed alert fires instead of
// re-queuing indefinitely (GH-3990).
func TestScopeCarrier_AttemptsCapEscalatesToFailedAlert(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	// Pre-load the row to attempts=maxScopeReleaseAttempts so the next failure crosses the cap.
	for i := 0; i < maxScopeReleaseAttempts; i++ {
		if err := stateStore.MarkScopeReleasePending("owner/repo", "epic:1", true, "redsha"); err != nil {
			t.Fatalf("MarkScopeReleasePending failed: %v", err)
		}
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 9, ScopeKey: "epic:1", IssueNumber: 1}
	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI failed", false)

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "failed" {
		t.Errorf("scope release state = %q, want failed (attempts cap exceeded)", row.State)
	}

	found := false
	for _, e := range sink.events {
		if e.Type == alerts.EventType("scope_release_failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a scope_release_failed alert event, got: %+v", sink.events)
	}
}

// TestScopeCarrier_RestartResumesFromPostMergeCI persists a carrier at
// StagePostMergeCI, simulates a daemon restart with a fresh Controller against
// the same StateStore, and verifies RestoreState resumes it through to a
// completed release (GH-3990).
func TestScopeCarrier_RestartResumesFromPostMergeCI(t *testing.T) {
	var gitRefPosts int64
	server := scopeCarrierServer(t, map[int][]*github.Commit{
		301: {makeCommit("feat: restart survivor")},
	}, &gitRefPosts)
	defer server.Close()

	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:2", "Restart epic", []int{301}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := stateStore.ClaimScopeRelease("owner/repo", "epic:2"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}

	// Simulate a carrier that reached StagePostMergeCI (SHA already captured)
	// before the daemon died — persisted the same way tryStartScopeRelease +
	// handlePostMergeCI would have left it.
	carrier := &PRState{
		PRNumber:     301,
		PRURL:        "https://github.com/owner/repo/pull/301",
		IssueNumber:  2,
		Stage:        StagePostMergeCI,
		PostMergeSHA: "mainsha",
		ScopeKey:     "epic:2",
	}
	if err := stateStore.SavePRState("owner/repo", carrier); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	// "Restart": brand new Controller against the same StateStore.
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.Release = &ReleaseConfig{
		Enabled: true, Trigger: "on_scope_close", TagPrefix: "v",
		RequireCI: true, VerifyRelease: boolPtr(false),
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	restored, ok := c.GetPRState(301)
	if !ok {
		t.Fatal("expected carrier restored into activePRs")
	}
	if restored.ScopeKey != "epic:2" {
		t.Errorf("restored ScopeKey = %q, want epic:2", restored.ScopeKey)
	}
	if len(restored.ScopeMemberPRs) != 0 {
		t.Errorf("restored ScopeMemberPRs = %v, want empty (not persisted — hydrated lazily)", restored.ScopeMemberPRs)
	}

	if err := c.handlePostMergeCI(context.Background(), restored); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}
	if restored.Stage != StageReleasing {
		t.Fatalf("stage after CI success = %v, want StageReleasing", restored.Stage)
	}

	if err := c.handleReleasing(context.Background(), restored); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}
	if restored.ScopeMemberPRs == nil || restored.ScopeMemberPRs[0] != 301 {
		t.Errorf("ScopeMemberPRs after hydration = %v, want [301]", restored.ScopeMemberPRs)
	}

	if got := atomic.LoadInt64(&gitRefPosts); got != 1 {
		t.Errorf("git/refs POST count = %d, want 1", got)
	}

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:2")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "done" {
		t.Errorf("scope release state = %q, want done", row.State)
	}
}

// TestStartPendingScopeReleases_RedrivesStaleReleasingRow verifies that a
// 'releasing' row left behind with no live carrier (daemon killed after claim,
// before the carrier was ever registered) is re-driven back to pending and
// then re-claimed with a fresh carrier (GH-3990).
func TestStartPendingScopeReleases_RedrivesStaleReleasingRow(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:3", "epic", []int{7}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	// Claim without ever registering a carrier — simulates a crash right after
	// ClaimScopeRelease, before tryStartScopeRelease published it to activePRs.
	if _, err := stateStore.ClaimScopeRelease("owner/repo", "epic:3"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	c.startPendingScopeReleases(context.Background())

	if _, ok := c.GetPRState(7); !ok {
		t.Fatal("expected a fresh carrier registered after re-driving the stale releasing row")
	}
	row, err := stateStore.GetScopeRelease("owner/repo", "epic:3")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "releasing" {
		t.Errorf("scope release state = %q, want releasing (re-claimed)", row.State)
	}
}

// TestCheckExternalMergeOrClose_IgnoresScopeCarrier verifies the GH-411
// external-merge hijack never touches a scope-release carrier: the carrier's
// anchor PR number is a real, already-merged member PR, so without the
// ScopeKey guard checkExternalMergeOrClose would re-run issue-close/label
// bookkeeping and re-evaluate release triggering on every poll tick (GH-3990).
func TestCheckExternalMergeOrClose_IgnoresScopeCarrier(t *testing.T) {
	var closeCalls, commentCalls int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
			atomic.AddInt64(&closeCalls, 1)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			atomic.AddInt64(&commentCalls, 1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	carrier := &PRState{PRNumber: 102, IssueNumber: 1, ScopeKey: "epic:1"}
	ghPR := &github.PullRequest{Number: 102, Merged: true, State: "closed"}

	resolved := c.checkExternalMergeOrClose(context.Background(), carrier, ghPR)
	if resolved {
		t.Error("checkExternalMergeOrClose returned true for a scope carrier, want false (must never hijack it)")
	}
	if got := atomic.LoadInt64(&closeCalls); got != 0 {
		t.Errorf("issue close calls = %d, want 0", got)
	}
	if got := atomic.LoadInt64(&commentCalls); got != 0 {
		t.Errorf("comment calls = %d, want 0", got)
	}
}

// TestScanRecentlyMergedPRs_SkipsScopeMemberPending verifies the scanner never
// cuts a per-merge tag for a PR that is still a pending/in-flight scope-release
// member — closing the race where the scope has already completed and closed
// the epic parent (heldByScope would fail open) before the carrier tags
// (GH-3990).
func TestScanRecentlyMergedPRs_SkipsScopeMemberPending(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)

	var gitRefPosts int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls") && !strings.Contains(r.URL.Path, "/commits"):
			prs := []*github.PullRequest{{
				Number:         42,
				Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha42"},
				Base:           github.PRRef{Ref: "main"},
				HTMLURL:        "https://github.com/owner/repo/pull/42",
				Title:          "feat(member): scoped work",
				Merged:         true,
				MergedAt:       recentMergedAt,
				MergeCommitSHA: "merge-sha-42",
			}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			atomic.AddInt64(&gitRefPosts, 1)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close", ScopeLabelPrefix: "scope:", TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	stateStore := newTestStateStore(t)
	c.SetStateStore(stateStore)

	// PR 42 is already a member of an in-flight scope release (e.g. its epic
	// closed and the carrier hasn't tagged yet).
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{42}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if _, ok := c.GetPRState(42); ok {
		t.Error("PR 42 (pending scope member) should not be registered by the scanner")
	}
	if got := atomic.LoadInt64(&gitRefPosts); got != 0 {
		t.Errorf("git/refs POST count = %d, want 0 (scanner must not cut a per-merge tag for a scope member)", got)
	}
}

// TestScopeCarrierAPIMode_ReleaseBodyContainsAllElements drives a scope
// carrier through publish mode "api" and verifies the release body created by
// CreateReleaseForRepo (handleReleasing's synchronous body — the LLM
// "What's New" addition is applied asynchronously afterward and is covered
// separately by TestEnrichScopeReleaseNotes) contains the locked notes
// requirements: scope headline, grouped Features with exact #PR + GH-issue
// attribution, a Breaking Changes section, and a compare + stats footer
// (GH-3992).
func TestScopeCarrierAPIMode_ReleaseBodyContainsAllElements(t *testing.T) {
	var createdBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "mainsha"}})
		case r.URL.Path == "/repos/owner/repo/commits/mainsha/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		case strings.Contains(r.URL.Path, "/pulls/101") && strings.HasSuffix(r.URL.Path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat(checkout)!: drop legacy coupon codes")})
		case r.URL.Path == "/repos/owner/repo/pulls/101":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.PullRequest{Number: 101, Body: "Closes #201", Merged: true})
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Release{TagName: "v1.0.0"})
		case strings.HasSuffix(r.URL.Path, "/tags"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/compare/"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "identical"})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/releases":
			var input map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&input)
			createdBody, _ = input["body"].(string)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Release{ID: 99, HTMLURL: "https://github.com/owner/repo/releases/tag/v2.0.0"})
		case strings.HasSuffix(r.URL.Path, "/git/refs"):
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	stateStore := newTestStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.Release = &ReleaseConfig{
		Enabled:           true,
		Trigger:           "on_scope_close",
		ScopeLabelPrefix:  "scope:",
		TagPrefix:         "v",
		RequireCI:         true,
		VerifyRelease:     boolPtr(false),
		Publish:           ReleasePublishAPI,
		GenerateChangelog: true,
		GenerateSummary:   true,
	}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)
	c.SetReleaseSummaryGenerator(&ReleaseSummaryGenerator{
		ghClient:   ghClient,
		apiKey:     testutil.FakeAnthropicKey,
		httpClient: http.DefaultClient,
		apiURL:     "http://127.0.0.1:0", // never called synchronously by handleReleasing's api-mode body
		log:        slog.Default(),
	})

	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "Checkout epic", []int{101}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	c.startPendingScopeReleases(context.Background())

	carrier, ok := c.GetPRState(101)
	if !ok {
		t.Fatal("expected carrier registered")
	}
	if err := c.handlePostMergeCI(context.Background(), carrier); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}
	if err := c.handleReleasing(context.Background(), carrier); err != nil {
		t.Fatalf("handleReleasing() error = %v", err)
	}

	for _, want := range []string{
		"# Checkout epic",
		"## Features",
		"(#101, GH-201)",
		"## ⚠ Breaking Changes",
		"**Full Changelog**:",
		"_1 PRs, 1 commits_",
	} {
		if !strings.Contains(createdBody, want) {
			t.Errorf("release body missing %q\n--- got ---\n%s", want, createdBody)
		}
	}
}

// TestHandleScopeReleaseFailure_TerminalFailedRowIsNotResurrected is the
// GH-4331 regression guard for the zombie-carrier bounce identified in the
// RCA: a scope-release row that already reached terminal 'failed' (attempts
// exhausted, human alerted) must never be flipped back to 'pending' by a
// restart-rehydrated carrier still ticking against it. Before the fix,
// MarkScopeReleasePending had no state guard, so each failure bounced the row
// failed->pending->failed and kept inflating attempts forever (observed in
// production: train:07-13 attempts=19, train:07-14 attempts=11).
func TestHandleScopeReleaseFailure_TerminalFailedRowIsNotResurrected(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	// Seed the row directly as already terminal-failed with attempts past the
	// cap — simulates state left behind by a prior daemon run.
	if _, err := stateStore.db.Exec(
		`UPDATE autopilot_scope_release SET state = 'failed', attempts = 6, last_failed_sha = 'redsha' WHERE repo = ? AND scope_key = ?`,
		"owner/repo", "epic:1",
	); err != nil {
		t.Fatalf("failed to seed terminal row: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)
	c.SetAlertsEngine(&fakeAlertSink{})

	// Restart-rehydrated zombie carrier: still tracked at StagePostMergeCI
	// even though its scope already resolved terminal.
	prState := &PRState{PRNumber: 9, ScopeKey: "epic:1", IssueNumber: 1, Stage: StagePostMergeCI, PostMergeSHA: "redsha"}

	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI failed", false)
	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI failed", false)

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "failed" {
		t.Errorf("state = %q, want failed (terminal row must not be resurrected)", row.State)
	}
	if row.Attempts != 6 {
		t.Errorf("attempts = %d, want 6 (unchanged)", row.Attempts)
	}
}

// TestMarkScopeReleaseDone_EmptyTagDoesNotBlankRecordedTag is the GH-4331
// regression guard: a restart replay that re-observes an already-'done' scope
// as a no-op release (empty tag, e.g. CompareCommits finds nothing new past
// the tag) must not blank the tag a prior pass already recorded.
func TestMarkScopeReleaseDone_EmptyTagDoesNotBlankRecordedTag(t *testing.T) {
	store := newTestStateStore(t)
	if err := store.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{1}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := store.ClaimScopeRelease("owner/repo", "epic:1"); err != nil {
		t.Fatalf("ClaimScopeRelease failed: %v", err)
	}
	if err := store.MarkScopeReleaseDone("owner/repo", "epic:1", "v2.238.0", "sha1"); err != nil {
		t.Fatalf("MarkScopeReleaseDone failed: %v", err)
	}

	// Restart replay re-observes the same scope as a no-op release.
	if err := store.MarkScopeReleaseDone("owner/repo", "epic:1", "", "sha2"); err != nil {
		t.Fatalf("MarkScopeReleaseDone (no-op replay) failed: %v", err)
	}

	row, err := store.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.Tag != "v2.238.0" {
		t.Errorf("Tag = %q, want v2.238.0 (must not be blanked by no-op replay)", row.Tag)
	}
	if row.FinalSHA != "sha2" {
		t.Errorf("FinalSHA = %q, want sha2 (final_sha always updates)", row.FinalSHA)
	}
}

// TestStartPendingScopeReleases_DoesNotClaimFailedRows is a characterization
// pin (GH-4331): startPendingScopeReleases only ever lists 'releasing' and
// 'pending' rows for claiming, so a terminal 'failed' row (with no
// LastFailedSHA to compare against, i.e. nothing for recoverFailedScopeReleases
// to act on) is left untouched by the sweep. Passes both before and after the
// GH-4331 fix — guards against a naive "retry failed rows unconditionally"
// implementation.
func TestStartPendingScopeReleases_DoesNotClaimFailedRows(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := stateStore.db.Exec(
		`UPDATE autopilot_scope_release SET state = 'failed', attempts = 6 WHERE repo = ? AND scope_key = ?`,
		"owner/repo", "epic:1",
	); err != nil {
		t.Fatalf("failed to seed terminal row: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	c.startPendingScopeReleases(context.Background())

	if _, ok := c.GetPRState(9); ok {
		t.Error("expected no carrier registered for a terminal failed row")
	}
	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "failed" {
		t.Errorf("state = %q, want failed (unclaimed, unchanged)", row.State)
	}
}

// TestRecoverFailedScopeReleases_ResetsAttemptsWhenMainAdvances verifies the
// GH-4331 self-healing recovery path from the acceptance criteria: once main
// moves past the SHA a terminal 'failed' scope last failed against, the next
// startPendingScopeReleases sweep resurrects it as 'pending' with attempts
// reset to 0, and the same sweep's tryStartScopeRelease claim registers a
// fresh carrier — turning a same-day fix into a same-day release instead of
// leaving the train dead until tomorrow's scope rolls it forward.
func TestRecoverFailedScopeReleases_ResetsAttemptsWhenMainAdvances(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "main", "commit": map[string]string{"sha": "greensha"}})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	if _, err := stateStore.db.Exec(
		`UPDATE autopilot_scope_release SET state = 'failed', attempts = 6, last_failed_sha = 'redsha' WHERE repo = ? AND scope_key = ?`,
		"owner/repo", "epic:1",
	); err != nil {
		t.Fatalf("failed to seed terminal row: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	c.startPendingScopeReleases(context.Background())

	if _, ok := c.GetPRState(9); !ok {
		t.Fatal("expected a fresh carrier registered after self-recovery against the new main HEAD")
	}
	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "releasing" {
		t.Errorf("state = %q, want releasing (claimed by tryStartScopeRelease)", row.State)
	}
	if row.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (reset on recovery)", row.Attempts)
	}
}
