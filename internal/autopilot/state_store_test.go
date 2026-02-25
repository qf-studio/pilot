package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/testutil"
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
	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	// Load single
	loaded, err := store.GetPRState(42)
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
		if err := store.SavePRState(pr); err != nil {
			t.Fatalf("SavePRState(%d) failed: %v", num, err)
		}
	}

	states, err := store.LoadAllPRStates()
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

	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("initial SavePRState failed: %v", err)
	}

	// Update stage
	pr.Stage = StageWaitingCI
	pr.CIStatus = CIRunning
	pr.CIWaitStartedAt = time.Now()
	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("update SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState(42)
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

	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	if err := store.RemovePRState(42); err != nil {
		t.Fatalf("RemovePRState failed: %v", err)
	}

	loaded, err := store.GetPRState(42)
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
	processed, err := store.IsIssueProcessed(100)
	if err != nil {
		t.Fatalf("IsIssueProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark processed
	if err := store.MarkIssueProcessed(100, "success"); err != nil {
		t.Fatalf("MarkIssueProcessed failed: %v", err)
	}

	processed, err = store.IsIssueProcessed(100)
	if err != nil {
		t.Fatalf("IsIssueProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.LoadProcessedIssues()
	if err != nil {
		t.Fatalf("LoadProcessedIssues failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all[100] {
		t.Error("issue 100 should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkIssueProcessed(100, "failed"); err != nil {
		t.Fatalf("idempotent MarkIssueProcessed failed: %v", err)
	}
	all, _ = store.LoadProcessedIssues()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkIssueProcessed(100); err != nil {
		t.Fatalf("UnmarkIssueProcessed failed: %v", err)
	}
	processed, err = store.IsIssueProcessed(100)
	if err != nil {
		t.Fatalf("IsIssueProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.UnmarkIssueProcessed(999); err != nil {
		t.Fatalf("UnmarkIssueProcessed for non-existent issue failed: %v", err)
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

func TestStateStore_PurgeOldProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some issues
	for i := 1; i <= 5; i++ {
		if err := store.MarkIssueProcessed(i, "success"); err != nil {
			t.Fatalf("MarkIssueProcessed(%d) failed: %v", i, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldProcessedIssues(0)
	if err != nil {
		t.Fatalf("PurgeOldProcessedIssues failed: %v", err)
	}
	if purged != 5 {
		t.Errorf("purged = %d, want 5", purged)
	}

	all, _ := store.LoadProcessedIssues()
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

	if err := store.SavePRState(failedPR); err != nil {
		t.Fatalf("SavePRState(failed) failed: %v", err)
	}
	if err := store.SavePRState(activePR); err != nil {
		t.Fatalf("SavePRState(active) failed: %v", err)
	}

	// Purge terminal states older than 0 (immediate)
	purged, err := store.PurgeTerminalPRStates(0)
	if err != nil {
		t.Fatalf("PurgeTerminalPRStates failed: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged = %d, want 1 (only failed)", purged)
	}

	// Active PR should still exist
	states, _ := store.LoadAllPRStates()
	if len(states) != 1 {
		t.Fatalf("got %d states, want 1", len(states))
	}
	if states[0].PRNumber != 2 {
		t.Errorf("remaining PR = %d, want 2", states[0].PRNumber)
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
		if err := store.SavePRState(pr); err != nil {
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
	loaded, err := store.GetPRState(42)
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
	loaded, err := store.GetPRState(42)
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
	loaded, err := store.GetPRState(42)
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
	if err := store.SavePRFailures(42, 3, failureTime); err != nil {
		t.Fatalf("SavePRFailures failed: %v", err)
	}

	// Load and verify
	failures, err := store.LoadAllPRFailures()
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
	if err := store.SavePRFailures(42, 5, time.Now()); err != nil {
		t.Fatalf("SavePRFailures update failed: %v", err)
	}
	failures, _ = store.LoadAllPRFailures()
	if failures[42].FailureCount != 5 {
		t.Errorf("FailureCount after update = %d, want 5", failures[42].FailureCount)
	}

	// Remove failure state
	if err := store.RemovePRFailures(42); err != nil {
		t.Fatalf("RemovePRFailures failed: %v", err)
	}
	failures, _ = store.LoadAllPRFailures()
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
	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}
	if err := store.SavePRFailures(42, 2, time.Now()); err != nil {
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
	failures, _ := store.LoadAllPRFailures()
	if len(failures) != 1 {
		t.Fatalf("expected 1 failure record, got %d", len(failures))
	}

	// Remove PR
	c.removePR(42)

	// Verify failure state also removed
	failures, _ = store.LoadAllPRFailures()
	if len(failures) != 0 {
		t.Errorf("expected 0 failure records after removePR, got %d", len(failures))
	}
}

// GH-1351: Test Linear processed issues (string IDs).
func TestStateStore_LinearProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsLinearIssueProcessed("abc-123-def")
	if err != nil {
		t.Fatalf("IsLinearIssueProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark processed
	if err := store.MarkLinearIssueProcessed("abc-123-def", "success"); err != nil {
		t.Fatalf("MarkLinearIssueProcessed failed: %v", err)
	}

	processed, err = store.IsLinearIssueProcessed("abc-123-def")
	if err != nil {
		t.Fatalf("IsLinearIssueProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.LoadLinearProcessedIssues()
	if err != nil {
		t.Fatalf("LoadLinearProcessedIssues failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all["abc-123-def"] {
		t.Error("issue abc-123-def should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkLinearIssueProcessed("abc-123-def", "failed"); err != nil {
		t.Fatalf("idempotent MarkLinearIssueProcessed failed: %v", err)
	}
	all, _ = store.LoadLinearProcessedIssues()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkLinearIssueProcessed("abc-123-def"); err != nil {
		t.Fatalf("UnmarkLinearIssueProcessed failed: %v", err)
	}
	processed, err = store.IsLinearIssueProcessed("abc-123-def")
	if err != nil {
		t.Fatalf("IsLinearIssueProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.UnmarkLinearIssueProcessed("nonexistent-id"); err != nil {
		t.Fatalf("UnmarkLinearIssueProcessed for non-existent issue failed: %v", err)
	}
}

// GH-1351: Test Linear processed issues purge.
func TestStateStore_PurgeOldLinearProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some issues
	for i := 1; i <= 5; i++ {
		id := "linear-issue-" + string(rune('a'+i-1))
		if err := store.MarkLinearIssueProcessed(id, "success"); err != nil {
			t.Fatalf("MarkLinearIssueProcessed(%s) failed: %v", id, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldLinearProcessedIssues(0)
	if err != nil {
		t.Fatalf("PurgeOldLinearProcessedIssues failed: %v", err)
	}
	if purged != 5 {
		t.Errorf("purged = %d, want 5", purged)
	}

	all, _ := store.LoadLinearProcessedIssues()
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

// GH-1351: Test Linear and GitHub processed stores are independent.
func TestStateStore_LinearAndGitHubProcessedIndependent(t *testing.T) {
	store := newTestStateStore(t)

	// Mark a GitHub issue (integer ID)
	if err := store.MarkIssueProcessed(100, "success"); err != nil {
		t.Fatalf("MarkIssueProcessed failed: %v", err)
	}

	// Mark a Linear issue (string ID)
	if err := store.MarkLinearIssueProcessed("linear-abc-123", "success"); err != nil {
		t.Fatalf("MarkLinearIssueProcessed failed: %v", err)
	}

	// Verify both are independent
	ghProcessed, _ := store.LoadProcessedIssues()
	linearProcessed, _ := store.LoadLinearProcessedIssues()

	if len(ghProcessed) != 1 {
		t.Errorf("GitHub processed = %d, want 1", len(ghProcessed))
	}
	if len(linearProcessed) != 1 {
		t.Errorf("Linear processed = %d, want 1", len(linearProcessed))
	}

	if !ghProcessed[100] {
		t.Error("GitHub issue 100 should be in processed map")
	}
	if !linearProcessed["linear-abc-123"] {
		t.Error("Linear issue linear-abc-123 should be in processed map")
	}
}

// GH-1356: Test GitLab processed issues (integer IDs like GitHub).
func TestStateStore_GitLabProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsGitLabIssueProcessed(200)
	if err != nil {
		t.Fatalf("IsGitLabIssueProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark processed
	if err := store.MarkGitLabIssueProcessed(200, "success"); err != nil {
		t.Fatalf("MarkGitLabIssueProcessed failed: %v", err)
	}

	processed, err = store.IsGitLabIssueProcessed(200)
	if err != nil {
		t.Fatalf("IsGitLabIssueProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.LoadGitLabProcessedIssues()
	if err != nil {
		t.Fatalf("LoadGitLabProcessedIssues failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all[200] {
		t.Error("issue 200 should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkGitLabIssueProcessed(200, "failed"); err != nil {
		t.Fatalf("idempotent MarkGitLabIssueProcessed failed: %v", err)
	}
	all, _ = store.LoadGitLabProcessedIssues()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkGitLabIssueProcessed(200); err != nil {
		t.Fatalf("UnmarkGitLabIssueProcessed failed: %v", err)
	}
	processed, err = store.IsGitLabIssueProcessed(200)
	if err != nil {
		t.Fatalf("IsGitLabIssueProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.UnmarkGitLabIssueProcessed(999); err != nil {
		t.Fatalf("UnmarkGitLabIssueProcessed for non-existent issue failed: %v", err)
	}
}

// GH-1356: Test GitLab processed issues purge.
func TestStateStore_PurgeOldGitLabProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some issues
	for i := 1; i <= 3; i++ {
		if err := store.MarkGitLabIssueProcessed(200+i, "success"); err != nil {
			t.Fatalf("MarkGitLabIssueProcessed(%d) failed: %v", 200+i, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldGitLabProcessedIssues(0)
	if err != nil {
		t.Fatalf("PurgeOldGitLabProcessedIssues failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3", purged)
	}

	all, _ := store.LoadGitLabProcessedIssues()
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

// GH-1356: Test Jira processed issues (string keys).
func TestStateStore_JiraProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsJiraIssueProcessed("PROJ-123")
	if err != nil {
		t.Fatalf("IsJiraIssueProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark processed
	if err := store.MarkJiraIssueProcessed("PROJ-123", "success"); err != nil {
		t.Fatalf("MarkJiraIssueProcessed failed: %v", err)
	}

	processed, err = store.IsJiraIssueProcessed("PROJ-123")
	if err != nil {
		t.Fatalf("IsJiraIssueProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.LoadJiraProcessedIssues()
	if err != nil {
		t.Fatalf("LoadJiraProcessedIssues failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all["PROJ-123"] {
		t.Error("issue PROJ-123 should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkJiraIssueProcessed("PROJ-123", "failed"); err != nil {
		t.Fatalf("idempotent MarkJiraIssueProcessed failed: %v", err)
	}
	all, _ = store.LoadJiraProcessedIssues()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkJiraIssueProcessed("PROJ-123"); err != nil {
		t.Fatalf("UnmarkJiraIssueProcessed failed: %v", err)
	}
	processed, err = store.IsJiraIssueProcessed("PROJ-123")
	if err != nil {
		t.Fatalf("IsJiraIssueProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.UnmarkJiraIssueProcessed("NONEXIST-999"); err != nil {
		t.Fatalf("UnmarkJiraIssueProcessed for non-existent issue failed: %v", err)
	}
}

// GH-1356: Test Jira processed issues purge.
func TestStateStore_PurgeOldJiraProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some issues
	jiraKeys := []string{"PROJ-100", "PROJ-101", "PROJ-102"}
	for _, key := range jiraKeys {
		if err := store.MarkJiraIssueProcessed(key, "success"); err != nil {
			t.Fatalf("MarkJiraIssueProcessed(%s) failed: %v", key, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldJiraProcessedIssues(0)
	if err != nil {
		t.Fatalf("PurgeOldJiraProcessedIssues failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3", purged)
	}

	all, _ := store.LoadJiraProcessedIssues()
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

// GH-1356: Test Asana processed tasks (string GIDs).
func TestStateStore_AsanaProcessedTasks(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsAsanaTaskProcessed("1234567890123456")
	if err != nil {
		t.Fatalf("IsAsanaTaskProcessed failed: %v", err)
	}
	if processed {
		t.Error("task should not be processed initially")
	}

	// Mark processed
	if err := store.MarkAsanaTaskProcessed("1234567890123456", "success"); err != nil {
		t.Fatalf("MarkAsanaTaskProcessed failed: %v", err)
	}

	processed, err = store.IsAsanaTaskProcessed("1234567890123456")
	if err != nil {
		t.Fatalf("IsAsanaTaskProcessed failed: %v", err)
	}
	if !processed {
		t.Error("task should be processed after marking")
	}

	// Load all
	all, err := store.LoadAsanaProcessedTasks()
	if err != nil {
		t.Fatalf("LoadAsanaProcessedTasks failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all["1234567890123456"] {
		t.Error("task 1234567890123456 should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkAsanaTaskProcessed("1234567890123456", "failed"); err != nil {
		t.Fatalf("idempotent MarkAsanaTaskProcessed failed: %v", err)
	}
	all, _ = store.LoadAsanaProcessedTasks()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkAsanaTaskProcessed("1234567890123456"); err != nil {
		t.Fatalf("UnmarkAsanaTaskProcessed failed: %v", err)
	}
	processed, err = store.IsAsanaTaskProcessed("1234567890123456")
	if err != nil {
		t.Fatalf("IsAsanaTaskProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("task should not be processed after unmarking")
	}

	// Unmark non-existent task should not error
	if err := store.UnmarkAsanaTaskProcessed("9999999999999999"); err != nil {
		t.Fatalf("UnmarkAsanaTaskProcessed for non-existent task failed: %v", err)
	}
}

// GH-1356: Test Asana processed tasks purge.
func TestStateStore_PurgeOldAsanaProcessedTasks(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some tasks
	asanaGIDs := []string{"1111111111111111", "2222222222222222", "3333333333333333"}
	for _, gid := range asanaGIDs {
		if err := store.MarkAsanaTaskProcessed(gid, "success"); err != nil {
			t.Fatalf("MarkAsanaTaskProcessed(%s) failed: %v", gid, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldAsanaProcessedTasks(0)
	if err != nil {
		t.Fatalf("PurgeOldAsanaProcessedTasks failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3", purged)
	}

	all, _ := store.LoadAsanaProcessedTasks()
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

// GH-1356: Test Azure DevOps processed work items (integer IDs).
func TestStateStore_AzureDevOpsProcessedWorkItems(t *testing.T) {
	store := newTestStateStore(t)

	// Not processed initially
	processed, err := store.IsAzureDevOpsWorkItemProcessed(5000)
	if err != nil {
		t.Fatalf("IsAzureDevOpsWorkItemProcessed failed: %v", err)
	}
	if processed {
		t.Error("work item should not be processed initially")
	}

	// Mark processed
	if err := store.MarkAzureDevOpsWorkItemProcessed(5000, "success"); err != nil {
		t.Fatalf("MarkAzureDevOpsWorkItemProcessed failed: %v", err)
	}

	processed, err = store.IsAzureDevOpsWorkItemProcessed(5000)
	if err != nil {
		t.Fatalf("IsAzureDevOpsWorkItemProcessed failed: %v", err)
	}
	if !processed {
		t.Error("work item should be processed after marking")
	}

	// Load all
	all, err := store.LoadAzureDevOpsProcessedWorkItems()
	if err != nil {
		t.Fatalf("LoadAzureDevOpsProcessedWorkItems failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("got %d processed, want 1", len(all))
	}
	if !all[5000] {
		t.Error("work item 5000 should be in processed map")
	}

	// Idempotent mark
	if err := store.MarkAzureDevOpsWorkItemProcessed(5000, "failed"); err != nil {
		t.Fatalf("idempotent MarkAzureDevOpsWorkItemProcessed failed: %v", err)
	}
	all, _ = store.LoadAzureDevOpsProcessedWorkItems()
	if len(all) != 1 {
		t.Errorf("got %d processed after idempotent mark, want 1", len(all))
	}

	// Unmark processed (for retry when pilot-failed label removed)
	if err := store.UnmarkAzureDevOpsWorkItemProcessed(5000); err != nil {
		t.Fatalf("UnmarkAzureDevOpsWorkItemProcessed failed: %v", err)
	}
	processed, err = store.IsAzureDevOpsWorkItemProcessed(5000)
	if err != nil {
		t.Fatalf("IsAzureDevOpsWorkItemProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("work item should not be processed after unmarking")
	}

	// Unmark non-existent work item should not error
	if err := store.UnmarkAzureDevOpsWorkItemProcessed(9999); err != nil {
		t.Fatalf("UnmarkAzureDevOpsWorkItemProcessed for non-existent work item failed: %v", err)
	}
}

// GH-1356: Test Azure DevOps processed work items purge.
func TestStateStore_PurgeOldAzureDevOpsProcessedWorkItems(t *testing.T) {
	store := newTestStateStore(t)

	// Mark some work items
	for i := 1; i <= 4; i++ {
		workItemID := 5000 + i
		if err := store.MarkAzureDevOpsWorkItemProcessed(workItemID, "success"); err != nil {
			t.Fatalf("MarkAzureDevOpsWorkItemProcessed(%d) failed: %v", workItemID, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldAzureDevOpsProcessedWorkItems(0)
	if err != nil {
		t.Fatalf("PurgeOldAzureDevOpsProcessedWorkItems failed: %v", err)
	}
	if purged != 4 {
		t.Errorf("purged = %d, want 4", purged)
	}

	all, _ := store.LoadAzureDevOpsProcessedWorkItems()
	if len(all) != 0 {
		t.Errorf("got %d after purge, want 0", len(all))
	}
}

// GH-1356: Test all new processed stores are independent.
func TestStateStore_AllProcessedStoresIndependent(t *testing.T) {
	store := newTestStateStore(t)

	// Mark issues in all platforms
	if err := store.MarkGitLabIssueProcessed(300, "success"); err != nil {
		t.Fatalf("MarkGitLabIssueProcessed failed: %v", err)
	}
	if err := store.MarkJiraIssueProcessed("TEST-456", "success"); err != nil {
		t.Fatalf("MarkJiraIssueProcessed failed: %v", err)
	}
	if err := store.MarkAsanaTaskProcessed("4444444444444444", "success"); err != nil {
		t.Fatalf("MarkAsanaTaskProcessed failed: %v", err)
	}
	if err := store.MarkAzureDevOpsWorkItemProcessed(6000, "success"); err != nil {
		t.Fatalf("MarkAzureDevOpsWorkItemProcessed failed: %v", err)
	}

	// Verify all are independent
	gitlabProcessed, _ := store.LoadGitLabProcessedIssues()
	jiraProcessed, _ := store.LoadJiraProcessedIssues()
	asanaProcessed, _ := store.LoadAsanaProcessedTasks()
	azureProcessed, _ := store.LoadAzureDevOpsProcessedWorkItems()

	if len(gitlabProcessed) != 1 || !gitlabProcessed[300] {
		t.Error("GitLab issue 300 should be processed")
	}
	if len(jiraProcessed) != 1 || !jiraProcessed["TEST-456"] {
		t.Error("Jira issue TEST-456 should be processed")
	}
	if len(asanaProcessed) != 1 || !asanaProcessed["4444444444444444"] {
		t.Error("Asana task 4444444444444444 should be processed")
	}
	if len(azureProcessed) != 1 || !azureProcessed[6000] {
		t.Error("Azure DevOps work item 6000 should be processed")
	}

	// Plane
	if err := store.MarkPlaneIssueProcessed("plane-issue-uuid-1", "success"); err != nil {
		t.Fatalf("MarkPlaneIssueProcessed failed: %v", err)
	}
	planeProcessed, _ := store.LoadPlaneProcessedIssues()
	if len(planeProcessed) != 1 || !planeProcessed["plane-issue-uuid-1"] {
		t.Error("Plane issue plane-issue-uuid-1 should be processed")
	}
}

// GH-1829: Test Plane.so processed issues (string IDs).
func TestStateStore_PlaneProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	// Initially not processed
	processed, err := store.IsPlaneIssueProcessed("plane-uuid-123")
	if err != nil {
		t.Fatalf("IsPlaneIssueProcessed failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed initially")
	}

	// Mark as processed
	if err := store.MarkPlaneIssueProcessed("plane-uuid-123", "success"); err != nil {
		t.Fatalf("MarkPlaneIssueProcessed failed: %v", err)
	}

	processed, err = store.IsPlaneIssueProcessed("plane-uuid-123")
	if err != nil {
		t.Fatalf("IsPlaneIssueProcessed failed: %v", err)
	}
	if !processed {
		t.Error("issue should be processed after marking")
	}

	// Load all
	all, err := store.LoadPlaneProcessedIssues()
	if err != nil {
		t.Fatalf("LoadPlaneProcessedIssues failed: %v", err)
	}
	if len(all) != 1 || !all["plane-uuid-123"] {
		t.Errorf("LoadPlaneProcessedIssues = %v, want {plane-uuid-123: true}", all)
	}

	// Idempotent mark (update result)
	if err := store.MarkPlaneIssueProcessed("plane-uuid-123", "failed"); err != nil {
		t.Fatalf("idempotent MarkPlaneIssueProcessed failed: %v", err)
	}
	all, _ = store.LoadPlaneProcessedIssues()
	if len(all) != 1 {
		t.Errorf("expected 1 processed issue after idempotent mark, got %d", len(all))
	}

	// Unmark
	if err := store.UnmarkPlaneIssueProcessed("plane-uuid-123"); err != nil {
		t.Fatalf("UnmarkPlaneIssueProcessed failed: %v", err)
	}
	processed, err = store.IsPlaneIssueProcessed("plane-uuid-123")
	if err != nil {
		t.Fatalf("IsPlaneIssueProcessed after unmark failed: %v", err)
	}
	if processed {
		t.Error("issue should not be processed after unmarking")
	}

	// Unmark non-existent issue should not error
	if err := store.UnmarkPlaneIssueProcessed("nonexist-uuid"); err != nil {
		t.Fatalf("UnmarkPlaneIssueProcessed for non-existent issue failed: %v", err)
	}
}

// GH-1829: Test Plane.so processed issues purge.
func TestStateStore_PurgeOldPlaneProcessedIssues(t *testing.T) {
	store := newTestStateStore(t)

	ids := []string{"plane-uuid-1", "plane-uuid-2", "plane-uuid-3"}
	for _, id := range ids {
		if err := store.MarkPlaneIssueProcessed(id, "success"); err != nil {
			t.Fatalf("MarkPlaneIssueProcessed(%s) failed: %v", id, err)
		}
	}

	// Purge older than 0 (all should be purged)
	purged, err := store.PurgeOldPlaneProcessedIssues(0)
	if err != nil {
		t.Fatalf("PurgeOldPlaneProcessedIssues failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3", purged)
	}

	all, _ := store.LoadPlaneProcessedIssues()
	if len(all) != 0 {
		t.Errorf("expected 0 after purge, got %d", len(all))
	}
}

// --- GH-1838: Generic adapter_processed tests ---

func TestStateStore_GenericAdapterProcessed(t *testing.T) {
	store := newTestStateStore(t)

	// Mark processed
	if err := store.MarkAdapterProcessed("jira", "PROJ-1", "success"); err != nil {
		t.Fatalf("MarkAdapterProcessed failed: %v", err)
	}
	if err := store.MarkAdapterProcessed("jira", "PROJ-2", "failed"); err != nil {
		t.Fatalf("MarkAdapterProcessed failed: %v", err)
	}
	if err := store.MarkAdapterProcessed("linear", "LIN-ABC", "success"); err != nil {
		t.Fatalf("MarkAdapterProcessed failed: %v", err)
	}

	// Check processed
	ok, err := store.IsAdapterProcessed("jira", "PROJ-1")
	if err != nil {
		t.Fatalf("IsAdapterProcessed failed: %v", err)
	}
	if !ok {
		t.Error("PROJ-1 should be processed for jira")
	}

	ok, err = store.IsAdapterProcessed("jira", "PROJ-999")
	if err != nil {
		t.Fatalf("IsAdapterProcessed failed: %v", err)
	}
	if ok {
		t.Error("PROJ-999 should not be processed for jira")
	}

	// Same issue ID different adapter should not conflict
	ok, err = store.IsAdapterProcessed("linear", "PROJ-1")
	if err != nil {
		t.Fatalf("IsAdapterProcessed failed: %v", err)
	}
	if ok {
		t.Error("PROJ-1 should not be processed for linear adapter")
	}

	// Load all for adapter
	jiraProcessed, err := store.LoadAdapterProcessed("jira")
	if err != nil {
		t.Fatalf("LoadAdapterProcessed failed: %v", err)
	}
	if len(jiraProcessed) != 2 {
		t.Errorf("jira processed count = %d, want 2", len(jiraProcessed))
	}
	if !jiraProcessed["PROJ-1"] || !jiraProcessed["PROJ-2"] {
		t.Error("jira processed map missing expected keys")
	}

	linearProcessed, err := store.LoadAdapterProcessed("linear")
	if err != nil {
		t.Fatalf("LoadAdapterProcessed failed: %v", err)
	}
	if len(linearProcessed) != 1 {
		t.Errorf("linear processed count = %d, want 1", len(linearProcessed))
	}

	// Unmark
	if err := store.UnmarkAdapterProcessed("jira", "PROJ-1"); err != nil {
		t.Fatalf("UnmarkAdapterProcessed failed: %v", err)
	}
	ok, _ = store.IsAdapterProcessed("jira", "PROJ-1")
	if ok {
		t.Error("PROJ-1 should be unmarked after UnmarkAdapterProcessed")
	}
}

func TestStateStore_GenericAdapterProcessed_Upsert(t *testing.T) {
	store := newTestStateStore(t)

	// Mark, then re-mark with different result (upsert)
	if err := store.MarkAdapterProcessed("github", "42", "pending"); err != nil {
		t.Fatalf("MarkAdapterProcessed failed: %v", err)
	}
	if err := store.MarkAdapterProcessed("github", "42", "success"); err != nil {
		t.Fatalf("MarkAdapterProcessed (upsert) failed: %v", err)
	}

	// Should still be one entry
	all, err := store.LoadAdapterProcessed("github")
	if err != nil {
		t.Fatalf("LoadAdapterProcessed failed: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 entry after upsert, got %d", len(all))
	}
}

func TestStateStore_PurgeOldAdapterProcessed(t *testing.T) {
	store := newTestStateStore(t)

	for _, id := range []string{"A", "B", "C"} {
		if err := store.MarkAdapterProcessed("test", id, "ok"); err != nil {
			t.Fatalf("MarkAdapterProcessed failed: %v", err)
		}
	}

	// Purge with 0 duration (all should be purged)
	purged, err := store.PurgeOldAdapterProcessed("test", 0)
	if err != nil {
		t.Fatalf("PurgeOldAdapterProcessed failed: %v", err)
	}
	if purged != 3 {
		t.Errorf("purged = %d, want 3", purged)
	}

	remaining, _ := store.LoadAdapterProcessed("test")
	if len(remaining) != 0 {
		t.Errorf("expected 0 after purge, got %d", len(remaining))
	}
}
