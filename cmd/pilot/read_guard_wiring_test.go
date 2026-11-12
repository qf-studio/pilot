package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
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
	if !callsIdent(mainGoFunc(t, "newStartCmd"), "applyReadGuardConfig") {
		t.Error("newStartCmd does not call applyReadGuardConfig: the loaded config path never reaches the helper spawns")
	}

	cfgPath := filepath.Join(t.TempDir(), "custom.yaml")
	t.Cleanup(func() { executor.SetReadGuardConfig(nil) })

	off := false
	applyReadGuardConfig(&executor.ClaudeCodeConfig{ReadGuard: &off}, cfgPath)
	got := executor.ReadGuardConfig()
	if got == nil || got.ConfigPath != cfgPath || got.ReadGuardEnabled() {
		t.Errorf("ReadGuardConfig() = %+v, want ConfigPath %q and guard off", got, cfgPath)
	}

	// No executor.claude_code block: the path is still protected.
	applyReadGuardConfig(nil, cfgPath)
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
		executor.PreflightRunAsUser(nil)
	})
	cfgPath := filepath.Join(t.TempDir(), "custom.yaml")

	applyReadGuardConfig(&executor.ClaudeCodeConfig{RunAsUser: "pilot-no-such-user-6321"}, cfgPath)
	if err := executor.RunAsUserPreflightError(); err == nil {
		t.Error("a run_as_user that does not exist left spawns open: the preflight is not wired into applyReadGuardConfig")
	}

	applyReadGuardConfig(&executor.ClaudeCodeConfig{}, cfgPath)
	if err := executor.RunAsUserPreflightError(); err != nil {
		t.Errorf("empty run_as_user: preflight error %v, want none", err)
	}
}
