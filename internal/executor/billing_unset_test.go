package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// GH-6032: billing.mode left unset passes the daemon environment through (the
// pre-v2.310.0 behaviour); only an explicit subscription mode strips it.

// unsetModeEnvs runs the claude backend (a chat turn), the effort classifier
// and the post-merge reviewer once each with billing as the configured block,
// under whatever ANTHROPIC_* the test put in the daemon env, and returns the
// environment each recorded.
func unsetModeEnvs(t *testing.T, billing *BillingConfig) (backend, classifier, reviewer []string) {
	t.Helper()
	script, dump := fakeEnvCLI(t)
	t.Setenv("PATH", filepath.Dir(script)+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := NewRunnerWithBackend(NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script}))
	r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Command: script, Billing: billing}}
	_, _ = r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	backend = readEnvDump(t, dump)
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}

	cred, err := NewBillingResolver(billing).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	defer cred.Zero()
	_, _ = NewEffortClassifier().defaultCmdRunner(WithBillingCredential(context.Background(), cred), "-p", "x")
	classifier = readEnvDump(t, dump)
	if err := os.Remove(dump); err != nil {
		t.Fatal(err)
	}

	pm := NewPostMergeReviewer(script, "", 0)
	pm.SetBillingResolver(NewBillingResolver(billing))
	spawn := &reviewSpawn{}
	spawn.install(pm, script)
	if _, err := pm.Review(context.Background(), reviewInput()); err != nil {
		t.Fatalf("Review: %v", err)
	}
	reviewer = readEnvDump(t, dump)
	return backend, classifier, reviewer
}

func TestBillingMode_UnsetKeepsInheritedKey(t *testing.T) {
	skipWithoutShell(t)

	if got := (*BillingConfig)(nil).EffectiveMode(); got != BillingModeUnset {
		t.Errorf("nil block EffectiveMode = %q, want %q", got, BillingModeUnset)
	}
	if got := (&BillingConfig{}).EffectiveMode(); got != BillingModeUnset {
		t.Errorf("empty mode EffectiveMode = %q, want %q", got, BillingModeUnset)
	}

	// The unset mode resolves to an empty credential without error and runs no
	// command, even with api_key fields present.
	marker := filepath.Join(t.TempDir(), "ran")
	cred, err := NewBillingResolver(&BillingConfig{APIKeyCommand: "touch " + marker}).Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve in unset mode: %v", err)
	}
	if cred.Mode() != BillingModeUnset || len(cred.Env()) != 0 {
		t.Errorf("credential = mode %q env %v, want unset with no env", cred.Mode(), cred.Env())
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the unset mode ran api_key_command")
	}

	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	backend, classifier, reviewer := unsetModeEnvs(t, nil)
	for name, env := range map[string][]string{"claude backend": backend, "effort classifier": classifier, "post-merge reviewer": reviewer} {
		if got, ok := billingEnvValue(env, "ANTHROPIC_API_KEY"); !ok || got != testAmbientAPIKey {
			t.Errorf("%s ANTHROPIC_API_KEY = %q (set=%v), want the inherited %q", name, got, ok, testAmbientAPIKey)
		}
	}
}

func TestBillingMode_ExplicitSubscriptionStripsInheritedKey(t *testing.T) {
	skipWithoutShell(t)
	t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", testAmbientAuthTok)

	backend, classifier, reviewer := unsetModeEnvs(t, &BillingConfig{Mode: BillingModeSubscription})
	for name, env := range map[string][]string{"claude backend": backend, "effort classifier": classifier, "post-merge reviewer": reviewer} {
		for _, banned := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
			if v, ok := billingEnvValue(env, banned); ok {
				t.Errorf("%s carries %s=%q in subscription mode", name, banned, v)
			}
		}
	}
}

func TestBillingMode_UnsetDerivesAttribution(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    string
	}{
		{name: "empty env", want: "subscription"},
		{name: "api key", environ: []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=" + testAmbientAPIKey}, want: "api_key"},
		{name: "auth token", environ: []string{"ANTHROPIC_AUTH_TOKEN=" + testAmbientAuthTok}, want: "api_key"},
		{name: "empty key value", environ: []string{"ANTHROPIC_API_KEY="}, want: "subscription"},
		{name: "oauth token only", environ: []string{"CLAUDE_CODE_OAUTH_TOKEN=fake-oauth-token", "ANTHROPIC_BASE_URL=" + testBillingBaseURL}, want: "subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DerivedBillingMode(tt.environ); got != tt.want {
				t.Errorf("DerivedBillingMode = %q, want %q", got, tt.want)
			}
		})
	}

	// The execution row of a daemon with no billing block reads the derived
	// mode; an explicit subscription block stamps its own value.
	for _, tc := range []struct {
		name    string
		key     string
		billing *BillingConfig
		want    string
	}{
		{name: "key in env", key: testAmbientAPIKey, want: "api_key"},
		{name: "no key in env", want: "subscription"},
		{name: "explicit subscription with key in env", key: testAmbientAPIKey, billing: &BillingConfig{Mode: BillingModeSubscription}, want: "subscription"},
	} {
		t.Run("row "+tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", tc.key)
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
			d, _ := newBillingDispatcher(t, &countingBackend{}, tc.billing, newFakePauseClock(usageLimitTestNow))
			execID, err := d.QueueTask(context.Background(), &Task{ID: "GH-6032", Title: "ok", ProjectPath: "/tmp/pilot-gh6032-" + strings.ReplaceAll(tc.name, " ", "-")})
			if err != nil || execID == "" {
				t.Fatalf("QueueTask = %q, %v", execID, err)
			}
			row, err := d.store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if row.BillingMode != tc.want {
				t.Errorf("billing_mode = %q, want %q", row.BillingMode, tc.want)
			}
			if d.IsAdmissionPaused() {
				t.Error("the unset mode paused admission")
			}
		})
	}

	// Chat turns derive the same way.
	t.Run("chat turn", func(t *testing.T) {
		skipWithoutShell(t)
		script, _ := fakeEnvCLI(t)
		r := NewRunnerWithBackend(NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script}))
		r.config = &BackendConfig{Type: BackendTypeClaudeCode, ClaudeCode: &ClaudeCodeConfig{Command: script}}
		t.Setenv("ANTHROPIC_API_KEY", testAmbientAPIKey)
		res, _ := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
		if res.BillingMode != "api_key" {
			t.Errorf("chat turn billing mode = %q, want api_key", res.BillingMode)
		}
		t.Setenv("ANTHROPIC_API_KEY", "")
		res, _ = r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
		if res.BillingMode != "subscription" {
			t.Errorf("chat turn billing mode = %q, want subscription", res.BillingMode)
		}
	})
}

func TestBillingMode_UnsetNoCredentialOnCtxPassesThrough(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_API_KEY=" + testAmbientAPIKey,
		"ANTHROPIC_AUTH_TOKEN=" + testAmbientAuthTok,
		"ANTHROPIC_BASE_URL=" + testBillingBaseURL,
		"TELEGRAM_BOT_TOKEN=test-telegram-bot-token",
	}
	want := modelSubprocessEnv(base)

	unset := WithBillingCredential(context.Background(), &BillingCredential{mode: BillingModeUnset})
	for name, ctx := range map[string]context.Context{"no credential": context.Background(), "unset credential": unset} {
		t.Run(name, func(t *testing.T) {
			got := modelSubprocessEnvFor(ctx, base)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("modelSubprocessEnvFor = %v, want modelSubprocessEnv result %v", got, want)
			}
			if v, _ := billingEnvValue(got, "ANTHROPIC_API_KEY"); v != testAmbientAPIKey {
				t.Errorf("ANTHROPIC_API_KEY = %q, want the inherited key", v)
			}
		})
	}
}
