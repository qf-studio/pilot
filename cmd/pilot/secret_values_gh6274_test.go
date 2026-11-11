package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

func collectedSet(tree map[string]interface{}) map[string]bool {
	have := map[string]bool{}
	for _, v := range collectKeyedStrings(tree, false, nil) {
		have[v] = true
	}
	return have
}

// mutation pin: every header value under headers/webhook_headers is registered, so application/json is masked in every recording -> this test fails
func TestCollectKeyedStrings_OrdinaryHeaderValuesNotCollected(t *testing.T) {
	for _, key := range []string{"headers", "webhook_headers"} {
		t.Run(key, func(t *testing.T) {
			have := collectedSet(map[string]interface{}{key: map[string]interface{}{
				"Content-Type":  "application/json",
				"accept":        "application/vnd.github+json",
				"User-Agent":    "pilot-alerts/1.0",
				"Authorization": "Bearer zq-header-bearer-6274",
				"X-Api-Key":     "zq-header-api-key-6274",
			}})
			for _, bad := range []string{"application/json", "application/vnd.github+json", "pilot-alerts/1.0"} {
				if have[bad] {
					t.Errorf("ordinary header value %q collected: %v", bad, have)
				}
			}
			for _, want := range []string{"Bearer zq-header-bearer-6274", "zq-header-bearer-6274", "zq-header-api-key-6274"} {
				if !have[want] {
					t.Errorf("credential header value %q missing: %v", want, have)
				}
			}
		})
	}
}

// yamlKeys adds to out every yaml key the struct tree of t can produce.
func yamlKeys(t reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if !strings.Contains(opts, "inline") {
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			out[name] = true
		}
		yamlKeys(f.Type, out, seen)
	}
}

// mutation pin: key dsn sits in credentialConfigKeys although no config field produces it -> this test fails
func TestCredentialConfigKeys_HaveAProducer(t *testing.T) {
	produced := map[string]bool{}
	yamlKeys(reflect.TypeOf(config.Config{}), produced, map[reflect.Type]bool{})
	for k := range credentialConfigKeys {
		if !produced[k] {
			t.Errorf("credentialConfigKeys has %q but no config field produces it; move it to reservedCredentialConfigKeys", k)
		}
	}
	for k := range reservedCredentialConfigKeys {
		if produced[k] {
			t.Errorf("reservedCredentialConfigKeys has %q and a config field now produces it; move it to credentialConfigKeys", k)
		}
		if !isSecretConfigKey(k) {
			t.Errorf("reserved key %q is not treated as a secret key", k)
		}
	}
}

func parseFuncSnippet(t *testing.T, body string) *ast.FuncDecl {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "snippet.go", "package x\nfunc f() {\n"+body+"\n}", 0)
	if err != nil {
		t.Fatal(err)
	}
	return f.Decls[0].(*ast.FuncDecl)
}

// mutation pin: the AST check anchors only on NewRunnerWithConfig(<x>.Executor) and follows only a variable named cfg, so reverting the dry-run load to config.Load passes -> this test fails
func TestRunnerConfigLoaders_FollowsAnyVariableAndBackendCfg(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"dry-run reverted", `
			dryRunCfg, cfgErr := config.Load(path)
			dryRunBackendCfg := dryRunCfg.Executor
			if dryRunBackendCfg == nil {
				dryRunBackendCfg = executor.DefaultBackendConfig()
			}
			runner, err := executor.NewRunnerWithConfig(dryRunBackendCfg)`, "dryRunCfg <- config.Load"},
		{"dry-run registering", `
			dryRunCfg, cfgErr := loadConfigRegisteringSecrets(path)
			dryRunBackendCfg := dryRunCfg.Executor
			runner, err := executor.NewRunnerWithConfig(dryRunBackendCfg)`, "dryRunCfg <- loadConfigRegisteringSecrets"},
		{"other selector variable", `
			c, err := config.Load(path)
			runner, err := executor.NewRunnerWithConfig(c.Executor)`, "c <- config.Load"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runnerConfigLoaders(parseFuncSnippet(t, tt.body))
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("runnerConfigLoaders = %v, want [%s]", got, tt.want)
			}
		})
	}
}

// mutation pin: pilot start still calls config.Load and registerSecretValues instead of loadConfigRegisteringSecrets -> this test fails
func TestStartCmdLoadsConfigRegisteringSecrets(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "newStartCmd" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("newStartCmd not found in main.go")
	}
	calls := map[string]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			calls[calleeName(call)]++
		}
		return true
	})
	if calls["config.Load"] != 0 || calls["registerSecretValues"] != 0 {
		t.Errorf("newStartCmd calls config.Load %d times and registerSecretValues %d times; use loadConfigRegisteringSecrets", calls["config.Load"], calls["registerSecretValues"])
	}
	if calls["loadConfigRegisteringSecrets"] != 1 {
		t.Errorf("newStartCmd calls loadConfigRegisteringSecrets %d times, want 1", calls["loadConfigRegisteringSecrets"])
	}
}

// mutation pin: the AST check only pins which function loads cfg and never runs the command's setup -> this test fails
func TestTaskCmdDryRunRegistersSecrets(t *testing.T) {
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)

	path := filepath.Join(t.TempDir(), "config.yaml")
	yml := "version: \"1.0\"\nadapters:\n  github:\n    enabled: true\n    token: cfg-dry-run-token-6274\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = prev })

	cmd := newTaskCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"describe the change", "--dry-run", "--project", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task --dry-run: %v\n%s", err, out.String())
	}
	if got := secretpatterns.Redact("a cfg-dry-run-token-6274 b"); got != "a <redacted> b" {
		t.Errorf("Redact = %q; the dry-run path did not register the config secrets", got)
	}
}

// mutation pin: webhookHostFragments lacks the legacy discordapp.com/api/webhooks host -> this test fails
func TestCollectKeyedStrings_LegacyDiscordWebhookHost(t *testing.T) {
	const hook = "https://discordapp.com/api/webhooks/1/zq-legacy-hook-6274"
	if have := collectedSet(map[string]interface{}{"url": hook}); !have[hook] {
		t.Errorf("legacy discordapp.com webhook url not collected: %v", have)
	}
}
