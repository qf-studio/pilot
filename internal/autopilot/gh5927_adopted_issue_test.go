package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// adoptFakeIssue is one issue the fake GitHub serves for adoption tests.
type adoptFakeIssue struct {
	state  string
	labels []string
}

// newAdoptController returns a Controller over a fake GitHub that serves the
// given issues (404 for unknown numbers) and records which issue numbers were
// fetched. Logs are captured in the returned buffer.
func newAdoptController(t *testing.T, issues map[int]adoptFakeIssue, lookup *fakeBorrowedBranchLookup) (*Controller, *bytes.Buffer, func() []int) {
	t.Helper()
	var (
		mu      sync.Mutex
		fetched []int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/owner/repo/issues/%d", &n); err != nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		fetched = append(fetched, n)
		mu.Unlock()
		spec, ok := issues[n]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		iss := github.Issue{Number: n, State: spec.state}
		for _, l := range spec.labels {
			iss.Labels = append(iss.Labels, github.Label{Name: l})
		}
		_ = json.NewEncoder(w).Encode(iss)
	}))
	t.Cleanup(server.Close)

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithLogger(logger))
	if lookup != nil {
		c.SetBorrowedBranchLookup(lookup)
	}
	return c, &logs, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), fetched...)
	}
}

func adoptPR(branch, body string) *github.PullRequest {
	return &github.PullRequest{
		Number: 5921, State: "open", Body: body,
		HTMLURL: "https://github.com/owner/repo/pull/5921",
		Head:    github.PRRef{Ref: branch, SHA: "abc1234"},
		Base:    github.PRRef{Ref: "main"},
	}
}

var fixLabels = []string{"pilot", "autopilot-fix"}

func TestResolveAdoptedIssueNumber(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		issues        map[int]adoptFakeIssue
		registry      int // fix issue in the registry for pilot/GH-5917; 0 = none
		wantIssue     int
		wantSource    string
		wantViaFix    bool
		wantForgotten []string
	}{
		{
			name:       "closing reference binds to its issue",
			body:       "Closes #5917",
			issues:     map[int]adoptFakeIssue{5917: {state: "open"}},
			wantIssue:  5917,
			wantSource: adoptSourceClosingReference,
		},
		{
			name:       "closing reference beats a live fix issue",
			body:       "Fixes #5917",
			issues:     map[int]adoptFakeIssue{5917: {state: "open"}, 5922: {state: "open", labels: fixLabels}},
			registry:   5922,
			wantIssue:  5917,
			wantSource: adoptSourceClosingReference,
		},
		{
			name:       "closing reference naming the fix issue registers via the fix issue",
			body:       "Resolves #5922",
			issues:     map[int]adoptFakeIssue{5922: {state: "open", labels: fixLabels}},
			registry:   5922,
			wantIssue:  5922,
			wantSource: adoptSourceClosingReference,
			wantViaFix: true,
		},
		{
			name:          "closed closing-reference target falls back to the branch",
			body:          "Closes #5917",
			issues:        map[int]adoptFakeIssue{5917: {state: "closed"}},
			wantIssue:     5917,
			wantSource:    adoptSourceBranch,
			wantForgotten: nil,
		},
		{
			name:       "open labeled fix issue wins without a closing reference",
			body:       "no reference here",
			issues:     map[int]adoptFakeIssue{5922: {state: "open", labels: fixLabels}},
			registry:   5922,
			wantIssue:  5922,
			wantSource: adoptSourceBorrowedBranch,
			wantViaFix: true,
		},
		{
			name:          "closed fix issue is ignored and its entry dropped",
			body:          "no reference here",
			issues:        map[int]adoptFakeIssue{5922: {state: "closed", labels: fixLabels}},
			registry:      5922,
			wantIssue:     5917,
			wantSource:    adoptSourceBranch,
			wantForgotten: []string{"GH-5922"},
		},
		{
			name:          "unlabeled fix issue is ignored and its entry dropped",
			body:          "",
			issues:        map[int]adoptFakeIssue{5922: {state: "open", labels: []string{"pilot"}}},
			registry:      5922,
			wantIssue:     5917,
			wantSource:    adoptSourceBranch,
			wantForgotten: []string{"GH-5922"},
		},
		{
			name:       "no closing reference and no registry entry binds to the branch number",
			body:       "just a description",
			issues:     map[int]adoptFakeIssue{},
			wantIssue:  5917,
			wantSource: adoptSourceBranch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lookup *fakeBorrowedBranchLookup
			if tt.registry > 0 {
				lookup = &fakeBorrowedBranchLookup{branch: "pilot/GH-5917", fixIssue: tt.registry}
			} else {
				lookup = &fakeBorrowedBranchLookup{}
			}
			c, logs, _ := newAdoptController(t, tt.issues, lookup)

			got := c.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", tt.body))

			if got.Number != tt.wantIssue || got.Source != tt.wantSource || got.ViaFixIssue != tt.wantViaFix {
				t.Errorf("resolve = {issue:%d source:%q viaFix:%v}, want {issue:%d source:%q viaFix:%v}",
					got.Number, got.Source, got.ViaFixIssue, tt.wantIssue, tt.wantSource, tt.wantViaFix)
			}
			if !strings.Contains(logs.String(), "source="+tt.wantSource) {
				t.Errorf("log does not name the winning source %q:\n%s", tt.wantSource, logs.String())
			}
			if strings.Join(lookup.forgotten, ",") != strings.Join(tt.wantForgotten, ",") {
				t.Errorf("forgotten = %v, want %v", lookup.forgotten, tt.wantForgotten)
			}
			// The registry read is destructive: whatever the outcome, no entry
			// may survive the adoption.
			if _, ok := lookup.FixIssueForBranch("pilot/GH-5917"); ok {
				t.Error("registry entry survived the adoption")
			}
		})
	}
}

// The PR#5921 incident: a closed fix issue #5922 in the registry must not
// win over the issue the PR closes, and its entry must be gone afterwards.
func TestResolveAdoptedIssueNumber_ClosedFixIssueIgnored(t *testing.T) {
	lookup := &fakeBorrowedBranchLookup{branch: "pilot/GH-5917", fixIssue: 5922}
	c, _, _ := newAdoptController(t, map[int]adoptFakeIssue{
		5922: {state: "closed", labels: fixLabels},
	}, lookup)

	got := c.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", "no closing reference"))

	if got.Number != 5917 {
		t.Fatalf("issue = %d, want 5917: a closed fix issue's registry entry must not win", got.Number)
	}
	if got.ViaFixIssue {
		t.Error("ViaFixIssue = true for a closed fix issue")
	}
}

func TestResolveAdoptedIssueNumber_ClosingReferenceWins(t *testing.T) {
	lookup := &fakeBorrowedBranchLookup{branch: "pilot/GH-5917", fixIssue: 5922}
	c, logs, _ := newAdoptController(t, map[int]adoptFakeIssue{
		5917: {state: "open"},
		5922: {state: "open", labels: fixLabels},
	}, lookup)

	got := c.resolveAdoptedIssueNumber(context.Background(), adoptPR("pilot/GH-5917", "## Summary\n\nCloses #5917"))

	if got.Number != 5917 || got.Source != adoptSourceClosingReference {
		t.Fatalf("resolve = {issue:%d source:%q}, want {5917 %q}", got.Number, got.Source, adoptSourceClosingReference)
	}
	if !strings.Contains(logs.String(), "source=closing-reference") {
		t.Errorf("log lacks source=closing-reference:\n%s", logs.String())
	}
}

// Acceptance: reopening the PR#5921 shape through the reconciler registers
// issue 5917 and leaves nothing behind in the registry.
func TestReconcileOrphanPRs_ReopenedPRBindsToClosingReference(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{adoptPR("pilot/GH-5917", "Closes #5917")})
		case "/repos/owner/repo/issues/5917":
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 5917, State: "open"})
		case "/repos/owner/repo/issues/5922":
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 5922, State: "closed", Labels: []github.Label{{Name: "autopilot-fix"}}})
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo", WithLogger(logger))
	lookup := &fakeBorrowedBranchLookup{branch: "pilot/GH-5917", fixIssue: 5922}
	c.SetBorrowedBranchLookup(lookup)

	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(5921)
	if !ok {
		t.Fatal("PR 5921 was not adopted")
	}
	if pr.IssueNumber != 5917 {
		t.Errorf("IssueNumber = %d, want 5917", pr.IssueNumber)
	}
	if !strings.Contains(logs.String(), "source=closing-reference") {
		t.Errorf("adoption log lacks source=closing-reference:\n%s", logs.String())
	}
	if _, ok := lookup.FixIssueForBranch("pilot/GH-5917"); ok {
		t.Error("registry entry for the closed fix issue survived the adoption")
	}
}

// The post-merge review reads the issue the PR closes, even when the PR is
// tracked under a different (fix) issue.
func TestGatherPostMergeReviewInput_PreferClosingOverTrackedIssue(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		closing     []int
		tracked     int
		preferClose bool
		wantIssue   int
	}{
		{name: "closing reference from the API beats the tracked issue", closing: []int{5917}, tracked: 5922, preferClose: true, wantIssue: 5917},
		{name: "closing reference from the PR body beats the tracked issue", body: "Closes #5917", tracked: 5922, preferClose: true, wantIssue: 5917},
		{name: "tracked issue is the fallback without a closing reference", body: "nothing", tracked: 5922, preferClose: true, wantIssue: 5922},
		{name: "explicit issue still wins when closing is not preferred", closing: []int{5917}, tracked: 5922, wantIssue: 5922},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newGatherFake("pilot/GH-5917")
			fake.pr.Body = tt.body
			fake.closing = tt.closing
			fake.issues[5917] = &github.Issue{Number: 5917, Title: "the real issue"}
			fake.issues[5922] = &github.Issue{Number: 5922, Title: "the fix issue"}

			got, err := gatherPostMergeReviewInput(context.Background(), fake, "o", "r", "", 5902, tt.tracked, tt.preferClose, false, nil)
			if err != nil {
				t.Fatalf("gather: %v", err)
			}
			if got.IssueNumber != tt.wantIssue || fake.gotIssue != tt.wantIssue {
				t.Errorf("issue = %d (fetched %d), want %d", got.IssueNumber, fake.gotIssue, tt.wantIssue)
			}
		})
	}
}

// End to end through the controller: the merged PR is tracked under fix issue
// 5922, closes 5917; the reviewer reads 5917 and the follow-up names both.
func TestPostMergeReview_UsesClosingIssueNotTrackedFixIssue(t *testing.T) {
	var (
		mu      sync.Mutex
		fetched []int
		created []github.IssueInput
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		var n int
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/pulls/"):
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 5921, State: "closed", Merged: true, MergeCommitSHA: "mergesha1",
				Head: github.PRRef{Ref: "pilot/GH-5917"}, Body: "Closes #5917",
			})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"x"}`))
		case r.Method == http.MethodGet && path == "/repos/owner/repo/issues":
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && path == "/repos/owner/repo/issues":
			var in github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			created = append(created, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 900, Title: in.Title, State: "open"})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/issues/"):
			if _, err := fmt.Sscanf(path, "/repos/owner/repo/issues/%d", &n); err == nil {
				fetched = append(fetched, n)
			}
			_ = json.NewEncoder(w).Encode(github.Issue{Number: n, Title: fmt.Sprintf("issue %d", n), State: "closed"})
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	reviewer := &pmrFakeReviewer{review: requestChangesReview()}
	c := newPMRController(t, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), reviewer, true)

	prState := mergedPRState(5921, 5922)
	if err := c.handleMerged(context.Background(), prState); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()

	mu.Lock()
	defer mu.Unlock()
	// handleMerged itself reads the tracked issue for its own bookkeeping; the
	// review must additionally have fetched the issue the PR closes.
	if !slices.Contains(fetched, 5917) {
		t.Fatalf("fetched issues = %v, want the review to fetch #5917", fetched)
	}
	if len(reviewer.inputs) != 1 || reviewer.inputs[0].IssueTitle != "issue 5917" {
		t.Fatalf("reviewer inputs = %+v, want issue 5917's title", reviewer.inputs)
	}
	if len(created) != 1 {
		t.Fatalf("follow-up issues = %d, want 1", len(created))
	}
	if !strings.Contains(created[0].Title, "#5921") || !strings.Contains(created[0].Title, "#5917") {
		t.Errorf("follow-up title %q must name PR #5921 and issue #5917", created[0].Title)
	}
	if !strings.Contains(created[0].Body, "PR #5921") || !strings.Contains(created[0].Body, "issue #5917") {
		t.Errorf("follow-up body must name PR #5921 and issue #5917:\n%s", created[0].Body)
	}
}
