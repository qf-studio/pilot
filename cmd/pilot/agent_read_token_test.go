package main

import (
	"context"
	"os"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
)

// TestAgentReadTokenProvider_NilWithoutApp: with no GitHub App configured
// there is no provider, even when the daemon holds a config token and a
// GITHUB_TOKEN — the token of the daemon is never handed to the agent.
func TestAgentReadTokenProvider_NilWithoutApp(t *testing.T) {
	resetGitHubTokenTestState(t)
	if err := os.Setenv("GITHUB_TOKEN", "test-github-token"); err != nil {
		t.Fatalf("setenv: %v", err)
	}

	for name, cfg := range map[string]*config.Config{
		"nil config":   nil,
		"no adapters":  {},
		"no github":    {Adapters: &config.AdaptersConfig{}},
		"token no app": {Adapters: &config.AdaptersConfig{GitHub: &github.Config{Enabled: true, Token: "config-token"}}},
	} {
		if agentReadTokenProvider(cfg) != nil {
			t.Errorf("%s: agentReadTokenProvider() != nil, want nil with no GitHub App", name)
		}
	}
}

// TestAgentReadTokenProvider_AppMintFailureNoFallback: a failed App mint
// gives an error, not the config token that resolveGitHubToken falls back to.
func TestAgentReadTokenProvider_AppMintFailureNoFallback(t *testing.T) {
	resetGitHubTokenTestState(t)

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{
			Enabled: true,
			Token:   "config-token",
			App: &github.AppConfig{
				AppID:          123456,
				InstallationID: 78901234,
				PrivateKeyPath: "/nonexistent/github-app.pem",
			},
		},
	}}

	provider := agentReadTokenProvider(cfg)
	if provider == nil {
		t.Fatal("agentReadTokenProvider() = nil, want a provider with a GitHub App configured")
	}
	token, _, err := provider(context.Background(), "owner/repo")
	if err == nil {
		t.Fatal("provider() = nil error, want the mint error of the unreadable key")
	}
	if token != "" {
		t.Errorf("provider() token = %q, want empty (no fallback to the daemon token)", token)
	}
}
