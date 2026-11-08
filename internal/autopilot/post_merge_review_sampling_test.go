package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/testutil"
	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// pmrNumberReviewer is a PostMergeReviewer that records the PR number of every
// review it is asked to run.
type pmrNumberReviewer struct {
	mu  sync.Mutex
	prs []int
}

func (f *pmrNumberReviewer) Review(_ context.Context, in executor.PostMergeReviewInput) (*executor.PostMergeReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prs = append(f.prs, in.PRNumber)
	return &executor.PostMergeReview{Verdict: executor.PostMergeApprove}, nil
}

func (f *pmrNumberReviewer) reviewed() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.prs...)
}

// prePRBody renders a PR body carrying a "Pre-PR review" section.
func prePRBody(mode, verdict, fix string) string {
	return fmt.Sprintf("## Summary\nx\n\n## Pre-PR review\n\nMode: %s\nVerdict: %s\nFix: %s\n", mode, verdict, fix)
}

// newPMRSamplingGitHub serves PR bodies per PR number; a PR missing from bodies
// answers 500, so fetching its body fails.
func newPMRSamplingGitHub(t *testing.T, bodies map[int]string) *github.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/pulls/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(path, "/repos/owner/repo/pulls/"))
			body, ok := bodies[n]
			if !ok {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: n, State: "closed", Merged: true, MergeCommitSHA: "mergesha1",
				Base: github.PRRef{Ref: "main"}, Body: body,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/issues/"):
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 10, Title: "feat(x): do the thing", Body: "Implement the thing.", State: "closed"})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/comments"):
			_ = json.NewEncoder(w).Encode([]github.Comment{})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Comment{ID: 1})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(server.Close)
	return github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
}

// runPMRSampling merges each PR through handleMerged on a controller with the
// given sample rate and daily cap and returns the PR numbers that were
// reviewed, the budget and the controller.
func runPMRSampling(t *testing.T, rate, dailyCap int, bodies map[int]string, prs []int) ([]int, *PostMergeReviewBudget, *Controller) {
	t.Helper()
	reviewer := &pmrNumberReviewer{}
	c := newPMRController(t, newPMRSamplingGitHub(t, bodies), reviewer, true)
	c.config.PostMergeReview.SampleRate = rate
	budget := NewPostMergeReviewBudget(dailyCap)
	c.SetPostMergeReviewBudget(budget)
	for _, n := range prs {
		if err := c.handleMerged(context.Background(), mergedPRState(n, 10)); err != nil {
			t.Fatalf("handleMerged PR %d: %v", n, err)
		}
	}
	c.waitPostMergeReviews()
	return reviewer.reviewed(), budget, c
}

func pmrRange(from, to int) []int {
	var out []int
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func pmrSorted(got []int) []int {
	out := append([]int(nil), got...)
	sort.Ints(out)
	return out
}

// pmrUsed reads how many reviews today's budget has granted.
func pmrUsed(b *PostMergeReviewBudget) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

func TestPostMergeReviewSampling_OneInN(t *testing.T) {
	cleared := prePRBody(executor.PrePRReviewModeEnforce, string(executor.PostMergeApprove), executor.PrePRFixNone)
	bodies := map[int]string{}
	for _, n := range pmrRange(1, 10) {
		bodies[n] = cleared
	}
	tests := []struct {
		name string
		rate int
		want []int
	}{
		{"rate 0 reviews every PR", 0, pmrRange(1, 10)},
		{"rate 1 reviews every PR", 1, pmrRange(1, 10)},
		{"rate 5 reviews 5 and 10", 5, []int{5, 10}},
		{"rate 3 reviews 3, 6 and 9", 3, []int{3, 6, 9}},
		{"rate 20 reviews none of 1..10", 20, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, budget, _ := runPMRSampling(t, tt.rate, 100, bodies, pmrRange(1, 10))
			got = pmrSorted(got)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("reviewed PRs = %v, want %v", got, tt.want)
			}
			if used := pmrUsed(budget); used != len(tt.want) {
				t.Errorf("budget used = %d, want %d", used, len(tt.want))
			}
		})
	}
}

// TestPostMergeReviewSampling_ForcedOnRCOrShadow: a PR the pre-PR review did
// not clear is reviewed whatever the rate says. PR 7 is not a multiple of 5.
func TestPostMergeReviewSampling_ForcedOnRCOrShadow(t *testing.T) {
	approve, rc := string(executor.PostMergeApprove), string(executor.PostMergeRequestChanges)
	enforce, shadow := executor.PrePRReviewModeEnforce, executor.PrePRReviewModeShadow
	tests := []struct {
		name       string
		body       string
		wantForced bool
	}{
		{"enforce approve, no fix: sampled out", prePRBody(enforce, approve, "none"), false},
		{"enforce approve-with-notes: sampled out", prePRBody(enforce, string(executor.PostMergeApproveWithNotes), "none"), false},
		{"enforce approve after a fixed round: sampled out", prePRBody(enforce, approve, "fixed"), false},
		{"section missing", "## Summary\nx\n", true},
		{"empty body", "", true},
		{"section without a mode", "## Pre-PR review\n\nVerdict: APPROVE\nFix: none\n", true},
		{"shadow mode", prePRBody(shadow, approve, "none"), true},
		{"off mode", prePRBody("off", approve, "none"), true},
		{"request-changes verdict", prePRBody(enforce, rc, "none"), true},
		{"request-changes after a fixed round", prePRBody(enforce, rc, "fixed"), true},
		{"unknown verdict", prePRBody(enforce, "MAYBE", "none"), true},
		{"fix failed", prePRBody(enforce, approve, "failed"), true},
		{"fix reverted", prePRBody(enforce, approve, "reverted"), true},
		{"fix line missing", "## Pre-PR review\n\nMode: enforce\nVerdict: APPROVE\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, budget, _ := runPMRSampling(t, 5, 100, map[int]string{7: tt.body}, []int{7})
			if reviewed := len(got) == 1; reviewed != tt.wantForced {
				t.Errorf("PR 7 reviewed = %v, want %v", reviewed, tt.wantForced)
			}
			wantUsed := 0
			if tt.wantForced {
				wantUsed = 1
			}
			if used := pmrUsed(budget); used != wantUsed {
				t.Errorf("budget used = %d, want %d", used, wantUsed)
			}
		})
	}
}

func TestPostMergeReviewSampling_ForcedOnFetchError(t *testing.T) {
	// No body registered for PR 7: every PR fetch answers 500, so the forced
	// review starts (budget spent) and then fails gathering its own inputs.
	got, budget, c := runPMRSampling(t, 5, 100, map[int]string{}, []int{7})
	if used := pmrUsed(budget); used != 1 {
		t.Errorf("budget used = %d, want 1: an unreadable PR body forces the review", used)
	}
	snap := c.metrics.Snapshot()
	if n := snap.PostMergeReviewVerdicts[postMergeReviewSampledOut]; n != 0 {
		t.Errorf("sampled_out count = %d, want 0", n)
	}
	if len(got) != 0 {
		t.Errorf("reviewer ran %v, want none: the input gather fails on the same 500", got)
	}
}

// TestPostMergeReviewSampling_SampledOutDoesNotConsumeBudget: with a budget of
// one, the sampled-out PRs in front of the reviewed one leave the budget alone.
func TestPostMergeReviewSampling_SampledOutDoesNotConsumeBudget(t *testing.T) {
	cleared := prePRBody(executor.PrePRReviewModeEnforce, string(executor.PostMergeApprove), executor.PrePRFixNone)
	bodies := map[int]string{}
	for _, n := range pmrRange(1, 10) {
		bodies[n] = cleared
	}
	got, budget, c := runPMRSampling(t, 5, 1, bodies, pmrRange(1, 5))
	if len(got) != 1 || got[0] != 5 {
		t.Errorf("reviewed PRs = %v, want [5]: PRs 1-4 must not spend the one-review budget", got)
	}
	if used := pmrUsed(budget); used != 1 {
		t.Errorf("budget used = %d, want 1", used)
	}

	snap := c.metrics.Snapshot()
	if n := snap.PostMergeReviewVerdicts[postMergeReviewSampledOut]; n != 4 {
		t.Errorf("sampled_out count = %d, want 4", n)
	}
}

// TestPostMergeReviewSampling_SampledOutPRIsNotMarkedReviewed: a skipped PR
// stays reviewable, and a repeat of the decision gives the same answer.
func TestPostMergeReviewSampling_SampledOutPRIsNotMarkedReviewed(t *testing.T) {
	cleared := prePRBody(executor.PrePRReviewModeEnforce, string(executor.PostMergeApprove), executor.PrePRFixNone)
	reviewer := &pmrNumberReviewer{}
	c := newPMRController(t, newPMRSamplingGitHub(t, map[int]string{3: cleared}), reviewer, true)
	c.config.PostMergeReview.SampleRate = 5
	prState := mergedPRState(3, 10)
	for i := 0; i < 2; i++ {
		if err := c.handleMerged(context.Background(), prState); err != nil {
			t.Fatalf("handleMerged: %v", err)
		}
	}
	c.waitPostMergeReviews()
	if prState.PostMergeReviewPosted || len(reviewer.reviewed()) != 0 {
		t.Errorf("sampled-out PR: posted=%v reviews=%v, want neither", prState.PostMergeReviewPosted, reviewer.reviewed())
	}
}
