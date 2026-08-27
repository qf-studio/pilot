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

// newRearmSupersededTestServer mirrors newRearmTestServer (rearm_canceled_test.go)
// but serves issue #5249 — a distinct number from the canceled tests' fixed
// #5139 fixture, since both test files share the terminalCompletionChecker
// struct and could otherwise collide if ever exercised against the same
// httptest.Server. Also handles the DELETE .../labels/pilot-superseded call
// tryRearmSuperseded makes on a successful re-arm (tryRearmCanceled never
// removes labels, so that call site has no counterpart in the sibling
// fixture).
func newRearmSupersededTestServer(t *testing.T, issue *github.Issue, events []*github.IssueEvent) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5249" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(issue)
		case r.URL.Path == "/repos/owner/repo/issues/5249/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(events)
		case r.URL.Path == "/repos/owner/repo/issues/5249/labels/"+github.LabelSuperseded && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedSupersededRow(t *testing.T, store *memory.Store, id, taskID, projectPath string, completedAt time.Time) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: id, TaskID: taskID, ProjectPath: projectPath,
		Status: "superseded", Error: "GH-5247: healthy hand-off to fix issue #5250", CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
}

// TestTerminalCompletionChecker_NilGHClient_SupersededStaysPermanent proves
// GH-5249 does not change behavior for any checker built the old way (nil
// ghClient) — mirrors TestTerminalCompletionChecker_NilGHClient_CanceledStaysPermanent
// for status='superseded'.
func TestTerminalCompletionChecker_NilGHClient_SupersededStaysPermanent(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	checker := terminalCompletionChecker{store: store} // no ghClient — old behavior

	taskID, projectPath := "GH-5249-NILCLIENT", "/project-nilclient-superseded"
	seedSupersededRow(t, store, "exec-nilclient-superseded", taskID, projectPath, time.Now().Add(-time.Hour))

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected a superseded row to still report done=true with no ghClient wired")
	}
}

// TestTerminalCompletionChecker_SupersededNoRearmEvidence_StaysTerminal
// mirrors TestTerminalCompletionChecker_CanceledNoRearmEvidence_StaysTerminal:
// the issue is currently open and carries the trigger label, but the
// timeline shows no labeled/reopened event AFTER the supersede — must stay
// permanent-for-now and arm the repick backoff (GH-4469).
func TestTerminalCompletionChecker_SupersededNoRearmEvidence_StaysTerminal(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	supersedeTime := time.Now().Add(-time.Hour)

	srv := newRearmSupersededTestServer(t,
		&github.Issue{Number: 5249, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelSuperseded}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: supersedeTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5249", "/project-no-evidence-superseded"
	seedSupersededRow(t, store, "exec-no-evidence-superseded", taskID, projectPath, supersedeTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected the task to remain terminal — no re-arm evidence after the supersede timestamp")
	}

	exec, err := store.GetExecution("exec-no-evidence-superseded")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "superseded" {
		t.Errorf("expected the row to remain status=superseded (not reclassified), got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); !gated {
		t.Error("expected the repick backoff to be armed after a failed re-arm probe, throttling the next GitHub call (GH-4469)")
	}
}

// TestTerminalCompletionChecker_SupersededWithRelabelAfterSupersede_Rearms
// mirrors TestTerminalCompletionChecker_CanceledWithRelabelAfterCancel_Rearms:
// a genuine relabel (timestamped after the supersede) on an open, labeled
// issue must re-admit the task_id, reclassifying the superseded row to
// 'failed' AND removing the pilot-superseded label (best-effort cosmetic
// cleanup, since — unlike pilot-blocked — it never excluded the issue from
// poller candidacy).
func TestTerminalCompletionChecker_SupersededWithRelabelAfterSupersede_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	supersedeTime := time.Now().Add(-time.Hour)
	relabelTime := supersedeTime.Add(30 * time.Minute)

	srv := newRearmSupersededTestServer(t,
		&github.Issue{Number: 5249, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelSuperseded}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: supersedeTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "unlabeled", CreatedAt: supersedeTime.Add(-time.Minute), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: relabelTime, Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5249", "/project-rearmed-superseded"
	seedSupersededRow(t, store, "exec-rearmed-superseded", taskID, projectPath, supersedeTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected the task to be re-armed (done=false) — a labeled event postdates the supersede on an open, labeled issue")
	}

	exec, err := store.GetExecution("exec-rearmed-superseded")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the superseded row to be reclassified to status=failed (demote-don't-delete), got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); gated {
		t.Error("expected repick backoff to be cleared on a successful re-arm, not left gated")
	}

	// The re-armed generation must flow through the ordinary retry path.
	stillDone, err := store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if stillDone {
		t.Fatal("expected HasTerminalCompletion to report false after re-arm, so nextRetryGeneration can grant the next generation normally")
	}
}

// TestTerminalCompletionChecker_SupersededClosedIssue_NotRearmed mirrors
// TestTerminalCompletionChecker_CanceledClosedIssue_NotRearmed: even with a
// labeled event after supersede, a currently-CLOSED issue must not re-arm.
func TestTerminalCompletionChecker_SupersededClosedIssue_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	supersedeTime := time.Now().Add(-time.Hour)

	srv := newRearmSupersededTestServer(t,
		&github.Issue{Number: 5249, State: "closed", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelSuperseded}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: supersedeTime.Add(time.Minute), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5249", "/project-closed-superseded"
	seedSupersededRow(t, store, "exec-closed-superseded", taskID, projectPath, supersedeTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected no re-arm — the issue is currently closed, regardless of a post-supersede labeled event")
	}
}
