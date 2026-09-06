package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestTryRearmStalled_PilotRetryReadyLabelAfterStall_Rearms is GH-5272's
// acceptance test: the exact documented re-arm recipe from
// surfaceStalledIssue's own posted comment —
// `gh issue edit N --remove-label pilot-blocked --add-label pilot-retry-ready`
// — never touches the base trigger label ("pilot") at all. Before GH-5272,
// tryRearmStalled's evidence check (latestRearmEvent scoped to the trigger
// label only) could never recognize this as a re-arm gesture, so the GH-493
// incident's stale hard-cap counter was never cleared by anything.
func TestTryRearmStalled_PilotRetryReadyLabelAfterStall_Rearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	rearmTime := stallTime.Add(30 * time.Minute)

	var blockedLabelRemoveAttempted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/493" && r.Method == http.MethodGet:
			// pilot-blocked already removed, pilot-retry-ready added — the
			// base "pilot" trigger label was never touched.
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 493, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryReady}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/493/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
				{Event: "labeled", CreatedAt: stallTime, Label: &github.Label{Name: github.LabelBlocked}},
				{Event: "unlabeled", CreatedAt: rearmTime, Label: &github.Label{Name: github.LabelBlocked}},
				{Event: "labeled", CreatedAt: rearmTime, Label: &github.Label{Name: github.LabelRetryReady}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/493/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			// Best-effort cleanup of an already-absent label — GitHub 404s
			// this in practice; tryRearmStalled treats any error here as
			// non-fatal (logged, not returned), so respond accordingly.
			blockedLabelRemoveAttempted = true
			w.WriteHeader(http.StatusNotFound)
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

	taskID, projectPath := "GH-493", "/project-gh-493-incident"
	seedStalledRow(t, store, "exec-gh493-stalled", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	rearmed, err := checker.tryRearmStalled(taskID, projectPath, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rearmed {
		t.Fatal("expected rearmed=true — a pilot-retry-ready labeled event postdates the stall on an open, pilot-labeled issue")
	}
	if !blockedLabelRemoveAttempted {
		t.Error("expected a best-effort pilot-blocked label removal attempt")
	}

	exec, err := store.GetExecution("exec-gh493-stalled")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected the stalled row to be reclassified to status=failed, got %q", exec.Status)
	}

	if gated, _ := repickBackoff.gateStatus(key); gated {
		t.Error("expected repick backoff (and the persisted hard-cap counter it proxies) to be cleared on a successful re-arm, not left gated")
	}
}

// TestSweepStalledRearm_RetryReadyLabelWithoutBlocked_DiscoversAndRearms is
// GH-5272's sweep-level acceptance test: sweepStalledRearm's candidate query
// used to list ONLY pilot-blocked issues, which the documented re-arm recipe
// removes in the same edit that adds pilot-retry-ready — so by the time any
// sweep pass ran, the re-armed issue had already dropped out of the
// candidate list entirely and tryRearmStalled was never even called.
func TestSweepStalledRearm_RetryReadyLabelWithoutBlocked_DiscoversAndRearms(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	stallTime := time.Now().Add(-time.Hour)
	rearmTime := stallTime.Add(30 * time.Minute)

	var listCalls, removeCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodGet:
			listCalls++
			if p := r.URL.Query().Get("page"); p != "1" && p != "" {
				_ = json.NewEncoder(w).Encode([]*github.Issue{})
				return
			}
			_ = json.NewEncoder(w).Encode([]*github.Issue{
				// No pilot-blocked label — the operator already removed it
				// per the bot's own recipe. Only pilot-retry-ready remains.
				{Number: 493, State: "open", Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryReady}}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/493" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(&github.Issue{
				Number: 493, State: "open",
				Labels: []github.Label{{Name: "pilot"}, {Name: github.LabelRetryReady}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/493/events" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]*github.IssueEvent{
				{Event: "labeled", CreatedAt: stallTime.Add(-24 * time.Hour), Label: &github.Label{Name: "pilot"}},
				{Event: "labeled", CreatedAt: rearmTime, Label: &github.Label{Name: github.LabelRetryReady}},
			})
		case r.URL.Path == "/repos/owner/repo/issues/493/labels/"+github.LabelBlocked && r.Method == http.MethodDelete:
			removeCalls++
			w.WriteHeader(http.StatusNotFound)
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

	taskID, projectPath := "GH-493", "/project-gh-493-sweep"
	seedStalledRow(t, store, "exec-gh493-sweep", taskID, projectPath, stallTime)
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	checker.sweepStalledRearm(context.Background(), projectPath)

	if listCalls == 0 {
		t.Fatal("expected sweepStalledRearm to list open issues")
	}
	if removeCalls != 1 {
		t.Errorf("expected exactly 1 pilot-blocked label removal attempt, got %d", removeCalls)
	}

	exec, err := store.GetExecution("exec-gh493-sweep")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status=failed after sweep-driven reclassify, got %q (GH-5272 candidate-discovery gap reproduced if still stalled)", exec.Status)
	}
}
