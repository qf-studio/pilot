package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// releaseBackfillCounts tallies the API traffic releaseBackfillServer sees so
// tests can pin how many CompareStatus and tag-listing requests a lookup cost
// (GH-5658). A nil *releaseBackfillCounts is allowed and counts nothing.
type releaseBackfillCounts struct {
	compares atomic.Int32
	// tagRequests counts /tags page requests (not ListTags calls).
	tagRequests atomic.Int32
}

// releaseBackfillServer serves the three endpoints GH-4370's reconciliation
// needs: GetPullRequest (merge status + merge commit SHA), ListTags, and
// CompareStatus (ancestry). compareStatus is keyed "base...head" exactly as
// CompareStatus builds its request path.
func releaseBackfillServer(t *testing.T, prs map[int]*github.PullRequest, tags []releaseBackfillFakeTag, compareStatus map[string]string, counts *releaseBackfillCounts) *httptest.Server {
	t.Helper()
	return releaseBackfillServerDyn(t, prs, func() []releaseBackfillFakeTag { return tags }, compareStatus, counts)
}

// releaseBackfillServerDyn is releaseBackfillServer with the tag list read per
// request, so a test can publish a new tag between sweep passes.
func releaseBackfillServerDyn(t *testing.T, prs map[int]*github.PullRequest, tagsFn func() []releaseBackfillFakeTag, compareStatus map[string]string, counts *releaseBackfillCounts) *httptest.Server {
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
			if counts != nil {
				counts.tagRequests.Add(1)
			}
			tags := tagsFn()
			// Honour per_page/page like GitHub: the SDK's ListTags keeps
			// paging until it sees a short page.
			perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if perPage <= 0 {
				perPage = len(tags)
			}
			if page <= 0 {
				page = 1
			}
			lo := min((page-1)*perPage, len(tags))
			hi := min(lo+perPage, len(tags))
			out := make([]*github.Tag, 0, hi-lo)
			for _, tag := range tags[lo:hi] {
				gt := &github.Tag{Name: tag.name}
				gt.Commit.SHA = tag.sha
				out = append(out, gt)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/compare/"):
			if counts != nil {
				counts.compares.Add(1)
			}
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
			server := releaseBackfillServer(t, prs, tt.tags, tt.compareStatus, nil)
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

// releaseBackfillCappedFixture seeds n StageReleasing rows (PR numbers 1..n) in
// a state store and serves every PR as open-and-unmerged through a server that
// records which PR numbers were fetched.
func releaseBackfillCappedFixture(t *testing.T, n int) (c *Controller, fetched func() map[int]int) {
	t.Helper()

	var mu sync.Mutex
	hits := map[int]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/pulls/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var num int
		if _, err := fmt.Sscanf(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], "%d", &num); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		hits[num]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(&github.PullRequest{Number: num, Merged: false})
	}))
	t.Cleanup(server.Close)

	c = NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)
	for i := 1; i <= n; i++ {
		if err := store.SavePRState("owner/repo", &PRState{
			PRNumber:    i,
			PRURL:       fmt.Sprintf("https://github.com/owner/repo/pull/%d", i),
			IssueNumber: i,
			Stage:       StageReleasing,
		}); err != nil {
			t.Fatalf("SavePRState: %v", err)
		}
	}

	return c, func() map[int]int {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[int]int, len(hits))
		for k, v := range hits {
			out[k] = v
		}
		return out
	}
}

// TestReconcileReleaseBackfill_RowCapPerPass pins GH-5657's per-pass row cap: a
// pass over 20 eligible rows touches at most maxReleaseBackfillRowsPerPass, and
// the window rotates so every row is eventually visited (a clean fetch of an
// unmerged PR clears its backoff, so rows stay due and would otherwise hold the
// first slots forever).
func TestReconcileReleaseBackfill_RowCapPerPass(t *testing.T) {
	const rows = 20
	c, fetched := releaseBackfillCappedFixture(t, rows)

	c.reconcileReleaseBackfill(context.Background())

	first := fetched()
	total := 0
	for _, n := range first {
		total += n
	}
	if total > maxReleaseBackfillRowsPerPass {
		t.Fatalf("one pass fetched %d rows (%v), want at most %d", total, first, maxReleaseBackfillRowsPerPass)
	}
	if total != maxReleaseBackfillRowsPerPass {
		t.Fatalf("one pass fetched %d rows, want exactly %d with %d eligible", total, maxReleaseBackfillRowsPerPass, rows)
	}

	for i := 1; i < rows/maxReleaseBackfillRowsPerPass; i++ {
		c.reconcileReleaseBackfill(context.Background())
	}
	seen := fetched()
	if len(seen) != rows {
		t.Fatalf("after %d passes %d distinct rows were visited, want all %d (window must rotate): %v",
			rows/maxReleaseBackfillRowsPerPass, len(seen), rows, seen)
	}
}

// TestReconcileReleaseBackfill_StopsOnCancelledContext pins the ctx.Err() check
// at the top of each row iteration: with the context already done the sweep must
// not start any row. A row attempted on a dead ctx fails its fetch and would
// leave backoff bookkeeping behind.
func TestReconcileReleaseBackfill_StopsOnCancelledContext(t *testing.T) {
	c, fetched := releaseBackfillCappedFixture(t, 20)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.reconcileReleaseBackfill(ctx)

	if got := fetched(); len(got) != 0 {
		t.Fatalf("cancelled ctx: %d rows reached the server (%v), want 0", len(got), got)
	}
	c.releaseBackfillMu.Lock()
	tracked := len(c.releaseBackfillRows)
	c.releaseBackfillMu.Unlock()
	if tracked != 0 {
		t.Fatalf("cancelled ctx: %d rows were attempted (failure bookkeeping recorded), want 0", tracked)
	}
}

// backfillTagFixture builds n semver tags v1.0.0..v1.(n-1).0 served
// newest-first (like GitHub's tags endpoint). shaFor(i) is the commit SHA of
// the i-th oldest tag.
func backfillTagFixture(n int) []releaseBackfillFakeTag {
	tags := make([]releaseBackfillFakeTag, 0, n)
	for i := n - 1; i >= 0; i-- {
		tags = append(tags, releaseBackfillFakeTag{name: backfillTagName(i), sha: backfillTagSHA(i)})
	}
	return tags
}

func backfillTagName(i int) string { return fmt.Sprintf("v1.%d.0", i) }
func backfillTagSHA(i int) string  { return fmt.Sprintf("tagsha%05d", i) }

// backfillCompareKey is the compare path key for sha against the i-th oldest tag.
func backfillCompareKey(sha string, i int) string { return sha + "..." + backfillTagSHA(i) }

// TestEarliestReleaseTagContaining pins GH-5658: finding the earliest
// containing tag must be a newest-first probe plus binary search under a
// per-row compare budget, not a linear walk from the oldest tag (903 compares
// held the controller tick for 57 minutes on 2026-10-13).
func TestEarliestReleaseTagContaining(t *testing.T) {
	const sha = "targetsha"

	newController := func(t *testing.T, n int, status map[string]string) (*Controller, *releaseBackfillCounts) {
		t.Helper()
		counts := &releaseBackfillCounts{}
		server := releaseBackfillServer(t, nil, backfillTagFixture(n), status, counts)
		t.Cleanup(server.Close)
		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		return NewController(DefaultConfig(), ghClient, nil, "owner", "repo"), counts
	}

	// containedFrom marks tags first..n-1 as containing sha ("ahead") and the
	// older ones "behind", as on a linear default branch.
	containedFrom := func(n, first int) map[string]string {
		status := make(map[string]string, n)
		for i := 0; i < n; i++ {
			if i >= first {
				status[backfillCompareKey(sha, i)] = "ahead"
			} else {
				status[backfillCompareKey(sha, i)] = "behind"
			}
		}
		return status
	}

	t.Run("ShaInNoTagCostsOneCompare", func(t *testing.T) {
		const n = 900
		c, counts := newController(t, n, containedFrom(n, n)) // no tag contains sha
		got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("tag = %q, want none", got)
		}
		if n := counts.compares.Load(); n != 1 {
			t.Errorf("compares = %d, want exactly 1", n)
		}
	})

	t.Run("ShaInMiddleTagResolvesWithinBudget", func(t *testing.T) {
		const n, first = 900, 450
		c, counts := newController(t, n, containedFrom(n, first))
		got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := backfillTagName(first); got != want {
			t.Errorf("tag = %q, want %q", got, want)
		}
		if n := counts.compares.Load(); n > maxReleaseBackfillCompares {
			t.Errorf("compares = %d, want <= %d", n, maxReleaseBackfillCompares)
		}
	})

	t.Run("ShaInNewestTagOnly", func(t *testing.T) {
		const n = 5
		status := containedFrom(n, n-1)
		status[backfillCompareKey(sha, n-1)] = "identical"
		c, counts := newController(t, n, status)
		got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := backfillTagName(n - 1); got != want {
			t.Errorf("tag = %q, want %q", got, want)
		}
		if n := counts.compares.Load(); n > maxReleaseBackfillCompares {
			t.Errorf("compares = %d, want <= %d", n, maxReleaseBackfillCompares)
		}
	})

	t.Run("DivergedNewestFallsBackToCappedScan", func(t *testing.T) {
		const n = 10
		t.Run("hit within the newest tags", func(t *testing.T) {
			// Newest tag has no entry -> the server answers "diverged".
			status := map[string]string{
				backfillCompareKey(sha, 5): "behind",
				backfillCompareKey(sha, 6): "behind",
				backfillCompareKey(sha, 7): "ahead",
				backfillCompareKey(sha, 8): "ahead",
			}
			c, counts := newController(t, n, status)
			got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if want := backfillTagName(7); got != want {
				t.Errorf("tag = %q, want %q (earliest containing among the scanned tags)", got, want)
			}
			if c := counts.compares.Load(); c > releaseBackfillDivergedScan {
				t.Errorf("compares = %d, want <= %d", c, releaseBackfillDivergedScan)
			}
		})
		t.Run("hit beyond the cap is not scanned", func(t *testing.T) {
			status := map[string]string{backfillCompareKey(sha, 2): "ahead"}
			c, counts := newController(t, n, status)
			got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != "" {
				t.Errorf("tag = %q, want none (tag 2 is outside the %d-tag scan)", got, releaseBackfillDivergedScan)
			}
			if c := counts.compares.Load(); c != releaseBackfillDivergedScan {
				t.Errorf("compares = %d, want exactly %d", c, releaseBackfillDivergedScan)
			}
		})
	})

	t.Run("BudgetExceededReturnsError", func(t *testing.T) {
		// 5000 tags with the sha in all of them: the search must walk down to
		// tag 0, which needs 1 probe + 13 halvings > maxReleaseBackfillCompares.
		const n = 5000
		c, counts := newController(t, n, containedFrom(n, 0))
		got, err := c.earliestReleaseTagContaining(context.Background(), "owner", "repo", sha)
		if err == nil {
			t.Fatalf("expected a budget error, got tag %q", got)
		}
		if !strings.Contains(err.Error(), "compare budget") {
			t.Errorf("error = %v, want it to mention the compare budget", err)
		}
		if n := counts.compares.Load(); n != maxReleaseBackfillCompares {
			t.Errorf("compares = %d, want exactly the budget (%d)", n, maxReleaseBackfillCompares)
		}
	})
}

// TestReconcileReleaseBackfill_TagListFetchedOncePerPass pins GH-5658: every
// candidate row in one sweep shares a single tag listing instead of
// refetching it per row.
func TestReconcileReleaseBackfill_TagListFetchedOncePerPass(t *testing.T) {
	prs := map[int]*github.PullRequest{}
	const rows = 4
	status := map[string]string{}
	for i := 0; i < rows; i++ {
		num := 300 + i
		sha := fmt.Sprintf("sha%d", num)
		prs[num] = &github.PullRequest{Number: num, Merged: true, MergeCommitSHA: sha}
		// Merged but in no tag: newest-tag probe says "behind".
		status[backfillCompareKey(sha, 2)] = "behind"
	}
	counts := &releaseBackfillCounts{}
	server := releaseBackfillServer(t, prs, backfillTagFixture(3), status, counts)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)
	for num := range prs {
		if err := store.SavePRState("owner/repo", &PRState{PRNumber: num, Stage: StageReleasing}); err != nil {
			t.Fatalf("SavePRState: %v", err)
		}
	}

	c.reconcileReleaseBackfill(context.Background())

	// 3 tags fit in one page, so one ListTags call == one /tags request.
	if n := counts.tagRequests.Load(); n != 1 {
		t.Errorf("tag list requests = %d, want 1 for the whole pass (%d rows)", n, rows)
	}
	if n := counts.compares.Load(); n != rows {
		t.Errorf("compares = %d, want %d (one newest-tag probe per row)", n, rows)
	}
}

// untaggedBackfillController builds a controller with one merged row (PR 300)
// whose merge commit is in no tag, on an injectable clock.
func untaggedBackfillController(t *testing.T, clock *time.Time) (*Controller, *releaseBackfillCounts) {
	t.Helper()
	const sha = "sha300"
	prs := map[int]*github.PullRequest{300: {Number: 300, Merged: true, MergeCommitSHA: sha}}
	status := map[string]string{backfillCompareKey(sha, 2): "behind"}
	counts := &releaseBackfillCounts{}
	server := releaseBackfillServer(t, prs, backfillTagFixture(3), status, counts)
	t.Cleanup(server.Close)

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)
	if err := store.SavePRState("owner/repo", &PRState{PRNumber: 300, Stage: StageReleasing}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	c.releaseBackfillClock = func() time.Time { return *clock }
	return c, counts
}

// TestReconcileReleaseBackfill_TagListReusedAcrossPassesWithinTTL pins GH-5665:
// the tag listing survives across passes for releaseBackfillTagTTL.
func TestReconcileReleaseBackfill_TagListReusedAcrossPassesWithinTTL(t *testing.T) {
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c, counts := untaggedBackfillController(t, &clock)

	c.reconcileReleaseBackfill(context.Background())
	if n := counts.tagRequests.Load(); n != 1 {
		t.Fatalf("after pass 1: tag requests = %d, want 1", n)
	}

	// Past the row's first back-off (30s) but well inside the TTL.
	clock = clock.Add(time.Minute)
	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 2 {
		t.Fatalf("after pass 2: compares = %d, want 2 (row must have been attempted again)", n)
	}
	if n := counts.tagRequests.Load(); n != 1 {
		t.Errorf("after pass 2 (inside TTL): tag requests = %d, want 1", n)
	}

	// Past the TTL (row back-off has doubled to 1m, long elapsed).
	clock = clock.Add(releaseBackfillTagTTL)
	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 3 {
		t.Fatalf("after pass 3: compares = %d, want 3", n)
	}
	if n := counts.tagRequests.Load(); n != 2 {
		t.Errorf("after pass 3 (past TTL): tag requests = %d, want 2", n)
	}
}

// TestReconcileReleaseBackfill_NoTagRowBacksOff pins GH-5665: a merged row in
// no tag is backed off, not re-attempted on the very next pass.
func TestReconcileReleaseBackfill_NoTagRowBacksOff(t *testing.T) {
	clock := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c, counts := untaggedBackfillController(t, &clock)
	base := c.config.CIPollInterval

	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 1 {
		t.Fatalf("after pass 1: compares = %d, want 1", n)
	}

	// Next pass 1s short of the back-off: not due, zero API calls.
	clock = clock.Add(base - time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 1 {
		t.Fatalf("after pass 2 (in back-off): compares = %d, want 1", n)
	}

	// Back-off elapsed: due again.
	clock = clock.Add(time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 2 {
		t.Fatalf("after pass 3 (back-off elapsed): compares = %d, want 2", n)
	}

	// Streak doubles: 2*base before the next attempt.
	clock = clock.Add(2*base - time.Second)
	c.reconcileReleaseBackfill(context.Background())
	if n := counts.compares.Load(); n != 2 {
		t.Fatalf("after pass 4 (doubled back-off): compares = %d, want 2", n)
	}
}

// TestReconcileReleaseBackfill_NoTagRowNeverAbandoned pins GH-5671: a merged
// row in no tag backs off but never counts toward abandonment (no API call
// failed), so it is still healed once a containing tag is published, however
// long that takes.
func TestReconcileReleaseBackfill_NoTagRowNeverAbandoned(t *testing.T) {
	const (
		sha    = "sha300"
		passes = 14
	)
	var tagsMu sync.Mutex
	tags := backfillTagFixture(3)
	tagsFn := func() []releaseBackfillFakeTag {
		tagsMu.Lock()
		defer tagsMu.Unlock()
		return append([]releaseBackfillFakeTag(nil), tags...)
	}
	prs := map[int]*github.PullRequest{300: {Number: 300, Merged: true, MergeCommitSHA: sha}}
	status := map[string]string{
		backfillCompareKey(sha, 0): "behind",
		backfillCompareKey(sha, 1): "behind",
		backfillCompareKey(sha, 2): "behind",
		backfillCompareKey(sha, 3): "ahead",
	}
	counts := &releaseBackfillCounts{}
	server := releaseBackfillServerDyn(t, prs, tagsFn, status, counts)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	store, err := NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	c.SetStateStore(store)
	mock := &mockApprovalPersister{execByTask: map[string]string{"GH-300": "exec-300"}}
	c.memoryStore = mock
	if err := store.SavePRState("owner/repo", &PRState{PRNumber: 300, IssueNumber: 300, Stage: StageReleasing}); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}

	start := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := start
	c.releaseBackfillClock = func() time.Time { return clock }

	for i := 1; i <= passes; i++ {
		before := counts.compares.Load()
		c.reconcileReleaseBackfill(context.Background())
		if counts.compares.Load() == before {
			t.Fatalf("pass %d at +%s: row was not attempted (abandoned or still backed off)", i, clock.Sub(start))
		}
		row, err := store.GetPRState("owner/repo", 300)
		if err != nil || row == nil {
			t.Fatalf("pass %d: GetPRState = %v, %v; want the row to remain", i, row, err)
		}
		if row.ReleaseBackfillAbandoned || row.Error != "" {
			t.Fatalf("pass %d at +%s: abandoned=%v error=%q, want a live row with no error",
				i, clock.Sub(start), row.ReleaseBackfillAbandoned, row.Error)
		}
		// The longest back-off is releaseBackfillMaxBackoff, so this step always
		// lands on a due pass.
		clock = clock.Add(releaseBackfillMaxBackoff)
	}
	if span := clock.Sub(start) - releaseBackfillMaxBackoff; span <= 7*time.Hour {
		t.Fatalf("passes spanned %s, want > 7h", span)
	}

	// A tag containing the merge commit is published; the next due pass heals.
	tagsMu.Lock()
	tags = append([]releaseBackfillFakeTag{{name: backfillTagName(3), sha: backfillTagSHA(3)}}, tags...)
	tagsMu.Unlock()
	c.reconcileReleaseBackfill(context.Background())

	row, err := store.GetPRState("owner/repo", 300)
	if err != nil {
		t.Fatalf("GetPRState: %v", err)
	}
	if row != nil {
		t.Errorf("row not drained after the tag was published: %+v", row)
	}
	released := 0
	for _, ev := range mock.executionEvents {
		if ev.executionID == "exec-300" && ev.stage == memory.StageReleased {
			released++
			if !strings.Contains(ev.detail, backfillTagName(3)) {
				t.Errorf("released event detail = %q, want it to mention %s", ev.detail, backfillTagName(3))
			}
		}
	}
	if released != 1 {
		t.Errorf("released events = %d, want 1", released)
	}
}
