package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandlePostMergeCI_NoWorkflowConfigured_ReleasesWithoutTimeout is the
// GH-4643 regression guard: a scope-release carrier in a repo with a
// required-checks allowlist but no push-main workflow at all must not poll
// CIPending for the full 30m timeout on every single carrier attempt. Once
// the no-workflow grace period elapses, HasAnyCIConfigured finds zero
// check-runs and zero legacy commit statuses on the merge SHA, so
// handlePostMergeCI treats post-merge CI as satisfied and proceeds straight
// to StageReleasing — sidestepping the timeout (and handleScopeReleaseFailure)
// entirely. Before the fix, this exact fixture would have hit the timeout
// branch (PostMergeCIStartedAt is set far enough in the past to prove it).
func TestHandlePostMergeCI_NoWorkflowConfigured_ReleasesWithoutTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/mainsha42/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{TotalCount: 0})
		case "/repos/owner/repo/commits/mainsha42/status":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CombinedStatus{TotalCount: 0})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	// Deliberately short: without the GH-4643 fix, this window would already
	// have elapsed by the time the probe fires below, forcing the timeout
	// branch. The fix must bypass that branch entirely.
	cfg.CIWaitTimeout = 5 * time.Second
	// A required-checks allowlist naming a check this workflow-less repo will
	// never post — the exact configuration that made checkRequiredChecks
	// return CIPending forever (auth-service/studio-sdk's RCA).
	cfg.RequiredChecks = []string{"required-check-that-never-runs"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:     9,
		ScopeKey:     "epic:1",
		IssueNumber:  1,
		Stage:        StagePostMergeCI,
		PostMergeSHA: "mainsha42",
		// Past both the 90s no-workflow grace period and the 5s CIWaitTimeout —
		// proves the probe path, not luck, is what avoids the timeout branch.
		PostMergeCIStartedAt: time.Now().Add(-2 * time.Minute),
	}
	c.mu.Lock()
	c.activePRs[9] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if prState.Stage != StageReleasing {
		t.Errorf("Stage = %v, want StageReleasing (post-merge CI should be treated as satisfied)", prState.Stage)
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty (must not time out)", prState.Error)
	}
	if !prState.PostMergeCINoWorkflowChecked {
		t.Error("PostMergeCINoWorkflowChecked = false, want true (probe should have run)")
	}
}

// TestHandlePostMergeCI_WithPostMergeChecks_UnaffectedByNoWorkflowProbe is the
// GH-4643 regression guard in the opposite direction: a repo that DOES post
// check-runs on the merge SHA must keep using the real CheckCI verdict — the
// no-workflow probe must never fire (elapsed time is well under the grace
// period) and must never override a real, still-pending or real check result.
func TestHandlePostMergeCI_WithPostMergeChecks_UnaffectedByNoWorkflowProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/mainsha42/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second
	cfg.RequiredChecks = []string{"ci"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:     10,
		ScopeKey:     "epic:2",
		IssueNumber:  2,
		Stage:        StagePostMergeCI,
		PostMergeSHA: "mainsha42",
		// Fresh — well within the 90s no-workflow grace period, so the probe
		// must never run; the real check-runs response drives the verdict.
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[10] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if prState.Stage != StageReleasing {
		t.Errorf("Stage = %v, want StageReleasing (real green check should release same as before)", prState.Stage)
	}
	if prState.PostMergeCINoWorkflowChecked {
		t.Error("PostMergeCINoWorkflowChecked = true, want false (probe must not fire before the grace period elapses)")
	}
}

// TestHandleScopeReleaseFailure_ParksAfterRepeatedTimeouts is the GH-4643
// regression guard for the carrier-retry cap: a scope that fails
// maxScopeReleaseTimeoutAttempts consecutive times with a "post-merge CI
// timeout" reason must be parked — a terminal state distinct from 'failed'
// that recoverFailedScopeReleases (which only lists state='failed' rows)
// never resurrects — with exactly one scope_release_parked alert, and must
// not be re-queued by further failures against the same (already parked) row.
func TestHandleScopeReleaseFailure_ParksAfterRepeatedTimeouts(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 9, ScopeKey: "epic:1", IssueNumber: 1, PostMergeSHA: "sha-red"}

	for i := 0; i < maxScopeReleaseTimeoutAttempts; i++ {
		c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI timeout after 30m0s")
	}

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "parked" {
		t.Errorf("state = %q, want parked after %d consecutive timeouts", row.State, maxScopeReleaseTimeoutAttempts)
	}
	if row.TimeoutAttempts != maxScopeReleaseTimeoutAttempts {
		t.Errorf("timeout_attempts = %d, want %d", row.TimeoutAttempts, maxScopeReleaseTimeoutAttempts)
	}

	parkedAlerts := countAlerts(sink.events, "scope_release_parked")
	if parkedAlerts != 1 {
		t.Errorf("scope_release_parked alerts = %d, want exactly 1", parkedAlerts)
	}

	// A further zombie-carrier failure against the now-parked scope must not
	// re-queue it, bump the counter further, or fire a second alert.
	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI timeout after 30m0s")

	row2, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row2 == nil {
		t.Fatalf("GetScopeRelease (2nd) failed: %v", err)
	}
	if row2.State != "parked" {
		t.Errorf("state = %q, want still parked", row2.State)
	}
	if row2.TimeoutAttempts != maxScopeReleaseTimeoutAttempts {
		t.Errorf("timeout_attempts = %d, want unchanged at %d", row2.TimeoutAttempts, maxScopeReleaseTimeoutAttempts)
	}
	if got := countAlerts(sink.events, "scope_release_parked"); got != 1 {
		t.Errorf("scope_release_parked alerts after repeat failure = %d, want still 1 (no duplicate alert)", got)
	}
}

// TestHandleScopeReleaseFailure_NonTimeoutReasonResetsTimeoutStreak verifies
// that TimeoutAttempts only accumulates across consecutive timeout failures —
// a genuine CI-red failure in between resets the streak, so a scope alternating
// between real (fixable) failures and the occasional timeout is never parked
// (GH-4643).
func TestHandleScopeReleaseFailure_NonTimeoutReasonResetsTimeoutStreak(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)
	c.SetAlertsEngine(&fakeAlertSink{})

	prState := &PRState{PRNumber: 9, ScopeKey: "epic:1", IssueNumber: 1, PostMergeSHA: "sha-red"}

	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI timeout after 30m0s")
	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI timeout after 30m0s")
	c.handleScopeReleaseFailure(context.Background(), prState, "post-merge CI failed")

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "pending" {
		t.Errorf("state = %q, want pending (must not be parked — streak was reset)", row.State)
	}
	if row.TimeoutAttempts != 0 {
		t.Errorf("timeout_attempts = %d, want 0 (reset by the non-timeout failure)", row.TimeoutAttempts)
	}
}

func countAlerts(events []alerts.Event, eventType string) int {
	n := 0
	for _, e := range events {
		if e.Type == alerts.EventType(eventType) {
			n++
		}
	}
	return n
}

// TestTryStartScopeRelease_DeferLogThrottled is the GH-4643 regression guard
// for the "deferring scope release" log throttle: a scope stuck deferring
// (here: its member PR still tracked in activePRs) logs its INFO line at
// most once per scopeDeferLogThrottle, not on every call.
func TestTryStartScopeRelease_DeferLogThrottled(t *testing.T) {
	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:1", "epic", []int{9}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
	c.SetStateStore(stateStore)

	var logBuf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	// Member PR 9 still mid-pipeline — every call defers for the same reason.
	c.mu.Lock()
	c.activePRs[9] = &PRState{PRNumber: 9}
	c.mu.Unlock()

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}

	for i := 0; i < 5; i++ {
		c.tryStartScopeRelease(row)
	}

	got := strings.Count(logBuf.String(), "deferring scope release: a member PR is still mid-pipeline")
	if got != 1 {
		t.Errorf("deferral log line count = %d, want exactly 1 across 5 calls within the throttle window\n--- logs ---\n%s", got, logBuf.String())
	}

	// Simulate the throttle window having elapsed: the next call should log again.
	c.mu.Lock()
	c.scopeDeferLogAt["epic:1"] = time.Now().Add(-(scopeDeferLogThrottle + time.Minute))
	c.mu.Unlock()

	c.tryStartScopeRelease(row)

	got = strings.Count(logBuf.String(), "deferring scope release: a member PR is still mid-pipeline")
	if got != 2 {
		t.Errorf("deferral log line count after throttle window elapsed = %d, want 2", got)
	}
}
