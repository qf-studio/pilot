package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
			Severity: executor.PostMergeSeverityBlocking,
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
		{"exhausted posts nothing", &executor.PostMergeReview{Verdict: executor.PostMergeExhausted}, 0, 0},
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
		Defects: []executor.PostMergeDefect{{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: "hid\u200bden", Evidence: strings.Repeat("e", 5000)}},
	}
	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "u", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := mock.issues[0].Body
	if strings.ContainsRune(body, '\u200b') {
		t.Error("invisible unicode survived into the issue body")
	}
	// GH-5986: an Evidence over the field cap is kept whole in a details block;
	// only the whole body is bounded, at 16 KB.
	if !strings.Contains(body, strings.Repeat("e", 5000)) || !strings.Contains(body, "<details>") {
		t.Error("long evidence must sit whole in a details block")
	}
	if len(body) > maxPostMergeIssueBodyChars {
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

func TestPostMergeReview_LedgerRecordsFullNotVerified(t *testing.T) {
	_, gh := newPMRGitHub(t)
	review := requestChangesReview()
	review.NotVerified = []string{"nv-1", "nv-2", "nv-3", "nv-4", "A: met. — not traced"}
	c := newPMRController(t, gh, &pmrFakeReviewer{review: review}, true)

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

	var raw string
	if err := store.DB().QueryRow(`SELECT not_verified FROM pr_reviews WHERE pr_number = ?`, 42).Scan(&raw); err != nil {
		t.Fatalf("no pr_reviews row: %v", err)
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("not_verified %q is not a JSON array: %v", raw, err)
	}
	if len(got) != len(review.NotVerified) || got[4] != review.NotVerified[4] {
		t.Errorf("ledger not_verified = %q, want the full set %q", got, review.NotVerified)
	}
}

func TestFormatPostMergeNotVerified_CountNamesUntraced(t *testing.T) {
	tests := []struct {
		name        string
		notVerified []string
		want        []string
		notWant     []string
		wantBullets int
	}{
		{
			name:        "within the cap everything is listed",
			notVerified: []string{"nv-1", "nv-2", "A: met. — not traced"},
			want:        []string{"- nv-1", "- nv-2", "- A: met. — not traced"},
			notWant:     []string{"omitted"},
			wantBullets: 3,
		},
		{
			name:        "untraced entries behind the model items stay counted",
			notVerified: []string{"nv-1", "nv-2", "nv-3", "A: met. — not traced", "B: met. — not traced"},
			want:        []string{"- nv-1", "- nv-2", "- nv-3", "- … 2 requirements marked met but not traced to a file read omitted"},
			notWant:     []string{"A: met", "B: met"},
			wantBullets: 4,
		},
		{
			name:        "only untraced entries",
			notVerified: []string{"A: met. — not traced", "B: met. — not traced", "C: met. — not traced", "D: met. — not traced"},
			want:        []string{"- A: met. — not traced", "- C: met. — not traced", "- … 1 requirements marked met but not traced to a file read omitted"},
			notWant:     []string{"D: met"},
			wantBullets: 4,
		},
		{
			name:        "no untraced entries",
			notVerified: []string{"nv-1", "nv-2", "nv-3", "nv-4", "nv-5"},
			want:        []string{"- nv-1", "- nv-2", "- nv-3", "- … 2 more not-verified items omitted"},
			notWant:     []string{"not traced", "nv-4"},
			wantBullets: 4,
		},
		{
			name:        "an entry that sanitises to empty is neither written nor counted",
			notVerified: []string{"nv-1", "\u200b", "nv-2", "nv-3", "nv-4"},
			want:        []string{"- nv-1", "- nv-2", "- nv-3", "- … 1 more not-verified items omitted"},
			wantBullets: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPostMergeNotVerified(tt.notVerified)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
			lines := strings.Split(strings.TrimSpace(got), "\n")
			if len(lines) != tt.wantBullets {
				t.Errorf("rendered %d slots, want %d:\n%s", len(lines), tt.wantBullets, got)
			}
			if tt.wantBullets > executor.MaxPostMergeNotVerified && !strings.HasPrefix(lines[len(lines)-1], "- … ") {
				t.Errorf("last bullet is not the count line: %q", lines[len(lines)-1])
			}
		})
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

func TestPostMergeReviewCommentSections(t *testing.T) {
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeApproveWithNotes,
		Defects: []executor.PostMergeDefect{
			{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: "claim-one"},
			{Severity: executor.PostMergeSeverityMinor, Location: "b.go:2", Claim: "claim-two"},
		},
		NotVerified: []string{"nv-one: counterpart not in the diff", "nv-two: evidence truncated", "nv-three"},
		Notes:       "requirement 1 met",
		DiffBytes:   100, DiffFiles: 2,
	}
	got := buildPostMergeReviewComment(review)

	order := []string{"### Defects", "- [blocking] at a.go:1 — claim-one", "- [minor] at b.go:2 — claim-two",
		"### Not verified", "- nv-one: counterpart not in the diff", "- nv-three", "### Notes", "requirement 1 met", "Diff: 100 bytes"}
	prev := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("comment missing %q:\n%s", want, got)
		}
		if i < prev {
			t.Errorf("%q is out of order in the comment:\n%s", want, got)
		}
		prev = i
	}
	if n := strings.Count(got, "\n- nv-"); n != 3 {
		t.Errorf("Not verified bullets = %d, want 3:\n%s", n, got)
	}

	none := buildPostMergeReviewComment(&executor.PostMergeReview{Verdict: executor.PostMergeApprove})
	if strings.Contains(none, "### Not verified") {
		t.Errorf("empty NotVerified must render no section:\n%s", none)
	}
}

func TestCreatePostMergeReviewIssue_BlockingDefectsOnly(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	fl := NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeRequestChanges,
		Defects: []executor.PostMergeDefect{
			{Severity: executor.PostMergeSeverityMinor, Location: "minor.go:1", Claim: "minor-claim"},
			{Severity: executor.PostMergeSeverityBlocking, Location: "block.go:2", Claim: "blocking-claim"},
		},
	}
	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "u", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := mock.issues[0].Body
	if !strings.Contains(body, "blocking-claim") {
		t.Errorf("issue body missing the blocking defect:\n%s", body)
	}
	if strings.Contains(body, "minor-claim") {
		t.Errorf("issue body carries a minor defect:\n%s", body)
	}
}

// GH-5986: the two defect lines of issue #5937 (PR#5936's review comment) had
// their claim text in Location and were cut at a literal 200 bytes. No rendered
// line may end inside a word, and every cut field ends with "(truncated)".
func TestCreatePostMergeReviewIssue_NoMidSentenceCut(t *testing.T) {
	const (
		// The 200-byte prefixes the byte cut left in the comment, and the words the
		// reviewer wrote after them.
		loc1 = "internal/autopilot/feedback_loop.go:376 - Defect 1 is only partly fixed: the diff adds the registry forget hook only on the CreateFailureIssue dedup dead-issue path and none in the issue-close or labe"
		loc2 = "internal/autopilot/adopted_issue.go:198 - restoreBorrowedBranch re-records the consumed entry with a hardcoded 0 as the third RecordBorrowedBranch argument - the original registration passes 5921 (see"
		more = "led paths of the fix issue, so the entry stays"
	)
	for _, l := range []string{loc1, loc2} {
		if len(l) != 200 {
			t.Fatalf("fixture is %d bytes, want the 200 the byte cut produced: %q", len(l), l)
		}
	}
	longEvidence := strings.Repeat("The evidence names a second caller that never sets the field. ", 40)

	mock, gh := newPMRGitHub(t)
	fl := NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeRequestChanges,
		Defects: []executor.PostMergeDefect{
			{Severity: executor.PostMergeSeverityBlocking, Location: loc1 + more, Claim: "so a fix issue closed or unlabeled outside a new failure signal still keeps its entry until an adoption-time check"},
			{Severity: executor.PostMergeSeverityBlocking, Location: loc2 + " " + more, Claim: "the registration site passes the real id", Evidence: longEvidence},
		},
	}
	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "u", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := mock.issues[0].Body

	// Each location line is cut at a word end and marked; the marker is the only
	// sign of the cut, and no byte-cut ellipsis is left.
	cutLines := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "…") {
			t.Errorf("line carries a byte-cut ellipsis: %q", line)
		}
		before, _, found := strings.Cut(line, " (truncated)")
		if !found {
			continue
		}
		cutLines++
		prefix := strings.TrimPrefix(before, "- at ")
		var orig string
		for _, l := range []string{loc1 + more, loc2 + " " + more} {
			if strings.HasPrefix(l, prefix) {
				orig = l
			}
		}
		if orig == "" || len(prefix) > 200 {
			t.Errorf("cut line %q is not a short prefix of a fixture location", line)
			continue
		}
		if next := orig[len(prefix)]; next != ' ' && !strings.HasSuffix(prefix, ".") && !strings.HasSuffix(prefix, ";") {
			t.Errorf("line ends inside a word (next byte %q): %q", next, line)
		}
		if !strings.HasSuffix(line, "(truncated)") && !strings.Contains(line, "(truncated) — ") {
			t.Errorf("marker is not at the end of the location field: %q", line)
		}
	}
	if cutLines != 2 {
		t.Errorf("%d cut location lines, want 2:\n%s", cutLines, body)
	}
	if !strings.Contains(body, "so a fix issue closed or unlabeled outside a new failure signal still keeps its entry until an adoption-time check") {
		t.Errorf("the full Claim must be rendered:\n%s", body)
	}
	if !strings.Contains(body, "<details><summary>Evidence") || strings.Count(body, "The evidence names a second caller") != 40 {
		t.Errorf("an Evidence over %d bytes goes whole into a details block:\n%s", maxPostMergeFieldChars, body)
	}
}

// The Defects section is bounded at a word or sentence end with the marker, and
// the whole body stays near the 16 KB budget.
func TestCreatePostMergeReviewIssue_BodyBounded(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	fl := NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeRequestChanges,
		Defects: []executor.PostMergeDefect{
			{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: strings.Repeat("A very long claim sentence. ", 1500)},
		},
	}
	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "u", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := mock.issues[0].Body
	if len(body) > maxPostMergeIssueBodyChars+len(" (truncated)\n") || !strings.Contains(body, "(truncated)\n") {
		t.Errorf("body is %d bytes, want it bounded with the marker:\n%s", len(body), body[max(0, len(body)-300):])
	}
}

// GH-5991: the Defects section is cut to its own budget, so the Acceptance
// Criteria and Refs sections after it survive, and a cut inside an Evidence
// details block closes that block.
func TestCreatePostMergeReviewIssue_KeepsTrailingSectionsUnderCut(t *testing.T) {
	tests := []struct {
		name   string
		defect executor.PostMergeDefect
	}{
		{
			name:   "long claim",
			defect: executor.PostMergeDefect{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: strings.Repeat("A very long claim sentence. ", 1500)},
		},
		{
			name:   "long evidence in a details block",
			defect: executor.PostMergeDefect{Severity: executor.PostMergeSeverityBlocking, Location: "a.go:1", Claim: "short claim", Evidence: strings.Repeat("The evidence names a caller that never sets the field. ", 600)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, gh := newPMRGitHub(t)
			fl := NewFeedbackLoop(gh, "owner", "repo", DefaultConfig())
			review := &executor.PostMergeReview{
				Verdict: executor.PostMergeRequestChanges,
				Defects: []executor.PostMergeDefect{tt.defect},
				Notes:   "A reviewer note.",
			}
			if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "https://example.com/pull/42", 10, review); err != nil {
				t.Fatalf("create: %v", err)
			}
			body := mock.issues[0].Body
			if !strings.Contains(body, "(truncated)") {
				t.Fatalf("defects were not cut, fixture too small (%d bytes)", len(body))
			}
			for _, want := range []string{"## Reviewer Notes", "A reviewer note.", "## Acceptance Criteria", "## Refs"} {
				if !strings.Contains(body, want) {
					t.Errorf("body lost %q after the cut", want)
				}
			}
			if !strings.HasSuffix(body, "- PR #42: https://example.com/pull/42\n- Issue #10\n") {
				t.Errorf("body does not end with the Refs section:\n%s", body[max(0, len(body)-300):])
			}
			if o, c := strings.Count(body, "<details>"), strings.Count(body, "</details>"); o != c {
				t.Errorf("details tags unbalanced: %d open, %d close", o, c)
			}
			if len(body) > maxPostMergeIssueBodyChars+len(" (truncated)\n") {
				t.Errorf("body is %d bytes, over the budget", len(body))
			}
		})
	}
}

// The PR comment keeps the Not-verified entries it is capped to render; the
// review itself carries them all.
func TestBuildPostMergeReviewComment_NotVerifiedCapAtRender(t *testing.T) {
	review := &executor.PostMergeReview{
		Verdict:     executor.PostMergeApproveWithNotes,
		NotVerified: []string{"one", "two", "three", "four", "five"},
	}
	got := buildPostMergeReviewComment(review)
	if !strings.Contains(got, "- one\n") || !strings.Contains(got, "- two\n") || !strings.Contains(got, "- three\n") || strings.Contains(got, "- four\n") {
		t.Errorf("comment should show three entries and a count line:\n%s", got)
	}
	if !strings.Contains(got, "2 more not-verified items omitted") {
		t.Errorf("comment lacks the count line:\n%s", got)
	}
	if len(review.NotVerified) != 5 {
		t.Errorf("render must not shrink the review's set: %q", review.NotVerified)
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
		executor.PostMergeExhausted:        "",
	}
	for in, want := range tests {
		if got := ledgerVerdict(in); got != want {
			t.Errorf("ledgerVerdict(%q) = %q, want %q", in, got, want)
		}
	}
}

// GH-5873: one autopilot controller runs per project, so the daily cap has to
// be one budget shared by all of them, not one per controller.
func TestPostMergeReview_SharedDailyCapAcrossControllers(t *testing.T) {
	_, gh := newPMRGitHub(t)
	reviewer := &pmrFakeReviewer{review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove}}
	c1 := newPMRController(t, gh, reviewer, true)
	c2 := newPMRController(t, gh, reviewer, true)

	var logs lockedBuffer
	for _, c := range []*Controller{c1, c2} {
		c.log = slog.New(slog.NewTextHandler(&logs, nil))
	}
	budget := NewPostMergeReviewBudget(2)
	c1.SetPostMergeReviewBudget(budget)
	c2.SetPostMergeReviewBudget(budget)

	ctx := context.Background()
	states := []*PRState{mergedPRState(301, 10), mergedPRState(302, 10), mergedPRState(303, 10), mergedPRState(304, 10)}
	for i, c := range []*Controller{c1, c2, c1, c2} {
		if err := c.handleMerged(ctx, states[i]); err != nil {
			t.Fatalf("handleMerged PR %d: %v", states[i].PRNumber, err)
		}
	}
	c1.waitPostMergeReviews()
	c2.waitPostMergeReviews()

	if got := reviewer.callCount(); got != 2 {
		t.Fatalf("reviews run = %d, want 2 across both controllers", got)
	}
	if states[2].PostMergeReviewPosted || states[3].PostMergeReviewPosted {
		t.Error("PRs over the shared cap must not be marked reviewed")
	}
	if got := strings.Count(logs.String(), "daily cap reached"); got != 1 {
		t.Errorf("cap-reached line logged %d times, want 1", got)
	}
}

func TestPostMergeReview_BudgetFallbackIsPerController(t *testing.T) {
	_, gh := newPMRGitHub(t)
	c := newPMRController(t, gh, &pmrFakeReviewer{}, true)
	first := c.PostMergeReviewBudget()
	if first == nil || first != c.PostMergeReviewBudget() {
		t.Fatal("unwired controller must lazily build one stable budget")
	}
	shared := NewPostMergeReviewBudget(3)
	c.SetPostMergeReviewBudget(shared)
	if c.PostMergeReviewBudget() != shared {
		t.Fatal("SetPostMergeReviewBudget did not install the shared budget")
	}
}

func TestCreatePostMergeReviewIssue_LabelsAndPlainLocations(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(gh, "owner", "repo", cfg)
	fl.issueLabels = []string{"pilot", postMergeReviewLabel} // overlap must not duplicate
	review := requestChangesReview()
	review.Defects[0].Evidence = "see `internal/server/handler.go` in the other repo"
	review.Notes = "counterpart lives in `console-ui/src/api/client.ts` in another repo"

	if _, err := fl.CreatePostMergeReviewIssue(context.Background(), 42, "https://github.com/owner/repo/pull/42", 10, review); err != nil {
		t.Fatalf("create: %v", err)
	}
	in := mock.issues[0]
	counts := map[string]int{}
	for _, l := range in.Labels {
		counts[l]++
	}
	for _, want := range []string{"pilot", postMergeReviewLabel, postMergeReviewNoDecomposeLabel} {
		if counts[want] != 1 {
			t.Errorf("label %q appears %d times in %v, want exactly once", want, counts[want], in.Labels)
		}
	}
	defects := in.Body[strings.Index(in.Body, "## Defects"):strings.Index(in.Body, "## Reviewer Notes")]
	if strings.Contains(defects, "`") {
		t.Errorf("defects section contains a backtick:\n%s", defects)
	}
	if !strings.Contains(defects, "- at internal/foo/client.go:42 — ") {
		t.Errorf("location not rendered as plain text:\n%s", defects)
	}
	if strings.Contains(in.Body, "`") {
		t.Errorf("issue body contains a backtick:\n%s", in.Body)
	}
	if !strings.Contains(in.Body, "console-ui/src/api/client.ts") {
		t.Errorf("reviewer notes path missing from body:\n%s", in.Body)
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent slog writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPostMergeReviewCommentDiffLine_Trimmed(t *testing.T) {
	got := buildPostMergeReviewComment(&executor.PostMergeReview{
		Verdict:      executor.PostMergeApprove,
		DiffBytes:    32127,
		DiffFiles:    7,
		DiffTrimmed:  true,
		TrimmedFiles: []string{"cmd/a.go", "internal/b.go", "internal/c.go"},
	})
	want := "Diff: 32127 bytes, 7 files, trimmed: yes (3 files: cmd/a.go, internal/b.go, internal/c.go)"
	if !strings.Contains(got, want) {
		t.Fatalf("comment missing %q:\n%s", want, got)
	}
	if strings.Index(got, want) > strings.Index(got, "<sub>") {
		t.Errorf("Diff line must come before the footer:\n%s", got)
	}
}

func TestPostMergeReviewCommentDiffLine(t *testing.T) {
	many := make([]string, 12)
	for i := range many {
		many[i] = fmt.Sprintf("f%02d.go", i)
	}
	tests := []struct {
		name   string
		review *executor.PostMergeReview
		want   string
		absent string
	}{
		{
			name:   "untrimmed",
			review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove, DiffBytes: 32127, DiffFiles: 7},
			want:   "Diff: 32127 bytes, 7 files, trimmed: no",
			absent: "trimmed: yes",
		},
		{
			name:   "trimmed lists are capped",
			review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove, DiffBytes: 900000, DiffFiles: 40, DiffTrimmed: true, TrimmedFiles: many},
			want:   "trimmed: yes (12 files: f00.go, f01.go, f02.go, f03.go, f04.go, f05.go, f06.go, f07.go, f08.go, f09.go, and 2 more)",
		},
		{
			name:   "no diff info omits the line",
			review: &executor.PostMergeReview{Verdict: executor.PostMergeApprove},
			absent: "Diff:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPostMergeReviewComment(tt.review)
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("comment missing %q:\n%s", tt.want, got)
			}
			if tt.absent != "" && strings.Contains(got, tt.absent) {
				t.Errorf("comment unexpectedly contains %q:\n%s", tt.absent, got)
			}
			if tt.want != "" && !strings.HasSuffix(strings.TrimSpace(got), "not a substitute for it.</sub>") {
				t.Errorf("footer must stay last:\n%s", got)
			}
		})
	}
}

// GH-5946: an unknown verdict keeps the model text in the log (first 2000
// characters, Warn, with the PR number), counts in the unknown metric and posts
// nothing.
func TestPostMergeReview_UnknownLogsRawOutput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantLog string
		notLog  string
	}{
		{"short", "no verdict here", "no verdict here", ""},
		{"truncated to 2000", strings.Repeat("a", 2000) + "TAIL-MARKER", strings.Repeat("a", 2000), "TAIL-MARKER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, gh := newPMRGitHub(t)
			review := &executor.PostMergeReview{Verdict: executor.PostMergeUnknown, RawOutput: tt.raw}
			c := newPMRController(t, gh, &pmrFakeReviewer{review: review}, true)
			var logs lockedBuffer
			c.log = slog.New(slog.NewTextHandler(&logs, nil))

			if err := c.handleMerged(context.Background(), mergedPRState(77, 10)); err != nil {
				t.Fatalf("handleMerged: %v", err)
			}
			c.waitPostMergeReviews()

			out := logs.String()
			for _, want := range []string{"level=WARN", "verdict unknown", "pr=77", tt.wantLog} {
				if !strings.Contains(out, want) {
					t.Errorf("log missing %q:\n%s", want, out)
				}
			}
			if tt.notLog != "" && strings.Contains(out, tt.notLog) {
				t.Errorf("log carries more than 2000 characters of raw output")
			}
			if got := c.metrics.PostMergeReviewVerdicts[string(executor.PostMergeUnknown)]; got != 1 {
				t.Errorf("unknown metric = %d, want 1", got)
			}
			if mock.commentCount(77) != 0 || mock.issueCount() != 0 {
				t.Errorf("raw output reached GitHub")
			}
		})
	}
}

// GH-5947: a checkout review that read no file is counted under its own metric
// label, and the result line carries the turns used and files read.
func TestPostMergeReview_NoFilesReadMetricAndResultLine(t *testing.T) {
	mock, gh := newPMRGitHub(t)
	review := &executor.PostMergeReview{
		Verdict: executor.PostMergeApproveWithNotes, NoFilesRead: true, Checkout: true, Turns: 4,
		Notes: "reviewer read no files from the checkout; findings rest on the diff only",
	}
	c := newPMRController(t, gh, &pmrFakeReviewer{review: review}, true)
	var logs lockedBuffer
	c.log = slog.New(slog.NewTextHandler(&logs, nil))

	if err := c.handleMerged(context.Background(), mergedPRState(77, 10)); err != nil {
		t.Fatalf("handleMerged: %v", err)
	}
	c.waitPostMergeReviews()

	if got := c.metrics.PostMergeReviewNoFilesRead; got != 1 {
		t.Errorf("no_files_read metric = %d, want 1", got)
	}
	if got := c.metrics.PostMergeReviewVerdicts["no_files_read"]; got != 0 {
		t.Errorf("no_files_read must not be a verdict label, got %d", got)
	}
	if got := c.metrics.PostMergeReviewVerdicts[string(executor.PostMergeApproveWithNotes)]; got != 1 {
		t.Errorf("verdict metric = %d, want 1", got)
	}
	out := logs.String()
	for _, want := range []string{`msg="post-merge review"`, "turns=4", "files_read=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("result line missing %q:\n%s", want, out)
		}
	}
	if mock.issueCount() != 0 {
		t.Errorf("a downgraded review filed %d follow-up issues, want 0", mock.issueCount())
	}
}

func TestFormatPostMergeNotVerified_CountIsExtraBullet(t *testing.T) {
	got := formatPostMergeNotVerified([]string{"nv-1", "nv-2", "nv-3", "A: met. — not traced"})
	lines := strings.Split(strings.TrimSpace(got), "\n")
	want := []string{
		"- nv-1", "- nv-2", "- nv-3",
		"- … 1 requirements marked met but not traced to a file read omitted (capped at 3)",
	}
	if len(lines) != len(want) {
		t.Fatalf("rendered %d bullets, want %d:\n%s", len(lines), len(want), got)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("bullet %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestFormatPostMergeDefects_EmptyClaimNoDash(t *testing.T) {
	defects := []executor.PostMergeDefect{
		{Severity: executor.PostMergeSeverityMinor, Location: "a.go:3", Evidence: "see b.go:9"},
		{Severity: executor.PostMergeSeverityBlocking, Location: "c.go:4"},
		{Severity: executor.PostMergeSeverityMinor, Location: "d.go:5", Claim: "real claim", Evidence: "ev"},
	}
	got := formatPostMergeDefects(defects, true)
	want := "- [minor] at a.go:3\n  Evidence: see b.go:9\n" +
		"- [blocking] at c.go:4\n" +
		"- [minor] at d.go:5 — real claim\n  Evidence: ev\n"
	if got != want {
		t.Errorf("formatPostMergeDefects =\n%q\nwant\n%q", got, want)
	}
	issue := formatPostMergeIssueDefects(defects[:2])
	if strings.Contains(issue, "—") {
		t.Errorf("issue defects carry a dash for an empty claim:\n%s", issue)
	}
}

func TestCutPostMergeDefects_MarkerOnce(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
	}{
		{"cut at a word end", strings.Repeat("word ", 40), 50},
		{"no boundary keeps the leading content", strings.Repeat("x", 200), 60},
		{"no room for content leaves the marker alone", strings.Repeat("x", 200), 5},
		{"cut inside a details block", "- at a.go:1 — c\n  <details><summary>s</summary>\n\n  " + strings.Repeat("evidence ", 30), 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cutPostMergeDefects(tt.in, tt.max)
			if n := strings.Count(got, "(truncated)"); n != 1 {
				t.Errorf("marker appears %d times in %q", n, got)
			}
			if !strings.HasSuffix(got, executor.PostMergeTruncatedMarker+"\n") {
				t.Errorf("section does not end in the marker and a newline: %q", got)
			}
			if strings.Contains(got, "<details>") && strings.Count(got, "<details>") != strings.Count(got, "</details>") {
				t.Errorf("details block left open: %q", got)
			}
		})
	}
	if got := cutPostMergeDefects("short", 50); got != "short" {
		t.Errorf("a section within max changed: %q", got)
	}
}
