package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
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
	if sp.consecutiveFailures != 1 {
		t.Errorf("expected consecutiveFailures=1 after a single dispatch's timeout, got %d", sp.consecutiveFailures)
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
	if sp.consecutiveFailures != judgeFailureStreakAlertThreshold-1 {
		t.Fatalf("expected streak of %d, got %d", judgeFailureStreakAlertThreshold-1, sp.consecutiveFailures)
	}

	sp.recordSuccess()
	if sp.consecutiveFailures != 0 {
		t.Fatalf("expected streak reset to 0 after success, got %d", sp.consecutiveFailures)
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
