package autopilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/ghbudget"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4391: on 2026-07-16 the founder box's daemon burned its entire shared
// GitHub rate budget on startup rescans (11 repos x wide merged-PR/orphan-PR
// scans, fired back-to-back with no delay) and then 403'd every issue
// poller for 67+ minutes. These tests cover the autopilot-level half of the
// fix: PriorityBackground scans (merged-PR scan, orphan-PR sweep) pausing
// behind a shared rate-budget floor, the metric/WARN dedup, staggered
// per-repo startup, and the persisted-cursor window shrink. The ghbudget
// package itself (Tracker/RoundTripper) has its own test coverage in
// internal/ghbudget/ghbudget_test.go.

// headerWithRateLimit builds an http.Header carrying GitHub rate-limit
// headers, mirroring ghbudget_test.go's identically-named unexported helper
// (not reusable across packages).
func headerWithRateLimit(remaining, limit int) http.Header {
	h := make(http.Header)
	h.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	return h
}

// engagedTracker returns a *ghbudget.Tracker with the floor already engaged
// (well below DefaultFloorPct).
func engagedTracker() *ghbudget.Tracker {
	tr := ghbudget.NewTracker(ghbudget.DefaultFloorPct, nil)
	tr.Observe(headerWithRateLimit(10, 5000))
	return tr
}

func newTestSQLiteStateStore(t *testing.T) *StateStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStateStore(db)
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	return store
}

// TestController_ScanRecentlyMergedPRsWithWindow_SkippedWhenBudgetFloorEngaged
// verifies the GH-4391 acceptance criterion directly: a fake GitHub client
// returning rate-limit headers below the floor causes the merged-PR
// background scan to skip its ListPullRequests call entirely and record the
// floor-engagement metric.
func TestController_ScanRecentlyMergedPRsWithWindow_SkippedWhenBudgetFloorEngaged(t *testing.T) {
	var pullsCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			atomic.AddInt32(&pullsCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]*github.PullRequest{})
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateBudget(engagedTracker()))

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := atomic.LoadInt32(&pullsCalls); calls != 0 {
		t.Errorf("expected 0 ListPullRequests calls while budget floor engaged, got %d", calls)
	}
	if snap := c.metrics.Snapshot(); snap.RateLimitFloorEngagements != 1 {
		t.Errorf("RateLimitFloorEngagements = %d, want 1", snap.RateLimitFloorEngagements)
	}
}

// TestController_ScanRecentlyMergedPRsWithWindow_RunsWhenBudgetHealthy is the
// control for the above: a nil tracker (budget tracking not wired) must
// never gate the scan, matching ghbudget.Tracker.Allow's nil-safety contract.
func TestController_ScanRecentlyMergedPRsWithWindow_RunsWhenBudgetHealthy(t *testing.T) {
	var pullsCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			atomic.AddInt32(&pullsCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]*github.PullRequest{})
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo") // no WithRateBudget

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := atomic.LoadInt32(&pullsCalls); calls != 1 {
		t.Errorf("expected 1 ListPullRequests call with no rate budget wired, got %d", calls)
	}
	if snap := c.metrics.Snapshot(); snap.RateLimitFloorEngagements != 0 {
		t.Errorf("RateLimitFloorEngagements = %d, want 0", snap.RateLimitFloorEngagements)
	}
}

// TestController_ReconcileOrphanPRs_SkippedWhenBudgetFloorEngaged mirrors the
// merged-PR-scan test above for the orphan-PR sweep — the other
// PriorityBackground consumer named explicitly in the GH-4391 issue body.
func TestController_ReconcileOrphanPRs_SkippedWhenBudgetFloorEngaged(t *testing.T) {
	var pullsCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			atomic.AddInt32(&pullsCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]*github.PullRequest{})
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateBudget(engagedTracker()))

	c.reconcileOrphanPRs(context.Background())

	if calls := atomic.LoadInt32(&pullsCalls); calls != 0 {
		t.Errorf("expected 0 ListPullRequests calls while budget floor engaged, got %d", calls)
	}
	if snap := c.metrics.Snapshot(); snap.RateLimitFloorEngagements != 1 {
		t.Errorf("RateLimitFloorEngagements = %d, want 1", snap.RateLimitFloorEngagements)
	}
}

// TestController_BackgroundScanAllowed_WarnAndMetricDedup verifies the
// controller-level dedup latch: the metric increments exactly once per
// engagement episode (not once per call while the floor stays engaged), and
// a fresh episode after a recovery increments it again.
func TestController_BackgroundScanAllowed_WarnAndMetricDedup(t *testing.T) {
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	cfg := DefaultConfig()
	tracker := ghbudget.NewTracker(ghbudget.DefaultFloorPct, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateBudget(tracker))

	tracker.Observe(headerWithRateLimit(10, 5000)) // engage the floor
	if c.backgroundScanAllowed("test_scan") {
		t.Fatal("expected scan to be blocked while floor engaged")
	}
	if c.backgroundScanAllowed("test_scan") {
		t.Fatal("expected scan to still be blocked on a second call")
	}
	if snap := c.metrics.Snapshot(); snap.RateLimitFloorEngagements != 1 {
		t.Fatalf("expected exactly 1 metric increment across two blocked calls in the same episode, got %d", snap.RateLimitFloorEngagements)
	}

	tracker.Observe(headerWithRateLimit(4000, 5000)) // recover
	if !c.backgroundScanAllowed("test_scan") {
		t.Fatal("expected scan to be allowed after recovery")
	}

	tracker.Observe(headerWithRateLimit(5, 5000)) // re-engage: a fresh episode
	if c.backgroundScanAllowed("test_scan") {
		t.Fatal("expected scan to be blocked again after re-engagement")
	}
	if snap := c.metrics.Snapshot(); snap.RateLimitFloorEngagements != 2 {
		t.Fatalf("expected 2 metric increments after a recovery + re-engagement, got %d", snap.RateLimitFloorEngagements)
	}
}

// TestStaggerRepoScans_SpreadsCallsAcrossInterval verifies the GH-4391
// acceptance criterion "staggered startup with bounded API spend for 10+
// repos": scanFn is called once per repo, in deterministic sorted order, with
// roughly interval-per-repo spacing rather than all firing back-to-back.
func TestStaggerRepoScans_SpreadsCallsAcrossInterval(t *testing.T) {
	const n = 10
	const interval = 20 * time.Millisecond

	cfg := DefaultConfig()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	repos := make(map[string]*Controller, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		repos[name] = NewController(cfg, ghClient, nil, "owner", name)
	}

	var mu sync.Mutex
	var order []string
	var stamps []time.Time
	start := time.Now()

	StaggerRepoScans(context.Background(), repos, interval, func(_ context.Context, repoName string, _ *Controller) {
		mu.Lock()
		order = append(order, repoName)
		stamps = append(stamps, time.Now())
		mu.Unlock()
	})

	if len(order) != n {
		t.Fatalf("expected %d scans, got %d", n, len(order))
	}
	for i := 1; i < n; i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("expected deterministic sorted repo order, got %v", order)
		}
	}

	// Jitter is +/-25% of interval, so total elapsed across (n-1) gaps should
	// be at least ~0.7x and at most ~1.4x (n-1)*interval — loose bounds to
	// avoid flakiness while still proving the calls are actually spread out
	// rather than bursted.
	elapsed := stamps[n-1].Sub(start)
	minExpected := time.Duration(float64(n-1) * 0.7 * float64(interval))
	maxExpected := time.Duration(float64(n-1) * 1.6 * float64(interval))
	if elapsed < minExpected {
		t.Errorf("elapsed %v is shorter than expected minimum %v — scans were not staggered", elapsed, minExpected)
	}
	if elapsed > maxExpected {
		t.Errorf("elapsed %v exceeds expected maximum %v — stagger delay grew unexpectedly large", elapsed, maxExpected)
	}
}

// TestStaggerRepoScans_ContextCancelStopsEarly verifies that cancelling ctx
// while waiting between repos stops the remaining scans instead of running
// them anyway.
func TestStaggerRepoScans_ContextCancelStopsEarly(t *testing.T) {
	cfg := DefaultConfig()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	repos := make(map[string]*Controller, 5)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		repos[name] = NewController(cfg, ghClient, nil, "owner", name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	var calls int32
	StaggerRepoScans(ctx, repos, 50*time.Millisecond, func(_ context.Context, _ string, _ *Controller) {
		atomic.AddInt32(&calls, 1)
	})

	if got := atomic.LoadInt32(&calls); got >= 5 {
		t.Errorf("expected fewer than 5 scans after early context cancellation, got %d", got)
	}
}

// TestStaggerRepoScans_ZeroIntervalRunsImmediately verifies interval<=0
// (staggering disabled) runs every repo back-to-back, matching pre-GH-4391
// behavior for single-repo deployments and tests.
func TestStaggerRepoScans_ZeroIntervalRunsImmediately(t *testing.T) {
	cfg := DefaultConfig()
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	repos := make(map[string]*Controller, 3)
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("repo-%02d", i)
		repos[name] = NewController(cfg, ghClient, nil, "owner", name)
	}

	start := time.Now()
	var calls int32
	StaggerRepoScans(context.Background(), repos, 0, func(_ context.Context, _ string, _ *Controller) {
		atomic.AddInt32(&calls, 1)
	})
	elapsed := time.Since(start)

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 scans, got %d", got)
	}
	if elapsed > 20*time.Millisecond {
		t.Errorf("expected near-instant execution with interval=0, took %v", elapsed)
	}
}

// TestController_ScanRecentlyMergedPRsAtStartup_CursorShrinksEffectiveWindow
// verifies the GH-4391 restart-resume behavior: a persisted "last successful
// startup scan" cursor shrinks the effective window below configuredWindow,
// so merges that predate the cursor (minus a small buffer) are no longer
// swept even though they'd fall inside the wide configured window.
func TestController_ScanRecentlyMergedPRsAtStartup_CursorShrinksEffectiveWindow(t *testing.T) {
	now := time.Now()
	oldPR := &github.PullRequest{
		Number:         1,
		HTMLURL:        "https://github.com/owner/repo/pull/1",
		Head:           github.PRRef{Ref: "pilot/GH-1"},
		Base:           github.PRRef{Ref: "main"},
		Merged:         true,
		MergedAt:       now.Add(-3 * time.Hour).Format(time.RFC3339),
		MergeCommitSHA: "sha0000000000000000000000000000000000001",
		Title:          "old merge",
	}
	recentPR := &github.PullRequest{
		Number:         2,
		HTMLURL:        "https://github.com/owner/repo/pull/2",
		Head:           github.PRRef{Ref: "pilot/GH-2"},
		Base:           github.PRRef{Ref: "main"},
		Merged:         true,
		MergedAt:       now.Add(-1 * time.Hour).Format(time.RFC3339),
		MergeCommitSHA: "sha0000000000000000000000000000000000002",
		Title:          "recent merge",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{oldPR, recentPR})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	store := newTestSQLiteStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)

	// Seed a cursor from 2 hours ago. Effective window becomes
	// 2h + startupScanCursorBuffer (10m) = 2h10m: wide enough to include the
	// 1h-old merge, too narrow for the 3h-old one — even though
	// configuredWindow (30 days) covers both.
	cursorKey := "startup_merged_pr_scan_cursor:owner/repo"
	if err := store.SaveMetadata(cursorKey, now.Add(-2*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	if err := c.ScanRecentlyMergedPRsAtStartup(context.Background(), 30*24*time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c.mu.RLock()
	_, oldRecorded := c.recordedMerges[1]
	_, recentRecorded := c.recordedMerges[2]
	c.mu.RUnlock()

	if oldRecorded {
		t.Error("3h-old merge should have fallen outside the shrunk ~2h10m effective window")
	}
	if !recentRecorded {
		t.Error("1h-old merge should have fallen inside the shrunk ~2h10m effective window")
	}

	// The cursor should have advanced to (approximately) now, since the scan
	// actually ran.
	updated, err := store.GetMetadata(cursorKey)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	updatedAt, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		t.Fatalf("parse updated cursor: %v", err)
	}
	if time.Since(updatedAt) > time.Minute {
		t.Errorf("expected cursor to advance to ~now after a successful scan, got %v (age %v)", updated, time.Since(updatedAt))
	}
}

// TestController_ScanRecentlyMergedPRsAtStartup_CursorNotAdvancedWhenSkipped
// verifies that a scan skipped by the budget-floor gate does not advance the
// persisted cursor — advancing it would wrongly shrink the next attempt's
// window past merges that were never actually scanned.
func TestController_ScanRecentlyMergedPRsAtStartup_CursorNotAdvancedWhenSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]*github.PullRequest{})
	}))
	defer server.Close()

	store := newTestSQLiteStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateBudget(engagedTracker()))
	c.SetStateStore(store)

	cursorKey := "startup_merged_pr_scan_cursor:owner/repo"
	staleCursor := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if err := store.SaveMetadata(cursorKey, staleCursor); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	if err := c.ScanRecentlyMergedPRsAtStartup(context.Background(), 30*24*time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := store.GetMetadata(cursorKey)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if got != staleCursor {
		t.Errorf("cursor advanced despite the scan being skipped by the budget floor: got %q, want unchanged %q", got, staleCursor)
	}
}

// TestController_ScanRecentlyMergedPRsAtStartup_NoCursorUsesConfiguredWindow
// verifies a first-ever startup scan (no persisted cursor) uses the full
// configuredWindow rather than being wrongly shrunk to zero.
func TestController_ScanRecentlyMergedPRsAtStartup_NoCursorUsesConfiguredWindow(t *testing.T) {
	now := time.Now()
	pr := &github.PullRequest{
		Number:         1,
		HTMLURL:        "https://github.com/owner/repo/pull/1",
		Head:           github.PRRef{Ref: "pilot/GH-1"},
		Base:           github.PRRef{Ref: "main"},
		Merged:         true,
		MergedAt:       now.Add(-6 * time.Hour).Format(time.RFC3339),
		MergeCommitSHA: "sha0000000000000000000000000000000000003",
		Title:          "six hours old",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{pr})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	store := newTestSQLiteStateStore(t)
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)

	// No cursor seeded — configuredWindow (72h) must apply unshrunk, so the
	// 6h-old merge is well within range.
	if err := c.ScanRecentlyMergedPRsAtStartup(context.Background(), 72*time.Hour); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c.mu.RLock()
	_, recorded := c.recordedMerges[1]
	c.mu.RUnlock()
	if !recorded {
		t.Error("expected the 6h-old merge to be recorded under the full configured window with no cursor present")
	}
}
