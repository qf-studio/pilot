package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestGithubPollerRegistration_Fields verifies the SDK-based registration has the correct
// name and that its Enabled predicate no longer gates on the use_sdk_poller flag (M7 4d.6,
// GH-4171): the in-tree fallback poller is gone, so the SDK poller is unconditional
// whenever the GitHub adapter is enabled with a repo and polling turned on.
func TestGithubPollerRegistration_Fields(t *testing.T) {
	reg := githubPollerRegistration()

	if reg.Name != "github" {
		t.Errorf("PollerRegistration.Name = %q, want %q", reg.Name, "github")
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "nil github config",
			cfg:  &config.Config{Adapters: &config.AdaptersConfig{}},
			want: false,
		},
		{
			name: "github disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: false, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "use_sdk_poller off (default) — flag is ignored, SDK poller starts anyway (GH-4171)",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: false, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: true,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: false}},
			}},
			want: false,
		},
		{
			name: "nil polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r"},
			}},
			want: false,
		},
		{
			name: "no default repo — SDK path covers the default repo only (4b)",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: false,
		},
		{
			name: "all enabled incl. use_sdk_poller",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{Enabled: true, UseSDKPoller: true, Repo: "o/r", Polling: &github.PollingConfig{Enabled: true}},
			}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Enabled(tt.cfg)
			if got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGithubPollerRegistered verifies the GitHub SDK registration IS wired into
// adapterPollerRegistrations(), so daemons with the GitHub adapter enabled start
// the SDK poller for the default repo regardless of use_sdk_poller (GH-4171).
func TestGithubPollerRegistered(t *testing.T) {
	for _, reg := range adapterPollerRegistrations() {
		if reg.Name == "github" {
			return
		}
	}
	t.Error("github must be in adapterPollerRegistrations()")
}

// TestGithubSDKClientDoesNotImplementPRCreator documents the github behavior delta: unlike the
// GitLab SDK client (which implements executor.PRCreator and is injected via SetPRCreator), the
// studio-sdk GitHub *Client does NOT satisfy executor.PRCreator. The Phase-4a handler relies on
// this — GitHub PRs keep going through the gh CLI, and no PRCreator is injected.
func TestGithubSDKClientDoesNotImplementPRCreator(t *testing.T) {
	var i interface{} = (*githubSDK.Client)(nil)
	if _, ok := i.(executor.PRCreator); ok {
		t.Error("studio-sdk github *Client unexpectedly implements executor.PRCreator; " +
			"the Phase-4a handler assumes it does not (GitHub keeps the gh-CLI PR path)")
	}
}

// TestGithubRepoResolution verifies the Phase-4 github branch of sdkshim.ResolveRepoForEvent:
// it resolves a configured default repo (unlike the still-stubbed sources, which return
// ErrRepoNotResolved), and the SequenceID-derived repo name routes per-project.
func TestGithubRepoResolution(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Repo: "qf-studio/pilot"},
		},
	}
	clone, owner, repo, err := sdkshim.ResolveRepoForEvent(cfg, "github", sdkcore.IssueEvent{SequenceID: "GH-42", ProjectID: "pilot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "qf-studio" || repo != "pilot" || clone != "https://github.com/qf-studio/pilot.git" {
		t.Errorf("got (%q,%q,%q), want (https://github.com/qf-studio/pilot.git, qf-studio, pilot)", clone, owner, repo)
	}
}

// TestGithubPollerNoLegacyImport verifies poller_github.go does not directly import the legacy
// in-tree internal/adapters/github package on the SDK poll path (it uses the studio-sdk github
// package). The config dependency carries github.Config transitively, which is fine.
func TestGithubPollerNoLegacyImport(t *testing.T) {
	content, err := os.ReadFile("poller_github.go")
	if err != nil {
		t.Fatalf("failed to read poller_github.go: %v", err)
	}
	const legacyImport = `"github.com/qf-studio/pilot/internal/adapters/github"`
	if strings.Contains(string(content), legacyImport) {
		t.Errorf("poller_github.go must not import the legacy in-tree github package; found %q", legacyImport)
	}
}

// TestGithubHandlerSDKFunctionInvariants is a source-level regression guard SCOPED to the
// handleGithubIssueEventSDK function body (not a whole-file grep — the legacy in-tree handler
// shares several of these lines and legitimately uses fmt.Sprintf("GH-%d", issue.Number)).
// The SDK handler must derive taskID from ev.SequenceID verbatim, set SourceAdapter "github",
// and never re-prefix via GH-%d.
func TestGithubHandlerSDKFunctionInvariants(t *testing.T) {
	body := githubFuncBody(t, "handlers.go", "func handleGithubIssueEventSDK(")
	if !strings.Contains(body, "taskID := ev.SequenceID") {
		t.Error("handleGithubIssueEventSDK must derive taskID from ev.SequenceID verbatim")
	}
	if !strings.Contains(strings.Join(strings.Fields(body), " "), `SourceAdapter: "github"`) {
		t.Error(`handleGithubIssueEventSDK must set SourceAdapter: "github"`)
	}
	if strings.Contains(body, `"GH-`+`%d"`) {
		t.Error("handleGithubIssueEventSDK must not re-prefix the raw issue number into a GH- sequence (would yield GH-GH form)")
	}
}

// githubFuncBody returns the source of file between funcSignature and the next top-level "func "
// declaration, so assertions can be scoped to one function rather than the whole file.
func githubFuncBody(t *testing.T, file, funcSignature string) string {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	src := string(content)
	start := strings.Index(src, funcSignature)
	if start < 0 {
		t.Fatalf("function %q not found in %s", funcSignature, file)
	}
	rest := src[start+len(funcSignature):]
	if end := strings.Index(rest, "\nfunc "); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestVerifySDKGithubToken_DeadTokenDisablesPoller confirms a 401 from the
// GitHub API disables the SDK poller (returns false) and fires a
// config_error alert naming the token source (GH-3917 acceptance: no
// resolvable/valid token means no "polling enabled" line).
func TestVerifySDKGithubToken_DeadTokenDisablesPoller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	client := githubSDK.NewClientWithBaseURL("dead-token", srv.URL)
	engine, ch := newTestAlertsEngine(t)

	if ok := verifySDKGithubToken(context.Background(), client, githubTokenSourceEnv, engine); ok {
		t.Error("verifySDKGithubToken() = true, want false for a 401 (poller must be disabled)")
	}

	deadline := time.After(2 * time.Second)
	for ch.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected an alert to be fired for a dead SDK poller token")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestVerifySDKGithubToken_ValidTokenEnablesPoller confirms a healthy token
// lets the poller proceed (the caller only logs "polling enabled" after this
// returns true) and fires no alert.
func TestVerifySDKGithubToken_ValidTokenEnablesPoller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"pilot-bot"}`))
	}))
	defer srv.Close()

	client := githubSDK.NewClientWithBaseURL("good-token", srv.URL)
	engine, ch := newTestAlertsEngine(t)

	if ok := verifySDKGithubToken(context.Background(), client, githubTokenSourceConfig, engine); !ok {
		t.Error("verifySDKGithubToken() = false, want true for a valid token")
	}
	engine.WaitForDispatch()
	if got := ch.count(); got != 0 {
		t.Errorf("expected no alerts for a valid token, got %d", got)
	}
}

// TestVerifySDKGithubToken_NetworkErrorDoesNotDisablePoller confirms a
// transient failure (unreachable API, not a 401) doesn't disable the
// poller — only a confirmed dead/invalid token should.
func TestVerifySDKGithubToken_NetworkErrorDoesNotDisablePoller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := srv.URL
	srv.Close() // closed immediately: connections to this URL now fail

	client := githubSDK.NewClientWithBaseURL("some-token", unreachableURL)
	if ok := verifySDKGithubToken(context.Background(), client, githubTokenSourceEnv, nil); !ok {
		t.Error("verifySDKGithubToken() = false, want true for a network error (not evidence the token is dead)")
	}
}

// TestGithubPollerCreateAndStart_NoTokenDisablesPoller confirms CreateAndStart
// returns immediately (poller disabled) when the resolution chain
// (config -> GITHUB_TOKEN env -> gh CLI) yields nothing — the M7 4b defect
// (GH-3917) was that the SDK poller used ghCfg.Token verbatim and started
// (and logged "polling enabled") even when empty.
func TestGithubPollerCreateAndStart_NoTokenDisablesPoller(t *testing.T) {
	resetGitHubTokenTestState(t)
	ghRunner = fakeGhRunner(t, false, "", "", nil)

	reg := githubPollerRegistration()
	deps := &PollerDeps{
		Cfg: &config.Config{
			Adapters: &config.AdaptersConfig{
				GitHub: &github.Config{
					Enabled:      true,
					UseSDKPoller: true,
					Repo:         "o/r",
					Polling:      &github.PollingConfig{Enabled: true},
				},
			},
		},
	}

	done := make(chan struct{})
	go func() {
		reg.CreateAndStart(context.Background(), deps)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CreateAndStart should return immediately when no token resolves (poller disabled)")
	}
}

// --- GH-4377: judge failure metric wiring ---

// TestJudgeFailureCause_StructuredError verifies the cause is pulled out of a
// wrapped *executor.JudgeSubprocessError via errors.As.
func TestJudgeFailureCause_StructuredError(t *testing.T) {
	structured := &executor.JudgeSubprocessError{Err: errors.New("signal: killed"), Cause: "external_sigkill", PeakRSSMB: 128}
	wrapped := fmt.Errorf("intent judge subprocess: %w", structured)

	if got := judgeFailureCause(wrapped); got != "external_sigkill" {
		t.Errorf("expected external_sigkill, got %q", got)
	}
}

// TestJudgeFailureCause_PlainErrorDefaultsToOther verifies non-subprocess
// errors (e.g. a malformed-response parse failure) fall back to "other"
// rather than panicking or misreporting a kill cause.
func TestJudgeFailureCause_PlainErrorDefaultsToOther(t *testing.T) {
	if got := judgeFailureCause(errors.New("no VERDICT signal found in response")); got != "other" {
		t.Errorf("expected other, got %q", got)
	}
}

// TestSdkPreFlightJudge_JudgeIssue_RecordsFailureMetric drives JudgeIssue
// with a claude binary that can't even start (exec.Start fails), confirming
// the failure is both returned to the caller and recorded against the
// wired *autopilot.Metrics — the GH-4377 "Judge failure rate is visible as
// a metric" acceptance criterion.
func TestSdkPreFlightJudge_JudgeIssue_RecordsFailureMetric(t *testing.T) {
	metrics := autopilot.NewMetrics()
	sp := &sdkPreFlightJudge{
		judge:   executor.NewIntentJudge("/nonexistent/gh-4377-claude-binary"),
		metrics: metrics,
	}

	if _, err := sp.JudgeIssue(context.Background(), "title", "body", ""); err == nil {
		t.Fatal("expected error for a nonexistent claude binary")
	}

	snap := metrics.Snapshot()
	if snap.IntentJudgeFailures["other"] != 1 {
		t.Errorf("expected 1 'other' judge failure recorded, got: %+v", snap.IntentJudgeFailures)
	}
}

// TestSdkPreFlightJudge_JudgeIssue_NilMetricsSafe verifies a repo with no
// autopilot controller (nil metrics) doesn't panic on judge failure.
func TestSdkPreFlightJudge_JudgeIssue_NilMetricsSafe(t *testing.T) {
	sp := &sdkPreFlightJudge{
		judge:   executor.NewIntentJudge("/nonexistent/gh-4377-claude-binary"),
		metrics: nil,
	}

	if _, err := sp.JudgeIssue(context.Background(), "title", "body", ""); err == nil {
		t.Fatal("expected error for a nonexistent claude binary")
	}
}

// TestSdkPreFlightJudge_JudgeIssue_TimeoutIncrementsMetricOnce drives a real
// subprocess (not a mock) that outlives its preflight deadline, confirming
// the GH-4669 acceptance criterion directly: the timeout path both (a)
// increments pilot_intent_judge_failures_total with cause=context_deadline,
// and (b) fails open exactly once for a single JudgeIssue call — no internal
// retry loop inflates the failure count for one dispatch.
func TestSdkPreFlightJudge_JudgeIssue_TimeoutIncrementsMetricOnce(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this system")
	}

	dir := t.TempDir()
	scriptPath := dir + "/slow-claude.sh"
	// Ignores all args (title/body/model/flags) and just outlives any
	// reasonable preflight deadline, forcing a real context-deadline kill.
	// Uses `exec` so sleep replaces the shell in-place (single PID) — without
	// it, killing the shell leaves an orphaned sleep holding the stdout pipe
	// open, and Wait() blocks on pipe EOF for the full 5s regardless of the
	// context deadline.
	script := "#!/bin/sh\nexec sleep 5\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write test script: %v", err)
	}

	judge := executor.NewIntentJudge(scriptPath)
	judge.SetPreflightTimeout(50 * time.Millisecond)

	metrics := autopilot.NewMetrics()
	sp := &sdkPreFlightJudge{judge: judge, metrics: metrics}

	if _, err := sp.JudgeIssue(context.Background(), "title", "body", ""); err == nil {
		t.Fatal("expected error from a subprocess killed by context deadline")
	}

	snap := metrics.Snapshot()
	if snap.IntentJudgeFailures["context_deadline"] != 1 {
		t.Errorf("expected exactly 1 context_deadline judge failure recorded, got: %+v", snap.IntentJudgeFailures)
	}
	if got := sp.trackerFor().ConsecutiveFailures(); got != 1 {
		t.Errorf("expected consecutiveFailures=1 after a single dispatch's timeout, got %d", got)
	}
}

// --- GH-4669: fail-open observability (streak alert + no retry storm) ---

// TestSdkPreFlightJudge_FiresStreakAlertExactlyOnceAtThreshold drives
// judgeFailureStreakAlertThreshold consecutive failures through JudgeIssue
// and confirms exactly one alerts.EventTypeIntentJudgeFailureStreak event is
// emitted, at the failure that makes the streak equal to the threshold —
// not before, and not fired again on further failures past it (no retry
// storm).
func TestSdkPreFlightJudge_FiresStreakAlertExactlyOnceAtThreshold(t *testing.T) {
	config := &alerts.AlertConfig{
		Enabled: true,
		Channels: []alerts.ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []alerts.AlertRule{
			{
				Name:     "intent_judge_failure_streak",
				Type:     alerts.AlertTypeIntentJudgeFailureStreak,
				Enabled:  true,
				Severity: alerts.SeverityCritical,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}
	mockCh := newMockAlertChannel("test-channel")
	dispatcher := alerts.NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)
	engine := alerts.NewEngine(config, alerts.WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	sp := &sdkPreFlightJudge{
		judge:        executor.NewIntentJudge("/nonexistent/gh-4669-claude-binary"),
		alertsEngine: engine,
		repoFullName: "qf-studio/pilot",
	}

	// Drive one more failure than the threshold to confirm no retry storm:
	// the alert must fire exactly once, at the threshold-th failure.
	for i := 0; i < judgeFailureStreakAlertThreshold+2; i++ {
		if _, err := sp.JudgeIssue(context.Background(), "title", "body", ""); err == nil {
			t.Fatal("expected error for a nonexistent claude binary")
		}
	}

	waitForMockAlerts(t, mockCh, 1, 2*time.Second)
	got := mockCh.getAlerts()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 streak alert (no retry storm), got %d", len(got))
	}
	if got[0].Type != alerts.AlertTypeIntentJudgeFailureStreak {
		t.Errorf("expected alert type %s, got %s", alerts.AlertTypeIntentJudgeFailureStreak, got[0].Type)
	}
}

// TestSdkPreFlightJudge_SuccessResetsStreak verifies a successful JudgeIssue
// call resets the consecutive-failure counter, so failures before and after
// an intervening success don't compound toward the alert threshold.
func TestSdkPreFlightJudge_SuccessResetsStreak(t *testing.T) {
	sp := &sdkPreFlightJudge{
		judge: executor.NewIntentJudge("/nonexistent/gh-4669-claude-binary"),
	}

	for i := 0; i < judgeFailureStreakAlertThreshold-1; i++ {
		if _, err := sp.JudgeIssue(context.Background(), "title", "body", ""); err == nil {
			t.Fatal("expected error for a nonexistent claude binary")
		}
	}
	if got := sp.trackerFor().ConsecutiveFailures(); got != judgeFailureStreakAlertThreshold-1 {
		t.Fatalf("expected streak of %d, got %d", judgeFailureStreakAlertThreshold-1, got)
	}

	sp.trackerFor().RecordSuccess()
	if got := sp.trackerFor().ConsecutiveFailures(); got != 0 {
		t.Fatalf("expected streak reset to 0 after success, got %d", got)
	}
}

// --- GH-4587: dispatch-gated declines must read the execution_claims +
// executions ledger, not be mistranslated into the vendored SDK poller's
// "failed without PR, unmarking for retry" / "completed execution exists"
// branches ---

// TestHandlerResult_IsDispatchGated verifies the helper handlers.go now
// relies on to translate a HandlerResult into an sdkcore.IssueResult:
// a bare or wrapped executor.ErrDispatchGated reports true; a genuine
// execution error, or no error at all, reports false.
func TestHandlerResult_IsDispatchGated(t *testing.T) {
	gated := &HandlerResult{Error: executor.ErrDispatchGated}
	if !gated.IsDispatchGated() {
		t.Error("expected IsDispatchGated() = true for a bare ErrDispatchGated")
	}

	wrapped := &HandlerResult{Error: fmt.Errorf("dispatch: %w", executor.ErrDispatchGated)}
	if !wrapped.IsDispatchGated() {
		t.Error("expected IsDispatchGated() = true for a wrapped ErrDispatchGated")
	}

	genuine := &HandlerResult{Error: errors.New("execution failed: boom")}
	if genuine.IsDispatchGated() {
		t.Error("expected IsDispatchGated() = false for a genuine execution error")
	}

	clean := &HandlerResult{}
	if clean.IsDispatchGated() {
		t.Error("expected IsDispatchGated() = false for a nil Error")
	}
}

// TestGithubSDKTranslation_LiveClaimStillRunning_DoesNotMislabelAsFailed is
// the GH-4587 criterion-(a) regression test: a task with a live claim
// (genuinely queued/running per the real execution_claims + executions
// ledger — not any in-memory/status-label heuristic) must translate into an
// sdkcore.IssueResult with Success=true, so the vendored GitHub poller's
// "!result.Success && result.PRNumber == 0 -> unmarking for retry" branch
// never fires for a task another channel/generation is actively working.
//
// handleGithubIssueEventSDK itself can't be exercised end-to-end in a unit
// test — it resolves a real GitHub token/repo and fetches the live issue
// over the network before ever reaching handleIssueGeneric, which is why
// every other SDK-handler test in this file (e.g.
// TestGithubHandlerSDKFunctionInvariants) asserts against its source body
// instead. This test drives the actual translation formula
// (`hr.Success || hr.IsDispatchGated()`, verbatim from handlers.go) against
// a *real* HandlerResult produced by handleIssueGeneric with a genuinely
// active task in a real on-disk store — the same admission gate
// handleGithubIssueEventSDK's own call into handleIssueGeneric reaches — so
// it's the ledger read, not a mock, proving the mislabel doesn't trip.
func TestGithubSDKTranslation_LiveClaimStillRunning_DoesNotMislabelAsFailed(t *testing.T) {
	dispatcher := newHandlerTestDispatcher(t)

	taskID := "GH-4587-LIVE-CLAIM"
	projectPath := "/tmp/pilot-gh-4587-live-claim-does-not-exist"
	seedTask := &executor.Task{ID: taskID, Title: "seed", ProjectPath: projectPath}
	if _, err := dispatcher.QueueTask(context.Background(), seedTask); err != nil {
		t.Fatalf("failed to seed queued task: %v", err)
	}

	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "seed", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "seed", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("expected nil error for an already-active task, got: %v", err)
	}
	if !errors.Is(hr.Error, executor.ErrDispatchGated) {
		t.Fatalf("expected hr.Error to wrap ErrDispatchGated, got: %v", hr.Error)
	}

	// This is the exact formula handleGithubIssueEventSDK / handleGitlabIssueWithResult
	// (cmd/pilot/handlers.go) use to build the sdkcore.IssueResult handed back to the poller.
	issueResult := &sdkcore.IssueResult{
		Success:  hr.Success || hr.IsDispatchGated(),
		PRNumber: hr.PRNumber,
	}

	if !issueResult.Success {
		t.Error("expected Success=true for a live-claim admission-gate decline — Success=false here " +
			"would trip the vendored poller's 'failed without PR, unmarking for retry' branch " +
			"for a task that is still actively running")
	}
	if issueResult.PRNumber != 0 {
		t.Errorf("expected PRNumber=0 (no execution happened), got %d", issueResult.PRNumber)
	}
}

// TestTerminalCompletionChecker_LiveRunningExecution_NotReportedAsCompleted
// is the GH-4587 criterion-(b) regression test: the ExecutionChecker wired
// into the SDK poller (terminalCompletionChecker, poller_github.go:362) must
// gate "completed execution exists — skipping re-dispatch" on the executions
// ledger's actual status, not merely on "some execution row for this task
// exists" — a task with a live (running, non-terminal) execution row and no
// completed row must report false, distinct from
// TestTerminalCompletionChecker_GenuineCompletion_StillReportsTrue (which
// covers the true/completed side) and the repick-backoff gating tests in
// terminal_completion_checker_test.go (an intentionally separate signal,
// GH-4469).
func TestTerminalCompletionChecker_LiveRunningExecution_NotReportedAsCompleted(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	taskID := "GH-4587-LIVE-RUNNING"
	projectPath := "/tmp/pilot-gh-4587-live-running-does-not-exist"
	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	if _, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	checker := terminalCompletionChecker{store: store}
	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected HasCompletedExecution = false for a task with only a live running execution row — " +
			"reporting true here would make the poller skip re-checking a task that is not actually done")
	}
}

// TestGithubPollerMetricsAdapter_RecordsMetricAndLogsPendingDependency is the
// GH-5336 regression test for the host-side visibility gap: the SDK poller's
// own log line for a pending_dependency skip only fires at Debug in its
// parallel-mode path (poller.go:1072), invisible at production's `level:
// info` — which is exactly why the #5322/#5325 fix-issue deadlock
// (2026-09-06) produced zero daemon.log lines for over two hours. This test
// verifies the adapter wired into pollerDeps.PollerMetrics both forwards the
// skip to autopilot.Metrics (so pilot_poller_skipped_total is populated,
// previously a silent no-op for the GitHub SDK poller since PollerMetrics
// was never wired at all) AND emits its own INFO line for that specific
// reason.
func TestGithubPollerMetricsAdapter_RecordsMetricAndLogsPendingDependency(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	metrics := autopilot.NewMetrics()
	adapter := newGithubPollerMetricsAdapter(metrics, log)

	adapter.RecordPollerSkipped("owner/repo", skipreason.ReasonPendingDependency)

	if got := metrics.PollerSkipped[struct {
		Repo   string
		Reason string
	}{Repo: "owner/repo", Reason: skipreason.ReasonPendingDependency}]; got != 1 {
		t.Errorf("PollerSkipped count = %d, want 1 (metric must still be recorded, not just logged)", got)
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=INFO") {
		t.Errorf("expected an INFO-level log line for a pending_dependency skip, got: %s", logged)
	}
	if !strings.Contains(logged, "pending dependency") {
		t.Errorf("expected the log line to mention the pending dependency skip, got: %s", logged)
	}
	if !strings.Contains(logged, "owner/repo") {
		t.Errorf("expected the log line to identify the repo, got: %s", logged)
	}
}

// TestGithubPollerMetricsAdapter_OtherReasonsDoNotLog confirms the adapter
// only adds its extra INFO line for pending_dependency skips — other skip
// reasons still forward to the metric but must not spam daemon.log with a
// duplicate, out-of-context INFO line the SDK doesn't already reason about.
func TestGithubPollerMetricsAdapter_OtherReasonsDoNotLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	metrics := autopilot.NewMetrics()
	adapter := newGithubPollerMetricsAdapter(metrics, log)

	adapter.RecordPollerSkipped("owner/repo", skipreason.ReasonDone)
	adapter.RecordPollerDispatched("owner/repo")
	adapter.RecordPollerDeferredScopeOverlap("owner/repo")
	adapter.RecordUnsourcedLabeledIssues("owner/repo", 3)

	if buf.Len() != 0 {
		t.Errorf("expected no log output for non-pending-dependency calls, got: %s", buf.String())
	}
	if metrics.PollerDispatched["owner/repo"] != 1 {
		t.Errorf("PollerDispatched not forwarded")
	}
	if metrics.PollerDeferredScopeOverlap["owner/repo"] != 1 {
		t.Errorf("PollerDeferredScopeOverlap not forwarded")
	}
	if metrics.UnsourcedLabeledIssues["owner/repo"] != 3 {
		t.Errorf("RecordUnsourcedLabeledIssues not bridged to SetUnsourcedLabeledIssues correctly, got %d", metrics.UnsourcedLabeledIssues["owner/repo"])
	}
}

// TestIntentJudgePreflightStillWired pins that GH-6157, which retired the in-run
// intent judge, left the pre-flight issue judge wired: with executor.pre_flight_judge
// enabled the per-repo builder returns a judge that answers through the claude
// subprocess, and with it disabled it returns none.
func TestIntentJudgePreflightStillWired(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this system")
	}
	scriptPath := t.TempDir() + "/fake-claude.sh"
	script := "#!/bin/sh\ncat >/dev/null\nprintf 'DECISION: accept\\nREASON: Clear and specific.\\nCONFIDENCE: 0.9\\n'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	repoLog := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	newCfg := func(enabled bool) *config.Config {
		return &config.Config{Executor: &executor.BackendConfig{
			ClaudeCode:     &executor.ClaudeCodeConfig{Command: scriptPath},
			PreFlightJudge: &executor.PreFlightJudgeConfig{Enabled: enabled, Timeout: "5s"},
			// The retired in-run judge's block must not matter to the pre-flight judge.
			IntentJudge: executor.DefaultIntentJudgeConfig(),
		}}
	}

	judge := newPreFlightJudge(newCfg(true), "o/r", nil, nil, repoLog)
	if judge == nil {
		t.Fatal("pre-flight judge is not wired with pre_flight_judge.enabled=true")
	}
	var _ sdkcore.PreFlightJudger = judge
	v, err := judge.JudgeIssue(context.Background(), "Add login button", "Add a login button to the header nav", "")
	if err != nil {
		t.Fatalf("JudgeIssue: %v", err)
	}
	if !v.Accepted || v.Decision != string(executor.PreFlightAccept) {
		t.Errorf("verdict = %+v, want an accept through the subprocess", v)
	}

	if got := newPreFlightJudge(newCfg(false), "o/r", nil, nil, repoLog); got != nil {
		t.Error("pre-flight judge wired although pre_flight_judge.enabled=false")
	}
}

const autopilotFixTestBody = "# Autopilot: Auto-Generated Fix Request\n\n## Task\n\nFix it.\n\n---\n*This issue was auto-generated by Pilot autopilot.*\n\n"

const autopilotFixTestFooter = "<!-- autopilot-meta branch:pilot/GH-6100 pr:6165 iteration:1 source:6100 sha:0123456789abcdef0123456789abcdef01234567 -->"

const autopilotFixTestTitle = "fix(ci): resolve CI failure from PR #6165"

// autopilotFixLabelServer serves GET /repos/o/r/issues the way GitHub does for
// the real github.Client: one open issue per entry, labels attached. It
// returns the pre-flight judge wired to that server and a request counter.
func autopilotFixLabelServer(t *testing.T, issues []map[string]any, status int) (*sdkPreFlightJudge, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/repos/o/r/issues" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	t.Cleanup(srv.Close)
	return &sdkPreFlightJudge{
		judge:        executor.NewIntentJudge("/nonexistent/gh-6182-claude-binary"),
		metrics:      autopilot.NewMetrics(),
		repoFullName: "o/r",
		ghClient:     githubSDK.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL),
	}, calls
}

func autopilotFixIssue(title, body string, labels ...string) map[string]any {
	ls := []map[string]any{}
	for _, l := range labels {
		ls = append(ls, map[string]any{"name": l})
	}
	return map[string]any{"number": 1, "title": title, "body": body, "state": "open", "labels": ls}
}

// TestPreFlight_AutopilotFixFooterSkipsJudge: an issue carrying the
// autopilot-fix label AND ending in a complete autopilot-meta footer is
// accepted without a model call (GH-6182). The judge binary does not exist,
// so any call would return an error.
func TestPreFlight_AutopilotFixFooterSkipsJudge(t *testing.T) {
	body := autopilotFixTestBody + autopilotFixTestFooter + "\n"
	sp, _ := autopilotFixLabelServer(t, []map[string]any{
		autopilotFixIssue(autopilotFixTestTitle, body, "pilot", "autopilot-fix"),
	}, http.StatusOK)

	v, err := sp.JudgeIssue(context.Background(), autopilotFixTestTitle, body, "")
	if err != nil {
		t.Fatalf("judge was invoked for a labelled footer-bearing autopilot-fix issue: %v", err)
	}
	if !v.Accepted || v.Decision != "accept" || v.Reason != "autopilot-fix footer" {
		t.Errorf("verdict = %+v, want accept / autopilot-fix footer", v)
	}
	if snap := sp.metrics.Snapshot(); len(snap.IntentJudgeFailures) != 0 {
		t.Errorf("no judge failure should be recorded: %+v", snap.IntentJudgeFailures)
	}
}

// TestPreFlight_AutopilotFixFooterWithoutLabelStillJudged: a complete footer
// (even with the generator's heading and auto-generated line copied verbatim)
// does not skip the judge unless the issue carries the autopilot-fix label.
func TestPreFlight_AutopilotFixFooterWithoutLabelStillJudged(t *testing.T) {
	body := autopilotFixTestBody + autopilotFixTestFooter + "\n"
	handFiled := "Please fix the thing.\n\n" + autopilotFixTestFooter + "\n"
	cases := map[string]struct {
		issues []map[string]any
		status int
		noGH   bool
		body   string // overrides the generated body when set
	}{
		"issue has no autopilot-fix label": {
			issues: []map[string]any{autopilotFixIssue(autopilotFixTestTitle, body, "pilot")},
			status: http.StatusOK,
		},
		"labelled issue has a different body": {
			issues: []map[string]any{autopilotFixIssue(autopilotFixTestTitle, "other body", "autopilot-fix")},
			status: http.StatusOK,
		},
		"hand-filed footer, no label": {
			issues: []map[string]any{autopilotFixIssue(autopilotFixTestTitle, handFiled, "pilot")},
			status: http.StatusOK,
			body:   handFiled,
		},
		"label lookup fails": {status: http.StatusInternalServerError},
		"no github client":   {status: http.StatusOK, noGH: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sp, _ := autopilotFixLabelServer(t, tc.issues, tc.status)
			if tc.noGH {
				sp.ghClient = nil
			}
			b := body
			if tc.body != "" {
				b = tc.body
			}
			if _, err := sp.JudgeIssue(context.Background(), autopilotFixTestTitle, b, ""); err == nil {
				t.Error("expected the judge to be invoked (error from missing binary), got a bypass")
			}
		})
	}
}

// TestPreFlight_AutopilotFixWithoutFooterStillJudged: even with the label, a
// body without a well-formed footer still reaches the judge (here: errors on
// the missing binary) and the label is not even looked up.
func TestPreFlight_AutopilotFixWithoutFooterStillJudged(t *testing.T) {
	cases := map[string]string{
		"no footer":             "## Task\n\nFix the CI failures.\n",
		"footer missing sha":    autopilotFixTestBody + "<!-- autopilot-meta branch:pilot/GH-6100 pr:6165 iteration:1 source:6100 -->\n",
		"footer missing pr":     autopilotFixTestBody + "<!-- autopilot-meta branch:pilot/GH-6100 iteration:1 source:6100 sha:0123456789abcdef -->\n",
		"footer missing src":    autopilotFixTestBody + "<!-- autopilot-meta branch:pilot/GH-6100 pr:6165 iteration:1 sha:0123456789abcdef -->\n",
		"footer missing brnch":  autopilotFixTestBody + "<!-- autopilot-meta pr:6165 iteration:1 source:6100 sha:0123456789abcdef -->\n",
		"footer not last":       autopilotFixTestBody + autopilotFixTestFooter + "\n\ntrailing prose\n",
		"review-feedback issue": "# Autopilot: Review Feedback\n\nreviewer text\n" + autopilotFixTestFooter + "\n",
		"footer in code fence":  "## Task\n\n```\n" + autopilotFixTestFooter + "\n```\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sp, calls := autopilotFixLabelServer(t, []map[string]any{
				autopilotFixIssue("title", body, "autopilot-fix"),
			}, http.StatusOK)
			if _, err := sp.JudgeIssue(context.Background(), "title", body, ""); err == nil {
				t.Error("expected the judge to be invoked (error from missing binary), got a bypass")
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("label lookup ran %d times for a body without a valid footer", n)
			}
		})
	}
}

// mutation pin: item 1, a hand-filed unlabelled copy of a live autopilot-fix issue still skips the judge (title+body matched, label read from the labelled twin only) -> this test fails
func TestPreFlight_AutopilotFixUnlabelledDuplicateStillJudged(t *testing.T) {
	body := autopilotFixTestBody + autopilotFixTestFooter + "\n"
	sp, _ := autopilotFixLabelServer(t, []map[string]any{
		autopilotFixIssue(autopilotFixTestTitle, body, "pilot", "autopilot-fix"),
		autopilotFixIssue(autopilotFixTestTitle, body),
	}, http.StatusOK)

	if _, err := sp.JudgeIssue(context.Background(), autopilotFixTestTitle, body, ""); err == nil {
		t.Error("expected the judge to be invoked (error from missing binary): an unlabelled copy shares the title and body, got a bypass")
	}
}

// mutation pin: item 2, every footer-bearing candidate costs its own full open-issue listing -> this test fails
func TestPreFlight_AutopilotFixListingReusedAcrossCandidates(t *testing.T) {
	bodyA := autopilotFixTestBody + autopilotFixTestFooter + "\n"
	bodyB := autopilotFixTestBody + strings.Replace(autopilotFixTestFooter, "pr:6165", "pr:6166", 1) + "\n"
	sp, calls := autopilotFixLabelServer(t, []map[string]any{
		autopilotFixIssue("fix A", bodyA, "autopilot-fix"),
		autopilotFixIssue("fix B", bodyB, "autopilot-fix"),
	}, http.StatusOK)

	for title, body := range map[string]string{"fix A": bodyA, "fix B": bodyB} {
		if _, err := sp.JudgeIssue(context.Background(), title, body, ""); err != nil {
			t.Fatalf("%s: judge was invoked for a labelled footer-bearing issue: %v", title, err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("open-issue listings = %d for two candidates in one cycle, want 1", n)
	}

	// A candidate missing from the reused listing was created after it: the
	// listing is refetched once before the issue is judged.
	bodyC := autopilotFixTestBody + strings.Replace(autopilotFixTestFooter, "pr:6165", "pr:6167", 1) + "\n"
	if _, err := sp.JudgeIssue(context.Background(), "fix C", bodyC, ""); err == nil {
		t.Error("expected the judge to be invoked for an issue absent from the listing")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("open-issue listings = %d after an unseen candidate, want 2", n)
	}
}

// mutation pin: item 3, issue_labels without autopilot-fix turns the bypass off with no message -> this test fails
func TestNewPreFlightJudge_WarnsWhenAutopilotFixLabelNotConfigured(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this system")
	}
	newCfg := func(labels []string) *config.Config {
		return &config.Config{
			Executor: &executor.BackendConfig{
				ClaudeCode:     &executor.ClaudeCodeConfig{Command: "sh"},
				PreFlightJudge: &executor.PreFlightJudgeConfig{Enabled: true},
			},
			Orchestrator: &config.OrchestratorConfig{
				Autopilot: &autopilot.Config{AutoCreateIssues: true, IssueLabels: labels},
			},
		}
	}
	cases := map[string]struct {
		labels   []string
		wantWarn bool
	}{
		"override without autopilot-fix": {labels: []string{"pilot", "ci-fix"}, wantWarn: true},
		"default labels":                 {labels: []string{"pilot", "autopilot-fix"}},
		"autopilot-fix, other casing":    {labels: []string{"Autopilot-Fix"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			repoLog := slog.New(slog.NewTextHandler(&buf, nil))
			if newPreFlightJudge(newCfg(tc.labels), "o/r", nil, nil, repoLog) == nil {
				t.Fatal("pre-flight judge not built")
			}
			if got := strings.Contains(buf.String(), "autopilot.issue_labels lacks autopilot-fix"); got != tc.wantWarn {
				t.Errorf("warning logged = %v, want %v; log: %s", got, tc.wantWarn, buf.String())
			}
		})
	}
}
