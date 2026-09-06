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

// GH-5337 (TASK-493 post-merge review follow-up, defect 1): handleReviewRequested
// anchored its reviewer cutoff on prState.CreatedAt directly, reopening the
// GH-5266 blind spot one hop downstream of hasChangesRequested's fix. Both the
// polling gate (hasChangesRequested) and the merge-time guard already prefer
// the GitHub PR's own, durable CreatedAt over prState.CreatedAt (which
// OnPRCreated/the reconciler's re-adoption sweep always stamp to time.Now()).
// This test reproduces the full failure sequence end to end: adopt a PR whose
// prState.CreatedAt lands after a standing CHANGES_REQUESTED review, let the
// polling gate flip the stage (as it already correctly does), and assert
// handleReviewRequested — running immediately after in the same tick — still
// sees that review as triggering and creates the revision issue, instead of
// filtering it out and landing on the empty-after-filter branch.
func TestGH5337_ReAdoptedPR_HandleReviewRequestedUsesGitHubCreatedAt(t *testing.T) {
	var issueCreated bool
	var issueBody string
	var prClosed bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5337" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:    5337,
				State:     "open",
				HTMLURL:   "https://github.com/owner/repo/pull/5337",
				Head:      github.PRRef{SHA: "sha5337"},
				Base:      github.PRRef{Ref: "main"},
				CreatedAt: "2026-08-01T00:00:00Z", // real, durable PR creation time
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5337/reviews":
			// Standing review: submitted after the PR's real creation, but
			// before prState.CreatedAt (stamped to time.Now() by the
			// re-adoption this test simulates via OnPRCreated).
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, Body: "please fix this", State: "CHANGES_REQUESTED", SubmittedAt: "2026-08-15T00:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5337/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))

		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueBody = body["body"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 950}))

		case r.URL.Path == "/repos/owner/repo/pulls/5337" && r.Method == http.MethodPatch:
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

	// Simulate re-adoption: OnPRCreated always stamps CreatedAt to time.Now(),
	// long after the standing review above but the PR itself already exists
	// on GitHub with an earlier CreatedAt.
	c.OnPRCreated(5337, "https://github.com/owner/repo/pull/5337", 700, "sha5337", "pilot/GH-700", "")
	pr, ok := c.GetPRState(5337)
	if !ok {
		t.Fatal("PR should be tracked")
	}
	pr.mu.Lock()
	pr.Stage = StageWaitingCI
	pr.mu.Unlock()

	// One polling tick: hasChangesRequested must flip the stage (GH-5266,
	// already correct), and ProcessPR's subsequent handleReviewRequested call
	// in the same tick must not filter the same review back out (GH-5337).
	c.processAllPRs(context.Background())

	if !issueCreated {
		t.Fatal("expected handleReviewRequested to create a revision issue for the standing review — it must not be filtered out by a prState.CreatedAt-anchored cutoff (GH-5337)")
	}
	if !prClosed {
		t.Error("expected the PR to be closed once the revision issue was created")
	}
	if issueBody == "" || !strings.Contains(issueBody, "please fix this") {
		t.Errorf("issue body = %q, want it to contain the standing review's body", issueBody)
	}

	got, ok := c.GetPRState(5337)
	if !ok {
		t.Fatal("PR should still be tracked (escalateAndHold/close paths keep it tracked until externally observed closed)")
	}
	got.mu.Lock()
	defer got.mu.Unlock()
	if got.Stage != StageFailed {
		t.Errorf("stage = %s, want %s (handleReviewRequested's healthy hand-off terminal state)", got.Stage, StageFailed)
	}
	if got.ReviewFilterEmptyPasses != 0 {
		t.Errorf("ReviewFilterEmptyPasses = %d, want 0 — this pass found triggering feedback, it must not count as empty", got.ReviewFilterEmptyPasses)
	}
}

// TestGH5337_HandleReviewRequested_RepeatedEmptyAfterFilter_Escalates covers
// defect 1's second half: a single empty-after-filter pass must stay quiet
// (matches gh5328_test.go's existing
// TestGH5328_HandleReviewRequested_EmptyAfterFilter_SkipsIssueCreation), but
// once the same PR racks up reviewFilterEmptyEscalateThreshold consecutive
// empty passes, handleReviewRequested must escalate via escalateAndHold
// (pilot-needs-human label, StageFailed) instead of leaving the PR parked in
// StageReviewRequested forever with no visibility.
func TestGH5337_HandleReviewRequested_RepeatedEmptyAfterFilter_Escalates(t *testing.T) {
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5338/reviews":
			// Every review is bot-authored — untrusted, filtered out on every pass.
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "ci-review[bot]"}, Body: "bot-changes-requested", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(-time.Minute).Format(time.RFC3339)},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/5338/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/700/labels" && r.Method == http.MethodPost:
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body.Labels...)
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
		PRNumber:    5338,
		IssueNumber: 700,
		BranchName:  "pilot/GH-700",
		Stage:       StageReviewRequested,
		CreatedAt:   time.Now().Add(-2 * time.Hour),
	}

	for i := 1; i < reviewFilterEmptyEscalateThreshold; i++ {
		if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
			t.Fatalf("pass %d: handleReviewRequested returned unexpected error: %v", i, err)
		}
		if prState.Stage != StageReviewRequested {
			t.Fatalf("pass %d: stage = %s, want %s (must not escalate before the threshold)", i, prState.Stage, StageReviewRequested)
		}
		if len(labelsAdded) != 0 {
			t.Fatalf("pass %d: labels added = %v, want none before the threshold", i, labelsAdded)
		}
	}

	// Final pass crosses the threshold.
	if err := c.handleReviewRequested(context.Background(), prState, nil); err != nil {
		t.Fatalf("final pass: handleReviewRequested returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s (escalated and held)", prState.Stage, StageFailed)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q label on the issue after repeated empty passes, got labels: %v", labelNeedsHuman, labelsAdded)
	}
}

// TestGH5337_IsTrustedReviewer_OwnLoginRejected_WithoutExternalClose covers
// defect 2: isTrustedReviewer's own-login check previously stayed dormant
// (string-pattern-only) for a controller's entire process lifetime unless
// notifyExternalClose's unrelated external-PR-close path happened to warm
// cachedBotLogin first. hasChangesRequested now warms it directly (and
// Start does too) — this test drives a freshly constructed controller
// through hasChangesRequested only, with no external close anywhere in the
// scenario, and asserts the own-login half of isTrustedReviewer is already
// live afterward.
func TestGH5337_IsTrustedReviewer_OwnLoginRejected_WithoutExternalClose(t *testing.T) {
	// Deliberately does not match the "[bot]"/"-bot" suffix pattern, so the
	// only way isTrustedReviewer can reject it is via the cached-identity
	// check this test exercises.
	const botLogin = "quantflow-pilot-svc"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.User{Login: botLogin}))
		case "/repos/owner/repo/pulls/9001/reviews":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	// Sanity: nothing has warmed the cache yet on a freshly constructed
	// controller — the own-login half of isTrustedReviewer is dormant.
	if !c.isTrustedReviewer(botLogin) {
		t.Fatal("precondition failed: own login should not yet be rejectable before any warm-up")
	}

	prState := &PRState{PRNumber: 9001, CreatedAt: time.Now().Add(-time.Hour)}
	c.hasChangesRequested(context.Background(), prState, nil)

	if c.isTrustedReviewer(botLogin) {
		t.Error("isTrustedReviewer should reject the controller's own login once hasChangesRequested has warmed cachedBotLogin — no external PR close ever happened in this scenario")
	}
}
