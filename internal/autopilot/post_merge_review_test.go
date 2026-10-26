package autopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	"github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// pmrFakeReviewer is a PostMergeReviewer returning a canned review.
type pmrFakeReviewer struct {
	mu     sync.Mutex
	calls  int
	inputs []executor.PostMergeReviewInput
	review *executor.PostMergeReview
	err    error
}

func (f *pmrFakeReviewer) Review(_ context.Context, in executor.PostMergeReviewInput) (*executor.PostMergeReview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.inputs = append(f.inputs, in)
	return f.review, f.err
}

func (f *pmrFakeReviewer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// pmrGitHub is a mock GitHub recording the comments and issues the review
// flow writes.
type pmrGitHub struct {
	mu       sync.Mutex
	comments map[int][]string // PR/issue number -> comment bodies
	issues   []github.IssueInput
	created  []*github.Issue
}


func newPMRGitHub(t *testing.T) (*pmrGitHub, *github.Client) {
	t.Helper()
	mock := &pmrGitHub{comments: make(map[int][]string)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/pulls/"):
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 42, State: "closed", Merged: true, MergeCommitSHA: "mergesha1",
				Base: github.PRRef{Ref: "main"},
				Body: "## Summary\nx\n\n## Evidence\n\n$ go test ./...\nok\n",
			})
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/comments"):
			_ = json.NewEncoder(w).Encode([]github.Comment{})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
			var n int
			_, _ = fmt.Sscanf(strings.TrimPrefix(path, "/repos/owner/repo/issues/"), "%d", &n)
			body, _ := io.ReadAll(r.Body)
			var in struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(body, &in)
			mock.comments[n] = append(mock.comments[n], in.Body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Comment{ID: int64(5000 + n*10 + len(mock.comments[n])), Body: in.Body})
		case r.Method == http.MethodGet && path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(mock.created)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/repos/owner/repo/issues/"):
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: 10, Title: "feat(x): do the thing", Body: "Implement the thing.\n\n## Out of scope\n- other things", State: "closed",
			})
		case r.Method == http.MethodPost && path == "/repos/owner/repo/issues":
			var in github.IssueInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			mock.issues = append(mock.issues, in)
			iss := &github.Issue{Number: 900 + len(mock.issues), Title: in.Title, Body: in.Body, State: "open"}
			for _, l := range in.Labels {
				iss.Labels = append(iss.Labels, github.Label{Name: l})
			}
			mock.created = append(mock.created, iss)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(iss)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	t.Cleanup(server.Close)
	return mock, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
}

func (m *pmrGitHub) commentCount(pr int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.comments[pr])
}

func (m *pmrGitHub) issueCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.issues)
}

func requestChangesReview() *executor.PostMergeReview {
	return &executor.PostMergeReview{
		Verdict: executor.PostMergeRequestChanges,
		Defects: []executor.PostMergeDefect{{
			Location: "internal/foo/client.go:42",
			Claim:    "binds to field org_id that the server never sends",
			Evidence: "internal/server/handler.go emits only organization_id",
		}},
		Notes: "mock hides the mismatch",
	}
}

func newPMRController(t *testing.T, gh *github.Client, reviewer PostMergeReviewer, enabled bool) *Controller {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.PostMergeReview = &PostMergeReviewConfig{Enabled: enabled}
	c := NewController(cfg, gh, nil, "owner", "repo")
	c.SetPostMergeReviewer(reviewer)
	c.postMergeDiff = func(_ context.Context, sha string) (string, error) {
		return "diff --git a/internal/foo/client.go b/internal/foo/client.go\n+x := resp.OrgID\n", nil
	}
	return c
}

func mergedPRState(prNumber, issueNumber int) *PRState {
	return &PRState{
		PRNumber: prNumber, PRURL: fmt.Sprintf("https://github.com/owner/repo/pull/%d", prNumber),
		IssueNumber: issueNumber, Stage: StageMerged, CIStatus: CISuccess, TargetBranch: "main",
	}
}

func TestHandleMerged_PostMergeReviewRunsOnce(t *testing.T) {
	_, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
	c := newPMRController(t, gh, reviewer, true)
	ctx := context.Background()

	prState := mergedPRState(42, 10)
	if err := c.handleMerged(ctx, prState); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()
	if got := reviewer.callCount(); got != 1 {
		t.Fatalf("reviewer calls after first pass = %d, want 1", got)
	}
	if !prState.PostMergeReviewPosted {
		t.Fatal("PostMergeReviewPosted not set after the review started")
	}

	// Second pass for the same PR (re-entry / crash recovery): no new review.
	prState.Stage = StageMerged
	if err := c.handleMerged(ctx, prState); err != nil {
		t.Fatalf("second handleMerged: %v", err)
	}
	c.waitPostMergeReviews()
	if got := reviewer.callCount(); got != 1 {
		t.Fatalf("reviewer calls after second pass = %d, want still 1 (PostMergeReviewPosted)", got)
	}

	// The reviewer saw the issue, the diff, the CI status and the PR evidence.
	in := reviewer.inputs[0]
	if in.PRNumber != 42 || in.IssueTitle != "feat(x): do the thing" || !strings.Contains(in.IssueBody, "Out of scope") {
		t.Errorf("issue inputs wrong: %+v", in)
	}
	if !strings.Contains(in.Diff, "resp.OrgID") {
		t.Errorf("diff input wrong: %q", in.Diff)
	}
	if !strings.Contains(in.CIResult, string(CISuccess)) {
		t.Errorf("CIResult = %q, want it to carry the CI status", in.CIResult)
	}
	if !strings.Contains(in.PRBody, "## Evidence") {
		t.Errorf("PRBody lacks the evidence block: %q", in.PRBody)
	}
}

func TestHandleMerged_PostMergeReviewDisabledRunsNothing(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: requestChangesReview()}
	c := newPMRController(t, gh, reviewer, false)

	prState := mergedPRState(42, 10)
	if err := c.handleMerged(context.Background(), prState); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()
	if reviewer.callCount() != 0 || prState.PostMergeReviewPosted {
		t.Fatalf("disabled knob: calls=%d posted=%v, want 0/false", reviewer.callCount(), prState.PostMergeReviewPosted)
	}
	if mock.commentCount(42) != 0 || mock.issueCount() != 0 {
		t.Fatalf("disabled knob wrote to GitHub: comments=%d issues=%d", mock.commentCount(42), mock.issueCount())
	}
}

func TestHandleMerged_PostMergeReviewSkipsPRWithoutIssue(t *testing.T) {
	_, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
	c := newPMRController(t, gh, reviewer, true)

	prState := mergedPRState(42, 0)
	if err := c.handleMerged(context.Background(), prState); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()
	if reviewer.callCount() != 0 {
		t.Fatalf("reviewer ran for a PR with no issue: %d calls", reviewer.callCount())
	}
}

func TestPostMergeReviewPostedPersists(t *testing.T) {
	store := newTestStateStore(t)
	pr := &PRState{PRNumber: 7, PRURL: "https://github.com/owner/repo/pull/7", Stage: StageMerged, PostMergeReviewPosted: true}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	got, err := store.GetPRState("owner/repo", 7)
	if err != nil || got == nil || !got.PostMergeReviewPosted {
		t.Fatalf("GetPRState = %+v, %v; want PostMergeReviewPosted", got, err)
	}
	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 || !all[0].PostMergeReviewPosted {
		t.Fatalf("LoadAllPRStates = %+v, %v; want PostMergeReviewPosted", all, err)
	}
	if !pr.snapshot().PostMergeReviewPosted {
		t.Fatal("snapshot drops PostMergeReviewPosted")
	}
}

func TestPostMergeReview_Posting(t *testing.T) {
	tests := []struct {
		name         string
		review       *executor.PostMergeReview
		wantComments int
		wantIssues   int
	}{
		{"request changes", requestChangesReview(), 1, 1},
		{"approve", &executor.PostMergeReview{Verdict: executor.PostMergeApprove}, 1, 0},
		{"approve with notes", &executor.PostMergeReview{Verdict: executor.PostMergeApproveWithNotes, Notes: "nit"}, 1, 0},
		{"unknown posts nothing", &executor.PostMergeReview{Verdict: executor.PostMergeUnknown}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, gh := newPMRGitHub(t)
			c := newPMRController(t, gh, &pmrFakeReviewer{review: tt.review}, true)

			if err := c.handleMerged(context.Background(), mergedPRState(42, 10)); err != nil {
				t.Fatalf("handleMerged: %v", err)
			}
			c.waitPostMergeReviews()

			if got := mock.commentCount(42); got != tt.wantComments {
				t.Errorf("PR comments = %d, want %d", got, tt.wantComments)
			}
			if got := mock.issueCount(); got != tt.wantIssues {
				t.Fatalf("issues filed = %d, want %d", got, tt.wantIssues)
			}
			if tt.wantComments == 1 {
				body := mock.comments[42][0]
				if !strings.HasPrefix(body, "**VERDICT: "+string(tt.review.Verdict)+"**") {
					t.Errorf("comment does not lead with the verdict: %q", body)
				}
			}
			if tt.wantIssues == 1 {
				in := mock.issues[0]
				if !strings.HasPrefix(in.Title, "fix(#42)") {
					t.Errorf("title %q does not lead with the PR number", in.Title)
				}
				for _, want := range []string{"pilot", "post-merge-review"} {
					found := false
					for _, l := range in.Labels {
						found = found || l == want
					}
					if !found {
						t.Errorf("labels %v missing %q", in.Labels, want)
					}
				}
				for _, want := range []string{"internal/foo/client.go:42", "never sends", "organization_id", "pull/42"} {
					if !strings.Contains(in.Body, want) {
						t.Errorf("issue body missing %q:\n%s", want, in.Body)
					}
				}
				if !strings.Contains(mock.comments[42][0], "internal/foo/client.go:42") {
					t.Errorf("comment lacks the defect list: %q", mock.comments[42][0])
				}
			}
		})
	}
}

func TestPostMergeReview_ReviewerErrorPostsNothing(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	c := newPMRController(t, gh, &pmrFakeReviewer{err: errors.New("exit status 1")}, true)

	if err := c.handleMerged(context.Background(), mergedPRState(42, 10)); err != nil {
		t.Fatalf("handleMerged must not fail on a reviewer error: %v", err)
	}
	c.waitPostMergeReviews()
	if mock.commentCount(42) != 0 || mock.issueCount() != 0 {
		t.Fatalf("reviewer error wrote to GitHub: comments=%d issues=%d", mock.commentCount(42), mock.issueCount())
	}
	if got := c.metrics.Snapshot().PostMergeReviewVerdicts["error"]; got != 1 {
		t.Errorf("error counter = %d, want 1", got)
	}
}

func TestCreatePostMergeReviewIssue_DedupsByTitle(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(gh, "owner", "repo", cfg)
	ctx := context.Background()

	first, err := fl.CreatePostMergeReviewIssue(ctx, 42, "https://github.com/owner/repo/pull/42", 10, requestChangesReview())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := fl.CreatePostMergeReviewIssue(ctx, 42, "https://github.com/owner/repo/pull/42", 10, requestChangesReview())
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first != second || mock.issueCount() != 1 {
		t.Fatalf("first=%d second=%d issues=%d, want one issue reused", first, second, mock.issueCount())
	}
	// A different PR is a different title, so it files its own issue.
	if _, err := fl.CreatePostMergeReviewIssue(ctx, 43, "https://github.com/owner/repo/pull/43", 11, requestChangesReview()); err != nil {
		t.Fatalf("other PR: %v", err)
	}
	if mock.issueCount() != 2 {
		t.Fatalf("issues = %d, want 2 (one per PR)", mock.issueCount())
	}
}

func TestCreatePostMergeReviewIssue_SanitizesModelOutput(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	fl := NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeRequestChanges,
		Defects: []executor.PostMergeDefect{{Location: "a.go:1", Claim: "hid\u200bden", Evidence: strings.Repeat("e", 5000)}},
	}
	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "u", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := mock.issues[0].Body
	if strings.ContainsRune(body, '\u200b') {
		t.Error("invisible unicode survived into the issue body")
	}
	if len(body) > 4000 {
		t.Errorf("issue body not bounded: %d chars", len(body))
	}
}

func TestPostMergeReviewBudget(t *testing.T) {
	now := time.Date(2026, 10, 26, 23, 59, 0, 0, time.UTC)
	b := &postMergeReviewBudget{cap: 2, now: func() time.Time { return now }}

	for i := 1; i <= 2; i++ {
		if ok, _, _, used := b.acquire(); !ok || used != i {
			t.Fatalf("acquire %d: ok=%v used=%d", i, ok, used)
		}
	}
	ok, first, _, _ := b.acquire()
	if ok || !first {
		t.Fatalf("third acquire: ok=%v firstDenial=%v, want denied with firstDenial", ok, first)
	}
	if ok, first, _, _ := b.acquire(); ok || first {
		t.Fatalf("fourth acquire: ok=%v firstDenial=%v, want denied silently", ok, first)
	}

	now = now.Add(2 * time.Minute) // next UTC day
	if ok, _, day, used := b.acquire(); !ok || used != 1 || day != "2026-10-27" {
		t.Fatalf("after midnight: ok=%v used=%d day=%s, want a fresh budget", ok, used, day)
	}
}

// TestPostMergeReview_DailyCapAndTicketCap: the 21st review of a day is
// skipped and logged once, and reviews never touch the executor's daily
// ticket cap — no executions row is written, so the ticket count stays put.
func TestPostMergeReview_DailyCapAndTicketCap(t *testing.T) {
	_, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
	c := newPMRController(t, gh, reviewer, true) // default cap: 20

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c.SetMemoryStore(store)

	gate := executor.NewDailyCapGate(1, "UTC")
	dayStart := time.Now().UTC().Add(-24 * time.Hour)
	dayEnd := time.Now().UTC().Add(24 * time.Hour)
	ticketsBefore, err := store.CountDistinctTasksInWindow(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("count tickets: %v", err)
	}

	ctx := context.Background()
	states := make([]*PRState, 21)
	for i := range states {
		states[i] = mergedPRState(100+i, 10)
		if err := c.handleMerged(ctx, states[i]); err != nil {
			t.Fatalf("handleMerged PR %d: %v", states[i].PRNumber, err)
		}
	}
	c.waitPostMergeReviews()

	if got := reviewer.callCount(); got != 20 {
		t.Fatalf("reviews run = %d, want 20 (the 21st is over the daily cap)", got)
	}
	if !states[19].PostMergeReviewPosted {
		t.Error("20th PR should have been reviewed")
	}
	if states[20].PostMergeReviewPosted {
		t.Error("21st PR was skipped, so it must not be marked reviewed")
	}

	ticketsAfter, err := store.CountDistinctTasksInWindow(dayStart, dayEnd)
	if err != nil {
		t.Fatalf("count tickets: %v", err)
	}
	if ticketsAfter != ticketsBefore {
		t.Fatalf("ticket cap counter moved from %d to %d; reviews must not count as tickets", ticketsBefore, ticketsAfter)
	}
	if hold := gate.Check(store, &executor.Task{ID: "GH-9999"}); hold != nil {
		t.Fatalf("reviews consumed the ticket cap: %v", hold)
	}
}

func TestPostMergeReview_ConfiguredCap(t *testing.T) {
	_, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
	c := newPMRController(t, gh, reviewer, true)
	c.config.PostMergeReview.DailyCap = 2

	for i := 0; i < 3; i++ {
		if err := c.handleMerged(context.Background(), mergedPRState(200+i, 10)); err != nil {
			t.Fatalf("handleMerged: %v", err)
		}
	}
	c.waitPostMergeReviews()
	if got := reviewer.callCount(); got != 2 {
		t.Fatalf("reviews run = %d, want 2", got)
	}
}

func TestPostMergeReview_LedgerRecordsVerdict(t *testing.T) {
	_, gh := newPMRGitHub(t)
	c := newPMRController(t, gh, &pmrFakeReviewer{review: requestChangesReview()}, true)

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c.SetMemoryStore(store)

	if err := c.handleMerged(context.Background(), mergedPRState(42, 10)); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()

	var reviewer, verdict string
	var reviewID int64
	row := store.DB().QueryRow(`SELECT review_id, reviewer, verdict FROM pr_reviews WHERE pr_number = ?`, 42)
	if err := row.Scan(&reviewID, &reviewer, &verdict); err != nil {
		t.Fatalf("no pr_reviews row: %v", err)
	}
	if reviewer != executor.PostMergeReviewerID || verdict != memory.VerdictRequestChanges {
		t.Errorf("ledger row reviewer=%q verdict=%q, want %q/%q", reviewer, verdict, executor.PostMergeReviewerID, memory.VerdictRequestChanges)
	}
	if reviewID >= 0 {
		t.Errorf("review_id = %d, want the negated comment id", reviewID)
	}
}

func TestBuildPostMergeReviewComment(t *testing.T) {
	got := buildPostMergeReviewComment(requestChangesReview())
	for _, want := range []string{"**VERDICT: REQUEST-CHANGES**", "### Defects", "internal/foo/client.go:42", "Evidence:", "### Notes", "not a substitute"} {
		if !strings.Contains(got, want) {
			t.Errorf("comment missing %q:\n%s", want, got)
		}
	}
	approve := buildPostMergeReviewComment(&executor.PostMergeReview{Verdict: executor.PostMergeApprove})
	if strings.Contains(approve, "### Defects") || strings.Contains(approve, "### Notes") {
		t.Errorf("clean approve should carry no empty sections:\n%s", approve)
	}
}

func TestPostMergeReviewConfig(t *testing.T) {
	var nilCfg *Config
	if nilCfg.PostMergeReviewEnabled() || DefaultConfig().PostMergeReviewEnabled() {
		t.Error("post_merge_review must default to off")
	}
	cfg := DefaultConfig()
	cfg.PostMergeReview = &PostMergeReviewConfig{Enabled: true}
	if !cfg.PostMergeReviewEnabled() {
		t.Error("enabled knob not honored")
	}
	if got := cfg.PostMergeReview.EffectiveDailyCap(); got != 20 {
		t.Errorf("default daily cap = %d, want 20", got)
	}
	cfg.PostMergeReview.DailyCap = 5
	if got := cfg.PostMergeReview.EffectiveDailyCap(); got != 5 {
		t.Errorf("daily cap = %d, want 5", got)
	}
}

func TestLedgerVerdict(t *testing.T) {
	tests := map[executor.PostMergeVerdict]string{
		executor.PostMergeApprove:          memory.VerdictApprove,
		executor.PostMergeApproveWithNotes: memory.VerdictApproveWNotes,
		executor.PostMergeRequestChanges:   memory.VerdictRequestChanges,
		executor.PostMergeUnknown:          "",
	}
	for in, want := range tests {
		if got := ledgerVerdict(in); got != want {
			t.Errorf("ledgerVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}
