package autopilot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestController_ParkForStackedSuperset_HoldsLabelsAlertsAndComments is the
// GH-5031 regression: on a positive detectStackedSuperset result,
// parkForStackedSuperset must reuse the parkForBaseMismatch pattern verbatim
// — hold (Parked=true), apply labelParkedAwaitingApproval, fire exactly one
// escalation alert, and post exactly one PR comment naming the base PR with
// "stacked on open PR #<N> — merge that first" wording. A second call for
// the SAME stack relationship must stay quiet (idempotent), mirroring
// TestController_ParkForBaseMismatch_DistinctFromMisconfigPark.
func TestController_ParkForStackedSuperset_HoldsLabelsAlertsAndComments(t *testing.T) {
	var (
		commentPosted atomic.Int32
		labelApplied  atomic.Int32
		commentBody   atomic.Value
	)
	commentBody.Store("")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/117/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			if commentPosted.Load() == 0 {
				_, _ = w.Write([]byte("[]"))
			} else {
				_, _ = w.Write([]byte(`[{"id":1,"body":"` + stackedSupersetCommentMarker + `\nposted"}]`))
			}
		case r.URL.Path == "/repos/owner/repo/issues/117/comments" && r.Method == http.MethodPost:
			body, _ := readRequestBody(r)
			commentBody.Store(body)
			commentPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		case r.URL.Path == "/repos/owner/repo/issues/74/labels" && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber:    117,
		IssueNumber: 74,
		Stage:       StageMerging,
	}
	stackedOn := &PRState{PRNumber: 116}

	c.parkForStackedSuperset(context.Background(), prState, stackedOn)

	if !prState.Parked {
		t.Error("Parked should be true after a positive stacked-superset detection")
	}
	if !strings.HasPrefix(prState.EscalationReason, stackedSupersetReasonPrefix) {
		t.Errorf("EscalationReason = %q, want prefix %q", prState.EscalationReason, stackedSupersetReasonPrefix)
	}
	if labelApplied.Load() != 1 {
		t.Errorf("parked-awaiting-approval label applied %d times, want 1", labelApplied.Load())
	}
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times, want 1", commentPosted.Load())
	}
	if body, _ := commentBody.Load().(string); !strings.Contains(body, "stacked on open PR #116") || !strings.Contains(body, "merge that first") {
		t.Errorf("comment body = %q, want it to name PR #116 with 'stacked on open PR #N — merge that first' wording", body)
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}

	// A second call for the SAME stack relationship must stay quiet (idempotent).
	c.parkForStackedSuperset(context.Background(), prState, stackedOn)
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times after repeat call, want 1 (idempotent)", commentPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times after repeat call, want 1 (idempotent)", len(sink.events))
	}
}

// TestController_HandleMerging_StackedSuperset_HoldsInsteadOfMerging is the
// GH-5031 end-to-end wiring check: two open PRs where #17's head descends
// from #16's still-open head (both base==main). Driving #17 through
// handleMerging via ProcessPR must NOT call MergePR — it must hold via
// parkForStackedSuperset instead, naming #16 in the PR comment.
func TestController_HandleMerging_StackedSuperset_HoldsInsteadOfMerging(t *testing.T) {
	local := newFixtureRepo(t)
	ctx := context.Background()

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-16")
	writeFixtureFile(t, local, "base.txt", "from base PR\n")
	runFixtureGit(t, local, "add", "base.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-16 work")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-16")
	baseSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	runFixtureGit(t, local, "checkout", "-b", "pilot/GH-17")
	writeFixtureFile(t, local, "stacked.txt", "from stacked PR\n")
	runFixtureGit(t, local, "add", "stacked.txt")
	runFixtureGit(t, local, "commit", "-m", "GH-17 work, stacked on GH-16")
	runFixtureGit(t, local, "push", "origin", "pilot/GH-17")
	stackedSHA := strings.TrimSpace(runFixtureGit(t, local, "rev-parse", "HEAD"))

	var mergeCalled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/17/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/issues/17/comments" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath(local))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[16] = &PRState{
		PRNumber:     16,
		BranchName:   "pilot/GH-16",
		HeadSHA:      baseSHA,
		TargetBranch: "main",
		Stage:        StageWaitingCI,
		CreatedAt:    time.Now(),
	}
	c.activePRs[17] = &PRState{
		PRNumber:     17,
		IssueNumber:  117,
		BranchName:   "pilot/GH-17",
		HeadSHA:      stackedSHA,
		TargetBranch: "main",
		Stage:        StageMerging,
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number: 17,
		Head:   github.PRRef{SHA: stackedSHA},
		Base:   github.PRRef{Ref: "main"},
	}
	if err := c.ProcessPR(ctx, 17, ghPR); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}

	if mergeCalled.Load() != 0 {
		t.Errorf("MergePR called %d times, want 0 — a PR stacked on another open PR must be held", mergeCalled.Load())
	}
	pr, ok := c.GetPRState(17)
	if !ok {
		t.Fatal("PR 17 should still be tracked")
	}
	if pr.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (still held)", pr.Stage, StageMerging)
	}
	if !pr.Parked {
		t.Error("Parked should be true")
	}
	if !strings.Contains(pr.EscalationReason, "PR #16") {
		t.Errorf("EscalationReason = %q, want it to name PR #16", pr.EscalationReason)
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}
}

func readRequestBody(r *http.Request) (string, error) {
	b, err := io.ReadAll(r.Body)
	return string(b), err
}
