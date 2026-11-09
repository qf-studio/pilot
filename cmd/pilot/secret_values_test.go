package main

import (
	"sort"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

func TestCollectSecretValues(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Adapters.GitHub.Token = "cfg-github-token-value"

	got := collectSecretValues(cfg, []string{
		"MY_SERVICE_TOKEN=env-deny-suffix-value",
		"GITHUB_TOKEN=env-github-token-value",
		"CLAUDE_CODE_OAUTH_TOKEN=env-claude-oauth-value",
		"PATH=/usr/bin:/bin",
		"HOME=/home/someone",
		"EMPTY_TOKEN=",
	})
	sort.Strings(got)
	have := map[string]bool{}
	for _, v := range got {
		have[v] = true
	}
	for _, want := range []string{"cfg-github-token-value", "env-deny-suffix-value", "env-github-token-value", "env-claude-oauth-value"} {
		if !have[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	for _, bad := range []string{"/usr/bin:/bin", "/home/someone", ""} {
		if have[bad] {
			t.Errorf("unexpected %q in %v", bad, got)
		}
	}
}

func TestCollectKeyedStrings_PublicKeyExcluded(t *testing.T) {
	tree := map[string]interface{}{
		"api_key":        "secret-api-key-value",
		"ssh_public_key": "ssh-ed25519-public-value",
		"name":           "plain-name-value",
		"nested":         map[string]interface{}{"tokens": []interface{}{"list-token-one", "list-token-two"}},
	}
	got := collectKeyedStrings(tree, false, nil)
	sort.Strings(got)
	want := []string{"list-token-one", "list-token-two", "secret-api-key-value"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestRegisterSecretValues_MasksConfiguredToken(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	cfg := config.DefaultConfig()
	cfg.Adapters.GitHub.Token = "cfg-github-token-value"
	if n := registerSecretValues(cfg); n == 0 {
		t.Fatal("expected registered values")
	}
	if got := secretpatterns.Redact("x cfg-github-token-value y"); got != "x <redacted> y" {
		t.Errorf("Redact = %q", got)
	}
}
