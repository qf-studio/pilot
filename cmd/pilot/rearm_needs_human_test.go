package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

// newRearmNeedsHumanTestServer mirrors newRearmTestServer (rearm_canceled_test.go)
// but serves issue #5414 — a distinct number from the canceled/superseded
// tests' fixed #5139/#5249 fixtures, since all three test files share the
// terminalCompletionChecker struct and could otherwise collide if ever
// exercised against the same httptest.Server. tryRearmNeedsHuman never
// removes labels itself (that's the operator's own re-arm gesture), so
// unlike newRearmSupersededTestServer this fixture has no DELETE route.
func newRearmNeedsHumanTestServer(t *testing.T, issue *github.Issue, events []*github.IssueEvent) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5414" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(issue)
		case r.URL.Path == "/repos/owner/repo/issues/5414/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(events)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedNeedsHumanRow(t *testing.T, store *memory.Store, id, taskID, projectPath string, completedAt time.Time) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: id, TaskID: taskID, ProjectPath: projectPath,
		Status: "needs_human", Error: "GH-5399: salvaged work pushed, awaiting manual review", CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
}

// TestTryRearmNeedsHuman_NoNeedsHumanRow_NoGitHubCallMade proves the probe is
// scoped strictly to needs_human rows: a task with no needs_human row (e.g.
// its terminal evidence was a genuine completed/canceled/superseded row
// instead) must never trigger a GitHub API call.
func TestTryRearmNeedsHuman_NoNeedsHumanRow_NoGitHubCallMade(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	srv := failIfCalledServer(t)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	rearmed, err := checker.tryRearmNeedsHuman("GH-5414-NOROW", "/project-no-row", "backoff-key-no-row")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false with no needs_human row present")
	}
}

// TestTryRearmNeedsHuman_LabelStillPresent_NotRearmed covers the "operator
// hasn't (yet) cleared pilot-needs-human" case: the issue is open and
// trigger-labeled, but still carries pilot-needs-human — the re-arm gesture
// is not complete yet, regardless of any other timeline evidence.
func TestTryRearmNeedsHuman_LabelStillPresent_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)

	srv := newRearmNeedsHumanTestServer(t,
		&github.Issue{Number: 5414, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: labelPilotNeedsHumanSDK}}},
		nil,
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-label-present"
	seedNeedsHumanRow(t, store, "exec-label-present", taskID, projectPath, holdTime)

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, "backoff-key-label-present")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — pilot-needs-human is still on the issue")
	}

	exec, err := store.GetExecution("exec-label-present")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "needs_human" {
		t.Errorf("expected the row to remain status=needs_human (not reclassified), got %q", exec.Status)
	}
}

// TestTryRearmNeedsHuman_UnlabeledAfterHold_Rearms is the AC1 regression
// test: an operator removing pilot-needs-human from an open, pilot-labeled
// issue AFTER the hold must re-admit the task_id — reclassifying the
// needs_human row to 'failed' so it flows through the ordinary
// retry/backoff/hard-cap path.
func TestTryRearmNeedsHuman_UnlabeledAfterHold_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)
	clearTime := holdTime.Add(30 * time.Minute)

	srv := newRearmNeedsHumanTestServer(t,
		&github.Issue{Number: 5414, State: "open", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: holdTime.Add(-24 * time.Hour), Label: &github.Label{Name: labelPilotNeedsHumanSDK}},
			{Event: "unlabeled", CreatedAt: clearTime, Label: &github.Label{Name: labelPilotNeedsHumanSDK}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-rearmed"
	seedNeedsHumanRow(t, store, "exec-rearmed", taskID, projectPath, holdTime)
	key := repickBackoffKey(projectPath, taskID)
	repickBackoff.recordDrop(key) // arm the backoff so we can prove recordSuccess clears it
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — pilot-needs-human was removed after the hold on an open, pilot-labeled issue")
	}

	exec, err := store.GetExecution("exec-rearmed")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the needs_human row to be reclassified to status=failed (demote-don't-delete), got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); gated {
		t.Error("expected repick backoff to be cleared on a successful re-arm, not left gated")
	}

	stillDone, err := store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if stillDone {
		t.Fatal("expected HasTerminalCompletion to report false after re-arm, so nextRetryGeneration can grant the next generation normally")
	}
}

// TestTryRearmNeedsHuman_RelabelTriggerAfterHold_Rearms covers the
// alternative re-arm gesture: re-adding the base trigger label after the
// hold (with pilot-needs-human already absent) is itself sufficient
// evidence, same as tryRearmCanceled accepts for its own row type.
func TestTryRearmNeedsHuman_RelabelTriggerAfterHold_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)
	relabelTime := holdTime.Add(30 * time.Minute)

	srv := newRearmNeedsHumanTestServer(t,
		&github.Issue{Number: 5414, State: "open", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "unlabeled", CreatedAt: holdTime.Add(-time.Minute), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: relabelTime, Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-relabel-rearmed"
	seedNeedsHumanRow(t, store, "exec-relabel-rearmed", taskID, projectPath, holdTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — the trigger label was re-added after the hold on an open, pilot-needs-human-free issue")
	}
}

// TestTryRearmNeedsHuman_UnlabeledBeforeHold_NotRearmed covers the ordering
// half of the check: an unlabeled(pilot-needs-human) event timestamped
// BEFORE the hold must not count as re-arm evidence — e.g. the label was
// removed and then re-applied (triggering the hold again) without any
// post-hold gesture.
func TestTryRearmNeedsHuman_UnlabeledBeforeHold_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)

	srv := newRearmNeedsHumanTestServer(t,
		&github.Issue{Number: 5414, State: "open", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "unlabeled", CreatedAt: holdTime.Add(-time.Minute), Label: &github.Label{Name: labelPilotNeedsHumanSDK}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-stale-unlabel"
	seedNeedsHumanRow(t, store, "exec-stale-unlabel", taskID, projectPath, holdTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — the unlabeled event predates the hold timestamp")
	}

	exec, err := store.GetExecution("exec-stale-unlabel")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "needs_human" {
		t.Errorf("expected the row to remain status=needs_human (not reclassified), got %q", exec.Status)
	}
}

// TestTryRearmNeedsHuman_ClosedIssue_NotRearmed covers the "reopen didn't
// happen" half of the AND: even with a qualifying unlabeled event after the
// hold, a currently-CLOSED issue must not re-arm.
func TestTryRearmNeedsHuman_ClosedIssue_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)

	srv := newRearmNeedsHumanTestServer(t,
		&github.Issue{Number: 5414, State: "closed", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "unlabeled", CreatedAt: holdTime.Add(time.Minute), Label: &github.Label{Name: labelPilotNeedsHumanSDK}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-closed"
	seedNeedsHumanRow(t, store, "exec-closed", taskID, projectPath, holdTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected no re-arm — the issue is currently closed, regardless of a post-hold unlabeled event")
	}
}

// TestTryRearmNeedsHuman_GitHubError_ReturnsError proves a genuine GitHub API
// failure (as opposed to "no evidence found") is surfaced as an error rather
// than silently treated as "not re-armed" — the caller (HasCompletedExecutionReason)
// needs the distinction to decide whether to trust the still-terminal verdict.
func TestTryRearmNeedsHuman_GitHubError_ReturnsError(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	holdTime := time.Now().Add(-time.Hour)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5414", "/project-gh-error"
	seedNeedsHumanRow(t, store, "exec-gh-error", taskID, projectPath, holdTime)

	rearmed, err := checker.tryRearmNeedsHuman(taskID, projectPath, "backoff-key-gh-error")
	if err == nil {
		t.Fatal("expected an error from a failing GitHub API call")
	}
	if rearmed {
		t.Fatal("expected rearmed=false alongside the error")
	}
}
