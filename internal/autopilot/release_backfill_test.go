package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// releaseBackfillFakeTag is the minimal shape needed to serve GitHub's
// /tags endpoint for earliestReleaseTagContaining.
type releaseBackfillFakeTag struct {
	name string
	sha  string
}

// releaseBackfillServer serves the three endpoints GH-4370's reconciliation
// needs: GetPullRequest (merge status + merge commit SHA), ListTags, and
// CompareStatus (ancestry). compareStatus is keyed "base...head" exactly as
// CompareStatus builds its request path.
func releaseBackfillServer(t *testing.T, prs map[int]*github.PullRequest, tags []releaseBackfillFakeTag, compareStatus map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			var num int
			if _, err := fmt.Sscanf(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "%d", &num); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			pr, ok := prs[num]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tags"):
			out := make([]*github.Tag, 0, len(tags))
			for _, tag := range tags {
				gt := &github.Tag{Name: tag.name}
				gt.Commit.SHA = tag.sha
				out = append(out, gt)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			key := r.URL.Path[strings.LastIndex(r.URL.Path, "/compare/")+len("/compare/"):]
			status, ok := compareStatus[key]
			if !ok {
				status = "diverged"
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestReconcileReleaseBackfill covers GH-4370's release-tag-ancestry
// reconciliation: a manual tag push bypasses the release train entirely,
// leaving autopilot_pr_state rows wedged at StageFailed/StageReleasing
// forever even though the PR shipped. Ground truth is git ancestry.
func TestReconcileReleaseBackfill(t *testing.T) {
	tests := []struct {
		name string
		pr   *github.PullRequest
		// stage is the pre-existing autopilot_pr_state row's stage — both
		// StageFailed and StageReleasing residue are reconciliation candidates.
		stage PRStage
		tags  []releaseBackfillFakeTag
		// compareStatus maps "<mergeSHA>...<tagSHA>" -> GitHub's compare status.
		compareStatus map[string]string

		// preExistingEvent seeds the mock execution ladder with a StageReleased
		// event before the sweep runs, to test the already-released-row case.
		preExistingEvent bool

		wantDrained    bool
		wantVersion    string
		wantEventCount int
	}{
		{
			name:  "manual-tag release (train never ran) backfilled with correct version",
			pr:    &github.PullRequest{Number: 100, Merged: true, MergeCommitSHA: "sha100"},
			stage: StageReleasing,
			tags: []releaseBackfillFakeTag{
				{name: "v2.240.0", sha: "tagsha240"},
				{name: "v2.241.0", sha: "tagsha241"},
			},
			compareStatus: map[string]string{
				"sha100...tagsha240": "ahead",
				"sha100...tagsha241": "ahead",
			},
			wantDrained:    true,
			wantVersion:    "v2.240.0", // earliest containing tag, not the newest
			wantEventCount: 1,
		},
		{
			name:  "merged PR not yet covered by any tag stays untouched",
			pr:    &github.PullRequest{Number: 101, Merged: true, MergeCommitSHA: "sha101"},
			stage: StageFailed,
			tags: []releaseBackfillFakeTag{
				{name: "v2.240.0", sha: "tagsha240"},
			},
			compareStatus: map[string]string{
				"sha101...tagsha240": "diverged",
			},
			wantDrained:    false,
			wantEventCount: 0,
		},
		{
			name:           "never-merged failure stays untouched",
			pr:             &github.PullRequest{Number: 102, Merged: false},
			stage:          StageFailed,
			wantDrained:    false,
			wantEventCount: 0,
		},
		{
			name:  "already-released row heals without a duplicate event",
			pr:    &github.PullRequest{Number: 103, Merged: true, MergeCommitSHA: "sha103"},
			stage: StageReleasing,
			tags: []releaseBackfillFakeTag{
				{name: "v2.239.0", sha: "tagsha239"},
			},
			compareStatus: map[string]string{
				"sha103...tagsha239": "ahead",
			},
			preExistingEvent: true,
			wantDrained:      true,
			wantVersion:      "v2.239.0",
			wantEventCount:   1, // still just the one pre-existing event, no duplicate
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := map[int]*github.PullRequest{tt.pr.Number: tt.pr}
			server := releaseBackfillServer(t, prs, tt.tags, tt.compareStatus)
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

			store, err := NewStateStoreFromPath(":memory:")
			if err != nil {
				t.Fatalf("NewStateStoreFromPath: %v", err)
			}
			c.SetStateStore(store)

			taskID := fmt.Sprintf("GH-%d", tt.pr.Number)
			execID := fmt.Sprintf("exec-%d", tt.pr.Number)
			mock := &mockApprovalPersister{
				execByTask: map[string]string{taskID: execID},
			}
			if tt.preExistingEvent {
				mock.executionEvents = append(mock.executionEvents, recordedExecutionEvent{
					executionID: execID,
					stage:       memory.StageReleased,
					detail:      "released via the normal automated path",
				})
			}
			c.memoryStore = mock

			prURL := fmt.Sprintf("https://github.com/owner/repo/pull/%d", tt.pr.Number)
			if err := store.SavePRState("owner/repo", &PRState{
				PRNumber:    tt.pr.Number,
				PRURL:       prURL,
				IssueNumber: tt.pr.Number,
				Stage:       tt.stage,
			}); err != nil {
				t.Fatalf("SavePRState: %v", err)
			}

			c.reconcileReleaseBackfill(context.Background())

			row, err := store.GetPRState("owner/repo", tt.pr.Number)
			if err != nil {
				t.Fatalf("GetPRState: %v", err)
			}
			drained := row == nil
			if drained != tt.wantDrained {
				t.Errorf("drained = %v, want %v (row = %+v)", drained, tt.wantDrained, row)
			}

			var releasedEvents []recordedExecutionEvent
			for _, ev := range mock.executionEvents {
				if ev.executionID == execID && ev.stage == memory.StageReleased {
					releasedEvents = append(releasedEvents, ev)
				}
			}
			if len(releasedEvents) != tt.wantEventCount {
				t.Errorf("released event count = %d, want %d (%+v)", len(releasedEvents), tt.wantEventCount, releasedEvents)
			}
			if tt.wantVersion != "" && len(releasedEvents) > 0 {
				last := releasedEvents[len(releasedEvents)-1]
				if !tt.preExistingEvent && !strings.Contains(last.detail, tt.wantVersion) {
					t.Errorf("event detail = %q, want it to mention version %q", last.detail, tt.wantVersion)
				}
			}
		})
	}
}

// TestReconcileReleaseBackfill_SkipsNonCandidateStages verifies the sweep
// never even fetches the PR for a row that isn't StageFailed/StageReleasing —
// e.g. a genuinely in-flight PR mid-CI must never be perturbed by this heal.
func TestReconcileReleaseBackfill_SkipsNonCandidateStages(t *testing.T) {
	var pullFetches int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pulls/") {
			pullFetches++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	for i, stage := range []PRStage{StageWaitingCI, StageCIPassed, StageMerging, StageMerged, StagePostMergeCI, StageAwaitApproval} {
		if err := store.SavePRState("owner/repo", &PRState{
			PRNumber: 200 + i,
			Stage:    stage,
		}); err != nil {
			t.Fatalf("SavePRState(%s): %v", stage, err)
		}
	}

	c.reconcileReleaseBackfill(context.Background())

	if pullFetches != 0 {
		t.Errorf("pull fetches = %d, want 0 — non-candidate stages must never be touched", pullFetches)
	}
}

// alwaysErrorPullServer serves a 500 for every /pulls/ request (any PR
// number) and counts how many were made — the fixture for GH-4919's
// backoff/abandon tests, which need every attempt to fail identically.
func alwaysErrorPullServer(t *testing.T, fetches *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pulls/") {
			*fetches++
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
}

// TestReconcileReleaseBackfill_Backoff covers GH-4919's per-row exponential
// backoff: a row whose GetPullRequest call errors must not be re-attempted
// on the very next tick, the backoff schedule must double from the poll
// interval each consecutive failure, and a due row (clock has reached
// nextRetryAt) must be retried exactly on schedule.
func TestReconcileReleaseBackfill_Backoff(t *testing.T) {
	var pullFetches int
	server := alwaysErrorPullServer(t, &pullFetches)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 300,
		PRURL:    "https://github.com/owner/repo/pull/300",
		Stage:    StageFailed,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	base := cfg.CIPollInterval // 30s default — releaseBackfillObserveFailure's backoff base
	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	c.releaseBackfillClock = func() time.Time { return clock }

	// Tick 1: first attempt, first failure. backoff = base.
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Fatalf("after tick 1: pull fetches = %d, want 1", pullFetches)
	}

	// Tick 2: same instant — row must be skipped without any API call.
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Fatalf("after tick 2 (still in backoff): pull fetches = %d, want 1", pullFetches)
	}

	// Just short of the first backoff deadline — still skipped.
	clock = clock.Add(base - time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Fatalf("after tick 3 (1s short of backoff): pull fetches = %d, want 1", pullFetches)
	}

	// Backoff elapsed — due again. Second failure doubles backoff to 2*base.
	clock = clock.Add(time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 2 {
		t.Fatalf("after tick 4 (backoff elapsed): pull fetches = %d, want 2", pullFetches)
	}

	// Just short of the doubled deadline — still skipped.
	clock = clock.Add(2*base - time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 2 {
		t.Fatalf("after tick 5 (1s short of doubled backoff): pull fetches = %d, want 2", pullFetches)
	}

	// Doubled backoff elapsed — third failure.
	clock = clock.Add(time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 3 {
		t.Fatalf("after tick 6 (doubled backoff elapsed): pull fetches = %d, want 3", pullFetches)
	}

	row, err := store.GetPRState("owner/repo", 300)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row == nil {
		t.Fatal("row unexpectedly drained")
	}
	if row.ReleaseBackfillAbandoned {
		t.Error("row abandoned prematurely — only 3 of the 10-failure threshold observed")
	}
}

// TestReconcileReleaseBackfill_BackoffResetsOnSuccess covers the "resets on
// success" half of GH-4919's backoff contract: once an API call succeeds
// (even if the row is genuinely still unreleased), the very next tick must
// not be held back by a stale backoff from an earlier failure.
func TestReconcileReleaseBackfill_BackoffResetsOnSuccess(t *testing.T) {
	var pullFetches int
	failFirst := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pullFetches++
		if failFirst {
			failFirst = false
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(&github.PullRequest{Number: 301, Merged: false})
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 301,
		PRURL:    "https://github.com/owner/repo/pull/301",
		Stage:    StageFailed,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	c.releaseBackfillClock = func() time.Time { return clock }

	// Tick 1: fails, backoff scheduled.
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Fatalf("after tick 1: pull fetches = %d, want 1", pullFetches)
	}

	// Advance past the backoff and succeed — this must clear the streak.
	clock = clock.Add(cfg.CIPollInterval)
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 2 {
		t.Fatalf("after tick 2 (success): pull fetches = %d, want 2", pullFetches)
	}

	// Immediately due again (no clock advance) — a stale backoff would skip this.
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 3 {
		t.Fatalf("after tick 3 (post-success, same instant): pull fetches = %d, want 3 — backoff should have reset on success", pullFetches)
	}
}

// TestReconcileReleaseBackfill_AbandonsAfterThreshold covers GH-4919's
// permanent-failure classification: a row must cross BOTH the consecutive-
// failure count and the minimum wall-clock window before it is marked
// abandoned, the transition must persist (ReleaseBackfillAbandoned + Error),
// and it must happen exactly once — every sweep after the transition makes
// zero further API calls for the row.
func TestReconcileReleaseBackfill_AbandonsAfterThreshold(t *testing.T) {
	var pullFetches int
	server := alwaysErrorPullServer(t, &pullFetches)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 400,
		PRURL:    "https://github.com/owner/repo/pull/400",
		Stage:    StageFailed,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	c.releaseBackfillClock = func() time.Time { return clock }

	// Drive releaseBackfillAbandonThreshold consecutive failures, jumping the
	// clock well past releaseBackfillMaxBackoff between each so every tick is
	// immediately due — this also comfortably clears
	// releaseBackfillAbandonMinWindow well before the threshold count is
	// reached, isolating the count as the binding constraint being exercised.
	for i := 0; i < releaseBackfillAbandonThreshold; i++ {
		if i > 0 {
			clock = clock.Add(releaseBackfillMaxBackoff + time.Minute)
		}
		c.reconcileReleaseBackfill(context.Background())
	}

	if pullFetches != releaseBackfillAbandonThreshold {
		t.Fatalf("pull fetches = %d, want exactly %d (the threshold) — abandon must fire on the crossing tick, not before or after",
			pullFetches, releaseBackfillAbandonThreshold)
	}

	row, err := store.GetPRState("owner/repo", 400)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row == nil {
		t.Fatal("row unexpectedly drained — abandon must persist the row, not remove it")
	}
	if !row.ReleaseBackfillAbandoned {
		t.Fatal("row not marked abandoned after crossing the threshold")
	}
	if row.Error == "" {
		t.Error("abandoned row's Error is empty — reason must be persisted")
	}
	if row.Stage != StageFailed {
		t.Errorf("abandoned row Stage = %q, want unchanged %q", row.Stage, StageFailed)
	}

	// Further sweeps — even ones due by the clock — must never call the API
	// for this row again: the persisted flag is now authoritative.
	for i := 0; i < 3; i++ {
		clock = clock.Add(releaseBackfillMaxBackoff + time.Minute)
		c.reconcileReleaseBackfill(context.Background())
	}
	if pullFetches != releaseBackfillAbandonThreshold {
		t.Errorf("pull fetches after abandonment = %d, want unchanged %d — abandoned row must never be retried",
			pullFetches, releaseBackfillAbandonThreshold)
	}
}

// TestReconcileReleaseBackfill_ShortIncidentDoesNotAbandon covers the window
// half of GH-4919's abandon gate: a failure streak that crosses the
// consecutive-failure count quickly (a burst of retries within a short span,
// not a sustained multi-hour incident) must NOT be classified permanent —
// this is the guard that stops a same-day platform incident from
// terminalizing a row that would have healed once the incident cleared.
func TestReconcileReleaseBackfill_ShortIncidentDoesNotAbandon(t *testing.T) {
	var pullFetches int
	server := alwaysErrorPullServer(t, &pullFetches)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 401,
		PRURL:    "https://github.com/owner/repo/pull/401",
		Stage:    StageFailed,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	clock := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	// Directly seed a failure streak one short of the count threshold, but
	// with firstFailAt only a minute old — nowhere near
	// releaseBackfillAbandonMinWindow. Exercises releaseBackfillObserveFailure
	// without needing releaseBackfillAbandonThreshold real sweeps.
	c.releaseBackfillRows = map[string]*releaseBackfillRowState{
		releaseBackfillRowKey("owner", "repo", 401): {
			consecutiveFails: releaseBackfillAbandonThreshold - 1,
			firstFailAt:      clock.Add(-time.Minute),
			nextRetryAt:      clock,
		},
	}
	c.releaseBackfillClock = func() time.Time { return clock }

	// One more failure crosses the count threshold but not the window.
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Fatalf("pull fetches = %d, want 1", pullFetches)
	}

	row, err := store.GetPRState("owner/repo", 401)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row.ReleaseBackfillAbandoned {
		t.Error("row abandoned on a short (1-minute) failure streak — the minimum wall-clock window must gate this")
	}
}

// TestReconcileReleaseBackfill_AbandonedRowSkipped covers a row that was
// already marked abandoned by a prior sweep (e.g. before a daemon restart):
// the very next sweep must make zero API calls for it.
func TestReconcileReleaseBackfill_AbandonedRowSkipped(t *testing.T) {
	var pullFetches int
	server := alwaysErrorPullServer(t, &pullFetches)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber:                 402,
		PRURL:                    "https://github.com/owner/repo/pull/402",
		Stage:                    StageFailed,
		Error:                    "release-backfill: abandoned after 10 consecutive API failures",
		ReleaseBackfillAbandoned: true,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	c.reconcileReleaseBackfill(context.Background())

	if pullFetches != 0 {
		t.Errorf("pull fetches = %d, want 0 — an already-abandoned row must never be retried", pullFetches)
	}
}

// TestReconcileReleaseBackfill_BreakerOpenSkipsSweep covers GH-4919's
// breaker gate: the TASK-458 platform-outage breaker being open must make
// the ENTIRE sweep a no-op for that tick — no row is fetched, healed, or
// have its backoff/failure state touched, regardless of stage or prior
// history.
func TestReconcileReleaseBackfill_BreakerOpenSkipsSweep(t *testing.T) {
	var pullFetches int
	server := alwaysErrorPullServer(t, &pullFetches)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	c.platformBreaker = &PlatformBreaker{open: true}

	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)

	if err := store.SavePRState("owner/repo", &PRState{
		PRNumber: 403,
		PRURL:    "https://github.com/owner/repo/pull/403",
		Stage:    StageFailed,
	}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	c.reconcileReleaseBackfill(context.Background())

	if pullFetches != 0 {
		t.Errorf("pull fetches = %d, want 0 — sweep must be a no-op while the platform breaker is open", pullFetches)
	}

	row, err := store.GetPRState("owner/repo", 403)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row.ReleaseBackfillAbandoned {
		t.Error("row abandoned despite the breaker-gated sweep never running")
	}

	// Once the breaker closes, the row must behave exactly as if nothing
	// happened — normal sweep resumes.
	c.platformBreaker = &PlatformBreaker{open: false}
	c.reconcileReleaseBackfill(context.Background())
	if pullFetches != 1 {
		t.Errorf("pull fetches after breaker closed = %d, want 1", pullFetches)
	}
}
