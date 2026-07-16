package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
