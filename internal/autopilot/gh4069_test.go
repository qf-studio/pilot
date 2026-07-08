package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4069/TASK-391: pilot_prs_conflicting_total had zero production call
// sites — RecordPRConflicting was only exercised by metrics_test.go, so the
// counter never moved regardless of how many PRs hit merge conflicts.
// handleMergeConflict now records the metric once per PR-conflict event.

// TestController_HandleMergeConflict_RecordsConflictMetricOnce verifies that
// entering the merge-conflict path increments pilot_prs_conflicting_total
// exactly once, and that re-entering the same path for the same PR (e.g. on
// a subsequent poll tick before the conflict is resolved) does not
// double-count.
func TestController_HandleMergeConflict_RecordsConflictMetricOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/55/update-branch" && r.Method == http.MethodPut:
			// 422: true conflict, auto-rebase cannot resolve it.
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    55,
		PRURL:       "https://github.com/owner/repo/pull/55",
		IssueNumber: 20,
		BranchName:  "pilot/GH-20",
		Stage:       StageMerging,
		CreatedAt:   time.Now(),
	}

	ctx := context.Background()
	if err := c.handleMergeConflict(ctx, prState); err != nil {
		t.Fatalf("handleMergeConflict (1st call) returned error: %v", err)
	}
	if err := c.handleMergeConflict(ctx, prState); err != nil {
		t.Fatalf("handleMergeConflict (2nd call, re-entry) returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsConflicting != 1 {
		t.Errorf("PRsConflicting = %d, want 1 (re-entry for the same PR must not double-count)", snap.PRsConflicting)
	}
	if !prState.ConflictRecorded {
		t.Error("ConflictRecorded should be true after handleMergeConflict runs")
	}
}

// TestController_HandleMergeConflict_RecordsConflictMetricPerPR verifies that
// distinct PRs each entering the merge-conflict path are counted
// independently (the guard is per-PR, not global).
func TestController_HandleMergeConflict_RecordsConflictMetricPerPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/56/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		case r.URL.Path == "/repos/owner/repo/pulls/57/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prA := &PRState{PRNumber: 56, PRURL: "https://github.com/owner/repo/pull/56", IssueNumber: 21, BranchName: "pilot/GH-21", Stage: StageMerging, CreatedAt: time.Now()}
	prB := &PRState{PRNumber: 57, PRURL: "https://github.com/owner/repo/pull/57", IssueNumber: 22, BranchName: "pilot/GH-22", Stage: StageMerging, CreatedAt: time.Now()}

	ctx := context.Background()
	if err := c.handleMergeConflict(ctx, prA); err != nil {
		t.Fatalf("handleMergeConflict(prA) returned error: %v", err)
	}
	if err := c.handleMergeConflict(ctx, prB); err != nil {
		t.Fatalf("handleMergeConflict(prB) returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsConflicting != 2 {
		t.Errorf("PRsConflicting = %d, want 2 (one per distinct PR)", snap.PRsConflicting)
	}
}
