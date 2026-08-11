package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4852: PR#4846 (GH-4841) made recovery-owner designation durable by
// consulting the autopilot_spawned_fixes claim in notifyExternalClose's
// fallback, but the fallback trusted `issue_number > 0` without ever
// checking whether that fix issue was still alive. A fix issue closed by a
// human during daemon downtime (no race needed) meant the fallback labeled
// the source pilot-failed pointing at a corpse — GH-4842's reactions are
// event-driven only and cannot fire for an already-closed, untracked issue,
// so the source was permanently stranded. These tests cover the two
// specific gaps named in the pre-merge review (D1 major, D3): the fallback
// must health-check the claim before trusting it, and the decline-reaction
// gate must accept the durable claim as an alternate designation source
// (not just the pilot-failed label) so a decline that races ahead of the
// external-close tick doesn't consume the one-shot reaction for nothing.

// TestNotifyExternalClose_DurableClaimFallback_HealthChecksDeadOwner covers
// the "dead owner, no race needed" scenario: a durable claim names a fix
// issue that is now closed without shipping. Before this fix,
// notifyExternalClose's fallback would trust `fixIssue > 0` blindly and
// label the source pilot-failed forever. After this fix, it must detect the
// dead owner, fall through to pilot-retry-ready instead, and emit an
// owner-death alert so the strand doesn't go unnoticed.
func TestNotifyExternalClose_DurableClaimFallback_HealthChecksDeadOwner(t *testing.T) {
	const (
		prNumber    = 5201
		issueNumber = 5202
		fixIssueNum = 5203
	)

	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5202" && r.Method == http.MethodGet:
			// Source issue: open, not yet pilot-done.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: issueNumber, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/5203" && r.Method == http.MethodGet:
			// The claimed fix issue: closed by a human during daemon
			// downtime, without ever shipping (no pilot-done label) — dead.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: fixIssueNum, State: github.StateClosed})
		case r.URL.Path == "/repos/owner/repo/issues/5202/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5202/labels/") && r.Method == http.MethodDelete:
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
	dedupKey := spawnedFixDedupKey(prNumber, FailureCIPreMerge, []string{"lint"})
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
		PRURL:       "https://github.com/owner/repo/pull/5201",
		IssueNumber: issueNumber,
		// TerminalLabel intentionally empty — this is the in-memory
		// designation lost to a restart that GH-4841's durable fallback
		// exists to recover from.
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
		t.Errorf("source issue must NOT be labeled %q pointing at a dead fix issue, labels added: %v", github.LabelFailed, issueLabelsAdded)
	}
	if !foundRetryReady {
		t.Errorf("expected source issue to be re-armed with %q after the claimed fix issue was found dead, labels added: %v", github.LabelRetryReady, issueLabelsAdded)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected exactly 1 owner-death alert, got %d: %v", len(sink.events), sink.events)
	}
}

// TestReactToDeadFixIssue_DeclineBeforeDesignation_ClaimGatesReaction covers
// the TASK-468 D1 ordering race: a daemon restart lands in the
// close→persist window, and the SDK poller's preflight-decline hook fires
// on the freshly-spawned fix issue BEFORE the external-close tick lands
// pilot-failed on the source. Before this fix, reactToDeadFixIssue's
// label-only designation check would see no pilot-failed label and skip —
// consuming the one-shot reaction — while the durable spawned-fix claim
// already designates this fix issue as the source's recovery owner. After
// this fix, the claim itself is an accepted alternate designation source, so
// the reaction still fires.
func TestReactToDeadFixIssue_DeclineBeforeDesignation_ClaimGatesReaction(t *testing.T) {
	// Matches the body shape FeedbackLoop.generateBody embeds: source #100,
	// originating PR 7.
	fixIssueBody := "Fixes CI failure.\n\nDepends on: #100\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 -->"

	var issueLabelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/100" && r.Method == http.MethodGet:
			// Source issue: open, NOT yet labeled pilot-failed — the
			// external-close tick that would normally add it hasn't landed
			// yet in this race.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 100, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/100/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/100/labels/") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	store := newTestStateStore(t)
	dedupRepo := "owner/repo"
	// The durable claim CreateFailureIssue recorded synchronously before the
	// PR was ever closed — this is what survives the restart, unlike the
	// pilot-failed label which is only written by the later external-close
	// tick.
	dedupKey := spawnedFixDedupKey(7, FailureCIPreMerge, []string{"lint"})
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	if err := store.RecordSpawnedFixIssue(dedupRepo, dedupKey, 200); err != nil {
		t.Fatalf("seed RecordSpawnedFixIssue failed: %v", err)
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	deadIssue := &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody}
	c.reactToDeadFixIssue(context.Background(), deadIssue, "declined at preflight: no fix issue number returned")

	if !containsLabel(issueLabelsAdded, github.LabelRetryReady) {
		t.Errorf("expected source #100 to be re-armed via the durable-claim designation fallback, labels added: %v", issueLabelsAdded)
	}
	if len(sink.events) != 1 {
		t.Errorf("expected exactly 1 owner-death alert (the reaction must not be silently consumed), got %d", len(sink.events))
	}
}

// TestReactToDeadFixIssue_NoClaimNoLabel_StillSkips confirms the new
// claim-based designation check doesn't loosen the existing "avoid
// double-processing" guard: with no durable claim at all (nil stateStore,
// matching every pre-GH-4852 caller/test) and no pilot-failed label, the
// reaction must still skip exactly as before.
func TestReactToDeadFixIssue_NoClaimNoLabel_StillSkips(t *testing.T) {
	fixIssueBody := "Fixes CI failure.\n\nDepends on: #100\n\n<!-- autopilot-meta branch:pilot/GH-100 pr:7 iteration:1 -->"

	var issueLabelsAdded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/100" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 100, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/100/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			issueLabelsAdded = append(issueLabelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// No SetStateStore call — mirrors every existing owner-death test.
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	deadIssue := &github.Issue{Number: 200, State: github.StateClosed, Body: fixIssueBody}
	c.reactToDeadFixIssue(context.Background(), deadIssue, "declined at preflight: no fix issue number returned")

	if len(issueLabelsAdded) != 0 {
		t.Errorf("expected no label writes with no claim and no label designating this fix issue, got %v", issueLabelsAdded)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no alerts, got %d", len(sink.events))
	}
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// TestCreateReviewIssue_DurableClaim_PreventsDuplicateOnRetick covers the
// review-path claim-window fix: CreateReviewIssue now claims the durable
// dedup row BEFORE creating the GitHub issue (mirroring CreateFailureIssue),
// instead of after. A re-tick or restart that calls CreateReviewIssue again
// for the same still-open PR before it's closed must reuse the already-
// created issue instead of minting a second revision issue.
func TestCreateReviewIssue_DurableClaim_PreventsDuplicateOnRetick(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			createCalls++
			resp := github.Issue{Number: 100}
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

	prState := &PRState{PRNumber: 55, IssueNumber: 10, BranchName: "pilot/GH-10"}
	reviews := []*github.PullRequestReview{{ID: 1, User: github.User{Login: "alice"}, Body: "Fix this", State: "CHANGES_REQUESTED"}}

	first, err := fl.CreateReviewIssue(context.Background(), prState, reviews, nil, 1)
	if err != nil {
		t.Fatalf("first CreateReviewIssue error: %v", err)
	}
	if first != 100 {
		t.Fatalf("first CreateReviewIssue = %d, want 100", first)
	}
	if createCalls != 1 {
		t.Fatalf("createCalls after first call = %d, want 1", createCalls)
	}

	second, err := fl.CreateReviewIssue(context.Background(), prState, reviews, nil, 1)
	if err != nil {
		t.Fatalf("second CreateReviewIssue error: %v", err)
	}
	if second != 100 {
		t.Errorf("second CreateReviewIssue = %d, want 100 (reused, not a duplicate)", second)
	}
	if createCalls != 1 {
		t.Errorf("createCalls after second call = %d, want 1 (no duplicate issue created)", createCalls)
	}
}

// TestCreateReviewIssue_ClaimWithoutRecordedIssue_NoDuplicateCreated covers
// the narrow residual window this fix accepts (mirroring CreateFailureIssue's
// identical trade-off): a claim row exists but was never backfilled with an
// issue number (the crash landed between ClaimSpawnedFix and
// CreatePilotIssue). The retry must NOT mint a second issue purely because
// the first attempt never got far enough to create one under the old
// create-then-claim ordering, this exact case free-ran into a duplicate.
func TestCreateReviewIssue_ClaimWithoutRecordedIssue_NoDuplicateCreated(t *testing.T) {
	createCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			createCalls++
			resp := github.Issue{Number: 100}
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

	prState := &PRState{PRNumber: 66, IssueNumber: 11, BranchName: "pilot/GH-11"}

	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(prState.PRNumber, FailureReviewRequested, nil)
	if _, err := store.ClaimSpawnedFix(dedupRepo, dedupKey); err != nil {
		t.Fatalf("seed ClaimSpawnedFix failed: %v", err)
	}
	// Deliberately no RecordSpawnedFixIssue call — simulates a crash after
	// the claim landed but before/during CreatePilotIssue.

	got, err := fl.CreateReviewIssue(context.Background(), prState, nil, nil, 1)
	if err != nil {
		t.Fatalf("CreateReviewIssue error: %v", err)
	}
	if got != 0 {
		t.Errorf("CreateReviewIssue() = %d, want 0 (claim already taken, no recorded issue to reuse)", got)
	}
	if createCalls != 0 {
		t.Errorf("createCalls = %d, want 0 (must not mint a duplicate issue while the claim is contended)", createCalls)
	}
}
