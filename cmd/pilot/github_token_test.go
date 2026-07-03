package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
)

// resetGitHubTokenTestState clears env and the memoized gh-CLI cache so each
// subtest starts from a clean resolution chain.
func resetGitHubTokenTestState(t *testing.T) {
	t.Helper()
	origEnv, hadEnv := os.LookupEnv("GITHUB_TOKEN")
	_ = os.Unsetenv("GITHUB_TOKEN")
	origCache := ghTokenCache
	ghTokenCache = &ghCLITokenCache{}
	origRunner := ghRunner
	t.Cleanup(func() {
		if hadEnv {
			_ = os.Setenv("GITHUB_TOKEN", origEnv)
		} else {
			_ = os.Unsetenv("GITHUB_TOKEN")
		}
		ghTokenCache = origCache
		ghRunner = origRunner
	})
}

// TestResolveGitHubToken_Precedence verifies config -> GITHUB_TOKEN env ->
// `gh auth token` CLI fallback, in that order (GH-3718).
func TestResolveGitHubToken_Precedence(t *testing.T) {
	t.Run("config wins over env and gh CLI", func(t *testing.T) {
		resetGitHubTokenTestState(t)
		_ = os.Setenv("GITHUB_TOKEN", "env-token")
		ghRunner = fakeGhRunner(t, true, "alice", "gh-cli-token", nil)

		cfg := &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true, Token: "config-token"},
		}}

		tok, source := resolveGitHubToken(cfg)
		if tok != "config-token" {
			t.Errorf("token = %q, want config-token", tok)
		}
		if source != githubTokenSourceConfig {
			t.Errorf("source = %q, want %q", source, githubTokenSourceConfig)
		}
	})

	t.Run("env wins over gh CLI when config empty", func(t *testing.T) {
		resetGitHubTokenTestState(t)
		_ = os.Setenv("GITHUB_TOKEN", "env-token")
		ghRunner = fakeGhRunner(t, true, "alice", "gh-cli-token", nil)

		cfg := &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		}}

		tok, source := resolveGitHubToken(cfg)
		if tok != "env-token" {
			t.Errorf("token = %q, want env-token", tok)
		}
		if source != githubTokenSourceEnv {
			t.Errorf("source = %q, want %q", source, githubTokenSourceEnv)
		}
	})

	t.Run("gh CLI fallback when config and env empty", func(t *testing.T) {
		resetGitHubTokenTestState(t)
		ghRunner = fakeGhRunner(t, true, "alice", "gh-cli-token", nil)

		cfg := &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		}}

		tok, source := resolveGitHubToken(cfg)
		if tok != "gh-cli-token" {
			t.Errorf("token = %q, want gh-cli-token", tok)
		}
		if source != githubTokenSourceGhCLI {
			t.Errorf("source = %q, want %q", source, githubTokenSourceGhCLI)
		}
	})

	t.Run("gh CLI result is cached across calls", func(t *testing.T) {
		resetGitHubTokenTestState(t)
		var calls int
		var mu sync.Mutex
		ghRunner = func(args ...string) ([]byte, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return fakeGhRunner(t, true, "alice", "gh-cli-token", nil)(args...)
		}

		cfg := &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		}}

		resolveGitHubToken(cfg)
		resolveGitHubToken(cfg)
		resolveGitHubToken(cfg)

		if calls != 1 {
			t.Errorf("expected exactly 1 gh CLI exec (cached), got %d", calls)
		}
	})

	t.Run("none resolved", func(t *testing.T) {
		resetGitHubTokenTestState(t)
		ghRunner = fakeGhRunner(t, false, "", "", nil)

		cfg := &config.Config{Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		}}

		tok, source := resolveGitHubToken(cfg)
		if tok != "" {
			t.Errorf("token = %q, want empty", tok)
		}
		if source != githubTokenSourceNone {
			t.Errorf("source = %q, want %q", source, githubTokenSourceNone)
		}
	})
}

// recordingChannel is a minimal alerts.Channel that records delivered alerts.
type recordingChannel struct {
	mu     sync.Mutex
	alerts []*alerts.Alert
}

func (c *recordingChannel) Name() string { return "test-channel" }
func (c *recordingChannel) Type() string { return "webhook" }
func (c *recordingChannel) Send(_ context.Context, alert *alerts.Alert) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts = append(c.alerts, alert)
	return nil
}
func (c *recordingChannel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.alerts)
}

func newTestAlertsEngine(t *testing.T) (*alerts.Engine, *recordingChannel) {
	t.Helper()
	cfg := &alerts.AlertConfig{
		Enabled:  true,
		Channels: []alerts.ChannelConfig{{Name: "test-channel", Type: "webhook", Enabled: true}},
		Rules: []alerts.AlertRule{{
			Name:     "service-unhealthy",
			Type:     alerts.AlertTypeServiceUnhealthy,
			Enabled:  true,
			Severity: alerts.SeverityWarning,
			Channels: []string{"test-channel"},
		}},
	}
	ch := &recordingChannel{}
	dispatcher := alerts.NewDispatcher(cfg)
	dispatcher.RegisterChannel(ch)
	engine := alerts.NewEngine(cfg, alerts.WithDispatcher(dispatcher))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	return engine, ch
}

// TestValidateGitHubToken_DeadTokenFiresAlert confirms a 401 from GitHub
// fires a config_error alert naming the token source (GH-3718 acceptance:
// startup with a dead token must not fail silently).
func TestValidateGitHubToken_DeadTokenFiresAlert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	client := github.NewClientWithBaseURL("dead-token", srv.URL)
	engine, ch := newTestAlertsEngine(t)

	validateGitHubToken(context.Background(), client, githubTokenSourceEnv, engine)

	deadline := time.After(2 * time.Second)
	for ch.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected an alert to be fired for a dead token")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestValidateGitHubToken_ValidTokenNoAlert confirms a healthy token does not
// fire a config_error alert.
func TestValidateGitHubToken_ValidTokenNoAlert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"login":"pilot-bot"}`))
	}))
	defer srv.Close()

	client := github.NewClientWithBaseURL("good-token", srv.URL)
	engine, ch := newTestAlertsEngine(t)

	validateGitHubToken(context.Background(), client, githubTokenSourceConfig, engine)
	engine.WaitForDispatch()

	if got := ch.count(); got != 0 {
		t.Errorf("expected no alerts for a valid token, got %d", got)
	}
}

// TestValidateGitHubToken_NilAlertsEngineDoesNotPanic confirms validation is
// safe to call before the alerts engine exists (some call-sites resolve the
// token earlier in startup than the alerts engine is constructed).
func TestValidateGitHubToken_NilAlertsEngineDoesNotPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	client := github.NewClientWithBaseURL("dead-token", srv.URL)
	validateGitHubToken(context.Background(), client, githubTokenSourceNone, nil)
}
