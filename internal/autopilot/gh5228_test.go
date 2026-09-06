package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5329 (TASK-493 phase 4): tests for the review-trigger hardening work
// that originated from external report #5228 (feature declined as security
// by design; two real defects credited — see
// .agent/knowledge/memories/decisions/pilot-never-reads-gh-comments-by-design.md
// and .agent/tasks/TASK-493-review-trigger-hardening.md). This file covers:
//
//  1. Reviewer-trust parity between the webhook path (OnReviewRequested) and
//     the polling path (hasChangesRequested) for [bot]/-bot/own-login/human
//     reviewers (GH-5326/#5331).
//  2. Stage-eligibility guard parity between both paths (GH-5327/#5332).
//  3. Revision-issue body scoping to only the triggering human reviewer's
//     content (GH-5328).
//
// gh5266_test.go (cutoff/COMMENTED hardening) and gh5327_test.go
// (reviewTriggerEligible + webhook stage guard) are untouched by this file.

// TestReviewTriggerReviewerTrust_WebhookPollingParity is table-driven over
// the reviewer identities isTrustedReviewer must reject or accept, and
// asserts the webhook path (OnReviewRequested) and the polling path
// (hasChangesRequested) reach the *same* verdict for the identical review
// payload — the whole point of GH-5326/#5331 extracting one shared
// predicate instead of two independently-maintained checks.
func TestReviewTriggerReviewerTrust_WebhookPollingParity(t *testing.T) {
	const botLogin = "pilot-bot"

	tests := []struct {
		name         string
		reviewer     string
		wantEligible bool
	}{
		{"bracket-bot suffix rejected", "dependabot[bot]", false},
		{"dash-bot suffix rejected", "ci-bot", false},
		{"own login rejected (identity, exact case)", "pilot-bot", false},
		{"own login rejected (identity, case-insensitive)", "Pilot-Bot", false},
		{"human reviewer accepted", "alice", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/pulls/42/reviews":
					resp := []*github.PullRequestReview{
						{ID: 1, User: github.User{Login: tt.reviewer}, Body: "please fix", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(time.Hour).Format(time.RFC3339)},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			cfg := DefaultConfig()
			cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

			// --- Webhook path: OnReviewRequested drives the transition directly. ---
			webhookClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			webhookController := NewController(cfg, webhookClient, nil, "owner", "repo")
			webhookController.cachedBotLogin = botLogin
			webhookController.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
			webhookController.OnReviewRequested(42, "submitted", "changes_requested", tt.reviewer)
			webhookPR, ok := webhookController.GetPRState(42)
			if !ok {
				t.Fatal("webhook PR should be tracked")
			}
			webhookAccepted := webhookPR.Stage == StageReviewRequested

			// --- Polling path: hasChangesRequested evaluates the same payload. ---
			pollingClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			pollingController := NewController(cfg, pollingClient, nil, "owner", "repo")
			pollingController.cachedBotLogin = botLogin
			pollingPRState := &PRState{PRNumber: 42, CreatedAt: time.Now()}
			pollingAccepted := pollingController.hasChangesRequested(context.Background(), pollingPRState, nil)

			if webhookAccepted != tt.wantEligible {
				t.Errorf("webhook path: accepted = %v, want %v", webhookAccepted, tt.wantEligible)
			}
			if pollingAccepted != tt.wantEligible {
				t.Errorf("polling path: accepted = %v, want %v", pollingAccepted, tt.wantEligible)
			}
			if webhookAccepted != pollingAccepted {
				t.Errorf("parity violated for reviewer %q: webhook accepted=%v, polling accepted=%v", tt.reviewer, webhookAccepted, pollingAccepted)
			}
		})
	}
}

// TestReviewTriggerStageGuard_WebhookPolling covers GH-5327/#5332's stage
// eligibility guard on both trigger paths: a PR parked in StageAwaitApproval
// must not be yanked out by a changes_requested review via either path,
// while a PR in an eligible pre-review stage still transitions.
func TestReviewTriggerStageGuard_WebhookPolling(t *testing.T) {
	t.Run("webhook: awaiting approval leaves stage unchanged", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}
		ghClient := github.NewClient(testutil.FakeGitHubToken)
		c := NewController(cfg, ghClient, nil, "owner", "repo")

		c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
		pr, ok := c.GetPRState(42)
		if !ok {
			t.Fatal("PR should be tracked")
		}
		pr.mu.Lock()
		pr.Stage = StageAwaitApproval
		pr.mu.Unlock()

		c.OnReviewRequested(42, "submitted", "changes_requested", "alice")

		pr, _ = c.GetPRState(42)
		pr.mu.Lock()
		gotStage := pr.Stage
		pr.mu.Unlock()
		if gotStage != StageAwaitApproval {
			t.Errorf("stage = %q, want %q (a review must not pull a PR out of awaiting_approval)", gotStage, StageAwaitApproval)
		}
	})

	t.Run("webhook: eligible stage transitions to review_requested", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}
		ghClient := github.NewClient(testutil.FakeGitHubToken)
		c := NewController(cfg, ghClient, nil, "owner", "repo")

		c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
		pr, ok := c.GetPRState(42)
		if !ok {
			t.Fatal("PR should be tracked")
		}
		pr.mu.Lock()
		pr.Stage = StageWaitingCI
		pr.mu.Unlock()

		c.OnReviewRequested(42, "submitted", "changes_requested", "alice")

		pr, _ = c.GetPRState(42)
		pr.mu.Lock()
		gotStage := pr.Stage
		pr.mu.Unlock()
		if gotStage != StageReviewRequested {
			t.Errorf("stage = %q, want %q", gotStage, StageReviewRequested)
		}
	})

	t.Run("polling: awaiting approval leaves stage unchanged", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
				resp := github.PullRequest{Number: 42, State: "open", HTMLURL: "https://github.com/owner/repo/pull/42", Head: github.PRRef{SHA: "sha1"}}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mustJSON(t, resp))
			case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
				resp := []*github.PullRequestReview{
					{ID: 1, User: github.User{Login: "alice"}, Body: "please fix", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(time.Minute).Format(time.RFC3339)},
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mustJSON(t, resp))
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

		prState := &PRState{
			PRNumber:   42,
			PRURL:      "https://github.com/owner/repo/pull/42",
			BranchName: "pilot/GH-10",
			Stage:      StageAwaitApproval,
			CreatedAt:  time.Now(),
		}
		c.mu.Lock()
		c.activePRs[42] = prState
		c.mu.Unlock()

		c.processAllPRs(context.Background())

		got, ok := c.GetPRState(42)
		if !ok {
			t.Fatal("PR should still be tracked")
		}
		got.mu.Lock()
		gotStage := got.Stage
		got.mu.Unlock()
		if gotStage != StageAwaitApproval {
			t.Errorf("stage = %q, want %q (a review must not pull a PR out of awaiting_approval)", gotStage, StageAwaitApproval)
		}
	})

	t.Run("polling: eligible stage transitions to review_requested", func(t *testing.T) {
		// The polling loop's changes_requested detection and the immediate
		// ProcessPR dispatch that follows it in the same tick both read
		// /pulls/42/reviews (the first read is hasChangesRequested's guard
		// check, the second is handleReviewRequested building the revision
		// issue once the stage has already flipped). Fail only the second
		// read so this test observes the flip in isolation, without also
		// asserting on handleReviewRequested's own downstream resolution
		// (which — by GH-5328 design — always moves a successfully-processed
		// StageReviewRequested PR on to StageFailed/terminal-superseded,
		// a separate, already-covered behavior in gh5328_test.go).
		var reviewCalls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
				resp := github.PullRequest{Number: 42, State: "open", HTMLURL: "https://github.com/owner/repo/pull/42", Head: github.PRRef{SHA: "sha1"}}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mustJSON(t, resp))
			case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
				if atomic.AddInt32(&reviewCalls, 1) > 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"message":"boom"}`))
					return
				}
				resp := []*github.PullRequestReview{
					{ID: 1, User: github.User{Login: "alice"}, Body: "please fix", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(time.Minute).Format(time.RFC3339)},
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mustJSON(t, resp))
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

		prState := &PRState{
			PRNumber:   42,
			PRURL:      "https://github.com/owner/repo/pull/42",
			BranchName: "pilot/GH-10",
			Stage:      StageWaitingCI,
			CreatedAt:  time.Now(),
		}
		c.mu.Lock()
		c.activePRs[42] = prState
		c.mu.Unlock()

		c.processAllPRs(context.Background())

		got, ok := c.GetPRState(42)
		if !ok {
			t.Fatal("PR should still be tracked")
		}
		got.mu.Lock()
		gotStage := got.Stage
		got.mu.Unlock()
		if gotStage != StageReviewRequested {
			t.Errorf("stage = %q, want %q (an eligible stage must transition on a trusted human changes_requested review)", gotStage, StageReviewRequested)
		}
	})
}

// TestReviewFeedbackBody_OnlyFirstHumanTriggeringReviewer covers GH-5328:
// with a human's CHANGES_REQUESTED review, a bot's COMMENTED review, a
// second human's COMMENTED-only review, and line comments from all three
// present on the PR, the revision-issue body must contain only the first
// (triggering) human's review body and line comment — nothing from the bot
// or from the second human, who never requested changes.
func TestReviewFeedbackBody_OnlyFirstHumanTriggeringReviewer(t *testing.T) {
	var issueBody string
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/601/reviews":
			resp := []*github.PullRequestReview{
				// dana: the first human, and the only one who requested changes.
				{ID: 1, User: github.User{Login: "dana"}, Body: "dana-changes-requested-body", State: "CHANGES_REQUESTED", SubmittedAt: time.Now().Add(-10 * time.Minute).Format(time.RFC3339)},
				// a bot: comments only, must never be able to trigger or appear.
				{ID: 2, User: github.User{Login: "lint-bot"}, Body: "bot-commented-body", State: "COMMENTED", SubmittedAt: time.Now().Add(-9 * time.Minute).Format(time.RFC3339)},
				// erin: a second human, but she only ever commented — never blocking.
				{ID: 3, User: github.User{Login: "erin"}, Body: "erin-commented-body", State: "COMMENTED", SubmittedAt: time.Now().Add(-8 * time.Minute).Format(time.RFC3339)},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/601/comments":
			resp := []*github.PRReviewComment{
				{ID: 10, Body: "dana-line-comment", Path: "foo.go", Line: 3, User: github.User{Login: "dana"}},
				{ID: 11, Body: "bot-line-comment", Path: "foo.go", Line: 7, User: github.User{Login: "lint-bot"}},
				{ID: 12, Body: "erin-line-comment", Path: "bar.go", Line: 12, User: github.User{Login: "erin"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueBody = body["body"]
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 950}))
		case r.URL.Path == "/repos/owner/repo/pulls/601" && r.Method == http.MethodPatch:
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
		PRNumber:   601,
		BranchName: "pilot/GH-601",
		Stage:      StageReviewRequested,
		CreatedAt:  time.Now().Add(-1 * time.Hour),
	}

	if err := c.handleReviewRequested(context.Background(), prState); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Fatal("expected a revision issue to be created for dana's triggering CHANGES_REQUESTED review")
	}

	if !strings.Contains(issueBody, "dana-changes-requested-body") {
		t.Error("issue body must contain dana's (the triggering human's) review body")
	}
	if !strings.Contains(issueBody, "dana-line-comment") {
		t.Error("issue body must contain dana's line comment")
	}
	if strings.Contains(issueBody, "bot-commented-body") {
		t.Error("issue body must NOT contain the bot's COMMENTED review body")
	}
	if strings.Contains(issueBody, "bot-line-comment") {
		t.Error("issue body must NOT contain the bot's line comment")
	}
	if strings.Contains(issueBody, "erin-commented-body") {
		t.Error("issue body must NOT contain erin's COMMENTED-only review body — she never requested changes")
	}
	if strings.Contains(issueBody, "erin-line-comment") {
		t.Error("issue body must NOT contain erin's line comment — she is not a triggering reviewer")
	}
}
