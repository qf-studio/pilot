package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5328: handleReviewRequested used to hand formatReviewFeedback every
// review/comment ever left on the PR — including stale CHANGES_REQUESTED
// reviews the reviewer later superseded with an APPROVED, bot noise, and
// other reviewers' unrelated comments. These tests cover the fix: the
// revision-issue body must be scoped to only the trusted reviewer(s) whose
// latest review since the PR's CreatedAt cutoff is CHANGES_REQUESTED, and
// issue creation must be skipped entirely (not filed empty) when filtering
// leaves nothing.

// TestGH5328_HandleReviewRequested_FiltersToTriggeringReviewer covers the
// non-empty case: alice's live CHANGES_REQUESTED review/comment must survive
// into the issue body, while bob's stale CHANGES_REQUESTED (superseded by his
// own later APPROVED) and carol's unrelated COMMENTED-only feedback must not.
func TestGH5328_HandleReviewRequested_FiltersToTriggeringReviewer(t *testing.T) {
	var issueBody string
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/501/reviews":
			resp := []*github.PullRequestReview{
				// bob requested changes first, then approved — his latest
				// state is APPROVED, so he must not trigger or appear.
				{ID: 1, User: github.User{Login: "bob"}, Body: "bob-changes-requested-stale", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(-time.Hour).Format(time.RFC3339)},
				{ID: 2, User: github.User{Login: "bob"}, Body: "", State: "APPROVED", SubmittedAt: time.Now().Add(-30 * time.Minute).Format(time.RFC3339)},
				// carol only ever commented — never blocking.
				{ID: 3, User: github.User{Login: "carol"}, Body: "carol-just-commented", State: "COMMENTED", SubmittedAt: time.Now().Add(-20 * time.Minute).Format(time.RFC3339)},
				// alice is the live, current CHANGES_REQUESTED review.
				{ID: 4, User: github.User{Login: "alice"}, Body: "alice-live-changes-requested", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339)},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/501/comments":
			resp := []*github.PRReviewComment{
				{ID: 10, Body: "alice-line-comment", Path: "foo.go", Line: 5, User: github.User{Login: "alice"}},
				{ID: 11, Body: "carol-line-comment", Path: "bar.go", Line: 9, User: github.User{Login: "carol"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueBody = body["body"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 900}))
		case r.URL.Path == "/repos/owner/repo/pulls/501" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
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
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	prState := &PRState{
		PRNumber:   501,
		BranchName: "pilot/GH-501",
		Stage:      StageReviewRequested,
		CreatedAt:  time.Now().Add(-2 * time.Hour),
	}

	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Fatal("expected a revision issue to be created for alice's live CHANGES_REQUESTED review")
	}

	if !strings.Contains(issueBody, "alice-live-changes-requested") {
		t.Error("issue body must contain alice's live review body")
	}
	if !strings.Contains(issueBody, "alice-line-comment") {
		t.Error("issue body must contain alice's line comment")
	}
	if strings.Contains(issueBody, "bob-changes-requested-stale") {
		t.Error("issue body must NOT contain bob's stale CHANGES_REQUESTED review — he later approved")
	}
	if strings.Contains(issueBody, "carol-just-commented") {
		t.Error("issue body must NOT contain carol's COMMENTED-only review — she never requested changes")
	}
	if strings.Contains(issueBody, "carol-line-comment") {
		t.Error("issue body must NOT contain carol's line comment — she is not a triggering reviewer")
	}
}

// TestGH5328_HandleReviewRequested_EmptyAfterFilter_SkipsIssueCreation covers
// the empty case: every review is bot-authored (untrusted), so
// triggeringReviewers filters everything out — no issue must be filed, and
// the PR must be left alone (not closed) rather than churning an empty issue.
func TestGH5328_HandleReviewRequested_EmptyAfterFilter_SkipsIssueCreation(t *testing.T) {
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/502/reviews":
			resp := []*github.PullRequestReview{
				// bot-authored CHANGES_REQUESTED — untrusted, must be excluded.
				{ID: 1, User: github.User{Login: "ci-review[bot]"}, Body: "bot-changes-requested", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(-time.Minute).Format(time.RFC3339)},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/502/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 901}))
		case r.URL.Path == "/repos/owner/repo/pulls/502" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	prState := &PRState{
		PRNumber:   502,
		BranchName: "pilot/GH-502",
		Stage:      StageReviewRequested,
		CreatedAt:  time.Now().Add(-2 * time.Hour),
	}

	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("expected no revision issue to be created when filtering leaves no triggering reviewer feedback")
	}
	if prClosed {
		t.Error("expected the PR to be left open when no issue was created (not silently closed with no follow-up)")
	}
	if prState.Stage != StageReviewRequested {
		t.Errorf("stage = %s, want %s (should be left untouched, not escalated, when there's nothing to act on)", prState.Stage, StageReviewRequested)
	}
}
