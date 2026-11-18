package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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
		!callsIdent(mainGoFunc(t, "applyStartReadGuardConfig"), "setReadGuardConfig") ||
		!callsIdent(mainGoFunc(t, "applyReadGuardConfig"), "setReadGuardConfig") {
		t.Error("newStartCmd does not call setReadGuardConfig through applyStartReadGuardConfig: the loaded config path never reaches the helper spawns")
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
	if !callsIdent(mainGoFunc(t, "runPollingMode"), "replaceStartPreflight") {
		t.Error("runPollingMode does not call replaceStartPreflight: pilot start --replace never probes the gateway port")
	}
	got := capturePreflight(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	applyStartReadGuardConfig(&config.Config{Gateway: &gateway.Config{Host: "0.0.0.0", Port: 9123}}, cfgPath)()
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
	applyStartReadGuardConfig(cfg, cfgPath)()
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

// preflightArgs is what reaches the run_as_user preflight.
type preflightArgs struct {
	cfg         executor.ClaudeCodeConfig
	backend     string
	useWorktree bool
	poolSize    int
}

// capturePreflightArgs is capturePreflight with every argument; calls counts
// the runs.
func capturePreflightArgs(t *testing.T) (got *preflightArgs, calls *int) {
	t.Helper()
	got, calls = &preflightArgs{}, new(int)
	prev := preflightRunAsUser
	preflightRunAsUser = func(cfg *executor.ClaudeCodeConfig, backend string, useWorktree bool, poolSize int) {
		*calls++
		*got = preflightArgs{cfg: *cfg, backend: backend, useWorktree: useWorktree, poolSize: poolSize}
	}
	t.Cleanup(func() {
		preflightRunAsUser = prev
		executor.SetReadGuardConfig(nil)
	})
	return got, calls
}

// GH-6477: the old daemon releases its lock before its gateway closes; with
// --replace the preflight waits for the gateway port.
// mutation pin: run the --replace preflight as soon as the lock is taken,
// while the old gateway still holds the port -> this test fails.
func TestReplaceStartPreflightWaitsForGatewayPort(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := held.Addr().(*net.TCPAddr).Port
	prev := gatewayPortWait
	gatewayPortWait = 5 * time.Second
	t.Cleanup(func() { gatewayPortWait = prev })
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = held.Close()
	}()

	ran, free := false, false
	replaceStartPreflight(&config.Config{Gateway: &gateway.Config{Host: "127.0.0.1", Port: port}}, func() {
		ran = true
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			free = true
			_ = l.Close()
		}
	})
	if !ran || !free {
		t.Errorf("preflight ran %v, gateway port %d free at the preflight %v: want a run once the old gateway has closed", ran, port, free)
	}
}

// GH-6477: the daemon start sets the read guard config of the loaded config
// at once, with --replace too; only the preflight waits.
// mutation pin: with --replace, set the read guard config only with the
// preflight -> this test fails.
func TestStartReadGuardConfigSetBeforePreflight(t *testing.T) {
	_, calls := capturePreflightArgs(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	off := false
	executor.SetReadGuardConfig(nil)
	preflight := applyStartReadGuardConfig(&config.Config{
		Executor: &executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{ReadGuard: &off}},
		Gateway:  &gateway.Config{Port: 9345},
	}, cfgPath)
	if got := executor.ReadGuardConfig(); got == nil || got.ConfigPath != cfgPath || got.ReadGuardEnabled() {
		t.Errorf("ReadGuardConfig() = %+v before the preflight, want ConfigPath %q and guard off", got, cfgPath)
	}
	if *calls != 0 {
		t.Fatalf("the preflight ran %d times before it was called", *calls)
	}
	preflight()
	if *calls != 1 {
		t.Errorf("the preflight ran %d times, want 1", *calls)
	}
}

// GH-6477: the preflight of the daemon start sees the executor config of
// the point where the read guard config is set, with --replace too: not the
// default block that gateway mode puts in place of a missing one.
// mutation pin: read cfg.Executor when the --replace preflight runs ->
// this test fails.
func TestStartPreflightInputsDoNotDependOnReplace(t *testing.T) {
	got, _ := capturePreflightArgs(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	newCfg := func() *config.Config { return &config.Config{Gateway: &gateway.Config{Port: 9456}} }

	// Without --replace: the preflight runs at once.
	applyStartReadGuardConfig(newCfg(), cfgPath)()
	direct := *got

	// With --replace: the start fills in the missing executor block before
	// the preflight runs.
	cfg := newCfg()
	preflight := applyStartReadGuardConfig(cfg, cfgPath)
	cfg.Executor = executor.DefaultBackendConfig()
	cfg.Executor.WorktreePoolSize = 3
	preflight()
	if !reflect.DeepEqual(*got, direct) {
		t.Errorf("preflight with --replace got %+v, without %+v: want the same inputs", *got, direct)
	}
}

// startPreflightCall finds in newStartCmd the assignment of the result of
// applyStartReadGuardConfig and reports the name it is assigned to and
// whether the assignment is under an if statement.
func startPreflightCall(t *testing.T) (name string, conditional bool) {
	t.Helper()
	var stack []ast.Node
	ast.Inspect(mainGoFunc(t, "newStartCmd"), func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 || len(as.Lhs) != 1 {
			return true
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "applyStartReadGuardConfig" {
			if lhs, ok := as.Lhs[0].(*ast.Ident); ok {
				name = lhs.Name
			}
			for _, outer := range stack {
				if _, ok := outer.(*ast.IfStmt); ok {
					conditional = true
				}
			}
		}
		return true
	})
	return name, conditional
}

// GH-6477: the daemon start sets the read guard config and the gateway of
// the preflight through applyStartReadGuardConfig on every path, not
// through a portless applyReadGuardConfig, and runs the preflight it
// returns.
// mutation pin: put a portless applyReadGuardConfig(cfg.Executor,
// configPath, 0, "") in place of the unconditional
// applyStartReadGuardConfig call of newStartCmd -> this test fails.
func TestStartCmdCallsStartReadGuardConfigOnEveryPath(t *testing.T) {
	body := mainGoFunc(t, "newStartCmd")
	if callsIdent(body, "applyReadGuardConfig") {
		t.Error("newStartCmd calls applyReadGuardConfig: the daemon start passes no gateway port to the preflight")
	}
	name, conditional := startPreflightCall(t)
	if name == "" || conditional {
		t.Fatalf("newStartCmd does not assign the preflight of applyStartReadGuardConfig outside an if statement (name %q, under an if %v)", name, conditional)
	}
	if !callsIdent(body, name) {
		t.Errorf("newStartCmd never runs the preflight %s", name)
	}
}
