package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testBillingKey     = "fake-billing-key-value"
	testAmbientAPIKey  = "ambient-fake-api-key"
	testAmbientAuthTok = "ambient-fake-auth-token"
	testBillingBaseURL = "https://proxy.example.test/anthropic"
)

func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("relies on sh")
	}
}

func apiKeyConfig(command string) *BillingConfig {
	return &BillingConfig{Mode: BillingModeAPIKey, APIKeyCommand: command}
}

func billingEnvValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// fakeEnvCLI writes a script that dumps its environment to a file and returns
// both paths. The script prints nothing, so callers only read the dump.
func fakeEnvCLI(t *testing.T) (script, dump string) {
	t.Helper()
	dir := t.TempDir()
	dump = filepath.Join(dir, "env.txt")
	script = filepath.Join(dir, "claude")
	body := "#!/bin/sh\n/usr/bin/env > " + dump + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	return script, dump
}

func readEnvDump(t *testing.T, dump string) []string {
	t.Helper()
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("fake CLI did not run: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestBillingMode_ResolverModes(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("PILOT_TEST_BILLING_KEY", "  "+testBillingKey+"  ")
	t.Setenv("PILOT_TEST_BILLING_EMPTY", "")

	tests := []struct {
		name      string
		cfg       *BillingConfig
		wantMode  BillingMode
		wantEnv   []string
		wantErr   string
		wantInErr string
	}{
		{name: "nil config is unset", cfg: nil, wantMode: BillingModeUnset},
		{name: "empty mode is unset", cfg: &BillingConfig{}, wantMode: BillingModeUnset},
		{name: "explicit subscription adds nothing", cfg: &BillingConfig{Mode: BillingModeSubscription, APIKeyCommand: "echo ignored"}, wantMode: BillingModeSubscription},
		{name: "command stdout is trimmed", cfg: apiKeyConfig("printf '  " + testBillingKey + "\\n'"), wantMode: BillingModeAPIKey, wantEnv: []string{"ANTHROPIC_API_KEY=" + testBillingKey}},
		{name: "env var", cfg: &BillingConfig{Mode: BillingModeAPIKey, APIKeyEnv: "PILOT_TEST_BILLING_KEY"}, wantMode: BillingModeAPIKey, wantEnv: []string{"ANTHROPIC_API_KEY=" + testBillingKey}},
		{name: "env var unset", cfg: &BillingConfig{Mode: BillingModeAPIKey, APIKeyEnv: "PILOT_TEST_BILLING_NOPE"}, wantErr: "billing_key_unavailable"},
		{name: "env var empty", cfg: &BillingConfig{Mode: BillingModeAPIKey, APIKeyEnv: "PILOT_TEST_BILLING_EMPTY"}, wantErr: "billing_key_unavailable"},
		{name: "command exit code", cfg: apiKeyConfig("exit 3"), wantErr: "billing_key_unavailable", wantInErr: "exit code 3"},
		{name: "command empty stdout", cfg: apiKeyConfig("true"), wantErr: "billing_key_unavailable"},
		{name: "neither source", cfg: &BillingConfig{Mode: BillingModeAPIKey}, wantErr: "billing_key_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cred, err := NewBillingResolver(tt.cfg).Resolve(context.Background())
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
				}
				if tt.wantInErr != "" && !strings.Contains(err.Error(), tt.wantInErr) {
					t.Errorf("err = %q, want it to contain %q", err, tt.wantInErr)
				}
				if cred != nil {
					t.Errorf("credential = %+v alongside an error, want nil", cred)
				}
				if !IsBillingKeyUnavailable(err) {
					t.Errorf("IsBillingKeyUnavailable(%v) = false", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if cred.Mode() != tt.wantMode {
				t.Errorf("mode = %q, want %q", cred.Mode(), tt.wantMode)
			}
			if got := cred.Env(); strings.Join(got, "|") != strings.Join(tt.wantEnv, "|") {
				t.Errorf("env = %v, want %v", got, tt.wantEnv)
			}
		})
	}
}

func TestBillingMode_CommandTimeout(t *testing.T) {
	skipWithoutShell(t)
	r := NewBillingResolver(apiKeyConfig("sleep 5"))
	r.timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := r.Resolve(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "billing_key_unavailable") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a billing_key_unavailable timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took %v, want ~100ms", elapsed)
	}
}

// TestBillingMode_NeverLogsStdout: stderr reaches the log at warn, the key on
// stdout never does.
func TestBillingMode_NeverLogsStdout(t *testing.T) {
	skipWithoutShell(t)
	var buf bytes.Buffer
	r := NewBillingResolver(apiKeyConfig("echo vault-warning >&2; printf " + testBillingKey))
	r.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "vault-warning") {
		t.Errorf("stderr not logged at warn: %q", logs)
	}
	if strings.Contains(logs, testBillingKey) {
		t.Errorf("log carries the key: %q", logs)
	}
}

// TestBillingMode_FailClosedNeverFallsBack: a failing command is an error, not
// a subscription credential.
func TestBillingMode_FailClosedNeverFallsBack(t *testing.T) {
	skipWithoutShell(t)
	cred, err := NewBillingResolver(apiKeyConfig("exit 1")).Resolve(context.Background())
	if err == nil {
		t.Fatalf("Resolve succeeded with mode %q, want a billing_key_unavailable error", cred.Mode())
	}
	if cred != nil {
		t.Errorf("credential = %+v, want nil on error", cred)
	}
	var ke *BillingKeyError
	if !errors.As(err, &ke) || ke.ExitCode != 1 {
		t.Errorf("err = %v, want *BillingKeyError with exit code 1", err)
	}
}

func TestBillingMode_CredentialZeroWipesKey(t *testing.T) {
	cred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	ctx := WithBillingCredential(context.Background(), cred)
	if got := BillingCredentialFromContext(ctx); got != cred || len(got.Env()) != 1 {
		t.Fatalf("context credential = %+v", got)
	}
	cred.Zero()
	if env := cred.Env(); len(env) != 0 {
		t.Errorf("env after Zero = %v, want none", env)
	}
	if BillingCredentialFromContext(context.Background()) != nil {
		t.Error("empty context returned a credential")
	}
	var nilCred *BillingCredential
	nilCred.Zero() // must not panic
	if nilCred.MemoryMode() != "unknown" || nilCred.Mode() != BillingModeUnset {
		t.Errorf("nil credential = %q / %q", nilCred.MemoryMode(), nilCred.Mode())
	}
}

func TestModelSubprocessEnvFor_SubscriptionStripsInheritedKey(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=" + testAmbientAPIKey,
		"ANTHROPIC_AUTH_TOKEN=" + testAmbientAuthTok,
		"ANTHROPIC_BASE_URL=" + testBillingBaseURL,
		"ANTHROPIC_MODEL=claude-test-model",
		"TELEGRAM_BOT_TOKEN=test-telegram-bot-token",
	}
	ctx := WithBillingCredential(context.Background(), &BillingCredential{mode: BillingModeSubscription})
	env := modelSubprocessEnvFor(ctx, base)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "TELEGRAM_BOT_TOKEN"} {
		if v, ok := billingEnvValue(env, banned); ok {
			t.Errorf("%s=%q reached the subprocess env", banned, v)
		}
	}
	for name, want := range map[string]string{"ANTHROPIC_BASE_URL": testBillingBaseURL, "ANTHROPIC_MODEL": "claude-test-model", "PATH": "/usr/bin"} {
		if got, _ := billingEnvValue(env, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestModelSubprocessEnvFor_APIKeyAppendsCredential(t *testing.T) {
	base := []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=" + testAmbientAPIKey}
	cred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	env := modelSubprocessEnvFor(WithBillingCredential(context.Background(), cred), base)

	var keys []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			keys = append(keys, kv)
		}
	}
	if len(keys) != 1 || keys[0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Fatalf("ANTHROPIC_API_KEY entries = %v, want exactly the resolved key", keys)
	}

	cred.Zero()
	if v, ok := billingEnvValue(modelSubprocessEnvFor(WithBillingCredential(context.Background(), cred), base), "ANTHROPIC_API_KEY"); ok {
		t.Errorf("zeroed credential still injected %q", v)
	}
}

// Subscription mode: the daemon env carries a key, the claude backend's
// recorded command env has neither credential, and the configured base URL
// survives.
func TestBillingMode_SubscriptionBackendEnvHasNoKey(t *testing.T) {
	skipWithoutShell(t)
	script, dump := fakeEnvCLI(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", testAmbientAuthTok)

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	backend.SetProviderEnv(testBillingBaseURL, "", "")
	r := NewRunnerWithBackend(backend)
	r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Command: script, Billing: &BillingConfig{Mode: BillingModeSubscription}}}

	res, _ := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	env := readEnvDump(t, dump)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := billingEnvValue(env, banned); ok {
			t.Errorf("%s=%q reached the claude backend", banned, v)
		}
	}
	if got, _ := billingEnvValue(env, "ANTHROPIC_BASE_URL"); got != testBillingBaseURL {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, testBillingBaseURL)
	}
	if res.BillingMode != "subscription" {
		t.Errorf("chat result billing mode = %q, want subscription", res.BillingMode)
	}
}

// Config api_auth_token is still injected after the scrub, so non-Anthropic
// providers behave as before.
func TestBillingMode_ProviderAuthTokenStillInjected(t *testing.T) {
	skipWithoutShell(t)
	script, dump := fakeEnvCLI(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", testAmbientAuthTok)

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	backend.SetProviderEnv(testBillingBaseURL, "provider-fake-token", "")
	r := NewRunnerWithBackend(backend)
	_, _ = r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if got, _ := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_AUTH_TOKEN"); got != "provider-fake-token" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want the configured provider token", got)
	}
}

// api_key mode: the claude backend and the effort classifier both carry the
// resolved key, and the daemon process environment does not.
func TestBillingMode_APIKeyReachesBackendAndClassifier(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", "") // the daemon env must stay free of the key
	if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
		t.Fatal(err)
	}
	script, dump := fakeEnvCLI(t)

	r := NewRunnerWithBackend(NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script}))
	r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{
		Command: script, Billing: apiKeyConfig("printf " + testBillingKey),
	}}
	res, _ := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if got, _ := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); got != testBillingKey {
		t.Errorf("claude backend ANTHROPIC_API_KEY = %q, want %q", got, testBillingKey)
	}
	if res.BillingMode != "api_key" {
		t.Errorf("chat result billing mode = %q, want api_key", res.BillingMode)
	}

	// The effort classifier spawns "claude" from PATH.
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(script)+string(os.PathListSeparator)+os.Getenv("PATH"))
	cred, err := NewBillingResolver(r.config.ClaudeCode.Billing).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer cred.Zero()
	_, _ = NewEffortClassifier().defaultCmdRunner(WithBillingCredential(context.Background(), cred), "-p", "x")
	if got, _ := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); got != testBillingKey {
		t.Errorf("effort classifier ANTHROPIC_API_KEY = %q, want %q", got, testBillingKey)
	}

	if v, ok := os.LookupEnv("ANTHROPIC_API_KEY"); ok {
		t.Errorf("daemon process environment gained ANTHROPIC_API_KEY=%q", v)
	}
}

func TestBillingMode_ChatTurnFailsClosed(t *testing.T) {
	skipWithoutShell(t)
	script, dump := fakeEnvCLI(t)
	r := NewRunnerWithBackend(NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script}))
	r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Billing: apiKeyConfig("exit 1")}}

	res, err := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "billing_key_unavailable") {
		t.Fatalf("err = %v, want billing_key_unavailable", err)
	}
	if res.BillingMode != "api_key" {
		t.Errorf("billing mode = %q, want api_key", res.BillingMode)
	}
	if _, statErr := os.Stat(dump); statErr == nil {
		t.Error("the claude CLI ran despite the unresolved key")
	}
}

// countingBackend counts Execute calls and succeeds.
type countingBackend struct {
	runs atomic.Int32
	mu   sync.Mutex
	env  []string // credential entries seen on the last run's context
}

func (b *countingBackend) Name() string      { return "counting" }
func (b *countingBackend) IsAvailable() bool { return true }
func (b *countingBackend) Execute(ctx context.Context, _ ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.env = BillingCredentialFromContext(ctx).Env()
	b.mu.Unlock()
	b.runs.Add(1)
	return &BackendResult{Success: true, Output: "ok"}, nil
}

type billingAlertSink struct {
	mu     sync.Mutex
	events []AlertEvent
}

func (s *billingAlertSink) ProcessEvent(e AlertEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *billingAlertSink) billing() []AlertEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []AlertEvent
	for _, e := range s.events {
		if e.Type == AlertEventTypeBillingKeyUnavailable {
			out = append(out, e)
		}
	}
	return out
}

func newBillingDispatcher(t *testing.T, backend Backend, billing *BillingConfig, clock pauseClock) (*Dispatcher, *billingAlertSink) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{Type: BackendTypeClaudeCode, SkipSelfReview: true, ClaudeCode: &ClaudeCodeConfig{Billing: billing}}
	sink := &billingAlertSink{}
	runner.SetAlertProcessor(sink)

	d := NewDispatcher(store, runner, nil)
	d.clock = clock
	if err := d.Start(context.Background()); err != nil {
		cleanup()
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(func() {
		d.Stop()
		cleanup()
	})
	return d, sink
}

// TestBillingMode_FailClosedPausesAdmission: an unresolvable key fails the
// execution row closed without starting the backend, pauses admission for the
// billing-key owner, emits one alert, and the first successful retry on the
// 5-minute tick resumes admission.
func TestBillingMode_FailClosedPausesAdmission(t *testing.T) {
	skipWithoutShell(t)
	const proj = "/tmp/pilot-gh6016-a"
	keyFile := filepath.Join(t.TempDir(), "key")
	backend := &countingBackend{}
	clock := newFakePauseClock(usageLimitTestNow)
	d, sink := newBillingDispatcher(t, backend, apiKeyConfig("cat "+keyFile+" 2>/dev/null || exit 1"), clock)
	ctx := context.Background()

	execID, err := d.QueueTask(ctx, &Task{ID: "GH-6016", Title: "needs key", ProjectPath: proj})
	if !errors.Is(err, ErrBillingKeyHold) || !errors.Is(err, ErrDispatchGated) {
		t.Fatalf("QueueTask err = %v, want ErrBillingKeyHold (gated)", err)
	}
	if execID != "" {
		t.Errorf("execID = %q, want empty", execID)
	}

	exec, getErr := d.store.GetLatestExecutionByTaskID("GH-6016", proj)
	if getErr != nil {
		t.Fatalf("no execution row recorded: %v", getErr)
	}
	if exec.Status != "infra" || !strings.HasPrefix(exec.Error, "billing_key_unavailable") || !strings.Contains(exec.Error, "exit code 1") {
		t.Errorf("row = status %q error %q, want infra / billing_key_unavailable ... exit code 1", exec.Status, exec.Error)
	}
	if IsEnvClassFailureText(exec.Error) {
		t.Errorf("error %q matches an env-class signature and would skip retry accounting", exec.Error)
	}
	if exec.BillingMode != "api_key" {
		t.Errorf("row billing mode = %q, want api_key", exec.BillingMode)
	}
	if n := backend.runs.Load(); n != 0 {
		t.Errorf("backend ran %d times, want 0", n)
	}
	st := d.AdmissionStatus()
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerBillingKey {
		t.Fatalf("admission = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerBillingKey)
	}
	if got := sink.billing(); len(got) != 1 || got[0].TaskID != "GH-6016" || !strings.HasPrefix(got[0].Error, "billing_key_unavailable") || !got[0].AdmissionPaused {
		t.Fatalf("alerts = %+v, want exactly one billing_key_unavailable for GH-6016", got)
	}

	// While paused another task is held without a second row or alert.
	if _, err := d.QueueTask(ctx, &Task{ID: "GH-6017", Title: "also needs key", ProjectPath: proj}); !errors.Is(err, ErrBillingKeyHold) {
		t.Errorf("second QueueTask err = %v, want ErrBillingKeyHold", err)
	}
	if _, getErr := d.store.GetLatestExecutionByTaskID("GH-6017", proj); getErr == nil {
		t.Error("held task got an execution row")
	}
	if got := sink.billing(); len(got) != 1 {
		t.Errorf("%d alerts after a held task, want still 1", len(got))
	}

	// A tick with the key still missing keeps the pause and re-arms the timer.
	clock.Advance(BillingKeyRetryInterval)
	if !d.IsAdmissionPaused() {
		t.Fatal("admission resumed while the key is still unavailable")
	}

	// The key appears; the next 5-minute tick resumes admission.
	if err := os.WriteFile(keyFile, []byte(testBillingKey), 0o600); err != nil {
		t.Fatal(err)
	}
	clock.Advance(BillingKeyRetryInterval - time.Second)
	if !d.IsAdmissionPaused() {
		t.Fatal("admission resumed before the retry tick")
	}
	clock.Advance(time.Second)
	if d.IsAdmissionPaused() {
		t.Fatalf("admission still paused after the key resolved: %+v", d.AdmissionStatus())
	}
}

// TestBillingMode_StampedOnExecution: a row created in api_key mode reads back
// billing_mode api_key with key_source still from the env label, and the
// backend then runs.
func TestBillingMode_StampedOnExecution(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv(KeySourceEnv, "own")
	const proj = "/tmp/pilot-gh6016-b"
	backend := &countingBackend{}
	d, _ := newBillingDispatcher(t, backend, apiKeyConfig("printf "+testBillingKey), newFakePauseClock(usageLimitTestNow))

	execID, err := d.QueueTask(context.Background(), &Task{ID: "GH-6018", Title: "ok", ProjectPath: proj})
	if err != nil || execID == "" {
		t.Fatalf("QueueTask = %q, %v", execID, err)
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.BillingMode != "api_key" {
		t.Errorf("billing_mode = %q, want api_key", exec.BillingMode)
	}
	if exec.KeySource != "own" {
		t.Errorf("key_source = %q, want own (unchanged)", exec.KeySource)
	}
	if d.IsAdmissionPaused() {
		t.Error("a resolved key paused admission")
	}

	// The worker hands the admission-time credential to the run's context.
	waitForExecStatus(t, d.store, execID, "completed", 5*time.Second)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.env) != 1 || backend.env[0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Errorf("backend context credential env = %v, want the resolved key", backend.env)
	}
}

// Default config with no key in the daemon env stamps subscription (derived,
// GH-6032).
func TestBillingMode_DefaultStampsSubscription(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	const proj = "/tmp/pilot-gh6016-c"
	d, _ := newBillingDispatcher(t, &countingBackend{}, nil, newFakePauseClock(usageLimitTestNow))
	execID, err := d.QueueTask(context.Background(), &Task{ID: "GH-6019", Title: "ok", ProjectPath: proj})
	if err != nil || execID == "" {
		t.Fatalf("QueueTask = %q, %v", execID, err)
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.BillingMode != "subscription" {
		t.Errorf("billing_mode = %q, want subscription", exec.BillingMode)
	}
}

// A queued row whose stashed credential is gone (daemon restart) resolves at
// run time and fails closed when the key is unavailable.
func TestBillingMode_WorkerResolvesWhenNoStashedCredential(t *testing.T) {
	skipWithoutShell(t)
	d, sink := newBillingDispatcher(t, &countingBackend{}, apiKeyConfig("exit 2"), newFakePauseClock(usageLimitTestNow))
	cred, err := d.billingCredentialForRun("exec-x", "GH-6020", "/tmp/pilot-gh6016-d")
	if err == nil || cred != nil {
		t.Fatalf("billingCredentialForRun = %v, %v; want error", cred, err)
	}
	if !d.IsAdmissionPaused() {
		t.Error("admission not paused")
	}
	if got := sink.billing(); len(got) != 1 {
		t.Errorf("%d alerts, want 1", len(got))
	}
}
