package health

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/health/verify"
)

// fakeVerifiable is a minimal verify.Verifiable double for exercising the
// doctor output path without hitting real adapter APIs (GH-3769).
type fakeVerifiable struct {
	name string
	err  error
}

func (f *fakeVerifiable) Name() string { return f.name }
func (f *fakeVerifiable) Verify(ctx context.Context) error {
	return f.err
}

var _ verify.Verifiable = (*fakeVerifiable)(nil)

// TestCheckAdapterVerify_FakeRegistry exercises checkAdapterVerify — the
// shared doctor-output builder behind checkTelegramTokenLive/
// checkSlackTokenLive/checkGitHubTokenLive-style checks — against a small
// fake registry of Verifiables covering the healthy, failing, and
// not-yet-implemented cases.
func TestCheckAdapterVerify_FakeRegistry(t *testing.T) {
	registry := map[string]*fakeVerifiable{
		"healthy":         {name: "healthy", err: nil},
		"dead-credential": {name: "dead-credential", err: errFakeInvalidToken},
		"unimplemented":   {name: "unimplemented", err: verify.ErrProbeNotImplemented},
	}

	tests := []struct {
		name        string
		key         string
		tokenSource string
		wantStatus  Status
		wantSubstr  string
	}{
		{
			name:        "healthy adapter reports OK with source",
			key:         "healthy",
			tokenSource: "config",
			wantStatus:  StatusOK,
			wantSubstr:  "source: config",
		},
		{
			name:        "failing adapter reports red-check with token source",
			key:         "dead-credential",
			tokenSource: "env FAKE_TOKEN",
			wantStatus:  StatusError,
			wantSubstr:  "env FAKE_TOKEN",
		},
		{
			name:       "unimplemented probe does not go red",
			key:        "unimplemented",
			wantStatus: StatusOK,
			wantSubstr: "no live probe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkAdapterVerify("fake.check", tt.tokenSource, registry[tt.key])
			if got.Status != tt.wantStatus {
				t.Errorf("status = %v, want %v (message: %q)", got.Status, tt.wantStatus, got.Message)
			}
			if !strings.Contains(got.Message, tt.wantSubstr) {
				t.Errorf("message = %q, want to contain %q", got.Message, tt.wantSubstr)
			}
		})
	}
}

var errFakeInvalidToken = &fakeVerifyError{"fake token invalid: 401"}

type fakeVerifyError struct{ msg string }

func (e *fakeVerifyError) Error() string { return e.msg }

// TestCheckTelegramTokenLive_UsesInjectedVerifier confirms
// checkTelegramTokenLive routes through the injected verify.Verifiable
// factory (rather than hitting api.telegram.org), reflecting both the
// healthy and failing outcomes in doctor output.
func TestCheckTelegramTokenLive_UsesInjectedVerifier(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{Enabled: true, BotToken: "fake-telegram-token"},
		},
	}

	got := checkTelegramTokenLive(cfg, func(token string) verify.Verifiable {
		if token != "fake-telegram-token" {
			t.Errorf("verifier built with token %q, want the configured bot token", token)
		}
		return &fakeVerifiable{name: "telegram", err: nil}
	})
	if got == nil {
		t.Fatal("expected a check result, got nil")
	}
	if got.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", got.Status)
	}

	got = checkTelegramTokenLive(cfg, func(token string) verify.Verifiable {
		return &fakeVerifiable{name: "telegram", err: errFakeInvalidToken}
	})
	if got == nil || got.Status != StatusError {
		t.Fatalf("expected StatusError for a failing probe, got %+v", got)
	}
}

// TestCheckSlackTokenLive_UsesInjectedVerifier mirrors the Telegram case
// for Slack.
func TestCheckSlackTokenLive_UsesInjectedVerifier(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Slack: &slack.Config{Enabled: true, BotToken: "fake-slack-token"},
		},
	}

	got := checkSlackTokenLive(cfg, func(token string) verify.Verifiable {
		return &fakeVerifiable{name: "slack", err: nil}
	})
	if got == nil || got.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %+v", got)
	}

	got = checkSlackTokenLive(cfg, func(token string) verify.Verifiable {
		return &fakeVerifiable{name: "slack", err: errFakeInvalidToken}
	})
	if got == nil || got.Status != StatusError {
		t.Fatalf("expected StatusError, got %+v", got)
	}
}

// TestRunChecks_TelegramSlackLive_EndToEnd swaps the package-level verifier
// factories (as production code does at startup) to confirm RunChecks — the
// exact path `pilot doctor` calls — surfaces a live probe failure as a red
// check, using a fake registry instead of real network calls.
func TestRunChecks_TelegramSlackLive_EndToEnd(t *testing.T) {
	origTelegram, origSlack := newTelegramVerifier, newSlackVerifier
	t.Cleanup(func() {
		newTelegramVerifier = origTelegram
		newSlackVerifier = origSlack
	})

	newTelegramVerifier = func(token string) verify.Verifiable {
		return &fakeVerifiable{name: "telegram", err: errFakeInvalidToken}
	}
	newSlackVerifier = func(token string) verify.Verifiable {
		return &fakeVerifiable{name: "slack", err: nil}
	}

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{Enabled: true, BotToken: "fake-telegram-token"},
			Slack:    &slack.Config{Enabled: true, BotToken: "fake-slack-token"},
		},
	}

	report := RunChecks(cfg)

	tgLive := findConfigCheck(report.Config, "telegram.token.live")
	if tgLive == nil || tgLive.Status != StatusError {
		t.Fatalf("expected telegram.token.live StatusError, got %+v", tgLive)
	}
	slackLive := findConfigCheck(report.Config, "slack.token.live")
	if slackLive == nil || slackLive.Status != StatusOK {
		t.Fatalf("expected slack.token.live StatusOK, got %+v", slackLive)
	}
	if !report.HasErrors {
		t.Error("HasErrors should be true when a live probe fails")
	}
}
