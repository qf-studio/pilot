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

// GH-4856: post-merge review of PR#4854 (GH-4852) found a regression at an
// unguarded caller plus two owner-health gaps. These tests cover the three
// fixes named in the review verdict:
//
//  1. handleReviewRequested must not close the PR/delete the branch when
//     CreateReviewIssue returns (0, nil) or an error — it must escalate and
//     hold instead (mirroring GH-4459's CI-path guard), and the claim row
//     must not stay poisoned forever after a transient create failure.
//  2. CreateReviewIssue's dedup-hit branch must re-check owner health before
//     handing back an existing review issue, mirroring CreateFailureIssue's
//     GH-4842 dedup-path re-check.
//  3. classifyOwnerHealth must treat an open, pilot-needs-clarification fix
//     issue (preflight-declined) as dead, not alive.

// TestHandleReviewRequested_CreateIssueErrors_EscalatesInsteadOfClosing covers
// a transient CreatePilotIssue error on a review round: the PR must not be
// closed or the branch deleted, and the tick must escalate-and-hold instead.
// A later successful create (once the transient failure clears) must then
// proceed normally — the claim row must not be left poisoned by the first
// failed attempt.
func TestHandleReviewRequested_CreateIssueErrors_EscalatesInsteadOfClosing(t *testing.T) {
	var prClosed, branchDeleted atomic.Bool
	var failCreate atomic.Bool
	failCreate.Store(true)
	var labelsAdded []string
	var createdIssueNum atomic.Int64

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
			if failCreate.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"internal server error"}`))
				return
			}
			resp := github.Issue{Number: 701}
			createdIssueNum.Store(701)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
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
		// GH-4872: safeDeleteBranch's branchIsBaseOfOpenPR check calls
		// ListPullRequests (GET .../pulls?state=open) before deleting — "{}"
		// fails to decode into []*PullRequest and fails the guard closed, so
		// the delete never fires. Reply "[]" (zero open PRs).
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
	cfg.Environment = EnvStage

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	prState := &PRState{
		PRNumber:    91,
		IssueNumber: 40,
		BranchName:  "pilot/GH-40",
		Stage:       StageReviewRequested,
	}

	// First tick: CreatePilotIssue fails transiently.
	if err := c.handleReviewRequested(context.Background(), prState); err != nil {
		t.Fatalf("handleReviewRequested returned unexpected error: %v", err)
	}

	if prClosed.Load() {
		t.Error("PR must NOT be closed when the review issue fails to create — hold via escalateAndHold instead (GH-4856/GH-4459)")
	}
	if branchDeleted.Load() {
		t.Error("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if c.consumeSelfClosedMarker(91) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "review-fix continuation declined at preflight" {
		t.Errorf("Error = %q, want %q", prState.Error, "review-fix continuation declined at preflight")
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
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}

	// Second tick: the transient failure clears. The claim row from the first
	// attempt must have been released, not left poisoned at issue_number=0
	// forever — otherwise this retry would hit the dedup-hit branch and get
	// back (0, nil) again, looping on escalateAndHold indefinitely.
	failCreate.Store(false)
	prState.Stage = StageReviewRequested
	prState.Error = ""

	if err := c.handleReviewRequested(context.Background(), prState); err != nil {
		t.Fatalf("handleReviewRequested (retry) returned unexpected error: %v", err)
	}

	if createdIssueNum.Load() != 701 {
		t.Fatalf("expected retry to successfully create issue #701, got %d", createdIssueNum.Load())
	}
	if !prClosed.Load() {
		t.Error("expected PR to be closed once the review issue was successfully created on retry")
	}
	if !branchDeleted.Load() {
		t.Error("expected branch to be deleted once the review issue was successfully created on retry")
	}
	if prState.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q after a successful review-issue create", prState.TerminalLabel, github.LabelFailed)
	}
}

// TestCreateReviewIssue_DedupHit_DeadOwner_MintsReplacement covers the
// dedup-hit branch of CreateReviewIssue: a durably-claimed review issue that
// has since closed without shipping (a human closed it during a crash/
// downtime window) must not be handed back out unverified — dedup must
// re-check owner health (mirroring CreateFailureIssue's GH-4842 path) and
// mint a replacement instead, so spawnReviewIssue never points TerminalLabel
// at a corpse.
func TestCreateReviewIssue_DedupHit_DeadOwner_MintsReplacement(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/100" && r.Method == http.MethodGet:
			// The previously-designated review issue: closed without ever
			// shipping (no pilot-done label) — dead.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 100, State: github.StateClosed})
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			createCalls++
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
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

	prState := &PRState{PRNumber: 77, IssueNumber: 12, BranchName: "pilot/GH-12"}

	// Seed a durable claim naming the now-dead review issue #100, simulating
	// a prior review round whose designated owner has since died.
	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, 100); err != nil {
		t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
	}

	got, err := fl.CreateReviewIssue(context.Background(), prState, nil, nil, 1)
	if err != nil {
		t.Fatalf("CreateReviewIssue error: %v", err)
	}
	if got != 200 {
		t.Errorf("CreateReviewIssue() = %d, want 200 (dead owner #100 replaced with a fresh issue)", got)
	}
	if createCalls != 1 {
		t.Errorf("createCalls = %d, want 1 (a replacement must be minted for the dead owner)", createCalls)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected exactly 1 owner-death alert for the replaced dead owner, got %d: %v", len(sink.events), sink.events)
	}

	recorded, err := store.GetSpawnedFixIssue(dedupRepo, dedupKey)
	if err != nil {
		t.Fatalf("GetSpawnedFixIssue: %v", err)
	}
	if recorded != 200 {
		t.Errorf("claim row after replacement = %d, want 200 (overwritten with the replacement)", recorded)
	}
}

// TestCreateReviewIssue_DedupHit_AliveOwner_ReusesExisting is the control
// case for the health re-check above: an open (still-alive) previously
// designated review issue must still be reused, not replaced.
func TestCreateReviewIssue_DedupHit_AliveOwner_ReusesExisting(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/101" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 101, State: github.StateOpen})
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

	prState := &PRState{PRNumber: 78, IssueNumber: 13, BranchName: "pilot/GH-13"}

	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, 101); err != nil {
		t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
	}

	got, err := fl.CreateReviewIssue(context.Background(), prState, nil, nil, 1)
	if err != nil {
		t.Fatalf("CreateReviewIssue error: %v", err)
	}
	if got != 101 {
		t.Errorf("CreateReviewIssue() = %d, want 101 (alive owner reused)", got)
	}
	if createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 (must not mint a duplicate for a still-alive owner)", createCalls)
	}
}

// TestClassifyOwnerHealth_PreflightDeclined_IsDead covers the third gap: a
// fix/review issue declined at preflight stays OPEN carrying
// pilot-needs-clarification (GH-2768) and never dispatches. classifyOwnerHealth
// must classify it as ownerDead, not ownerAlive, so callers that re-check
// health (dedup paths, notifyExternalClose's durable-claim fallback) don't
// re-designate the zombie after ReactToDeclinedFixIssue has already re-armed
// its source.
func TestClassifyOwnerHealth_PreflightDeclined_IsDead(t *testing.T) {
	issue := &github.Issue{
		Number: 500,
		State:  github.StateOpen,
		Labels: []github.Label{{Name: github.LabelNeedsClarification}},
	}
	if got := classifyOwnerHealth(issue); got != ownerDead {
		t.Errorf("classifyOwnerHealth(open+needs-clarification) = %v, want ownerDead", got)
	}
}

// TestClassifyOwnerHealth_OpenWithoutLabel_IsAlive is the control case: an
// ordinary open issue with no needs-clarification label must still read as
// alive.
func TestClassifyOwnerHealth_OpenWithoutLabel_IsAlive(t *testing.T) {
	issue := &github.Issue{Number: 501, State: github.StateOpen}
	if got := classifyOwnerHealth(issue); got != ownerAlive {
		t.Errorf("classifyOwnerHealth(open, no label) = %v, want ownerAlive", got)
	}
}

// TestNotifyExternalClose_DurableClaimFallback_PreflightDeclinedOwner_RearmsNotFails
// reproduces the exact TASK-468 D1 ordering the issue describes: the SDK's
// preflight-decline hook fires first and correctly re-arms the source via
// ReactToDeclinedFixIssue (simulated here by seeding the source already
// pilot-retry-ready), then the external-close fallback runs minutes later
// and must NOT re-designate the still-open, still-declined fix issue as the
// recovery owner (pilot-failed) — it must recognize the declined owner as
// dead and leave the source on its rearmed retry-ready path.
func TestNotifyExternalClose_DurableClaimFallback_PreflightDeclinedOwner_RearmsNotFails(t *testing.T) {
	const (
		prNumber    = 5301
		issueNumber = 5302
		fixIssueNum = 5303
	)

	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5302" && r.Method == http.MethodGet:
			// Source issue: open, already re-armed with pilot-retry-ready by
			// the earlier preflight-decline reaction.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: issueNumber, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/5303" && r.Method == http.MethodGet:
			// The claimed fix issue: still open, declined at preflight.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Number: fixIssueNum,
				State:  github.StateOpen,
				Labels: []github.Label{{Name: github.LabelNeedsClarification}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5302/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5302/labels/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	store := newTestStateStore(t)
	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, fixIssueNum); err != nil {
		t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber:    prNumber,
		PRURL:       "https://github.com/owner/repo/pull/5301",
		IssueNumber: issueNumber,
		// TerminalLabel intentionally empty — in-memory designation lost to
		// a restart, forcing the durable-claim fallback.
	}

	c.notifyExternalClose(context.Background(), prState)

	foundFailed := false
	foundRetryReady := false
	for _, l := range issueLabelsAdded {
		if l == github.LabelFailed {
			foundFailed = true
		}
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if foundFailed {
		t.Errorf("source issue must NOT be re-designated %q pointing at a preflight-declined owner, labels added: %v", github.LabelFailed, issueLabelsAdded)
	}
	if !foundRetryReady {
		t.Errorf("expected source issue to stay/be re-armed with %q, labels added: %v", github.LabelRetryReady, issueLabelsAdded)
	}
}
