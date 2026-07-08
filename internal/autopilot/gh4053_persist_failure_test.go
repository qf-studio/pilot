package autopilot

import (
	"testing"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// corruptPRStateTableSingleColumnPK rebuilds autopilot_pr_state with the
// pre-GH-3903 single-column PRIMARY KEY (pr_number only, no repo), while
// leaving SavePRState's query text (state_store.go) untouched — it still
// issues `ON CONFLICT(repo, pr_number)`. This reproduces the GH-4053
// production symptom: a row whose live schema doesn't match the upsert's
// conflict target raises "ON CONFLICT clause does not match any PRIMARY KEY
// or UNIQUE constraint" on every SavePRState call for that installation.
func corruptPRStateTableSingleColumnPK(t *testing.T, store *StateStore) {
	t.Helper()
	if _, err := store.db.Exec(`DROP TABLE autopilot_pr_state`); err != nil {
		t.Fatalf("drop autopilot_pr_state: %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TABLE autopilot_pr_state (
			pr_number INTEGER PRIMARY KEY,
			repo TEXT NOT NULL DEFAULT '',
			pr_url TEXT NOT NULL DEFAULT '',
			issue_number INTEGER DEFAULT 0,
			branch_name TEXT NOT NULL DEFAULT '',
			head_sha TEXT DEFAULT '',
			stage TEXT NOT NULL DEFAULT '',
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
			rebase_attempts INTEGER NOT NULL DEFAULT 0,
			scope_key TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("recreate autopilot_pr_state with single-column PK: %v", err)
	}
}

// TestGH4053_PersistFailureAlertsOnceAndEvicts verifies that a PR whose
// SavePRState call fails repeatedly (e.g. a reconciler-adopted row hitting an
// ON CONFLICT/schema mismatch) fires exactly one pr_persist_failed alert and
// is evicted from tracking after persistFailureEvictThreshold consecutive
// failures, instead of retry-looping forever with only WARN logs (GH-4053).
func TestGH4053_PersistFailureAlertsOnceAndEvicts(t *testing.T) {
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	corruptPRStateTableSingleColumnPK(t, store)

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, nil, "qf-studio", "pilot")
	c.SetStateStore(store)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber: 4047,
		PRURL:    "https://github.com/qf-studio/pilot/pull/4047",
		Stage:    StageAwaitApproval,
	}
	c.mu.Lock()
	c.activePRs[4047] = prState
	c.mu.Unlock()

	for i := 0; i < persistFailureEvictThreshold; i++ {
		prState.mu.Lock()
		c.persistPRState(prState)
		prState.mu.Unlock()
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected exactly 1 pr_persist_failed alert (deduped across retries), got %d", len(sink.events))
	}
	if sink.events[0].Type != alerts.EventType("pr_persist_failed") {
		t.Errorf("alert type = %s, want pr_persist_failed", sink.events[0].Type)
	}

	if _, tracked := c.GetPRState(4047); tracked {
		t.Error("PR should have been evicted from activePRs after threshold consecutive persist failures")
	}
	if !c.recentlyEvictedForPersistFailure(4047) {
		t.Error("recentlyEvictedForPersistFailure should report true immediately after eviction")
	}
}

// TestGH4053_PersistSuccessResetsFailureCount verifies that a successful
// persist clears PersistFailureCount, so a transient failure (e.g. a busy
// DB) doesn't count toward eviction once the write starts succeeding again.
func TestGH4053_PersistSuccessResetsFailureCount(t *testing.T) {
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, nil, "qf-studio", "pilot")
	c.SetStateStore(store)

	prState := &PRState{
		PRNumber: 55,
		PRURL:    "https://github.com/qf-studio/pilot/pull/55",
		Stage:    StagePRCreated,
	}
	prState.mu.Lock()
	prState.PersistFailureCount = persistFailureEvictThreshold - 1
	c.persistPRState(prState)
	failures := prState.PersistFailureCount
	prState.mu.Unlock()

	if failures != 0 {
		t.Errorf("PersistFailureCount = %d after a successful persist, want 0", failures)
	}
}
