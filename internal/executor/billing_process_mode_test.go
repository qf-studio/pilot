package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a bytes.Buffer safe for the slog handler and the test to share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// setProcessModeForTest sets the process billing mode and restores the
// previous one on cleanup.
func setProcessModeForTest(t *testing.T, mode BillingMode) {
	t.Helper()
	prev := processBillingMode()
	SetProcessBillingMode(mode)
	t.Cleanup(func() { SetProcessBillingMode(prev) })
}

// fakeEnvRecordingCLI puts a `claude` on PATH that records its environment to
// the returned file and answers the effort classifier with a fixed verdict.
// The ambient credential variables are set for the test.
func fakeEnvRecordingCLI(t *testing.T) string {
	t.Helper()
	skipWithoutShell(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > '" + envFile + "'\nprintf '%s' '{\"effort\":\"low\",\"reason\":\"fake\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", testAmbientAuthTok)
	return envFile
}

// subprocessEffortClassifier is an effort classifier forced onto the real
// subprocess runner (no direct API call).
func subprocessEffortClassifier() *EffortClassifier {
	c := newEffortClassifierWithRunner(nil)
	c.cmdRunner = c.defaultCmdRunner
	return c
}

func readRecordedEnv(t *testing.T, envFile string) []string {
	t.Helper()
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("fake claude did not record its env: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func captureWarnLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func driveResolveComplexity(t *testing.T, id string) {
	t.Helper()
	router := NewModelRouter(nil, nil)
	router.SetEffortClassifier(subprocessEffortClassifier())
	router.resolveComplexity(&Task{ID: id, Title: "Fix typo", Description: "Fix typo in README"})
}

func TestBillingMode_ExplicitSubscriptionStripsAtBareCtxSites(t *testing.T) {
	envFile := fakeEnvRecordingCLI(t)
	setProcessModeForTest(t, BillingModeSubscription)

	driveResolveComplexity(t, "GH-6038-sub")

	env := readRecordedEnv(t, envFile)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := billingEnvValue(env, banned); ok {
			t.Errorf("%s=%q reached the effort classifier subprocess in subscription mode", banned, v)
		}
	}
}

func TestBillingMode_UnsetProcessModeBareCtxPassesThrough(t *testing.T) {
	envFile := fakeEnvRecordingCLI(t)
	setProcessModeForTest(t, BillingModeUnset)

	driveResolveComplexity(t, "GH-6038-unset")

	env := readRecordedEnv(t, envFile)
	if got, ok := billingEnvValue(env, "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
		t.Errorf("ANTHROPIC_API_KEY = %q (present=%v), want the inherited key in unset mode", got, ok)
	}
}

func TestBillingMode_APIKeyProcessModeBareCtxStripsAndWarns(t *testing.T) {
	envFile := fakeEnvRecordingCLI(t)
	setProcessModeForTest(t, BillingModeAPIKey)
	logs := captureWarnLogs(t)

	driveResolveComplexity(t, "GH-6038-apikey")

	env := readRecordedEnv(t, envFile)
	for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := billingEnvValue(env, banned); ok {
			t.Errorf("%s=%q reached the subprocess on a bare ctx in api_key mode", banned, v)
		}
	}
	const msg = "model subprocess spawned without a billing credential in api_key mode"
	if n := strings.Count(logs.String(), msg); n != 1 {
		t.Fatalf("WARN %q logged %d times, want once; logs:\n%s", msg, n, logs.String())
	}
	if !strings.Contains(logs.String(), "defaultCmdRunner") {
		t.Errorf("WARN does not name the caller's component; logs:\n%s", logs.String())
	}
}

func TestBillingMode_CtxCredentialWinsOverProcessMode(t *testing.T) {
	envFile := fakeEnvRecordingCLI(t)
	setProcessModeForTest(t, BillingModeSubscription)

	cred := &BillingCredential{mode: BillingModeAPIKey, key: []byte(testBillingKey)}
	ctx := WithBillingCredential(context.Background(), cred)
	subprocessEffortClassifier().Classify(ctx, &Task{ID: "GH-6038-ctx", Title: "Fix typo", Description: "Fix typo in README"})

	env := readRecordedEnv(t, envFile)
	var keys []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			keys = append(keys, kv)
		}
	}
	if len(keys) != 1 || keys[0] != "ANTHROPIC_API_KEY="+testBillingKey {
		t.Fatalf("ANTHROPIC_API_KEY entries = %v, want exactly the ctx credential's key", keys)
	}
	if v, ok := billingEnvValue(env, "ANTHROPIC_AUTH_TOKEN"); ok {
		t.Errorf("inherited ANTHROPIC_AUTH_TOKEN=%q reached the subprocess", v)
	}
}
