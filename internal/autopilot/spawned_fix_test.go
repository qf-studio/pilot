package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestStateStore_ClaimSpawnedFix_ExactlyOneWinner covers GH-4307: a re-tick, a
// release-scan re-discovery, or a second daemon racing the same failure
// signal must all lose the claim and only one call may proceed to create a
// fix issue.
func TestStateStore_ClaimSpawnedFix_ExactlyOneWinner(t *testing.T) {
	store := newTestStateStore(t)
	// Mirrors TestStateStore_ClaimScopeRelease_ExactlyOneWinner: an in-memory
	// SQLite DB is per-connection, so concurrent goroutines below must share
	// one connection to see the same schema/rows.
	store.db.SetMaxOpenConns(1)

	const n = 8
	var wg sync.WaitGroup
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := store.ClaimSpawnedFix("owner/repo", "fix:pr42:ci_post_merge:")
			if err != nil {
				t.Errorf("ClaimSpawnedFix failed: %v", err)
				return
			}
			results[i] = claimed
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, r := range results {
		if r {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1 (results: %v)", winners, results)
	}
}

func TestStateStore_ClaimSpawnedFix_DistinctKeysBothClaim(t *testing.T) {
	store := newTestStateStore(t)

	claimed1, err := store.ClaimSpawnedFix("owner/repo", "fix:pr42:ci_post_merge:")
	if err != nil {
		t.Fatalf("ClaimSpawnedFix failed: %v", err)
	}
	if !claimed1 {
		t.Error("first claim on a fresh key should succeed")
	}

	// Different PR number is a distinct failure signal — must claim independently.
	claimed2, err := store.ClaimSpawnedFix("owner/repo", "fix:pr43:ci_post_merge:")
	if err != nil {
		t.Fatalf("ClaimSpawnedFix failed: %v", err)
	}
	if !claimed2 {
		t.Error("claim on a distinct dedup key should succeed independently")
	}

	// Repeating the first key must lose the claim.
	claimedAgain, err := store.ClaimSpawnedFix("owner/repo", "fix:pr42:ci_post_merge:")
	if err != nil {
		t.Fatalf("ClaimSpawnedFix failed: %v", err)
	}
	if claimedAgain {
		t.Error("re-claiming an already-claimed key should fail")
	}
}

func TestStateStore_RecordAndGetSpawnedFixIssue(t *testing.T) {
	store := newTestStateStore(t)

	// No row yet — lookup should return 0, not an error.
	issueNum, err := store.GetSpawnedFixIssue("owner/repo", "fix:pr42:ci_post_merge:")
	if err != nil {
		t.Fatalf("GetSpawnedFixIssue failed: %v", err)
	}
	if issueNum != 0 {
		t.Errorf("GetSpawnedFixIssue on missing row = %d, want 0", issueNum)
	}

	if _, err := store.ClaimSpawnedFix("owner/repo", "fix:pr42:ci_post_merge:"); err != nil {
		t.Fatalf("ClaimSpawnedFix failed: %v", err)
	}
	// Claimed but not yet recorded — still 0 (a create may be in flight).
	issueNum, err = store.GetSpawnedFixIssue("owner/repo", "fix:pr42:ci_post_merge:")
	if err != nil {
		t.Fatalf("GetSpawnedFixIssue failed: %v", err)
	}
	if issueNum != 0 {
		t.Errorf("GetSpawnedFixIssue before RecordSpawnedFixIssue = %d, want 0", issueNum)
	}

	if err := store.RecordSpawnedFixIssue("owner/repo", "fix:pr42:ci_post_merge:", 4307); err != nil {
		t.Fatalf("RecordSpawnedFixIssue failed: %v", err)
	}
	issueNum, err = store.GetSpawnedFixIssue("owner/repo", "fix:pr42:ci_post_merge:")
	if err != nil {
		t.Fatalf("GetSpawnedFixIssue failed: %v", err)
	}
	if issueNum != 4307 {
		t.Errorf("GetSpawnedFixIssue = %d, want 4307", issueNum)
	}
}

// TestFeedbackLoop_CreateFailureIssue_DedupSuppressesSecondCall verifies the
// end-to-end guard through FeedbackLoop: two CreateFailureIssue calls for the
// same PR/failure-type/failed-checks (the exact shape of a re-tick or a
// release-scan re-discovery hitting the same false-positive canary check)
// must create exactly one GitHub issue, not two (GH-4307).
func TestFeedbackLoop_CreateFailureIssue_DedupSuppressesSecondCall(t *testing.T) {
	createCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			createCalls++
			var input github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&input)

			resp := github.Issue{Number: 4307}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)
	fl.SetStateStore(newTestStateStore(t))

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
	}

	first, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"epic-lifecycle / run"}, "", 1)
	if err != nil {
		t.Fatalf("first CreateFailureIssue() error = %v", err)
	}
	if first != 4307 {
		t.Errorf("first CreateFailureIssue() = %d, want 4307", first)
	}

	// Simulates a re-tick / release-scan re-discovery / second daemon
	// observing the identical failure signal.
	second, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"epic-lifecycle / run"}, "", 1)
	if err != nil {
		t.Fatalf("second CreateFailureIssue() error = %v", err)
	}
	if second != 4307 {
		t.Errorf("second CreateFailureIssue() = %d, want 4307 (existing issue, not a new one)", second)
	}

	if createCalls != 1 {
		t.Errorf("createCalls = %d, want exactly 1 — duplicate must be suppressed", createCalls)
	}
}

// TestFeedbackLoop_CreateFailureIssue_DedupDistinguishesFailedChecks verifies
// a genuinely different failure signal (different failed checks) on the same
// PR is NOT suppressed — the dedup key must include the check signature.
func TestFeedbackLoop_CreateFailureIssue_DedupDistinguishesFailedChecks(t *testing.T) {
	createCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			createCalls++
			resp := github.Issue{Number: 100 + createCalls}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)
	fl.SetStateStore(newTestStateStore(t))

	prState := &PRState{PRNumber: 42, HeadSHA: "abc1234"}

	if _, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"lint"}, "", 1); err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}
	if _, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"test"}, "", 1); err != nil {
		t.Fatalf("CreateFailureIssue() error = %v", err)
	}

	if createCalls != 2 {
		t.Errorf("createCalls = %d, want 2 — distinct failed-checks signature must not be deduped", createCalls)
	}
}

// TestFeedbackLoop_CreateFailureIssue_NoStateStoreCreatesEveryTime verifies
// the guard is opt-in: without SetStateStore, CreateFailureIssue never
// dedups (pre-GH-4307 behavior is preserved for callers that don't wire a
// store, e.g. existing unit tests using NewFeedbackLoop directly).
func TestFeedbackLoop_CreateFailureIssue_NoStateStoreCreatesEveryTime(t *testing.T) {
	createCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST" {
			createCalls++
			resp := github.Issue{Number: 100 + createCalls}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42, HeadSHA: "abc1234"}

	for i := 0; i < 2; i++ {
		if _, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPostMerge, []string{"lint"}, "", 1); err != nil {
			t.Fatalf("CreateFailureIssue() error = %v", err)
		}
	}

	if createCalls != 2 {
		t.Errorf("createCalls = %d, want 2 — no state store means no dedup guard", createCalls)
	}
}

func TestController_SetStateStore_ForwardsToFeedbackLoop(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := &Controller{
		owner:        "owner",
		repo:         "repo",
		config:       cfg,
		feedbackLoop: NewFeedbackLoop(ghClient, "owner", "repo", cfg),
	}

	store := newTestStateStore(t)
	c.SetStateStore(store)

	if c.feedbackLoop.stateStore != store {
		t.Error("SetStateStore should forward the store to the feedback loop")
	}
}
