package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// GH-6022: paths outside the dispatcher — OpenCode/Qwen keep their key, the
// post-merge reviewer and Navigator bootstrap resolve a credential, and a
// non-dispatcher resolution failure alerts.

// sleepingEnvCLI is fakeEnvCLI for a server that must stay up: it dumps its
// environment atomically, then sleeps until the test stops it.
func sleepingEnvCLI(t *testing.T) (script, dump string) {
	t.Helper()
	dir := t.TempDir()
	dump = filepath.Join(dir, "env.txt")
	script = filepath.Join(dir, "opencode")
	body := "#!/bin/sh\n/usr/bin/env > " + dump + ".tmp && mv " + dump + ".tmp " + dump + "\nexec sleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	return script, dump
}

func TestOpenCode_EnvKeepsAnthropicKey(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	script, dump := sleepingEnvCLI(t)

	// Health answers only once the server command has written its env dump, so
	// startServer returns as soon as the spawned command ran.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := os.Stat(dump); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	backend := NewOpenCodeBackend(&OpenCodeConfig{
		ServerURL:       srv.URL,
		AutoStartServer: true,
		ServerCommand:   script,
		Provider:        "anthropic",
	})
	defer func() { _ = backend.StopServer() }()

	if err := backend.startServer(context.Background()); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	if got, ok := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Errorf("OpenCode server ANTHROPIC_API_KEY = %q (set=%v), want %q", got, ok, testAmbientAPIKey)
	}
}

func TestQwenCode_EnvKeepsAnthropicKey(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	script, dump := fakeEnvCLI(t)
	backend := NewQwenCodeBackend(&QwenCodeConfig{Command: script})

	// The --version probe.
	backend.IsAvailable()
	if got, ok := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Errorf("qwen --version probe ANTHROPIC_API_KEY = %q (set=%v), want %q", got, ok, testAmbientAPIKey)
	}
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}

	// The Execute spawn, on a ctx carrying a different api_key credential: the
	// billing mode is the claude backend's, so it must not touch this env.
	cred := &BillingCredential{mode: BillingModeSubscription}
	_, _ = backend.Execute(WithBillingCredential(context.Background(), cred), ExecuteOptions{Prompt: "hi", ProjectPath: t.TempDir()})
	if got, ok := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Errorf("qwen Execute ANTHROPIC_API_KEY = %q (set=%v), want %q", got, ok, testAmbientAPIKey)
	}
}

// reviewSpawn records the credential on each spawn ctx and runs the real
// subprocess runner against script.
type reviewSpawn struct {
	mu    sync.Mutex
	calls int
	creds []*BillingCredential
}

func (s *reviewSpawn) install(r *PostMergeReviewer, script string) {
	real := NewIntentJudge(script).runCmdInHome
	r.cmdRunner = func(ctx context.Context, dir, home string, args ...string) ([]byte, error) {
		s.mu.Lock()
		s.calls++
		s.creds = append(s.creds, BillingCredentialFromContext(ctx))
		s.mu.Unlock()
		return real(ctx, dir, home, args...)
	}
}

func reviewInput() PostMergeReviewInput {
	return PostMergeReviewInput{PRNumber: 1, IssueTitle: "t", Diff: "diff --git a/x b/x\n+x\n"}
}

func TestPostMergeReviewer_BillingAPIKeyCredentialOnSpawn(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	script, dump := fakeEnvCLI(t)

	r := NewPostMergeReviewer(script, "", 0)
	r.SetBillingResolver(NewBillingResolver(apiKeyConfig("printf " + testBillingKey)))
	spawn := &reviewSpawn{}
	spawn.install(r, script)

	if _, err := r.Review(context.Background(), reviewInput()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if got, _ := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); got != testBillingKey {
		t.Errorf("reviewer spawn ANTHROPIC_API_KEY = %q, want the resolved key %q", got, testBillingKey)
	}
	if len(spawn.creds) != 1 || spawn.creds[0] == nil {
		t.Fatalf("spawn credentials = %v, want one credential on the spawn ctx", spawn.creds)
	}
	if env := spawn.creds[0].Env(); len(env) != 0 {
		t.Errorf("credential still holds the key after Review returned: %v", env)
	}
}

func TestPostMergeReviewer_BillingKeyUnavailableFailsClosed(t *testing.T) {
	skipWithoutShell(t)
	script, dump := fakeEnvCLI(t)

	r := NewPostMergeReviewer(script, "", 0)
	r.SetBillingResolver(NewBillingResolver(apiKeyConfig("exit 1")))
	spawn := &reviewSpawn{}
	spawn.install(r, script)

	review, err := r.Review(context.Background(), reviewInput())
	if err == nil || !IsBillingKeyUnavailable(err) {
		t.Fatalf("Review err = %v, want a billing key unavailable error", err)
	}
	if review != nil {
		t.Errorf("Review returned %+v with the error, want nil", review)
	}
	if spawn.calls != 0 {
		t.Errorf("reviewer spawned %d times despite the unresolved key", spawn.calls)
	}
	if _, statErr := os.Stat(dump); statErr == nil {
		t.Error("the review CLI ran despite the unresolved key")
	}
}

func TestPostMergeReviewer_BillingSubscriptionUnchanged(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	script, dump := fakeEnvCLI(t)

	r := NewPostMergeReviewer(script, "", 0)
	r.SetBillingResolver(NewBillingResolver(&BillingConfig{Mode: BillingModeSubscription}))
	spawn := &reviewSpawn{}
	spawn.install(r, script)

	if _, err := r.Review(context.Background(), reviewInput()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if spawn.calls != 1 {
		t.Fatalf("spawns = %d, want 1", spawn.calls)
	}
	if v, ok := billingEnvValue(readEnvDump(t, dump), "ANTHROPIC_API_KEY"); ok {
		t.Errorf("subscription review carried ANTHROPIC_API_KEY=%q", v)
	}
	if env := spawn.creds[0].Env(); len(env) != 0 {
		t.Errorf("subscription credential has env entries: %v", env)
	}
}

func TestBillingContext_UnavailableEmitsAlert(t *testing.T) {
	skipWithoutShell(t)
	sink := &recordingAlertProcessor{}
	r := NewRunnerWithBackend(&countingBackend{})
	r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Billing: apiKeyConfig("exit 1")}}
	r.SetAlertProcessor(sink)

	_, cred, release, err := r.billingContext(context.Background(), "TASK-1", "/p")
	defer release()
	if !IsBillingKeyUnavailable(err) {
		t.Fatalf("err = %v, want a billing key unavailable error", err)
	}
	if cred != nil {
		t.Errorf("cred = %v, want nil on failure", cred)
	}
	if n := sink.countByType(AlertEventTypeBillingKeyUnavailable); n != 1 {
		t.Fatalf("billing key alerts = %d, want exactly 1", n)
	}
	got := sink.events[0]
	if got.TaskID != "TASK-1" || got.Project != "/p" || got.Error == "" {
		t.Errorf("alert = %+v, want task TASK-1, project /p and the error text", got)
	}

	// A subscription runner resolves without an alert.
	sink2 := &recordingAlertProcessor{}
	ok := NewRunnerWithBackend(&countingBackend{})
	ok.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{}}
	ok.SetAlertProcessor(sink2)
	if _, _, rel, err := ok.billingContext(context.Background(), "TASK-2", "/p"); err != nil {
		t.Fatalf("subscription billingContext: %v", err)
	} else {
		rel()
	}
	if n := len(sink2.events); n != 0 {
		t.Errorf("subscription mode raised %d alerts", n)
	}
}

// credCapturingBackend wraps the bootstrap fake and records the credential env
// its spawn ctx carries.
type credCapturingBackend struct {
	*bootstrapFakeBackend
	mu  sync.Mutex
	env [][]string
}

func (b *credCapturingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.env = append(b.env, BillingCredentialFromContext(ctx).Env())
	b.mu.Unlock()
	return b.bootstrapFakeBackend.Execute(ctx, opts)
}

func TestNavigatorBootstrap_BillingModeStampedAndCredentialOnSpawn(t *testing.T) {
	skipWithoutShell(t)
	store, cleanup := setupTestStore(t)
	defer cleanup()
	dir := fixtureClone(t)

	inner := &bootstrapFakeBackend{script: fillingScript(t, dir)}
	b := &credCapturingBackend{bootstrapFakeBackend: inner}
	r := newBootstrapRunner(b, "")
	r.config.ClaudeCode = &ClaudeCodeConfig{Billing: apiKeyConfig("printf " + testBillingKey)}
	r.SetLogStore(store)

	res, err := r.ExecuteNavigatorBootstrap(context.Background(), dir)
	if err != nil {
		t.Fatalf("ExecuteNavigatorBootstrap: %v", err)
	}
	row, err := store.GetLatestExecutionByTaskID(res.TaskID, dir)
	if err != nil {
		t.Fatalf("no executions row for %s: %v", res.TaskID, err)
	}
	if row.BillingMode != "api_key" {
		t.Errorf("row billing_mode = %q, want api_key", row.BillingMode)
	}
	if len(b.env) != 1 || len(b.env[0]) != 1 || b.env[0][0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Errorf("spawn credential env = %v, want ANTHROPIC_API_KEY=%s", b.env, testBillingKey)
	}
}

func TestNavigatorBootstrap_BillingKeyUnavailableFailsClosed(t *testing.T) {
	skipWithoutShell(t)
	dir := fixtureClone(t)
	inner := &bootstrapFakeBackend{script: fillingScript(t, dir)}
	r := newBootstrapRunner(inner, "")
	r.config.ClaudeCode = &ClaudeCodeConfig{Billing: apiKeyConfig("exit 1")}
	sink := &recordingAlertProcessor{}
	r.SetAlertProcessor(sink)

	_, err := r.ExecuteNavigatorBootstrap(context.Background(), dir)
	if !IsBillingKeyUnavailable(err) {
		t.Fatalf("err = %v, want a billing key unavailable error", err)
	}
	if inner.callCount() != 0 {
		t.Errorf("bootstrap spawned %d times despite the unresolved key", inner.callCount())
	}
	if n := sink.countByType(AlertEventTypeBillingKeyUnavailable); n != 1 {
		t.Errorf("billing key alerts = %d, want 1", n)
	}
}
