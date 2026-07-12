package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestSweepOrphanedRunningExecutions_LiveMonitorEntryNotFlipped is the GH-4206
// regression gate: a status='running' row whose task_id is in the live
// Monitor's running/queued set must never be resolved by the orphan sweep,
// even if the row would otherwise look orphaned. TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_LiveMonitorEntryNotFlipped(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-live", TaskID: "GH-4206", ProjectPath: "/proj"},
		},
	}
	c.SetEvalStore(evalMock)
	monitor := newMockTaskMonitor()
	monitor.runningTaskIDs = []string{"GH-4206"}
	c.SetMonitor(monitor)

	c.sweepOrphanedRunningExecutions(nil)

	if len(evalMock.resolvedOrphans) != 0 {
		t.Fatalf("expected GH-4206 to never be resolved, got %d resolve calls: %+v", len(evalMock.resolvedOrphans), evalMock.resolvedOrphans)
	}
	if len(evalMock.lastExcludeTaskIDs) != 1 || evalMock.lastExcludeTaskIDs[0] != "GH-4206" {
		t.Errorf("expected FindOrphanedRunningExecutions to be called with the live Monitor set, got %v", evalMock.lastExcludeTaskIDs)
	}
}

// TestSweepOrphanedRunningExecutions_FreshHeartbeatNotFlipped verifies the
// second half of the GH-4206 regression gate: even when a row has no live
// Monitor entry (e.g. right after a restart), a recent execution_events
// heartbeat must still protect it from being flipped. TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_FreshHeartbeatNotFlipped(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-fresh", TaskID: "GH-500", ProjectPath: "/proj"},
		},
		executionEvents: map[string][]*memory.Event{
			"exec-fresh": {
				{ExecutionID: "exec-fresh", Stage: memory.StageRunning, OccurredAt: time.Now().Add(-2 * time.Minute)},
			},
		},
	}
	c.SetEvalStore(evalMock)
	// No monitor set — simulates a fresh daemon restart with an empty in-memory Monitor.

	c.sweepOrphanedRunningExecutions(nil)

	if len(evalMock.resolvedOrphans) != 0 {
		t.Fatalf("expected fresh-heartbeat row to survive the sweep, got resolve calls: %+v", evalMock.resolvedOrphans)
	}
}

// TestSweepOrphanedRunningExecutions_StaleNoEvidenceMarkedFailed verifies that
// a row with no live Monitor entry, no recent heartbeat, and no matching
// merged PR resolves to 'failed' (empty prURL). TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_StaleNoEvidenceMarkedFailed(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-stale", TaskID: "GH-900", ProjectPath: "/proj", TaskBranch: "pilot/GH-900"},
		},
		executionEvents: map[string][]*memory.Event{
			"exec-stale": {
				{ExecutionID: "exec-stale", Stage: memory.StageRunning, OccurredAt: time.Now().Add(-2 * time.Hour)},
			},
		},
	}
	c.SetEvalStore(evalMock)

	c.sweepOrphanedRunningExecutions(nil) // no merged PRs at all

	if len(evalMock.resolvedOrphans) != 1 {
		t.Fatalf("expected exactly 1 resolve call, got %d", len(evalMock.resolvedOrphans))
	}
	got := evalMock.resolvedOrphans[0]
	if got.ID != "exec-stale" || got.PRURL != "" {
		t.Errorf("expected exec-stale resolved with empty prURL (failed), got %+v", got)
	}
}

// TestSweepOrphanedRunningExecutions_MergedPRHealsToCompleted verifies that a
// stale orphan whose task branch matches a merged PR in the reused scan list
// resolves to 'completed' with that PR's URL. TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_MergedPRHealsToCompleted(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-shipped", TaskID: "GH-4189", ProjectPath: "/proj", TaskBranch: "pilot/GH-4189"},
		},
	}
	c.SetEvalStore(evalMock)

	mergedPRs := []*github.PullRequest{
		{Number: 42, Head: github.PRRef{Ref: "pilot/GH-4189"}, HTMLURL: "https://github.com/owner/repo/pull/42", Merged: true},
	}

	c.sweepOrphanedRunningExecutions(mergedPRs)

	if len(evalMock.resolvedOrphans) != 1 {
		t.Fatalf("expected exactly 1 resolve call, got %d", len(evalMock.resolvedOrphans))
	}
	got := evalMock.resolvedOrphans[0]
	if got.ID != "exec-shipped" || got.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("expected exec-shipped healed to the merged PR URL, got %+v", got)
	}
}

// TestSweepOrphanedRunningExecutions_MatchesByStoredPRURL verifies the
// pr_url-based match (as opposed to branch) — a row that already carries its
// own pr_url (stamped at PR-creation time) heals when that URL appears in the
// merged PR list. TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_MatchesByStoredPRURL(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-by-url", TaskID: "GH-4155", ProjectPath: "/proj", PRUrl: "https://github.com/owner/repo/pull/77"},
		},
	}
	c.SetEvalStore(evalMock)

	mergedPRs := []*github.PullRequest{
		// Deliberately non-matching branch — only pr_url should drive the match.
		{Number: 77, Head: github.PRRef{Ref: "fix/renamed-branch"}, HTMLURL: "https://github.com/owner/repo/pull/77", Merged: true},
	}

	c.sweepOrphanedRunningExecutions(mergedPRs)

	if len(evalMock.resolvedOrphans) != 1 || evalMock.resolvedOrphans[0].PRURL != "https://github.com/owner/repo/pull/77" {
		t.Fatalf("expected exec-by-url healed via its own stored pr_url, got %+v", evalMock.resolvedOrphans)
	}
}

// TestSweepOrphanedRunningExecutions_NoEvalStoreNoOp verifies the sweep is
// silent (no panic, no queries) when no eval store is wired. TASK-399/GH-4209.
func TestSweepOrphanedRunningExecutions_NoEvalStoreNoOp(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.sweepOrphanedRunningExecutions(nil) // must not panic
}

// TestScanRecentlyMergedPRsWithWindow_WideLookbackHealsOutsideDefaultWindow
// verifies TASK-399/GH-4209's Defect B widening: a shipped issue whose PR
// merged outside the default 30-min scanWindow still self-heals to
// 'completed' when the startup catch-up sweep runs with a wide lookback.
func TestScanRecentlyMergedPRsWithWindow_WideLookbackHealsOutsideDefaultWindow(t *testing.T) {
	oldMergedAt := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			prs := []*github.PullRequest{
				{
					Number:   42,
					Head:     github.PRRef{Ref: "pilot/GH-4189", SHA: "sha1"},
					Base:     github.PRRef{Ref: "main"},
					HTMLURL:  "https://github.com/owner/repo/pull/42",
					Merged:   true,
					MergedAt: oldMergedAt,
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MergedPRScanWindow = 30 * time.Minute
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	// The ordinary (config-window) scan must NOT heal this — it's outside 30m.
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs: %v", err)
	}
	if len(evalMock.selfHealed) != 0 {
		t.Fatalf("expected no self-heal within the default window, got %+v", evalMock.selfHealed)
	}

	// The wide-lookback startup sweep DOES heal it.
	if err := c.ScanRecentlyMergedPRsWithWindow(context.Background(), StartupMergedPRLookback); err != nil {
		t.Fatalf("ScanRecentlyMergedPRsWithWindow: %v", err)
	}
	if len(evalMock.selfHealed) != 1 || evalMock.selfHealed[0].TaskID != "GH-4189" {
		t.Fatalf("expected GH-4189 healed by the wide-lookback sweep, got %+v", evalMock.selfHealed)
	}
}

// TestScanRecentlyMergedPRsWithWindow_ClosesMarkerHealsNonPilotBranch verifies
// TASK-399/GH-4209's issueNum widening: a shipped PR merged on a non-standard
// branch (no "pilot/GH-N" prefix) still heals to 'completed' when its body
// carries the "Closes #N" auto-close marker.
func TestScanRecentlyMergedPRsWithWindow_ClosesMarkerHealsNonPilotBranch(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			prs := []*github.PullRequest{
				{
					Number:   99,
					Head:     github.PRRef{Ref: "pilot/fix-renamed-branch", SHA: "sha1"},
					Base:     github.PRRef{Ref: "main"},
					Body:     "This resolves the bug.\n\nCloses #4029",
					HTMLURL:  "https://github.com/owner/repo/pull/99",
					Merged:   true,
					MergedAt: recentMergedAt,
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MergedPRScanWindow = 30 * time.Minute
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)
	monitor := newMockTaskMonitor()
	c.SetMonitor(monitor)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs: %v", err)
	}

	if len(evalMock.selfHealed) != 1 || evalMock.selfHealed[0].TaskID != "GH-4029" {
		t.Fatalf("expected GH-4029 healed via the PR body's Closes marker, got %+v", evalMock.selfHealed)
	}

	// TASK-399/GH-4209 monitor-consistency rider: the external-merge branch
	// must also retire the QUEUE card, mirroring handleMerging (GH-1336).
	if prURL, ok := monitor.completedTasks["GH-4029"]; !ok || prURL != "https://github.com/owner/repo/pull/99" {
		t.Errorf("expected monitor.Complete(\"GH-4029\", ...) to be called, got %+v", monitor.completedTasks)
	}
}

// TestScanRecentlyMergedPRsWithWindow_Idempotent verifies re-running the scan
// against the same merged-PR/execution state does not re-trigger duplicate
// heals or resolves. TASK-399/GH-4209.
func TestScanRecentlyMergedPRsWithWindow_Idempotent(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			prs := []*github.PullRequest{
				{
					Number:   7,
					Head:     github.PRRef{Ref: "pilot/GH-7", SHA: "sha1"},
					Base:     github.PRRef{Ref: "main"},
					HTMLURL:  "https://github.com/owner/repo/pull/7",
					Merged:   true,
					MergedAt: recentMergedAt,
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MergedPRScanWindow = 30 * time.Minute
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	evalMock := &mockEvalStore{
		orphanedRunning: []*memory.Execution{
			{ID: "exec-7", TaskID: "GH-7", ProjectPath: "/proj", TaskBranch: "pilot/GH-7"},
		},
	}
	c.SetEvalStore(evalMock)

	for i := 0; i < 2; i++ {
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs call %d: %v", i, err)
		}
	}

	if len(evalMock.selfHealed) != 2 {
		t.Fatalf("expected selfHealForPR to run once per tick (idempotent per the store's own guards), got %d calls", len(evalMock.selfHealed))
	}
	// The orphan-running resolve fires every tick too (the mock doesn't mutate
	// exec.Status), mirroring the real store's `AND status = 'running'` guard
	// which makes the second call a no-op in production.
	if len(evalMock.resolvedOrphans) != 2 {
		t.Fatalf("expected the orphan sweep to run once per tick, got %d calls", len(evalMock.resolvedOrphans))
	}
	for _, call := range evalMock.resolvedOrphans {
		if call.PRURL != "https://github.com/owner/repo/pull/7" {
			t.Errorf("expected every resolve to converge on the same merged PR URL, got %+v", call)
		}
	}
}
