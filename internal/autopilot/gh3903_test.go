package autopilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-3903: autopilot_pr_state/autopilot_pr_failures used to key rows on
// pr_number alone. With one project-scoped Controller per `projects:` repo
// sharing a single SQLite DB, every controller restored and could act on
// every other controller's rows whenever PR numbers collided across repos —
// applying labels, closing issues, and deleting branches in the wrong repo.
// These tests pin the fix: repo joins the primary key, RestoreState/persist
// are scoped per-controller, and a 404-eviction guard bounds any residual
// foreign-row retry loop.

// TestController_CrossRepoPRNumberCollision_RestoreIsolation verifies that a
// PR persisted by one repo's controller is never restored by a different
// repo's controller, even when both use the identical PR number and share
// one SQLite-backed StateStore (the exact shape of the 2026-07-06 incident:
// studio-sdk PR #74 and pilot PR #74 sharing one daemon's DB).
func TestController_CrossRepoPRNumberCollision_RestoreIsolation(t *testing.T) {
	store := newTestStateStore(t)

	ctrlA := NewController(DefaultConfig(), nil, nil, "qf-studio", "studio-sdk")
	ctrlA.SetStateStore(store)
	ctrlA.OnPRCreated(74, "https://github.com/qf-studio/studio-sdk/pull/74", 500, "sha-a", "pilot/GH-500", "")

	ctrlB := NewController(DefaultConfig(), nil, nil, "qf-studio", "pilot")
	ctrlB.SetStateStore(store)
	ctrlB.OnPRCreated(74, "https://github.com/qf-studio/pilot/pull/74", 72, "sha-b", "pilot/GH-72", "")

	// Fresh controllers simulate a daemon restart: RestoreState must only
	// adopt each controller's own repo's row.
	freshA := NewController(DefaultConfig(), nil, nil, "qf-studio", "studio-sdk")
	freshA.SetStateStore(store)
	restoredA, err := freshA.RestoreState()
	if err != nil {
		t.Fatalf("RestoreState (studio-sdk): %v", err)
	}
	if restoredA != 1 {
		t.Fatalf("restored (studio-sdk) = %d, want 1 (only its own PR 74)", restoredA)
	}
	prA, ok := freshA.GetPRState(74)
	if !ok {
		t.Fatal("studio-sdk's own PR 74 should be restored")
	}
	if prA.IssueNumber != 500 {
		t.Errorf("studio-sdk PR 74 issue = %d, want 500 (its own issue, not pilot's)", prA.IssueNumber)
	}

	freshB := NewController(DefaultConfig(), nil, nil, "qf-studio", "pilot")
	freshB.SetStateStore(store)
	restoredB, err := freshB.RestoreState()
	if err != nil {
		t.Fatalf("RestoreState (pilot): %v", err)
	}
	if restoredB != 1 {
		t.Fatalf("restored (pilot) = %d, want 1 (only its own PR 74)", restoredB)
	}
	prB, ok := freshB.GetPRState(74)
	if !ok {
		t.Fatal("pilot's own PR 74 should be restored")
	}
	if prB.IssueNumber != 72 {
		t.Errorf("pilot PR 74 issue = %d, want 72 (its own issue, not studio-sdk's)", prB.IssueNumber)
	}
}

// TestController_CrossRepoPRNumberCollision_PersistIsolation verifies that
// removing a PR in one repo's controller does not delete or otherwise affect
// the colliding-numbered row belonging to a different repo.
func TestController_CrossRepoPRNumberCollision_PersistIsolation(t *testing.T) {
	store := newTestStateStore(t)

	ctrlA := NewController(DefaultConfig(), nil, nil, "qf-studio", "studio-sdk")
	ctrlA.SetStateStore(store)
	ctrlA.OnPRCreated(74, "https://github.com/qf-studio/studio-sdk/pull/74", 500, "sha-a", "pilot/GH-500", "")

	ctrlB := NewController(DefaultConfig(), nil, nil, "qf-studio", "pilot")
	ctrlB.SetStateStore(store)
	ctrlB.OnPRCreated(74, "https://github.com/qf-studio/pilot/pull/74", 72, "sha-b", "pilot/GH-72", "")

	// Repo A resolves (e.g. merged externally) and removes its own PR 74.
	ctrlA.removePR(74)

	removedA, err := store.GetPRState("qf-studio/studio-sdk", 74)
	if err != nil {
		t.Fatalf("GetPRState(studio-sdk): %v", err)
	}
	if removedA != nil {
		t.Error("studio-sdk's PR 74 should be removed from the store")
	}

	stillThereB, err := store.GetPRState("qf-studio/pilot", 74)
	if err != nil {
		t.Fatalf("GetPRState(pilot): %v", err)
	}
	if stillThereB == nil {
		t.Fatal("pilot's PR 74 must survive studio-sdk removing its own colliding PR 74")
	}
	if stillThereB.IssueNumber != 72 {
		t.Errorf("surviving pilot PR 74 issue = %d, want 72", stillThereB.IssueNumber)
	}
}

// TestStateStore_PurgeTerminalPRStates_RepoIsolation verifies that a
// housekeeping purge scoped to one repo never deletes a colliding-numbered
// terminal row belonging to a different repo.
func TestStateStore_PurgeTerminalPRStates_RepoIsolation(t *testing.T) {
	store := newTestStateStore(t)

	failedA := &PRState{PRNumber: 1, BranchName: "pilot/GH-1", Stage: StageFailed, CreatedAt: time.Now()}
	failedB := &PRState{PRNumber: 1, BranchName: "pilot/GH-1", Stage: StageFailed, CreatedAt: time.Now()}
	if err := store.SavePRState("qf-studio/studio-sdk", failedA); err != nil {
		t.Fatalf("SavePRState(studio-sdk): %v", err)
	}
	if err := store.SavePRState("qf-studio/pilot", failedB); err != nil {
		t.Fatalf("SavePRState(pilot): %v", err)
	}

	purged, err := store.PurgeTerminalPRStates("qf-studio/studio-sdk", 0)
	if err != nil {
		t.Fatalf("PurgeTerminalPRStates: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged = %d, want 1 (only studio-sdk's own row)", purged)
	}

	remaining, err := store.GetPRState("qf-studio/pilot", 1)
	if err != nil {
		t.Fatalf("GetPRState(pilot): %v", err)
	}
	if remaining == nil {
		t.Fatal("pilot's colliding-numbered failed row must survive a studio-sdk-scoped purge")
	}
}

// TestStateStore_MigratePRStateRepoScoping verifies that a DB seeded with the
// pre-GH-3903 schema (pr_number alone as PRIMARY KEY, no repo column) is
// rebuilt so repo joins the primary key, with repo backfilled from pr_url for
// autopilot_pr_state and resolved via pr_number for autopilot_pr_failures.
// Failure rows with no resolvable repo are dropped (ephemeral counters).
func TestStateStore_MigratePRStateRepoScoping(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Seed the pre-fix schema directly, bypassing NewStateStore's migrate().
	if _, err := db.Exec(`
		CREATE TABLE autopilot_pr_state (
			pr_number INTEGER PRIMARY KEY,
			pr_url TEXT NOT NULL,
			issue_number INTEGER DEFAULT 0,
			branch_name TEXT NOT NULL DEFAULT '',
			head_sha TEXT DEFAULT '',
			stage TEXT NOT NULL,
			ci_status TEXT NOT NULL DEFAULT 'pending',
			last_checked DATETIME,
			ci_wait_started_at DATETIME,
			merge_attempts INTEGER DEFAULT 0,
			error TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			release_version TEXT DEFAULT '',
			release_bump_type TEXT DEFAULT '',
			merge_notification_posted INTEGER NOT NULL DEFAULT 0,
			approval_request_id TEXT NOT NULL DEFAULT '',
			approval_decision TEXT NOT NULL DEFAULT '',
			approval_requested_at DATETIME,
			post_merge_sha TEXT NOT NULL DEFAULT '',
			post_merge_ci_started_at DATETIME,
			rebase_attempts INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		t.Fatalf("seed legacy autopilot_pr_state: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE autopilot_pr_failures (
			pr_number INTEGER PRIMARY KEY,
			failure_count INTEGER NOT NULL DEFAULT 0,
			last_failure_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("seed legacy autopilot_pr_failures: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO autopilot_pr_state (pr_number, pr_url, stage, ci_status, created_at)
		VALUES (74, 'https://github.com/qf-studio/studio-sdk/pull/74', 'merging', 'success', CURRENT_TIMESTAMP)
	`); err != nil {
		t.Fatalf("seed legacy pr_state row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO autopilot_pr_failures (pr_number, failure_count) VALUES (74, 2)`); err != nil {
		t.Fatalf("seed legacy pr_failures row: %v", err)
	}
	// A failure row with no matching pr_state row (unresolvable repo) — must be dropped.
	if _, err := db.Exec(`INSERT INTO autopilot_pr_failures (pr_number, failure_count) VALUES (999, 1)`); err != nil {
		t.Fatalf("seed orphan pr_failures row: %v", err)
	}

	store, err := NewStateStore(db)
	if err != nil {
		t.Fatalf("NewStateStore (running migrations) failed: %v", err)
	}

	// Pre-existing row survives with repo backfilled from pr_url.
	state, err := store.GetPRState("qf-studio/studio-sdk", 74)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if state == nil {
		t.Fatal("pre-existing PR 74 row should survive the repo-scoping migration with backfilled repo")
	}
	if state.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s", state.Stage, StageMerging)
	}

	// A different repo with the colliding PR number must not see the backfilled row.
	collision, err := store.GetPRState("qf-studio/pilot", 74)
	if err != nil {
		t.Fatalf("GetPRState(qf-studio/pilot, 74): %v", err)
	}
	if collision != nil {
		t.Error("a different repo must not see another repo's backfilled PR 74 row")
	}

	// The failure row matched to a resolved repo survives, scoped to that repo.
	failures, err := store.LoadAllPRFailures("qf-studio/studio-sdk")
	if err != nil {
		t.Fatalf("LoadAllPRFailures: %v", err)
	}
	if failures[74] == nil || failures[74].FailureCount != 2 {
		t.Errorf("failure state for PR 74 not preserved: %+v", failures)
	}

	// The unresolvable orphan failure row (pr_number 999) must be dropped entirely.
	var orphanCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM autopilot_pr_failures WHERE pr_number = 999`).Scan(&orphanCount); err != nil {
		t.Fatalf("count orphan failures: %v", err)
	}
	if orphanCount != 0 {
		t.Errorf("orphan failure row with no resolvable repo should be dropped, found %d", orphanCount)
	}

	// Re-running migrate() must be a no-op (idempotent).
	if err := store.migrate(); err != nil {
		t.Fatalf("second migrate() failed: %v", err)
	}
	state, err = store.GetPRState("qf-studio/studio-sdk", 74)
	if err != nil {
		t.Fatalf("GetPRState after second migrate: %v", err)
	}
	if state == nil {
		t.Fatal("PR 74 row should still exist after a second, idempotent migrate() run")
	}
}

// TestController_ProcessAllPRs_EvictsAfterRepeatedNotFound verifies the
// 404-eviction guard: a PR that 404s on every fetch (e.g. a stale or foreign
// state-store row) is dropped from tracking and the persisted store after
// notFoundEvictionThreshold consecutive failures, WITHOUT ever attempting to
// delete a remote branch — a 404 means this controller has no verified
// relationship to the PR, so touching its repo's branches based on nothing
// but a matching number would repeat the wrong-repo mutation this guard
// exists to prevent.
func TestController_ProcessAllPRs_EvictsAfterRepeatedNotFound(t *testing.T) {
	branchDeleteCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/74" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodDelete:
			branchDeleteCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	store := newTestStateStore(t)

	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	c.SetStateStore(store)
	c.OnPRCreated(74, "https://github.com/owner/repo/pull/74", 500, "sha74", "pilot/GH-500", "")

	for i := 0; i < notFoundEvictionThreshold; i++ {
		c.processAllPRs(context.Background())
	}

	if _, ok := c.GetPRState(74); ok {
		t.Error("PR should be evicted from in-memory tracking after repeated 404s")
	}
	persisted, err := store.GetPRState("owner/repo", 74)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if persisted != nil {
		t.Error("persisted row should be removed after eviction")
	}
	if branchDeleteCalled {
		t.Error("eviction must not attempt remote branch cleanup for an unverified PR")
	}
}

// TestController_ProcessAllPRs_NotFoundCountResetsOnSuccess verifies that a
// PR is NOT evicted while its consecutive-404 count is under threshold, and
// that a subsequent successful fetch resets the counter to 0.
func TestController_ProcessAllPRs_NotFoundCountResetsOnSuccess(t *testing.T) {
	fetchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/74" && r.Method == http.MethodGet:
			fetchCount++
			if fetchCount <= 2 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			resp := github.PullRequest{
				Number: 74,
				State:  "open",
				Head:   github.PRRef{SHA: "sha74", Ref: "pilot/GH-500"},
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(74, "https://github.com/owner/repo/pull/74", 500, "sha74", "pilot/GH-500", "")

	c.processAllPRs(context.Background())
	c.processAllPRs(context.Background())

	pr, ok := c.GetPRState(74)
	if !ok {
		t.Fatal("PR should still be tracked after 2 consecutive 404s (below threshold)")
	}
	if pr.NotFoundCount != 2 {
		t.Errorf("NotFoundCount = %d, want 2", pr.NotFoundCount)
	}

	// Third tick succeeds — counter must reset.
	c.processAllPRs(context.Background())

	pr, ok = c.GetPRState(74)
	if !ok {
		t.Fatal("PR should still be tracked after recovering")
	}
	if pr.NotFoundCount != 0 {
		t.Errorf("NotFoundCount after a successful fetch = %d, want 0", pr.NotFoundCount)
	}
}
