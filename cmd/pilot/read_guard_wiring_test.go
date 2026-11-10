package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
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

// callPositions returns the source offsets of the calls in body that match: a
// plain identifier call (f()) when pkg is empty, otherwise a pkg.name() call.
func callPositions(body *ast.BlockStmt, pkg, name string) []token.Pos {
	var out []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if pkg == "" && fun.Name == name {
				out = append(out, call.Pos())
			}
		case *ast.SelectorExpr:
			if id, ok := fun.X.(*ast.Ident); ok && pkg != "" && id.Name == pkg && fun.Sel.Name == name {
				out = append(out, call.Pos())
			}
		}
		return true
	})
	return out
}

func minPos(ps []token.Pos) token.Pos {
	m := token.NoPos
	for _, p := range ps {
		if m == token.NoPos || p < m {
			m = p
		}
	}
	return m
}

func maxPos(ps []token.Pos) token.Pos {
	m := token.NoPos
	for _, p := range ps {
		if p > m {
			m = p
		}
	}
	return m
}

// mutation pin: `pilot start` stops running the read guard preflight (executor.PreflightReadGuard dropped) -> a host without realpath or jq/python3 blocks every Read, Grep, Glob and Bash call of every run and this test fails
func TestStartCmdRunsReadGuardPreflight(t *testing.T) {
	body := mainGoFunc(t, "newStartCmd")
	pre := callPositions(body, "executor", "PreflightReadGuard")
	if len(pre) == 0 {
		t.Fatal("newStartCmd does not call executor.PreflightReadGuard")
	}
	// GH-6287: the Error line of the preflight must land in the daemon log, so
	// it runs after logging.Init and after the dashboard log redirect.
	at := minPos(pre)
	for _, setup := range []struct{ pkg, name string }{{"logging", "Init"}, {"", "setupDashboardLogging"}} {
		ps := callPositions(body, setup.pkg, setup.name)
		if len(ps) == 0 {
			t.Fatalf("newStartCmd does not call %s %s", setup.pkg, setup.name)
		}
		if maxPos(ps) > at {
			t.Errorf("newStartCmd runs the read guard preflight before %s %s: the Error line would miss the daemon log", setup.pkg, setup.name)
		}
	}
	// ...and before the first agent session is possible.
	for _, spawn := range callPositions(body, "executor", "NewRunnerWithConfig") {
		if spawn < at {
			t.Error("newStartCmd builds a runner before the read guard preflight")
		}
	}
	if poll := callPositions(body, "", "runPollingMode"); len(poll) == 0 || minPos(poll) < at {
		t.Error("newStartCmd must call runPollingMode (which builds a runner) after the read guard preflight")
	}
}

// mutation pin: a command that spawns agent sessions (task, github run, navigator bootstrap, the interactive new-task flow) stops calling preflightReadGuardForSpawn before its runner -> a host without realpath or jq/python3 blocks every call of the session instead of getting the Error line and the guard off, and this test fails
func TestEverySpawnCommandRunsReadGuardPreflight(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// runPollingMode is only reached from newStartCmd, which preflights first
	// (TestStartCmdRunsReadGuardPreflight pins that order).
	startOnly := map[string]bool{"runPollingMode": true}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			spawns := callPositions(fn.Body, "executor", "NewRunnerWithConfig")
			if len(spawns) == 0 || startOnly[fn.Name.Name] {
				continue
			}
			seen[fn.Name.Name] = true
			pre := append(callPositions(fn.Body, "", "preflightReadGuardForSpawn"), callPositions(fn.Body, "executor", "PreflightReadGuard")...)
			// A dry run builds a runner only to print the prompt; the last
			// runner of the function is the one that spawns sessions.
			if len(pre) == 0 || minPos(pre) > maxPos(spawns) {
				t.Errorf("%s (%s) builds an executor runner without running the read guard preflight first", fn.Name.Name, file)
			}
		}
	}
	for _, want := range []string{"newStartCmd", "newTaskCmd", "newGitHubRunCmd", "newNavigatorBootstrapCmd", "interactiveNewTask"} {
		if !seen[want] {
			t.Errorf("expected %s to build an executor runner; the scan lost it", want)
		}
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
