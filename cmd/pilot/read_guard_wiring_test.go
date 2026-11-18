package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
)

// mainGoFunc returns the body of the top-level func name in main.go.
func mainGoFunc(t *testing.T, name string) *ast.BlockStmt {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			return fn.Body
		}
	}
	t.Fatalf("main.go has no func %s", name)
	return nil
}

func callsIdent(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// mutation pin: `pilot start` stops handing the loaded config path and claude_code settings to the read guard (the main.go SetReadGuardConfig wiring dropped) -> this test fails
func TestStartCmdWiresReadGuardConfig(t *testing.T) {
	if !callsIdent(mainGoFunc(t, "newStartCmd"), "applyStartReadGuardConfig") ||
		!callsIdent(mainGoFunc(t, "applyStartReadGuardConfig"), "applyReadGuardConfig") {
		t.Error("newStartCmd does not call applyReadGuardConfig through applyStartReadGuardConfig: the loaded config path never reaches the helper spawns")
	}

	cfgPath := filepath.Join(t.TempDir(), "custom.yaml")
	t.Cleanup(func() { executor.SetReadGuardConfig(nil) })

	off := false
	applyReadGuardConfig(&executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{ReadGuard: &off}}, cfgPath, 0, "")
	got := executor.ReadGuardConfig()
	if got == nil || got.ConfigPath != cfgPath || got.ReadGuardEnabled() {
		t.Errorf("ReadGuardConfig() = %+v, want ConfigPath %q and guard off", got, cfgPath)
	}

	// No executor.claude_code block: the path is still protected.
	applyReadGuardConfig(nil, cfgPath, 0, "")
	got = executor.ReadGuardConfig()
	if got == nil || got.ConfigPath != cfgPath || !got.ReadGuardEnabled() {
		t.Errorf("ReadGuardConfig() = %+v, want ConfigPath %q and guard on", got, cfgPath)
	}
}

// mutation pin: only `pilot start` sets the daemon-wide read guard config, other entrypoints keep the defaults (read_guard: false ignored, custom --config path not denied) -> this test fails
func TestEveryEntrypointAppliesReadGuardConfig(t *testing.T) {
	t.Cleanup(func() { executor.SetReadGuardConfig(nil) })

	t.Run("read_guard false and custom path", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "custom.yaml")
		if err := os.WriteFile(cfgPath, []byte("executor:\n  claude_code:\n    read_guard: false\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		executor.SetReadGuardConfig(nil)
		applyReadGuardConfigFromFile(cfgPath)
		got := executor.ReadGuardConfig()
		if got == nil || got.ReadGuardEnabled() || got.ConfigPath != cfgPath {
			t.Errorf("ReadGuardConfig() = %+v, want guard off and ConfigPath %q", got, cfgPath)
		}
	})

	t.Run("no claude_code block", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "custom.yaml")
		if err := os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		executor.SetReadGuardConfig(nil)
		applyReadGuardConfigFromFile(cfgPath)
		got := executor.ReadGuardConfig()
		if got == nil || !got.ReadGuardEnabled() || got.ConfigPath != cfgPath {
			t.Errorf("ReadGuardConfig() = %+v, want guard on and ConfigPath %q", got, cfgPath)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "absent.yaml")
		executor.SetReadGuardConfig(nil)
		applyReadGuardConfigFromFile(cfgPath)
		got := executor.ReadGuardConfig()
		if got == nil || !got.ReadGuardEnabled() || got.ConfigPath != cfgPath {
			t.Errorf("ReadGuardConfig() = %+v, want guard on and ConfigPath %q", got, cfgPath)
		}
	})

	t.Run("root command runs it for every subcommand", func(t *testing.T) {
		assigned := false
		ast.Inspect(mainGoFunc(t, "main"), func(n ast.Node) bool {
			if as, ok := n.(*ast.AssignStmt); ok {
				for _, lhs := range as.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "PersistentPreRun" {
						assigned = true
					}
				}
			}
			return true
		})
		if !assigned {
			t.Error("main() does not set rootCmd.PersistentPreRun")
		}
	})
}

// mutation pin: applyReadGuardConfig stops running the run_as_user preflight, so an entrypoint with a broken run_as_user spawns model processes as the daemon user -> this test fails
func TestReadGuardConfigRunsRunAsUserPreflight(t *testing.T) {
	t.Cleanup(func() {
		executor.SetReadGuardConfig(nil)
		executor.PreflightRunAsUser(nil, "", true, 0)
	})
	cfgPath := filepath.Join(t.TempDir(), "custom.yaml")

	applyReadGuardConfig(&executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{RunAsUser: "pilot-no-such-user-6321"}}, cfgPath, 0, "")
	if err := executor.RunAsUserPreflightError(); err == nil {
		t.Error("a run_as_user that does not exist left spawns open: the preflight is not wired into applyReadGuardConfig")
	}

	applyReadGuardConfig(&executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{}}, cfgPath, 0, "")
	if err := executor.RunAsUserPreflightError(); err != nil {
		t.Errorf("empty run_as_user: preflight error %v, want none", err)
	}
}

// capturePreflight replaces the run_as_user preflight with a stub that
// records the claude_code config it gets.
func capturePreflight(t *testing.T) func() *executor.ClaudeCodeConfig {
	t.Helper()
	var got *executor.ClaudeCodeConfig
	prev := preflightRunAsUser
	preflightRunAsUser = func(cfg *executor.ClaudeCodeConfig, _ string, _ bool, _ int) { got = cfg }
	t.Cleanup(func() {
		preflightRunAsUser = prev
		executor.SetReadGuardConfig(nil)
	})
	return func() *executor.ClaudeCodeConfig { return got }
}

// writeGatewayConfig writes a config file with the gateway port and host.
func writeGatewayConfig(t *testing.T, port int, host string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("version: \"1.0\"\ngateway:\n  host: %q\n  port: %d\n", host, port)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// GH-6455: only the daemon start passes the gateway port and host of the
// config to the run_as_user preflight; with --replace after the old daemon
// has stopped.
// mutation pin: drop the assignment of the gateway port at the daemon start
// -> this test fails.
func TestStartCmdPassesGatewayToRunAsPreflight(t *testing.T) {
	if !callsIdent(mainGoFunc(t, "newStartCmd"), "applyStartReadGuardConfig") {
		t.Error("newStartCmd does not call applyStartReadGuardConfig")
	}
	if !callsIdent(mainGoFunc(t, "runPollingMode"), "applyStartReadGuardConfig") {
		t.Error("runPollingMode does not call applyStartReadGuardConfig: pilot start --replace never probes the gateway port")
	}
	got := capturePreflight(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	applyStartReadGuardConfig(&config.Config{Gateway: &gateway.Config{Host: "0.0.0.0", Port: 9123}}, cfgPath)
	if c := got(); c == nil || c.RunAsGatewayPort != 9123 || c.RunAsGatewayHost != "0.0.0.0" || c.ConfigPath != cfgPath {
		t.Errorf("preflight got %+v, want gateway port 9123, host 0.0.0.0 and ConfigPath %q", c, cfgPath)
	}
}

// The daemon start loads the YAML file: its gateway port reaches the
// preflight.
func TestStartCmdYAMLGatewayPortReachesRunAsPreflight(t *testing.T) {
	got := capturePreflight(t)
	cfgPath := writeGatewayConfig(t, 9234, "0.0.0.0")
	cfg, err := loadConfigRegisteringSecrets(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	applyStartReadGuardConfig(cfg, cfgPath)
	if c := got(); c == nil || c.RunAsGatewayPort != 9234 || c.RunAsGatewayHost != "0.0.0.0" {
		t.Errorf("preflight got %+v, want gateway port 9234 and host 0.0.0.0 from the file", c)
	}
}

// Every other entry point passes no gateway port: a running daemon holds it.
func TestEntrypointPassesNoGatewayToRunAsPreflight(t *testing.T) {
	got := capturePreflight(t)
	cfgPath := writeGatewayConfig(t, 9234, "0.0.0.0")
	applyReadGuardConfigFromFile(cfgPath)
	if c := got(); c == nil || c.RunAsGatewayPort != 0 || c.RunAsGatewayHost != "" || c.ConfigPath != cfgPath {
		t.Errorf("preflight got %+v, want no gateway port and host and ConfigPath %q", c, cfgPath)
	}
}
