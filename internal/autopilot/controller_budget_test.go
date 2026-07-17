package autopilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_BackgroundScanAllowed_RateLimitFloor is table-driven over
// the GitHub rate-limit budget (GH-4391 acceptance): a fake GitHub client
// returning rate-limit headers must gate backgroundScanAllowed once
// remaining budget drops below the configured floor.
func TestController_BackgroundScanAllowed_RateLimitFloor(t *testing.T) {
	tests := []struct {
		name        string
		remaining   int
		limit       int
		floorPct    int
		wantAllowed bool
	}{
		{"plenty of budget, default floor", 4500, 5000, 0, true},
		{"below default floor", 100, 5000, 0, false},
		{"exactly at a custom floor", 500, 5000, 10, true}, // 10.0% not < 10%
		{"below a custom floor", 400, 5000, 10, false},     // 8% < 10%
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budgetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"resources":{"core":{"limit":%d,"remaining":%d,"reset":0}}}`, tt.limit, tt.remaining)
			}))
			defer budgetServer.Close()

			ghClient := github.NewClient(testutil.FakeGitHubToken) // never called by backgroundScanAllowed
			cfg := DefaultConfig()
			cfg.RateLimitFloorPercent = tt.floorPct
			bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, budgetServer.URL)
			c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateLimitBudget(bc))

			if got := c.backgroundScanAllowed(context.Background()); got != tt.wantAllowed {
				t.Errorf("backgroundScanAllowed() = %v, want %v (remaining=%d limit=%d floor=%d)",
					got, tt.wantAllowed, tt.remaining, tt.limit, tt.floorPct)
			}
		})
	}
}

// TestController_NoBudgetClient_BackgroundScansAlwaysAllowed verifies
// WithRateLimitBudget is opt-in: a controller with no budget client wired
// behaves exactly as before GH-4391.
func TestController_NoBudgetClient_BackgroundScansAlwaysAllowed(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	if !c.backgroundScanAllowed(context.Background()) {
		t.Fatal("expected backgroundScanAllowed=true when no budget client is wired")
	}
}

// TestController_BudgetFloor_PausesBackgroundScans_PollerStillRuns is the
// end-to-end acceptance case for GH-4391: when the shared rate-limit budget
// is below floor, reconcileOrphanPRs and ScanRecentlyMergedPRsWithWindow
// (background scans) make zero GitHub calls, while processAllPRs — standing
// in for the poller/active-PR-CI-watch priority tier, which always gets the
// reserve — still executes its GitHub call. A single WARN (not per-call
// spam) is logged and the RateLimitFloorHits metric increments.
func TestController_BudgetFloor_PausesBackgroundScans_PollerStillRuns(t *testing.T) {
	var listCalls, pullCalls int32

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			atomic.AddInt32(&listCalls, 1)
		case "/repos/owner/repo/pulls/42":
			atomic.AddInt32(&pullCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ghServer.Close()

	budgetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 2% remaining: below the default 15% floor.
		fmt.Fprint(w, `{"resources":{"core":{"limit":5000,"remaining":100,"reset":0}}}`)
	}))
	defer budgetServer.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ghServer.URL)
	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, budgetServer.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateLimitBudget(bc))

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		IssueNumber: 100,
		BranchName:  "pilot/GH-100",
		HeadSHA:     "abc1234",
		Stage:       StageWaitingCI,
	}
	c.mu.Unlock()

	c.reconcileOrphanPRs(context.Background())
	if got := atomic.LoadInt32(&listCalls); got != 0 {
		t.Fatalf("reconcileOrphanPRs: expected 0 GitHub calls under budget floor, got %d", got)
	}

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("ScanRecentlyMergedPRsWithWindow: unexpected error %v", err)
	}
	if got := atomic.LoadInt32(&listCalls); got != 0 {
		t.Fatalf("ScanRecentlyMergedPRsWithWindow: expected 0 GitHub calls under budget floor, got %d", got)
	}

	c.processAllPRs(context.Background())
	if got := atomic.LoadInt32(&pullCalls); got != 1 {
		t.Fatalf("processAllPRs: expected 1 GitHub call even under budget floor, got %d", got)
	}

	if got := c.metrics.Snapshot().RateLimitFloorHits; got == 0 {
		t.Fatal("expected RateLimitFloorHits metric to be incremented by the two skipped background scans")
	}
}

// TestController_ScanRecentlyMergedPRsWithWindow_ConditionalProbeSkipsFullScan
// verifies the 304 conditional-request path (GH-4391 acceptance): a second
// scan of an unchanged repo costs only the cheap probe request, not the full
// paginated closed-PR list.
func TestController_ScanRecentlyMergedPRsWithWindow_ConditionalProbeSkipsFullScan(t *testing.T) {
	var listCalls, probeCalls int32
	const etag = `"deadbeef"`

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			atomic.AddInt32(&listCalls, 1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[]`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ghServer.Close()

	// The conditional probe hits a separate server since GitHubBudgetClient
	// makes its own raw HTTP calls (studio-sdk's Client has no ETag support
	// to instrument) — a real deployment points both at api.github.com.
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rate_limit" {
			// backgroundScanAllowed's floor check shares this server/baseURL;
			// answer with plenty of budget so it never gates this test.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"resources":{"core":{"limit":5000,"remaining":4500,"reset":0}}}`)
			return
		}
		atomic.AddInt32(&probeCalls, 1)
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[]`)
	}))
	defer probeServer.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, ghServer.URL)
	bc := NewGitHubBudgetClientWithBaseURL(testutil.FakeGitHubToken, probeServer.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithRateLimitBudget(bc))

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("first scan: unexpected error %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 1 {
		t.Fatalf("expected 1 probe call after first scan, got %d", got)
	}
	if got := atomic.LoadInt32(&listCalls); got != 1 {
		t.Fatalf("expected 1 full-list call after first scan (no prior ETag), got %d", got)
	}

	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), time.Hour); err != nil {
		t.Fatalf("second scan: unexpected error %v", err)
	}
	if got := atomic.LoadInt32(&probeCalls); got != 2 {
		t.Fatalf("expected 2 total probe calls after second scan, got %d", got)
	}
	if got := atomic.LoadInt32(&listCalls); got != 1 {
		t.Fatalf("expected still only 1 full-list call after an unchanged (304) second scan, got %d", got)
	}
}
