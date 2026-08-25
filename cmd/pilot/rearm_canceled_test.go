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

// newRearmTestServer serves a fixed Issue at GET .../issues/{n} and a fixed
// event list at GET .../issues/{n}/events, and fails the test if any other
// path is hit — used to prove HasCompletedExecution makes NO GitHub calls at
// all for genuine completed/no_op rows (only a canceled row should ever be
// probed).
func newRearmTestServer(t *testing.T, issue *github.Issue, events []*github.IssueEvent) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5139" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(issue)
		case r.URL.Path == "/repos/owner/repo/issues/5139/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(events)
		case r.URL.Path == "/repos/owner/repo/issues/5139/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			// Only exercised by GH-5212 stalled-rearm tests (tryRearmCanceled
			// never removes labels) — kept here since this fixture is shared.
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// failIfCalledServer fails the test if hit at all — used to assert a code
// path makes zero GitHub API calls.
func failIfCalledServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub API call: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedCanceledRow(t *testing.T, store *memory.Store, id, taskID, projectPath string, completedAt time.Time) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: id, TaskID: taskID, ProjectPath: projectPath,
		Status: "canceled", Error: "operator: test cancel", CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
}

// TestTerminalCompletionChecker_NilGHClient_CanceledStaysPermanent proves
// GH-5139 does not change behavior for any checker built the old way (nil
// ghClient) — every pre-GH-5139 construction site and test keeps the
// original "canceled is permanent" semantics byte-for-byte.
func TestTerminalCompletionChecker_NilGHClient_CanceledStaysPermanent(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	checker := terminalCompletionChecker{store: store} // no ghClient — old behavior

	taskID, projectPath := "GH-5139-NILCLIENT", "/project-nilclient"
	seedCanceledRow(t, store, "exec-nilclient", taskID, projectPath, time.Now().Add(-time.Hour))

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected a canceled row to still report done=true with no ghClient wired")
	}
}

// TestTerminalCompletionChecker_GenuineCompletion_NoGitHubCallMade proves the
// re-arm probe is scoped strictly to canceled rows: a genuinely completed row
// must never trigger a GitHub API call even when ghClient IS wired.
func TestTerminalCompletionChecker_GenuineCompletion_NoGitHubCallMade(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	srv := failIfCalledServer(t)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139-GENUINE", "/project-genuine"
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-genuine", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/owner/repo/pull/1",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected a genuinely completed row to report done=true")
	}
}

// TestTerminalCompletionChecker_CanceledNoRearmEvidence_StaysTerminal covers
// the "operator hasn't (yet) done the deliberate re-arm gesture" case: the
// issue is currently open and carries the trigger label, but the timeline
// shows no labeled/reopened event AFTER the cancel — e.g. the label was
// already present before cancel and never touched again. Must stay
// permanent-for-now (not rearmed) and must arm the repick backoff so the next
// probe is throttled (GH-4469).
func TestTerminalCompletionChecker_CanceledNoRearmEvidence_StaysTerminal(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	cancelTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: cancelTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-no-evidence"
	seedCanceledRow(t, store, "exec-no-evidence", taskID, projectPath, cancelTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected the task to remain terminal — no re-arm evidence after the cancel timestamp")
	}

	exec, err := store.GetExecution("exec-no-evidence")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "canceled" {
		t.Errorf("expected the row to remain status=canceled (not reclassified), got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); !gated {
		t.Error("expected the repick backoff to be armed after a failed re-arm probe, throttling the next GitHub call (GH-4469)")
	}
}

// TestTerminalCompletionChecker_CanceledWithRelabelAfterCancel_Rearms is the
// AC2 integration-style regression test: cancel, then a genuine relabel
// (timestamped after the cancel) on an open, labeled issue must re-admit the
// task_id on the next admission check — reclassifying the canceled row to
// 'failed' so it flows through the ordinary retry/backoff/hard-cap path
// rather than any bespoke bypass.
func TestTerminalCompletionChecker_CanceledWithRelabelAfterCancel_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	cancelTime := time.Now().Add(-time.Hour)
	relabelTime := cancelTime.Add(30 * time.Minute)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: cancelTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "unlabeled", CreatedAt: cancelTime.Add(-time.Minute), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: relabelTime, Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-rearmed"
	seedCanceledRow(t, store, "exec-rearmed", taskID, projectPath, cancelTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected the task to be re-armed (done=false) — a labeled event postdates the cancel on an open, labeled issue")
	}

	exec, err := store.GetExecution("exec-rearmed")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the canceled row to be reclassified to status=failed (demote-don't-delete), got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); gated {
		t.Error("expected repick backoff to be cleared on a successful re-arm, not left gated")
	}

	// The re-armed generation must flow through the ordinary retry path: with
	// the row now 'failed' (not terminal), HasTerminalCompletion — the same
	// check nextRetryGeneration (internal/executor/dispatcher.go) consults —
	// must report false, so a fresh generation is genuinely grantable, with
	// no bypass of the existing repick-backoff/hard-cap machinery.
	stillDone, err := store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if stillDone {
		t.Fatal("expected HasTerminalCompletion to report false after re-arm, so nextRetryGeneration can grant the next generation normally")
	}
}

// TestTerminalCompletionChecker_CanceledClosedIssue_NotRearmed covers the
// "reopen didn't happen" half of the AND: even with a labeled event after
// cancel, a currently-CLOSED issue must not re-arm.
func TestTerminalCompletionChecker_CanceledClosedIssue_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	cancelTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "closed", Labels: []github.Label{{Name: "pilot"}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: cancelTime.Add(time.Minute), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-closed"
	seedCanceledRow(t, store, "exec-closed", taskID, projectPath, cancelTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected no re-arm — the issue is currently closed, regardless of a post-cancel labeled event")
	}
}
