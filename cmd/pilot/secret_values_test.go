package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
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

func TestCollectKeyedStrings_CredentialKeys(t *testing.T) {
	tests := []struct {
		name string
		tree map[string]interface{}
		want []string
		not  []string
	}{
		{"pat", map[string]interface{}{"azure_devops": map[string]interface{}{"pat": "azdo-pat-value-01"}}, []string{"azdo-pat-value-01"}, nil},
		{"webhook_url", map[string]interface{}{"webhook_url": "https://example.test/hook/abc123"}, []string{"https://example.test/hook/abc123"}, nil},
		{"webhook_headers values not names", map[string]interface{}{"webhook_headers": map[string]interface{}{"X-Auth-Name": "hdr-secret-value"}}, []string{"hdr-secret-value"}, []string{"X-Auth-Name"}},
		{"headers bearer", map[string]interface{}{"headers": map[string]interface{}{"Authorization": "Bearer bearer-secret-01"}}, []string{"Bearer bearer-secret-01", "bearer-secret-01"}, []string{"Authorization"}},
		{"routing_key", map[string]interface{}{"routing_key": "pd-routing-key-01"}, []string{"pd-routing-key-01"}, nil},
		{"dsn", map[string]interface{}{"dsn": "postgres://u:p@h/db"}, []string{"postgres://u:p@h/db"}, nil},
		{"url userinfo", map[string]interface{}{"url": "https://user:pw@example.test/x"}, []string{"https://user:pw@example.test/x"}, nil},
		{"url query", map[string]interface{}{"url": "https://example.test/x?token=abc"}, []string{"https://example.test/x?token=abc"}, nil},
		{"url slack host", map[string]interface{}{"url": "https://hooks.slack.com/services/T0/B0/xyz"}, []string{"https://hooks.slack.com/services/T0/B0/xyz"}, nil},
		{"url discord host", map[string]interface{}{"url": "https://discord.com/api/webhooks/1/abc"}, []string{"https://discord.com/api/webhooks/1/abc"}, nil},
		{"plain url not collected", map[string]interface{}{"url": "https://example.test/health"}, nil, []string{"https://example.test/health"}},
		{"api_key_env name not collected", map[string]interface{}{"api_key_env": "MY_BILLING_KEY_VAR"}, nil, []string{"MY_BILLING_KEY_VAR"}},
		{"api_key_command not collected", map[string]interface{}{"api_key_command": "pass show anthropic"}, nil, []string{"pass show anthropic"}},
		{"private_key_path not collected", map[string]interface{}{"private_key_path": "/home/x/key.pem"}, nil, []string{"/home/x/key.pem"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectKeyedStrings(tt.tree, false, nil)
			have := map[string]bool{}
			for _, v := range got {
				have[v] = true
			}
			for _, w := range tt.want {
				if !have[w] {
					t.Errorf("missing %q in %v", w, got)
				}
			}
			for _, n := range tt.not {
				if have[n] {
					t.Errorf("unexpected %q in %v", n, got)
				}
			}
		})
	}
}

func TestCollectSecretValues_APIKeyEnvRegistersValueNotName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Executor = &executor.BackendConfig{
		Type: executor.BackendTypeClaudeCode,
		ClaudeCode: &executor.ClaudeCodeConfig{Billing: &executor.BillingConfig{
			Mode:          executor.BillingModeAPIKey,
			APIKeyEnv:     "BILLING_KEY_HOLDER",
			APIKeyCommand: "",
		}},
	}
	got := collectSecretValues(cfg, []string{"BILLING_KEY_HOLDER=sk-billing-env-value-01"})
	have := map[string]bool{}
	for _, v := range got {
		have[v] = true
	}
	if !have["sk-billing-env-value-01"] {
		t.Errorf("env value not collected: %v", got)
	}
	if have["BILLING_KEY_HOLDER"] {
		t.Errorf("env var name collected: %v", got)
	}
}

func TestCollectSecretValues_EnvNames(t *testing.T) {
	got := collectSecretValues(nil, []string{
		"TELEGRAM_CHAT_ID=123456789012",
		"TELEGRAM_APPROVER_CHAT_ID=987654321098",
		"AWS_ACCESS_KEY_ID=AKIA-fake-access-id",
		"STRIPE_LIVE_KEY=env-key-suffix-value",
		"SENTRY_DSN=https://abc@sentry.test/1",
		"SSH_PUBLIC_KEY=ssh-ed25519-public-value",
		"MY_SERVICE_TOKEN=env-token-suffix-value",
	})
	have := map[string]bool{}
	for _, v := range got {
		have[v] = true
	}
	for _, w := range []string{"AKIA-fake-access-id", "env-key-suffix-value", "https://abc@sentry.test/1", "env-token-suffix-value"} {
		if !have[w] {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	for _, n := range []string{"123456789012", "987654321098", "ssh-ed25519-public-value"} {
		if have[n] {
			t.Errorf("unexpected %q in %v", n, got)
		}
	}
}

func TestLoadConfigRegisteringSecrets_TaskPath(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)

	path := filepath.Join(t.TempDir(), "config.yaml")
	yml := "version: \"1.0\"\nadapters:\n  github:\n    enabled: true\n    token: cfg-task-path-token-01\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfigRegisteringSecrets(path); err != nil {
		t.Fatalf("loadConfigRegisteringSecrets: %v", err)
	}
	if got := secretpatterns.Redact("a cfg-task-path-token-01 b"); got != "a <redacted> b" {
		t.Errorf("Redact = %q; value not registered", got)
	}
}

// lastCfgLoader returns the name of the function whose result was last
// assigned to the variable cfg in fn before pos ("" when none).
func lastCfgLoader(fn *ast.FuncDecl, before token.Pos) string {
	var name string
	var at token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) == 0 || len(as.Rhs) != 1 || as.Pos() >= before || as.Pos() < at {
			return true
		}
		lhs, ok := as.Lhs[0].(*ast.Ident)
		call, isCall := as.Rhs[0].(*ast.CallExpr)
		if !ok || lhs.Name != "cfg" || !isCall {
			return true
		}
		at = as.Pos()
		switch f := call.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			if x, ok := f.X.(*ast.Ident); ok {
				name = x.Name + "." + f.Sel.Name
			}
		}
		return true
	})
	return name
}

// The config a command hands to executor.NewRunnerWithConfig must come from
// loadConfigRegisteringSecrets, so no entry point records a session before
// its secrets are registered (GH-6266). The check is per command function: the
// last cfg assignment before the Runner is built is the one that counts.
func TestRunnerBuildingCommandsRegisterSecrets(t *testing.T) {
	tests := []struct {
		file, fn string
		// runnerAnchored: the Runner is built in fn from cfg.Executor; else
		// fn only loads the config that its helpers build Runners from.
		runnerAnchored bool
	}{
		{"commands.go", "newTaskCmd", true},
		{"commands.go", "newGitHubRunCmd", true},
		{"navigator.go", "newNavigatorBootstrapCmd", true},
		{"interactive.go", "runInteractiveMode", false},
	}
	fset := token.NewFileSet()
	for _, tt := range tests {
		t.Run(tt.fn, func(t *testing.T) {
			f, err := parser.ParseFile(fset, tt.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			var fn *ast.FuncDecl
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == tt.fn {
					fn = fd
				}
			}
			if fn == nil {
				t.Fatalf("%s not found in %s", tt.fn, tt.file)
			}
			anchor := fn.End()
			if tt.runnerAnchored {
				anchor = token.NoPos
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) != 1 {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					arg, argOK := call.Args[0].(*ast.SelectorExpr)
					if ok && argOK && sel.Sel.Name == "NewRunnerWithConfig" && arg.Sel.Name == "Executor" {
						anchor = call.Pos()
					}
					return true
				})
				if anchor == token.NoPos {
					t.Fatalf("%s: no NewRunnerWithConfig(cfg.Executor) call", tt.fn)
				}
			}
			if got := lastCfgLoader(fn, anchor); got != "loadConfigRegisteringSecrets" {
				t.Errorf("%s builds its Runner from a cfg loaded by %q, want loadConfigRegisteringSecrets", tt.fn, got)
			}
		})
	}
}
