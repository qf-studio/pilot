package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4860: post-merge review of PR#4858 (GH-4856) found three remaining
// gaps. These tests cover the fixes named in the review verdict:
//
//  1. D1: the "(0, nil)" half of handleReviewRequested's guard
//     (`if err != nil || issueNum <= 0`, controller.go:3145) was unreachable
//     by the suite — a seeded claim with no recorded issue number is the
//     real-world producer of that shape (crash between CreatePilotIssue
//     succeeding and RecordSpawnedFixIssue landing, or a concurrent claim in
//     flight).
//  2. N1: CreateFailureIssue's create-error path must release its claim on
//     the CI path too, mirroring CreateReviewIssue's GH-4856 fix, so a
//     transient create error doesn't poison the dedup key forever.

// TestHandleReviewRequested_SeededClaimWithoutRecord_EscalatesAndHolds
// reproduces D1's real-world (0, nil) producer directly: a claim landed
// (ClaimSpawnedFix succeeded) but no issue number was ever recorded — the
// exact shape left behind by a crash between CreatePilotIssue succeeding and
// RecordSpawnedFixIssue, or a concurrent claim still in flight. Mutating
// controller.go:3145's `if err != nil || issueNum <= 0` down to `if err !=
// nil` must fail this test: CreateReviewIssue returns (0, nil) for this
// shape (feedback_loop.go's "existing <= 0: return existing, nil" branch),
// so the mutated guard would fall through and close the PR / delete the
// branch, discarding the review round — the original GH-4856 bug.
func TestHandleReviewRequested_SeededClaimWithoutRecord_EscalatesAndHolds(t *testing.T) {
	var prClosed, branchDeleted atomic.Bool
	var createCalls atomic.Int64
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/91/reviews" && r.Method == http.MethodGet:
			resp := []*github.PullRequestReview{{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls/91/comments") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			// Must never be called: the claim above already landed, so
			// CreateReviewIssue's dedup-hit "existing <= 0" branch must
			// return (0, nil) without ever attempting a create.
			createCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 701}))
		case r.URL.Path == "/repos/owner/repo/issues/40/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/pulls/91" && r.Method == http.MethodPatch:
			prClosed.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber:    91,
		IssueNumber: 40,
		BranchName:  "pilot/GH-40",
		Stage:       StageReviewRequested,
	}

	// Seed the durable claim WITHOUT recording an issue number — the shape
	// left by a crash between CreatePilotIssue succeeding and
	// RecordSpawnedFixIssue landing, or a concurrent claim still in flight.
	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}

	if err := c.handleReviewRequested(context.Background(), prState); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if createCalls.Load() != 0 {
		t.Errorf("expected no issue creation attempt (dedup-hit existing<=0 branch), got %d create calls", createCalls.Load())
	}
	if prClosed.Load() {
		t.Error("PR must NOT be closed when the review-issue owner is a poisoned (0, nil) claim — hold via escalateAndHold instead (GH-4860 D1)")
	}
	if branchDeleted.Load() {
		t.Error("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty — no review issue was ever created", prState.TerminalLabel)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue (escalate-and-hold), got labels: %v", labelsAdded)
	}
	if len(sink.events) == 0 {
		t.Error("expected an escalation alert to fire")
	}
}

// TestCreateReviewIssue_DedupHit_ClarificationOwner_ReusesExisting covers
// GH-4860 D2 for the review path: an open + pilot-needs-clarification
// previously-designated review issue is the documented-resumable
// preflight-decline state (notifier.go, epic.go), not abandonment. The
// dedup re-check must reuse it, not mint a replacement — replacing here
// would move the claim, leave the declined issue open as a zombie, and once
// the clarification label is removed, dispatch both issues against review
// issues' shared `branch:` meta, clobbering each other.
func TestCreateReviewIssue_DedupHit_ClarificationOwner_ReusesExisting(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/102" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: 102,
				State:  github.StateOpen,
				Labels: []github.Label{{Name: github.LabelNeedsClarification}},
			})
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			createCalls++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 999})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)
	store := newTestStateStore(t)
	fl.SetStateStore(store)
	sink := &fakeAlertSink{}
	fl.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 79, IssueNumber: 14, BranchName: "pilot/GH-14"}

	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, 102); err != nil {
		t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
	}

	got, err := fl.CreateReviewIssue(context.Background(), prState, nil, nil, 1)
	if err != nil {
		t.Fatalf("CreateReviewIssue error: %v", err)
	}
	if got != 102 {
		t.Errorf("CreateReviewIssue() = %d, want 102 (resumable clarification owner reused, not replaced)", got)
	}
	if createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 (must not mint a replacement for a resumable clarification owner)", createCalls)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no owner-death alert (reuse, not replace), got %d: %v", len(sink.events), sink.events)
	}

	recorded, err := store.GetSpawnedFixIssue(dedupRepo, dedupKey)
	if err != nil {
		t.Fatalf("GetSpawnedFixIssue: %v", err)
	}
	if recorded != 102 {
		t.Errorf("claim row = %d, want unchanged at 102 (no replace)", recorded)
	}
}

// TestCreateFailureIssue_CreateError_ReleasesClaimForRetry covers GH-4860
// N1: the CI path's create-error branch must release the claim it took, not
// leave it poisoned at issue_number=0 forever — mirroring CreateReviewIssue's
// GH-4856 fix. Without the release, every subsequent tick for this PR would
// hit the dedup-hit "existing <= 0" branch and return (0, nil) permanently,
// with no way to ever record a real issue number.
func TestCreateFailureIssue_CreateError_ReleasesClaimForRetry(t *testing.T) {
	var failCreate atomic.Bool
	failCreate.Store(true)
	var createdIssueNum atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
			// GH-4309 belt-and-suspenders search — no match.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Issue{})
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			if failCreate.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"internal server error"}`))
				return
			}
			createdIssueNum.Store(801)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 801})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	fl := NewFeedbackLoop(ghClient, "owner", "repo", cfg)
	store := newTestStateStore(t)
	fl.SetStateStore(store)

	prState := &PRState{PRNumber: 92, HeadSHA: "def5678"}
	failedChecks := []string{"test"}

	// First attempt: CreatePilotIssue fails transiently.
	got, err := fl.CreateFailureIssue(context.Background(), prState, FailureCIPreMerge, failedChecks, "", 0)
	if err == nil {
		t.Fatalf("CreateFailureIssue() error = nil, want create-error to propagate")
	}
	if got != 0 {
		t.Errorf("CreateFailureIssue() = %d, want 0 on create error", got)
	}

	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureCIPreMerge, failedChecks)
	if stored, lookupErr := store.GetSpawnedFixIssue(dedupRepo, dedupKey); lookupErr == nil && stored != 0 {
		t.Errorf("claim row should have been released after create failure, got stored=%d", stored)
	}

	// Second attempt: the transient failure clears. The claim must have been
	// released so this retry can claim fresh and create successfully instead
	// of hitting a poisoned dedup-hit branch.
	failCreate.Store(false)
	got, err = fl.CreateFailureIssue(context.Background(), prState, FailureCIPreMerge, failedChecks, "", 0)
	if err != nil {
		t.Fatalf("CreateFailureIssue() retry error = %v", err)
	}
	if got != 801 || createdIssueNum.Load() != 801 {
		t.Errorf("CreateFailureIssue() retry = %d, want 801 (clean retry after claim release)", got)
	}
}
