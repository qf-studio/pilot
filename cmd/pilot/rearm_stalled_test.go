package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

func seedStalledRow(t *testing.T, store *memory.Store, id, taskID, projectPath string, completedAt time.Time) {
	t.Helper()
	if err := store.SaveExecution(&memory.Execution{
		ID: id, TaskID: taskID, ProjectPath: projectPath,
		Status: "stalled", Error: "consecutive identical failures (will not retry): boom", CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
}

// TestTryRearmStalled_NoStalledRow_NoGitHubCallMade proves the probe is
// scoped strictly to stalled rows: a task_id with no stalled row at all
// must never trigger a GitHub API call.
func TestTryRearmStalled_NoStalledRow_NoGitHubCallMade(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	srv := failIfCalledServer(t)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5212-NOROW", "/project-norow"
	rearmed, err := checker.tryRearmStalled(taskID, projectPath, repickBackoffKey(projectPath, taskID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false with no stalled row to evaluate")
	}
}

// TestTryRearmStalled_NoPostStallEvidence_StaysStalledAndGatesBackoff is
// acceptance criterion 2: a stalled row with no post-stall relabel/reopen
// evidence must not be re-armed, and the caller (sweepStalledRearm) must arm
// the repick backoff so the next sweep pass doesn't re-probe GitHub on every
// tick.
func TestTryRearmStalled_NoPostStallEvidence_StaysStalledAndGatesBackoff(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "labeled", CreatedAt: stallTime, Label: &github.Label{Name: github.LabelBlocked}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	// newRearmTestServer hardcodes issue #5139 in its routes (see
	// rearm_canceled_test.go), so this test's task_id must match.
	taskID, projectPath := "GH-5139", "/project-no-evidence"
	seedStalledRow(t, store, "exec-stalled-no-evidence", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected rearmed=false — no re-arm evidence after the stall timestamp")
	}

	exec, err := store.GetExecution("exec-stalled-no-evidence")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain status=stalled (not reclassified), got %q", exec.Status)
	}
}

// TestTryRearmStalled_RelabelAfterStall_RearmsAndRemovesBlockedLabel is
// acceptance criterion 1: a synthetic post-stall trigger-label event
// re-admits the task — the row is demoted stalled->failed AND the
// pilot-blocked label removal is invoked (both must happen per the task
// spec, since a surviving label keeps the poller excluding the issue
// forever regardless of row state).
func TestTryRearmStalled_RelabelAfterStall_RearmsAndRemovesBlockedLabel(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	relabelTime := stallTime.Add(30 * time.Minute)

	var blockedLabelRemoved bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/5212" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 5212, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5212/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
				{Event: "labeled", CreatedAt: stallTime, Label: &github.Label{Name: github.LabelBlocked}},
				{Event: "labeled", CreatedAt: relabelTime, Label: &github.Label{Name: "pilot"}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5212/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			blockedLabelRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5212", "/project-rearmed"
	seedStalledRow(t, store, "exec-stalled-rearmed", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — a labeled event postdates the stall on an open, labeled issue")
	}
	if !blockedLabelRemoved {
		t.Error("expected the pilot-blocked label removal to be invoked")
	}

	exec, err := store.GetExecution("exec-stalled-rearmed")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the stalled row to be reclassified to status=failed (demote-don't-delete), got %q", exec.Status)
	}

	// The re-armed generation must flow through the ordinary retry path —
	// same invariant rearm_canceled_test.go checks for the canceled case.
	stillDone, err := store.HasTerminalCompletion(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if stillDone {
		t.Fatal("expected HasTerminalCompletion to report false after re-arm, so nextRetryGeneration can grant the next generation normally")
	}

	if gated, _ := repickBackoff.gateStatus(key); gated {
		t.Error("expected repick backoff to be cleared on a successful re-arm, not left gated")
	}
}

// TestTryRearmStalled_ReopenAfterStall_Rearms is acceptance criterion 3: a
// reopen event (not just a relabel) after the stall also counts as evidence.
func TestTryRearmStalled_ReopenAfterStall_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	reopenTime := stallTime.Add(45 * time.Minute)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
			{Event: "closed", CreatedAt: stallTime.Add(-time.Minute)},
			{Event: "reopened", CreatedAt: reopenTime},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	// newRearmTestServer hardcodes issue #5139 in its routes (see
	// rearm_canceled_test.go), so this test's task_id must match.
	taskID, projectPath := "GH-5139", "/project-reopen"
	seedStalledRow(t, store, "exec-stalled-reopen", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — a reopened event postdates the stall on an open, labeled issue")
	}

	exec, err := store.GetExecution("exec-stalled-reopen")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status=failed after reopen-triggered reclassify, got %q", exec.Status)
	}
}

// TestTryRearmStalled_ClosedIssue_NotRearmed mirrors the canceled path's
// closed-issue guard: even with a labeled event after the stall, a
// currently-CLOSED issue must not re-arm.
func TestTryRearmStalled_ClosedIssue_NotRearmed(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	srv := newRearmTestServer(t,
		&github.Issue{Number: 5139, State: "closed", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}}},
		[]*github.IssueEvent{
			{Event: "labeled", CreatedAt: stallTime.Add(time.Minute), Label: &github.Label{Name: "pilot"}},
		},
	)
	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5139", "/project-closed-stalled"
	seedStalledRow(t, store, "exec-stalled-closed", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rearmed {
		t.Fatal("expected no re-arm — the issue is currently closed, regardless of a post-stall labeled event")
	}
}

// TestSweepStalledRearm_ListsBlockedIssues_RearmsMatchingStalledRow proves
// the sweep's end-to-end shape: it lists open pilot-blocked issues (the
// site that stands in for the SDK poller's unreachable candidate-exclusion
// checkpoint — see tryRearmStalled's doc comment), finds the one backed by a
// stalled store row, and re-arms it.
func TestSweepStalledRearm_ListsBlockedIssues_RearmsMatchingStalledRow(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	relabelTime := stallTime.Add(30 * time.Minute)

	var listCalls, removeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
			listCalls++
			if r.URL.Query().Get("page") != "1" && r.URL.Query().Get("page") != "" {
				_ = json.NewEncoder(w).Encode([]*github.Issue{})
				return
			}
			_ = json.NewEncoder(w).Encode([]*github.Issue{
				{Number: 5212, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5212" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 5212, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5212/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
				{Event: "labeled", CreatedAt: relabelTime, Label: &github.Label{Name: "pilot"}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/5212/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			removeCalls++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5212", "/project-sweep"
	seedStalledRow(t, store, "exec-stalled-sweep", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	checker.sweepStalledRearm(context.Background(), projectPath)

	if listCalls == 0 {
		t.Fatal("expected sweepStalledRearm to list pilot-blocked issues")
	}
	if removeCalls != 1 {
		t.Errorf("expected exactly 1 pilot-blocked label removal, got %d", removeCalls)
	}

	exec, err := store.GetExecution("exec-stalled-sweep")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status=failed after sweep-driven reclassify, got %q", exec.Status)
	}
}

// TestSweepStalledRearm_GatedIssue_NoGitHubProbeCall is acceptance criterion
// 2's sweep-level half: once an issue's backoff key is gated (a prior sweep
// pass already probed it and found no evidence), a later sweep pass must
// list issues but skip the expensive GetIssue+ListIssueEvents probe entirely
// — no hot per-tick GitHub API loop.
func TestSweepStalledRearm_GatedIssue_NoGitHubProbeCall(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)

	var probeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
			if r.URL.Query().Get("page") != "1" && r.URL.Query().Get("page") != "" {
				_ = json.NewEncoder(w).Encode([]*github.Issue{})
				return
			}
			_ = json.NewEncoder(w).Encode([]*github.Issue{
				{Number: 5212, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelBlocked}}},
			})
		default:
			probeCalls++
			t.Errorf("unexpected request while gated: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	checker := terminalCompletionChecker{
		store: store, ghClient: github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
		repoOwner: "owner", repoName: "repo", triggerLabel: "pilot",
	}

	taskID, projectPath := "GH-5212", "/project-gated-sweep"
	seedStalledRow(t, store, "exec-stalled-gated", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	// Pre-arm the gate exactly like a prior "no evidence yet" probe would
	// (mirrors HasCompletedExecution's own GH-4469 throttle test shape).
	repickBackoff.recordClaimLostDrop(key)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	checker.sweepStalledRearm(context.Background(), projectPath)

	if probeCalls != 0 {
		t.Errorf("expected zero probe calls while the backoff key is gated, got %d", probeCalls)
	}

	exec, err := store.GetExecution("exec-stalled-gated")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected the row to remain untouched while gated, got status %q", exec.Status)
	}
}
