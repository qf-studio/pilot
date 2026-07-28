package autopilot

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	_ "modernc.org/sqlite"
)

func newTestStateStore(t *testing.T) *StateStore {
	t.Helper()
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("failed to create test state store: %v", err)
	}
	return store
}

func TestStateStore_SaveAndLoadPRState(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:        42,
		PRURL:           "https://github.com/owner/repo/pull/42",
		IssueNumber:     10,
		BranchName:      "pilot/GH-10",
		HeadSHA:         "abc123def456",
		PRTitle:         "feat(gateway): add webhook handler",
		Stage:           StageWaitingCI,
		CIStatus:        CIRunning,
		LastChecked:     time.Now().Truncate(time.Second),
		CIWaitStartedAt: time.Now().Add(-5 * time.Minute).Truncate(time.Second),
		MergeAttempts:   1,
		Error:           "",
		CreatedAt:       time.Now().Add(-10 * time.Minute).Truncate(time.Second),
		ReleaseVersion:  "",
		ReleaseBumpType: BumpNone,
	}

	// Save
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	// Load single
	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}

	if loaded.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", loaded.PRNumber)
	}
	if loaded.PRURL != pr.PRURL {
		t.Errorf("PRURL = %s, want %s", loaded.PRURL, pr.PRURL)
	}
	if loaded.IssueNumber != 10 {
		t.Errorf("IssueNumber = %d, want 10", loaded.IssueNumber)
	}
	if loaded.BranchName != "pilot/GH-10" {
		t.Errorf("BranchName = %s, want pilot/GH-10", loaded.BranchName)
	}
	if loaded.HeadSHA != "abc123def456" {
		t.Errorf("HeadSHA = %s, want abc123def456", loaded.HeadSHA)
	}
	// TASK-390: pr_title survives the restart round-trip — post-merge stages
	// never re-fetch the PR, so a lost title stays lost on the dashboard.
	if loaded.PRTitle != pr.PRTitle {
		t.Errorf("PRTitle = %q, want %q", loaded.PRTitle, pr.PRTitle)
	}
	if loaded.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", loaded.Stage, StageWaitingCI)
	}
	if loaded.CIStatus != CIRunning {
		t.Errorf("CIStatus = %s, want %s", loaded.CIStatus, CIRunning)
	}
	if loaded.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1", loaded.MergeAttempts)
	}
}

// TestStateStore_InfraRerunFields_SurvivesRestart verifies that
// InfraRerunCount/InfraRerunSHA are persisted to SQLite and reloaded
// correctly via both GetPRState and LoadAllPRStates (GH-4533) — the path a
// daemon restart actually uses to restore activePRs. Without this, a
// restart between an infra-outage rerun and the next CI verdict would
// silently grant a fresh 2-retry budget on the same SHA.
func TestStateStore_InfraRerunFields_SurvivesRestart(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:        56,
		PRURL:           "https://github.com/owner/repo/pull/56",
		IssueNumber:     21,
		BranchName:      "pilot/GH-21",
		HeadSHA:         "sha56",
		Stage:           StageWaitingCI,
		InfraRerunCount: 1,
		InfraRerunSHA:   "sha56",
		CreatedAt:       time.Now().Truncate(time.Second),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 56)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}
	if loaded.InfraRerunCount != 1 {
		t.Errorf("GetPRState: InfraRerunCount = %d, want 1", loaded.InfraRerunCount)
	}
	if loaded.InfraRerunSHA != "sha56" {
		t.Errorf("GetPRState: InfraRerunSHA = %q, want %q", loaded.InfraRerunSHA, "sha56")
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAllPRStates returned %d states, want 1", len(all))
	}
	if all[0].InfraRerunCount != 1 {
		t.Errorf("LoadAllPRStates: InfraRerunCount = %d, want 1", all[0].InfraRerunCount)
	}
	if all[0].InfraRerunSHA != "sha56" {
		t.Errorf("LoadAllPRStates: InfraRerunSHA = %q, want %q", all[0].InfraRerunSHA, "sha56")
	}
}

// TestStateStore_ParkedAndEscalationReason_SurviveRestart is the GH-4598
// regression test: before this, Parked and EscalationReason were in-memory
// only (GH-4596/#4602), so a daemon restart reloaded an already-parked PR
// with Parked=false and EscalationReason="" — the very next tick then
// treated the still-true misconfig as brand new, re-logging the WARN and
// re-invoking postMisconfigComment instead of staying quiet. Both fields
// must now round-trip through SavePRState via both read paths a restart
// actually uses: GetPRState (single-PR lookups) and LoadAllPRStates (the
// path RestoreState calls to rehydrate activePRs).
func TestStateStore_ParkedAndEscalationReason_SurviveRestart(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:         57,
		PRURL:            "https://github.com/owner/repo/pull/57",
		IssueNumber:      22,
		BranchName:       "pilot/GH-22",
		HeadSHA:          "sha57",
		Stage:            StageAwaitApproval,
		Parked:           true,
		EscalationReason: "PR adds 656 net lines (> 500 threshold)",
		CreatedAt:        time.Now().Truncate(time.Second),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 57)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}
	if !loaded.Parked {
		t.Error("GetPRState: Parked = false, want true")
	}
	if loaded.EscalationReason != pr.EscalationReason {
		t.Errorf("GetPRState: EscalationReason = %q, want %q", loaded.EscalationReason, pr.EscalationReason)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAllPRStates returned %d states, want 1", len(all))
	}
	if !all[0].Parked {
		t.Error("LoadAllPRStates: Parked = false, want true")
	}
	if all[0].EscalationReason != pr.EscalationReason {
		t.Errorf("LoadAllPRStates: EscalationReason = %q, want %q", all[0].EscalationReason, pr.EscalationReason)
	}
}

func TestStateStore_LoadAllPRStates(t *testing.T) {
	store := newTestStateStore(t)

	// Save multiple PRs
	for _, num := range []int{1, 2, 3} {
		pr := &PRState{
			PRNumber:   num,
			PRURL:      "https://github.com/owner/repo/pull/1",
			BranchName: "pilot/GH-1",
			Stage:      StagePRCreated,
			CIStatus:   CIPending,
			CreatedAt:  time.Now(),
		}
		if err := store.SavePRState("owner/repo", pr); err != nil {
			t.Fatalf("SavePRState(%d) failed: %v", num, err)
		}
	}

	states, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(states) != 3 {
		t.Errorf("got %d states, want 3", len(states))
	}
}

func TestStateStore_UpdatePRState(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/GH-10",
		Stage:      StagePRCreated,
		CIStatus:   CIPending,
		CreatedAt:  time.Now(),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("initial SavePRState failed: %v", err)
	}

	// Update stage
	pr.Stage = StageWaitingCI
	pr.CIStatus = CIRunning
	pr.CIWaitStartedAt = time.Now()
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("update SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", loaded.Stage, StageWaitingCI)
	}
	if loaded.CIStatus != CIRunning {
		t.Errorf("CIStatus = %s, want %s", loaded.CIStatus, CIRunning)
	}
}

func TestStateStore_RemovePRState(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/GH-10",
		Stage:      StagePRCreated,
		CIStatus:   CIPending,
		CreatedAt:  time.Now(),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	if err := store.RemovePRState("owner/repo", 42); err != nil {
		t.Fatalf("RemovePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil after removal, got non-nil")
	}
}

func TestStateStore_ProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsProcessed("github", "owner/repo", "100")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark processed
	if err := store.Mark("github", "owner/repo", "100"); err != nil {
		t.Fatalf("Mark failed: %v", err)
	}

	processed, err = store.IsProcessed("github", "owner/repo", "100")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.Load("github", "owner/repo")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if _, ok := all["100"]; !ok {
		t.Error("issue 100 should be in processed map")
	}

	// Idempotent mark
	if err := store.Mark("github", "owner/repo", "100"); err != nil {
		t.Fatalf("idempotent Mark failed: %v", err)
	}
	all, _ = store.Load("github", "owner/repo")
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.Unmark("github", "owner/repo", "100"); err != nil {
		t.Fatalf("Unmark failed: %v", err)
	}
	processed, err = store.IsProcessed("github", "owner/repo", "100")
	if err != nil {
		t.Fatalf("IsProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.Unmark("github", "owner/repo", "999"); err != nil {
		t.Fatalf("Unmark for non-existent issue failed: %v", err)
	}
}

func TestStateStore_Metadata(t *testing.T) {
	store := newTestStateStore(t)

	// Get non-existent key
	val, err := store.GetMetadata("missing")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for missing key, got %q", val)
	}

	// Set and get
	if err := store.SaveMetadata("consecutive_failures", "5"); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	val, err = store.GetMetadata("consecutive_failures")
	if err != nil {
		t.Fatalf("GetMetadata failed: %v", err)
	}
	if val != "5" {
		t.Errorf("got %q, want %q", val, "5")
	}

	// Update
	if err := store.SaveMetadata("consecutive_failures", "0"); err != nil {
		t.Fatalf("SaveMetadata update failed: %v", err)
	}
	val, _ = store.GetMetadata("consecutive_failures")
	if val != "0" {
		t.Errorf("got %q after update, want %q", val, "0")
	}
}

func TestStateStore_Purge(t *testing.T) {
	store := newTestStateStore(t)

	for i := 1; i <= 5; i++ {
		if err := store.Mark("github", "owner/repo", fmt.Sprintf("%d", i)); err != nil {
			t.Fatalf("Mark(%d) failed: %v", i, err)
		}
	}

	purged, err := store.Purge("github", 0)
	if err != nil {
		t.Fatalf("Purge failed: %v", err)
	}
	if purged != 5 {
		t.Errorf("purged = %d, want 5", purged)
	}

	all, _ := store.Load("github", "owner/repo")
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

func TestStateStore_PurgeTerminalPRStates(t *testing.T) {
	store := newTestStateStore(t)

	// Save a failed PR and an active PR
	failedPR := &PRState{
		PRNumber:   1,
		PRURL:      "https://github.com/owner/repo/pull/1",
		BranchName: "pilot/GH-1",
		Stage:      StageFailed,
		CIStatus:   CIFailure,
		CreatedAt:  time.Now(),
	}
	activePR := &PRState{
		PRNumber:   2,
		PRURL:      "https://github.com/owner/repo/pull/2",
		BranchName: "pilot/GH-2",
		Stage:      StageWaitingCI,
		CIStatus:   CIRunning,
		CreatedAt:  time.Now(),
	}

	if err := store.SavePRState("owner/repo", failedPR); err != nil {
		t.Fatalf("SavePRState(failed) failed: %v", err)
	}
	if err := store.SavePRState("owner/repo", activePR); err != nil {
		t.Fatalf("SavePRState(active) failed: %v", err)
	}

	// Purge terminal states older than 0 (immediate)
	purged, err := store.PurgeTerminalPRStates("owner/repo", 0)
	if err != nil {
		t.Fatalf("PurgeTerminalPRStates failed: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want 1 (only failed)", purged)
	}

	// Active PR should still exist
	states, _ := store.LoadAllPRStates("owner/repo")
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	if states[0].PRNumber != 2 {
		t.Errorf("remaining PR = %d, want 2", states[0].PRNumber)
	}
}

// B4 (TASK-309): PurgeTerminalPRStates also reaps 'releasing' rows stuck past
// releasingStaleThreshold, while leaving fresh releases-in-flight alone.
func TestStateStore_PurgeTerminalPRStates_Releasing(t *testing.T) {
	store := newTestStateStore(t)

	freshReleasing := &PRState{
		PRNumber:   1,
		BranchName: "pilot/GH-1",
		Stage:      StageReleasing,
		CreatedAt:  time.Now(),
	}
	staleReleasing := &PRState{
		PRNumber:   2,
		BranchName: "pilot/GH-2",
		Stage:      StageReleasing,
		CreatedAt:  time.Now(),
	}
	for _, pr := range []*PRState{freshReleasing, staleReleasing} {
		if err := store.SavePRState("owner/repo", pr); err != nil {
			t.Fatalf("SavePRState(%d): %v", pr.PRNumber, err)
		}
	}
	// Backdate PR 2 well past the 30-min staleness threshold.
	if _, err := store.db.Exec(
		`UPDATE autopilot_pr_state SET updated_at = datetime('now', '-2 hours') WHERE pr_number = ?`, 2,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// A large olderThan keeps any 'failed' row out of scope, so the only eligible
	// row is the stale 'releasing' one (reaped on its own 30-min threshold).
	purged, err := store.PurgeTerminalPRStates("owner/repo", 24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeTerminalPRStates: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want 1 (only the stale releasing row)", purged)
	}

	states, _ := store.LoadAllPRStates("owner/repo")
	if len(states) != 1 || states[0].PRNumber != 1 {
		t.Errorf("remaining states = %+v, want only the fresh releasing PR 1", states)
	}
}

// B3 (TASK-309): PersistedReleasingAge reports whether a PR already has a release
// in flight in the state store, and how stale that row is.
func TestStateStore_PersistedReleasingAge(t *testing.T) {
	store := newTestStateStore(t)

	// (a) no row → not found.
	if _, found, err := store.PersistedReleasingAge("owner/repo", 99); err != nil || found {
		t.Errorf("missing row: got found=%v err=%v, want found=false err=nil", found, err)
	}

	// (b) non-releasing stage → not found.
	if err := store.SavePRState("owner/repo", &PRState{PRNumber: 10, BranchName: "pilot/GH-10", Stage: StageWaitingCI, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("SavePRState(waiting): %v", err)
	}
	if _, found, err := store.PersistedReleasingAge("owner/repo", 10); err != nil || found {
		t.Errorf("non-releasing row: got found=%v err=%v, want found=false", found, err)
	}

	// (c) fresh releasing row → found, age below threshold.
	if err := store.SavePRState("owner/repo", &PRState{PRNumber: 20, BranchName: "pilot/GH-20", Stage: StageReleasing, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("SavePRState(releasing): %v", err)
	}
	age, found, err := store.PersistedReleasingAge("owner/repo", 20)
	if err != nil || !found {
		t.Fatalf("fresh releasing: got found=%v err=%v, want found=true", found, err)
	}
	if age >= releasingStaleThreshold {
		t.Errorf("fresh releasing age = %v, want < %v", age, releasingStaleThreshold)
	}

	// (d) stale releasing row → found, age above threshold.
	if _, err := store.db.Exec(
		`UPDATE autopilot_pr_state SET updated_at = datetime('now', '-2 hours') WHERE pr_number = ?`, 20,
	); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	age, found, err = store.PersistedReleasingAge("owner/repo", 20)
	if err != nil || !found {
		t.Fatalf("stale releasing: got found=%v err=%v, want found=true", found, err)
	}
	if age < releasingStaleThreshold {
		t.Errorf("stale releasing age = %v, want >= %v", age, releasingStaleThreshold)
	}
}

func TestController_RestoreState(t *testing.T) {
	store := newTestStateStore(t)

	// Pre-populate store with PR states
	pr1 := &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		IssueNumber: 10,
		BranchName:  "pilot/GH-10",
		HeadSHA:     "abc123",
		Stage:       StageWaitingCI,
		CIStatus:    CIRunning,
		CreatedAt:   time.Now(),
	}
	pr2 := &PRState{
		PRNumber:    43,
		PRURL:       "https://github.com/owner/repo/pull/43",
		IssueNumber: 11,
		BranchName:  "pilot/GH-11",
		HeadSHA:     "def456",
		Stage:       StageCIPassed,
		CIStatus:    CISuccess,
		CreatedAt:   time.Now(),
	}
	// Failed PR should NOT be restored as active
	pr3 := &PRState{
		PRNumber:    44,
		PRURL:       "https://github.com/owner/repo/pull/44",
		IssueNumber: 12,
		BranchName:  "pilot/GH-12",
		Stage:       StageFailed,
		CIStatus:    CIFailure,
		CreatedAt:   time.Now(),
	}

	for _, pr := range []*PRState{pr1, pr2, pr3} {
		if err := store.SavePRState("owner/repo", pr); err != nil {
			t.Fatalf("SavePRState(%d) failed: %v", pr.PRNumber, err)
		}
	}

	// Save circuit breaker state
	if err := store.SaveMetadata("consecutive_failures", "2"); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Create controller and restore
	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.SetStateStore(store)

	restored, err := c.RestoreState()
	if err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	// Should restore 3 total (from LoadAllPRStates), but only 2 active (failed filtered)
	if restored != 3 {
		t.Errorf("restored = %d, want 3 (total from store)", restored)
	}

	prs := c.GetActivePRs()
	if len(prs) != 2 {
		t.Fatalf("active PRs = %d, want 2 (failed should be excluded)", len(prs))
	}

	// Verify stages preserved
	pr42, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 not found in active PRs")
	}
	if pr42.Stage != StageWaitingCI {
		t.Errorf("PR 42 stage = %s, want %s", pr42.Stage, StageWaitingCI)
	}

	pr43, ok := c.GetPRState(43)
	if !ok {
		t.Fatal("PR 43 not found in active PRs")
	}
	if pr43.Stage != StageCIPassed {
		t.Errorf("PR 43 stage = %s, want %s", pr43.Stage, StageCIPassed)
	}

	// Failed PR should not be in active map
	_, ok = c.GetPRState(44)
	if ok {
		t.Error("PR 44 (failed) should not be in active PRs")
	}
}

func TestController_OnPRCreated_PersistsToStore(t *testing.T) {
	store := newTestStateStore(t)

	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.SetStateStore(store)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Verify persisted to store
	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("PR state not persisted to store")
	}
	if loaded.Stage != StagePRCreated {
		t.Errorf("persisted stage = %s, want %s", loaded.Stage, StagePRCreated)
	}
}

func TestController_RemovePR_RemovesFromStore(t *testing.T) {
	store := newTestStateStore(t)

	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.SetStateStore(store)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Remove
	c.removePR(42)

	// Verify removed from store
	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded != nil {
		t.Error("PR state should be removed from store")
	}
}

func TestController_ProcessPR_PersistsTransition(t *testing.T) {
	// Set up mock GitHub server for CI check
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Return empty JSON for any request
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	store := newTestStateStore(t)
	cfg := DefaultConfig()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)

	// Register a PR
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Process — should transition from StagePRCreated to StageWaitingCI
	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR failed: %v", err)
	}

	// Verify state persisted with new stage
	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("PR state not found in store after ProcessPR")
	}
	if loaded.Stage != StageWaitingCI {
		t.Errorf("persisted stage = %s, want %s", loaded.Stage, StageWaitingCI)
	}
}

func TestStateStore_MigrateIdempotent(t *testing.T) {
	store := newTestStateStore(t)

	// Running migrate again should not fail
	if err := store.migrate(); err != nil {
		t.Fatalf("second migration failed: %v", err)
	}
}

// GH-834: Test per-PR failure persistence.
func TestStateStore_PRFailures(t *testing.T) {
	store := newTestStateStore(t)

	// Save failure state
	failureTime := time.Now().Truncate(time.Second)
	if err := store.SavePRFailures("owner/repo", 42, 3, failureTime); err != nil {
		t.Fatalf("SavePRFailures failed: %v", err)
	}

	// Load and verify
	failures, err := store.LoadAllPRFailures("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRFailures failed: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("got %d failures, want 1", len(failures))
	}
	if failures[42] == nil {
		t.Fatal("PR 42 not in failures map")
	}
	if failures[42].FailureCount != 3 {
		t.Errorf("FailureCount = %d, want 3", failures[42].FailureCount)
	}

	// Update failure state
	if err := store.SavePRFailures("owner/repo", 42, 5, time.Now()); err != nil {
		t.Fatalf("SavePRFailures update failed: %v", err)
	}
	failures, _ = store.LoadAllPRFailures("owner/repo")
	if failures[42].FailureCount != 5 {
		t.Errorf("FailureCount after update = %d, want 5", failures[42].FailureCount)
	}

	// Remove failure state
	if err := store.RemovePRFailures("owner/repo", 42); err != nil {
		t.Fatalf("RemovePRFailures failed: %v", err)
	}
	failures, _ = store.LoadAllPRFailures("owner/repo")
	if len(failures) != 0 {
		t.Errorf("got %d failures after remove, want 0", len(failures))
	}
}

// GH-834: Test that RestoreState loads per-PR failures.
func TestController_RestoreState_LoadsPRFailures(t *testing.T) {
	store := newTestStateStore(t)

	// Pre-populate with PR state and failure state
	pr := &PRState{
		PRNumber:   42,
		PRURL:      "https://github.com/owner/repo/pull/42",
		BranchName: "pilot/GH-10",
		Stage:      StageWaitingCI,
		CIStatus:   CIRunning,
		CreatedAt:  time.Now(),
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}
	if err := store.SavePRFailures("owner/repo", 42, 2, time.Now()); err != nil {
		t.Fatalf("SavePRFailures failed: %v", err)
	}

	// Create controller and restore
	cfg := DefaultConfig()
	cfg.MaxFailures = 3
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.SetStateStore(store)

	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	// Verify failure count restored
	if c.GetPRFailures(42) != 2 {
		t.Errorf("GetPRFailures(42) = %d, want 2", c.GetPRFailures(42))
	}
}

// GH-834: Test that removePR also removes failure state.
func TestController_RemovePR_RemovesFailures(t *testing.T) {
	store := newTestStateStore(t)

	cfg := DefaultConfig()
	c := NewController(cfg, nil, nil, "owner", "repo")
	c.SetStateStore(store)

	// Create PR and add failure state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 2, LastFailureTime: time.Now()}
	c.mu.Unlock()
	c.persistPRFailures(42, c.prFailures[42])

	// Verify failure state persisted
	failures, _ := store.LoadAllPRFailures("owner/repo")
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure record, got %d", len(failures))
	}

	// Remove PR
	c.removePR(42)

	// Verify failure state also removed
	failures, _ = store.LoadAllPRFailures("owner/repo")
	if len(failures) != 0 {
		t.Errorf("expected 0 failure records after removePR, got %d", len(failures))
	}
}

// --- GH-1838: Generic adapter_processed tests ---

func TestStateStore_GenericAdapterProcessed(t *testing.T) {
	store := newTestStateStore(t)

	if err := store.Mark("jira", "", "PROJ-1"); err != nil {
		t.Fatalf("Mark failed: %v", err)
	}
	if err := store.Mark("jira", "", "PROJ-2"); err != nil {
		t.Fatalf("Mark failed: %v", err)
	}
	if err := store.Mark("linear", "", "LIN-ABC"); err != nil {
		t.Fatalf("Mark failed: %v", err)
	}

	ok, err := store.IsProcessed("jira", "", "PROJ-1")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if !ok {
		t.Error("PROJ-1 should be processed for jira")
	}

	ok, err = store.IsProcessed("jira", "", "PROJ-999")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if ok {
		t.Error("PROJ-999 should not be processed for jira")
	}

	// Same issue ID, different adapter — should not conflict
	ok, err = store.IsProcessed("linear", "", "PROJ-1")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if ok {
		t.Error("PROJ-1 should not be processed for linear adapter")
	}

	jiraProcessed, err := store.Load("jira", "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(jiraProcessed) != 2 {
		t.Errorf("jira processed count = %d, want 2", len(jiraProcessed))
	}
	if _, ok := jiraProcessed["PROJ-1"]; !ok {
		t.Error("jira processed map missing PROJ-1")
	}
	if _, ok := jiraProcessed["PROJ-2"]; !ok {
		t.Error("jira processed map missing PROJ-2")
	}

	linearProcessed, err := store.Load("linear", "")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(linearProcessed) != 1 {
		t.Errorf("linear processed count = %d, want 1", len(linearProcessed))
	}

	if err := store.Unmark("jira", "", "PROJ-1"); err != nil {
		t.Fatalf("Unmark failed: %v", err)
	}
	ok, _ = store.IsProcessed("jira", "", "PROJ-1")
	if ok {
		t.Error("PROJ-1 should be unmarked after Unmark")
	}
}

func TestStateStore_GenericAdapterProcessed_Upsert(t *testing.T) {
	store := newTestStateStore(t)

	if err := store.Mark("github", "owner/repo", "42"); err != nil {
		t.Fatalf("Mark failed: %v", err)
	}
	// Re-mark (upsert) — should update processed_at, not add a duplicate
	if err := store.Mark("github", "owner/repo", "42"); err != nil {
		t.Fatalf("Mark (upsert) failed: %v", err)
	}

	all, err := store.Load("github", "owner/repo")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 entry after upsert, got %d", len(all))
	}
}

// TestStateStore_GenericAdapterProcessed_CrossRepoIssueIDCollision guards
// against GH-3819: adapter_processed's primary key must include repo, or two
// repos processing the same issue_id silently overwrite each other's row.
func TestStateStore_GenericAdapterProcessed_CrossRepoIssueIDCollision(t *testing.T) {
	store := newTestStateStore(t)

	if err := store.Mark("github", "org/repo-a", "5"); err != nil {
		t.Fatalf("Mark(repo-a) failed: %v", err)
	}
	if err := store.Mark("github", "org/repo-b", "5"); err != nil {
		t.Fatalf("Mark(repo-b) failed: %v", err)
	}

	okA, err := store.IsProcessed("github", "org/repo-a", "5")
	if err != nil {
		t.Fatalf("IsProcessed(repo-a) failed: %v", err)
	}
	if !okA {
		t.Error("issue 5 in repo-a should still be marked processed after repo-b marks its own issue 5")
	}

	okB, err := store.IsProcessed("github", "org/repo-b", "5")
	if err != nil {
		t.Fatalf("IsProcessed(repo-b) failed: %v", err)
	}
	if !okB {
		t.Error("issue 5 in repo-b should be marked processed")
	}

	repoAIssues, err := store.Load("github", "org/repo-a")
	if err != nil {
		t.Fatalf("Load(repo-a) failed: %v", err)
	}
	if len(repoAIssues) != 1 {
		t.Errorf("expected 1 processed issue for repo-a, got %d", len(repoAIssues))
	}

	repoBIssues, err := store.Load("github", "org/repo-b")
	if err != nil {
		t.Fatalf("Load(repo-b) failed: %v", err)
	}
	if len(repoBIssues) != 1 {
		t.Errorf("expected 1 processed issue for repo-b, got %d", len(repoBIssues))
	}
}

// TestStateStore_MigrateAdapterProcessedPrimaryKey verifies that a DB with the
// pre-GH-3819 schema (PRIMARY KEY (adapter, issue_id), repo not part of the
// key) is rebuilt so repo joins the primary key, and existing rows survive
// the rebuild.
func TestStateStore_MigrateAdapterProcessedPrimaryKey(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Seed the old (pre-fix) schema directly, bypassing NewStateStore's migrate().
	if _, err := db.Exec(`
		CREATE TABLE adapter_processed (
			adapter TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT '',
			PRIMARY KEY (adapter, issue_id)
		)
	`); err != nil {
		t.Fatalf("failed to seed legacy schema: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE adapter_processed ADD COLUMN repo TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("failed to add legacy repo column: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO adapter_processed (adapter, repo, issue_id, processed_at, result)
		VALUES ('github', 'org/repo-a', '5', CURRENT_TIMESTAMP, '')
	`); err != nil {
		t.Fatalf("failed to seed legacy row: %v", err)
	}

	store, err := NewStateStore(db)
	if err != nil {
		t.Fatalf("NewStateStore (running migrations) failed: %v", err)
	}

	// Pre-existing row must survive the rebuild.
	ok, err := store.IsProcessed("github", "org/repo-a", "5")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if !ok {
		t.Error("pre-existing row for org/repo-a issue 5 should survive the primary-key migration")
	}

	// Post-migration, colliding issue IDs across repos must not overwrite each other.
	if err := store.Mark("github", "org/repo-b", "5"); err != nil {
		t.Fatalf("Mark(repo-b) failed: %v", err)
	}
	ok, err = store.IsProcessed("github", "org/repo-a", "5")
	if err != nil {
		t.Fatalf("IsProcessed failed: %v", err)
	}
	if !ok {
		t.Error("org/repo-a issue 5 should remain processed after org/repo-b marks its own issue 5")
	}
}

// TestStateStore_PruneOrphanedEmptyRepoRows verifies GH-3819 cleanup: rows for
// repo-scoped adapters (github/gitlab/azuredevops) with an empty repo are
// dead weight (repoKey() is never empty for these adapters) and get purged,
// while tracker-style adapters (linear/jira/asana/plane), which legitimately
// use repo="", are left untouched.
func TestStateStore_PruneOrphanedEmptyRepoRows(t *testing.T) {
	store := newTestStateStore(t)

	// Seed an orphaned empty-repo row directly, bypassing Mark() (which a
	// correctly-behaving repo-scoped poller would never call with repo="").
	if _, err := store.db.Exec(
		`INSERT INTO adapter_processed (adapter, repo, issue_id, processed_at, result)
		 VALUES ('github', '', '5', CURRENT_TIMESTAMP, '')`,
	); err != nil {
		t.Fatalf("seed orphaned row: %v", err)
	}

	// Legitimate rows that must survive: a real repo-scoped row, and the
	// tracker-style repo="" rows.
	if err := store.Mark("github", "org/repo-a", "5"); err != nil {
		t.Fatalf("Mark(github, org/repo-a) failed: %v", err)
	}
	if err := store.Mark("jira", "", "PROJ-1"); err != nil {
		t.Fatalf("Mark(jira) failed: %v", err)
	}

	if err := store.pruneOrphanedEmptyRepoRows(); err != nil {
		t.Fatalf("pruneOrphanedEmptyRepoRows failed: %v", err)
	}

	ok, err := store.IsProcessed("github", "", "5")
	if err != nil {
		t.Fatalf("IsProcessed(github, empty repo) failed: %v", err)
	}
	if ok {
		t.Error("orphaned empty-repo github row should have been pruned")
	}

	ok, err = store.IsProcessed("github", "org/repo-a", "5")
	if err != nil {
		t.Fatalf("IsProcessed(github, org/repo-a) failed: %v", err)
	}
	if !ok {
		t.Error("legitimate github row should survive pruning")
	}

	ok, err = store.IsProcessed("jira", "", "PROJ-1")
	if err != nil {
		t.Fatalf("IsProcessed(jira) failed: %v", err)
	}
	if !ok {
		t.Error("tracker-style jira row (repo=\"\" by design) should survive pruning")
	}
}

// TestStateStore_LegacyMigrationRowsPrunedSamePass verifies GH-3819 subtask 5:
// a legacy per-adapter table (pre-TASK-298, pre-repo-column) for a repo-scoped
// adapter is consolidated into adapter_processed with repo=” by
// migrateLegacyProcessedTables, and that dead-weight row must be purged in the
// SAME migrate() pass by pruneOrphanedEmptyRepoRows — not left to linger until
// a second restart. This pins the migration step ordering in migrate().
func TestStateStore_LegacyMigrationRowsPrunedSamePass(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Seed the pre-TASK-298 legacy github table directly, bypassing migrate().
	if _, err := db.Exec(`
		CREATE TABLE autopilot_processed (
			issue_number INTEGER PRIMARY KEY,
			processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT ''
		)
	`); err != nil {
		t.Fatalf("failed to seed legacy autopilot_processed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO autopilot_processed (issue_number) VALUES (5)`); err != nil {
		t.Fatalf("failed to seed legacy row: %v", err)
	}

	store, err := NewStateStore(db)
	if err != nil {
		t.Fatalf("NewStateStore (running migrations) failed: %v", err)
	}

	// The legacy table must be dropped and its row consolidated, then
	// immediately pruned as dead weight — a repo-scoped adapter can never
	// look this row up again since real lookups always pass a non-empty repo.
	var exists int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='autopilot_processed'`,
	).Scan(&exists); err != nil {
		t.Fatalf("check legacy table: %v", err)
	}
	if exists != 0 {
		t.Error("legacy autopilot_processed table should have been dropped")
	}

	ok, err := store.IsProcessed("github", "", "5")
	if err != nil {
		t.Fatalf("IsProcessed(github, \"\") failed: %v", err)
	}
	if ok {
		t.Error("orphaned empty-repo row from legacy migration should be pruned within the same migrate() pass, not left for a second restart")
	}
}

func TestStateStore_MigrateLegacyProcessedTables(t *testing.T) {
	// Migration test: seed adapter_processed directly (legacy tables are dropped on migrate).
	store := newTestStateStore(t)

	// Verify migration is idempotent (already ran in NewStateStoreFromPath).
	if err := store.migrateLegacyProcessedTables(); err != nil {
		t.Fatalf("second migrateLegacyProcessedTables: %v", err)
	}

	// After migration on a fresh DB, adapter_processed should be empty.
	for _, src := range []string{"github", "linear", "gitlab", "jira", "asana", "azuredevops", "plane"} {
		rows, err := store.Load(src, "")
		if err != nil {
			t.Errorf("Load(%s): %v", src, err)
		}
		if len(rows) != 0 {
			t.Errorf("expected 0 rows for %s after fresh migration, got %d", src, len(rows))
		}
	}
}
