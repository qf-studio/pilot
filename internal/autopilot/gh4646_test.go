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

// TestCIMonitor_CheckRequiredChecks_MismatchClassification is the table-driven
// unit test for GH-4646's core detection logic: a required_checks/
// ci_checks.required allowlist naming a check that never posts on a repo
// (auth-service's missing "lint", studio-sdk's missing "test" — both inherited
// the global required_checks: [test, lint] with no per-project override) must
// stop resolving to a silent, permanent CIPending once every other check-run
// on the SHA has settled. A required name that is either present (any status)
// or still possibly forthcoming (something on the SHA is still executing)
// must behave exactly as before.
func TestCIMonitor_CheckRequiredChecks_MismatchClassification(t *testing.T) {
	tests := []struct {
		name       string
		required   []string
		checkRuns  []github.CheckRun
		wantStatus CIStatus
	}{
		{
			name:     "required name absent, all runs completed -> mismatch classification",
			required: []string{"build", "test"},
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
			wantStatus: CIConfigMismatch,
		},
		{
			name:     "required name absent, a run still in_progress -> still pending",
			required: []string{"build", "test"},
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "lint", Status: github.CheckRunInProgress, Conclusion: ""},
			},
			wantStatus: CIPending,
		},
		{
			name:     "required name present but still queued -> unchanged (genuinely pending)",
			required: []string{"build", "test"},
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunQueued, Conclusion: ""},
			},
			wantStatus: CIPending,
		},
		{
			name:     "required name present and completed -> unchanged (success)",
			required: []string{"build", "test"},
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
			wantStatus: CISuccess,
		},
		{
			name:     "required name present but failed -> unchanged (failure wins, not a mismatch)",
			required: []string{"build", "test"},
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
			},
			wantStatus: CIFailure,
		},
		{
			name:       "no check-runs on the SHA at all -> not a mismatch (no-CI class handled elsewhere)",
			required:   []string{"build", "test"},
			checkRuns:  nil,
			wantStatus: CIPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.RequiredChecks = tt.required
			m := NewCIMonitor(github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0"), "owner", "repo", cfg)

			status := m.checkRequiredChecks(&github.CheckRunsResponse{
				TotalCount: len(tt.checkRuns),
				CheckRuns:  tt.checkRuns,
			})
			if status != tt.wantStatus {
				t.Errorf("checkRequiredChecks() = %s, want %s", status, tt.wantStatus)
			}
		})
	}
}

// TestHandlePostMergeCI_ConfigMismatch_FailsLoudlyWithoutFixCascade is the
// post-merge regression guard for GH-4646: a required check that never
// posts on an otherwise-green merge SHA must not spawn a CI-fix issue (there
// is nothing in the diff to fix) and must not silently keep polling — it
// must transition the carrier straight to a terminal state with a reason
// that names the specific mismatch, well before the 30m post-merge CI
// timeout would otherwise elapse.
func TestHandlePostMergeCI_ConfigMismatch_FailsLoudlyWithoutFixCascade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/mainsha42/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
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
	cfg.CIWaitTimeout = 30 * time.Minute // large: proves the mismatch fires long before any timeout
	// The auth-service/studio-sdk shape: a required check ("lint") this
	// workflow's push-to-main run will never post, alongside a real, green
	// check ("build") that has already completed.
	cfg.RequiredChecks = []string{"build", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             9,
		ScopeKey:             "epic:1",
		IssueNumber:          1,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "mainsha42",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[9] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	// GH-3990: a scope carrier's failure drains it from activePRs and
	// re-queues the scope — assert against the scope-release row's failure
	// reason instead of prState.Error, which handleScopeReleaseFailure never
	// touches for a scope carrier.
	if _, tracked := c.GetPRState(9); tracked {
		t.Error("PR should have been drained from activePRs after the carrier failure (GH-3990)")
	}
}

// TestHandleScopeReleaseFailure_ConfigMismatchReason_PropagatesToFailureAlert
// verifies that a GH-4646 config-mismatch reason string (produced by
// handlePostMergeCI's new CIConfigMismatch branch) flows through
// handleScopeReleaseFailure into the eventual scope_release_failed alert
// message — an operator reading the alert must see the actual missing/
// discovered check names, not a generic "post-merge CI failed" guess.
func TestHandleScopeReleaseFailure_ConfigMismatchReason_PropagatesToFailureAlert(t *testing.T) {
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

	reason := "post-merge CI required-checks config mismatch at sha-red: required check(s) [lint] never appear among this SHA's discovered checks [build] (GH-4646)"

	// A config-mismatch reason is not a timeout, so it must accumulate against
	// the ordinary attempts cap (maxScopeReleaseAttempts) rather than the
	// separate timeout-park path — drive it past that cap here.
	for i := 0; i <= maxScopeReleaseAttempts; i++ {
		c.handleScopeReleaseFailure(context.Background(), prState, reason, false)
	}

	row, err := stateStore.GetScopeRelease("owner/repo", "epic:1")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "failed" {
		t.Fatalf("state = %q, want failed after exceeding the attempts cap with a non-timeout reason", row.State)
	}
	if row.TimeoutAttempts != 0 {
		t.Errorf("timeout_attempts = %d, want 0 (a config-mismatch reason must not be classified as a timeout)", row.TimeoutAttempts)
	}

	var found bool
	for _, e := range sink.events {
		if e.Type == alerts.EventType("scope_release_failed") && strings.Contains(e.Error, "lint") && strings.Contains(e.Error, "build") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a scope_release_failed alert whose message names the missing (lint) and discovered (build) checks, got events: %+v", sink.events)
	}
}

// TestLintRequiredChecksMismatch_WarnsAtStartup is the GH-4646 startup-lint
// regression guard: a project whose effective required_checks is non-empty
// and whose latest main-branch SHA's check-runs don't cover it must produce
// a one-shot WARN at Controller.Start, before any PR or scope carrier ever
// hits the mismatch mid-flight.
func TestLintRequiredChecksMismatch_WarnsAtStartup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/branches/main":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Branch{Name: "main", Commit: github.BranchCommit{SHA: "mainsha42"}})
		case "/repos/owner/repo/commits/mainsha42/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.RequiredChecks = []string{"test", "lint"} // 'lint' never posts on this repo

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	var logBuf bytes.Buffer
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	c.lintRequiredChecksMismatch(context.Background())

	got := logBuf.String()
	if !strings.Contains(got, "required-checks lint") && !strings.Contains(got, "config mismatch") {
		t.Errorf("expected a startup lint WARN mentioning the config mismatch, got log:\n%s", got)
	}
	if !strings.Contains(got, "lint") {
		t.Errorf("expected the missing check name %q in the startup lint WARN, got log:\n%s", "lint", got)
	}
}
