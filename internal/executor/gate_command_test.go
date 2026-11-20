package executor

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/quality"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-651, gates leg part 1: with run_as_user set the quality gates, the
// commands of the change scope and the reads of the task directory run as the
// agent user. sudo is the mock of the package (mockSudoAgent); no test needs
// root.

// useGateCommandBuilderForTest registers gateCommandBuilder as the builder of
// internal/quality for one test and restores the builder before it.
func useGateCommandBuilderForTest(t *testing.T) {
	t.Helper()
	prev := quality.CurrentCommandBuilder()
	quality.SetCommandBuilder(gateCommandBuilder{})
	t.Cleanup(func() { quality.SetCommandBuilder(prev) })
}

// gateFixture is a workspace below the workspace root, the option set for
// that root, the mock sudo and the builder of the executor registered.
type gateFixture struct {
	root, ws string
	sudoLog  string
}

func newGateFixture(t *testing.T, extra string) *gateFixture {
	t.Helper()
	stubRunAsGOOS(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &gateFixture{root: root, ws: ws}
	f.sudoLog = mockSudoAgent(t, "", extra)
	setRunAsStateForTest(t, gitRouteSettings(t, root), "")
	useGateCommandBuilderForTest(t)
	return f
}

// log is the raw log of the mock sudo: a script argument spans lines.
func (f *gateFixture) log(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(f.sudoLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func gateConfig(gates ...*quality.Gate) *quality.Config {
	return &quality.Config{Enabled: true, Gates: gates}
}

// gateEnvAllowed is the fixed list of the names a gate environment may hold:
// the allowlist, the names the agent base and the git identity set, the scope
// names and the names the shell sets. No gh guard name: a gate does not call
// gh.
var gateEnvAllowed = map[string]bool{
	"PATH": true, "LANG": true, "LC_ALL": true, "TERM": true, "TZ": true, "TMPDIR": true,
	"GOCACHE": true, "GOMODCACHE": true, "GOFLAGS": true, "GOTOOLCHAIN": true,
	"GOPROXY": true, "GONOSUMDB": true, "GOPRIVATE": true, "NODE_OPTIONS": true, "PILOT_EXECUTOR": true,
	"HOME": true, "CLAUDE_CONFIG_DIR": true, "GOPATH": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true, "GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"PILOT_CHANGED_PACKAGES": true, "PILOT_CHANGED_FULL": true, "PILOT_CHANGED_REASON": true,
	"PWD": true, "OLDPWD": true, "SHLVL": true, "_": true,
}

// clearModelEnvForTest unsets the CLAUDE_* and ANTHROPIC_* names of the test
// process for one test: the allowlist keeps them by prefix, and a test run
// from a model session has its own.
func clearModelEnvForTest(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "CLAUDE_") || strings.HasPrefix(name, "ANTHROPIC_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestRunAsUserGateEnvHasNoDaemonSecret: a gate that prints its environment
// runs through sudo as the agent user and gets only the names of
// gateEnvAllowed: no name of the daemon (probes with no denied ending, which
// only the allowlist removes), no PILOT_CHANGED_ name and no PILOT_TASK_ID of
// the daemon, no ANTHROPIC_CUSTOM_HEADERS; the scope names arrive with the
// values of the scope. git and go list of the scope run through sudo. The
// agent session gets no task id and no PILOT_CHANGED_ name of the daemon.
func TestRunAsUserGateEnvHasNoDaemonSecret(t *testing.T) {
	f := newGateFixture(t, "")
	clearModelEnvForTest(t)
	t.Setenv("DAEMON_ONLY_VALUE", "daemon-only-value")
	t.Setenv("SSH_AUTH_SOCK", "/daemon/agent.sock")
	t.Setenv("AWS_ACCESS_KEY_ID", "daemon-aws-key-id")
	t.Setenv("GITHUB_TOKEN", testutil.FakeGitHubToken)
	t.Setenv("PILOT_CHANGED_OPERATOR", "x")
	t.Setenv("PILOT_TASK_ID", "GH-DAEMON")
	t.Setenv(quality.EnvChangedReason, "daemon-reason")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Daemon: daemon-header")

	writeTestFile(t, filepath.Join(f.ws, "go.mod"), "module example.com/gate\n\ngo 1.21\n")
	writeTestFile(t, filepath.Join(f.ws, "a", "a.go"), "package a\n\nconst A = 1\n")
	writeTestFile(t, filepath.Join(f.ws, "b", "b.go"), "package b\n\nconst B = 1\n")
	writeTestFile(t, filepath.Join(f.ws, "c", "c.go"), "package c\n\nconst C = 1\n")
	wsGit(t, f.ws, "init", "-q", "-b", "main")
	wsGit(t, f.ws, "add", ".")
	wsGit(t, f.ws, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "-m", "init")
	wsGit(t, f.ws, "checkout", "-q", "-b", "feature")
	writeTestFile(t, filepath.Join(f.ws, "a", "a.go"), "package a\n\nconst A = 2\n")

	gate := &quality.Gate{Name: "env", Type: quality.GateCustom, Command: "env", Timeout: time.Minute}
	e := quality.NewExecutor(&quality.ExecutorConfig{Config: gateConfig(gate), ProjectPath: f.ws, TaskID: "GH-1", BaseBranch: "main"})
	outcome, err := e.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	res := outcome.Results.Results[0]
	if res.Status != quality.StatusPassed {
		t.Fatalf("gate status = %s (%s)", res.Status, res.Error)
	}
	// The messages name variables only: the output holds the values of the
	// environment of the test process.
	names := gateEnvNames(res.Output)
	if len(names) == 0 {
		t.Fatalf("the gate printed no environment")
	}
	for _, name := range names {
		if !gateEnvAllowed[name] {
			t.Errorf("gate environment has %s, a name outside the fixed list", name)
		}
	}
	for _, v := range []string{"daemon-only-value", "/daemon/agent.sock", "daemon-aws-key-id", "GH-DAEMON", "daemon-reason", "daemon-header"} {
		if strings.Contains(res.Output, v) {
			t.Errorf("gate environment has a value of the daemon; names: %v", names)
		}
	}
	got := map[string]string{}
	for _, line := range strings.Split(res.Output, "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			got[name] = value
		}
	}
	for name, want := range map[string]string{
		quality.EnvChangedPackages: "example.com/gate/a",
		quality.EnvChangedFull:     "0",
		quality.EnvChangedReason:   "affected 1 of 3 packages",
	} {
		if v, ok := got[name]; !ok || v != want {
			t.Errorf("gate environment %s = %q (set %v), want the value of the scope %q", name, v, ok, want)
		}
	}

	log := f.log(t)
	for _, want := range []string{"-n -u pilot-agent", " -- sh -c env", " -- git rev-parse --show-toplevel", " -- git merge-base", " -- go list -f"} {
		if !strings.Contains(log, want) {
			t.Errorf("sudo log lacks %q:\n%s", want, log)
		}
	}

	// The agent session: no task id and no PILOT_CHANGED_ name of the daemon.
	cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
	if err != nil {
		t.Fatal(err)
	}
	setAgentEnv(cmd, modelSubprocessEnv(os.Environ()))
	if _, ok := agentCmdUser(cmd); !ok {
		t.Fatalf("the agent session is not a sudo command: %q", cmd.Args)
	}
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "PILOT_TASK_ID" || strings.HasPrefix(name, "PILOT_CHANGED_") {
			t.Errorf("the agent session has %s of the daemon", name)
		}
	}
}

// TestRunAsUserGateRefusesTheDefaultBuilder: with the option set and the
// default builder of internal/quality in place, the gate start of the
// executor returns the error and no gate command runs. After
// PreflightRunAsUser alone the builder of internal/quality is the gate
// builder of the executor.
func TestRunAsUserGateRefusesTheDefaultBuilder(t *testing.T) {
	f := newGateFixture(t, "")
	quality.SetCommandBuilder(nil) // the default builder; the fixture restores
	marker := filepath.Join(t.TempDir(), "gate-ran")
	gate := &quality.Gate{Name: "touch", Type: quality.GateCustom, Command: "touch " + marker, Timeout: time.Minute}
	inner := &gateTestChecker{runner: quality.NewRunner(gateConfig(gate), f.ws)}

	_, err := guardGateChecker(inner).Check(context.Background())
	if err == nil || err.Error() != "run_as_user: the quality gates have no agent command builder" {
		t.Errorf("Check = %v, want the refusal", err)
	}
	if inner.calls != 0 {
		t.Errorf("the checker ran %d times", inner.calls)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a gate command ran")
	}
	if calls := sudoCalls(t, f.sudoLog); len(calls) != 0 {
		t.Errorf("sudo ran: %q", calls)
	}

	// The preflight registers the gate builder on its own, here with the
	// option empty; the run_as state of the fixture comes back at cleanup.
	PreflightRunAsUser(&ClaudeCodeConfig{}, "claude-code", false, 0)
	if _, ok := quality.CurrentCommandBuilder().(gateCommandBuilder); !ok {
		t.Fatalf("after PreflightRunAsUser the builder is %T, want gateCommandBuilder", quality.CurrentCommandBuilder())
	}
	setRunAsStateForTest(t, gitRouteSettings(t, f.root), "")
	if _, err := guardGateChecker(inner).Check(context.Background()); err != nil {
		t.Fatalf("Check with the gate builder = %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("the checker ran %d times, want 1", inner.calls)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the gate did not run: %v", err)
	}
}

// gateTestChecker is a QualityChecker that runs the gates of runner.
type gateTestChecker struct {
	runner *quality.Runner
	calls  int
}

func (c *gateTestChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	c.calls++
	results, err := c.runner.RunAll(ctx, "")
	if err != nil {
		return nil, err
	}
	return &QualityOutcome{Passed: results.AllPassed}, nil
}

// TestRunAsUserGateCommandNotAgentSudo: a command agentCommand gives without
// sudo (the platform is not Linux) is an error of Build with the option set,
// never a command that runs as the daemon user.
func TestRunAsUserGateCommandNotAgentSudo(t *testing.T) {
	f := newGateFixture(t, "")
	runAsGOOS = "darwin" // stubRunAsGOOS of the fixture restores
	cmd, err := gateCommandBuilder{}.Build(context.Background(), f.ws, nil, "sh", "-c", "true")
	if err == nil {
		t.Fatalf("Build = %q, want an error", cmd.Args)
	}
	if cmd != nil {
		t.Errorf("Build returned a command with the error: %q", cmd.Args)
	}
}

// gateCmdEnv returns the value of name in the environment of cmd.
func gateCmdEnv(cmd *exec.Cmd, name string) (string, bool) {
	v, ok := "", false
	for _, kv := range cmd.Env {
		if n, val, _ := strings.Cut(kv, "="); n == name {
			v, ok = val, true
		}
	}
	return v, ok
}

// TestRunAsUserGateEnvFromScratchKeepsCallerValue: an env built from scratch
// keeps a scope name the caller set, also with the value the daemon holds; an
// env that starts from a copy of os.Environ() loses the entry of the copy and
// keeps the one the caller appended; a nil env loses the names of the daemon.
//
// mutation pin: dropDaemonCodeSetEnv drops the first entry with the value of the daemon also from an env built from scratch -> this test fails
func TestRunAsUserGateEnvFromScratchKeepsCallerValue(t *testing.T) {
	f := newGateFixture(t, "")
	t.Setenv(quality.EnvChangedPackages, "daemon-pkgs")
	t.Setenv("PILOT_TASK_ID", "GH-DAEMON")
	build := func(env []string) *exec.Cmd {
		t.Helper()
		cmd, err := gateCommandBuilder{}.Build(context.Background(), f.ws, env, "sh", "-c", "true")
		if err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	cmd := build([]string{"PATH=" + os.Getenv("PATH"), quality.EnvChangedPackages + "=daemon-pkgs", "PILOT_TASK_ID=GH-DAEMON"})
	if v, ok := gateCmdEnv(cmd, quality.EnvChangedPackages); !ok || v != "daemon-pkgs" {
		t.Errorf("env from scratch: %s = %q (set %v), want the value the caller set", quality.EnvChangedPackages, v, ok)
	}
	if v, ok := gateCmdEnv(cmd, "PILOT_TASK_ID"); !ok || v != "GH-DAEMON" {
		t.Errorf("env from scratch: PILOT_TASK_ID = %q (set %v), want the value the caller set", v, ok)
	}

	cmd = build(append(os.Environ(), quality.EnvChangedPackages+"=scope-pkgs"))
	if v, ok := gateCmdEnv(cmd, quality.EnvChangedPackages); !ok || v != "scope-pkgs" {
		t.Errorf("copy of the daemon: %s = %q (set %v), want the appended value", quality.EnvChangedPackages, v, ok)
	}
	if v, ok := gateCmdEnv(cmd, "PILOT_TASK_ID"); ok {
		t.Errorf("copy of the daemon: PILOT_TASK_ID = %q, want the entry of the daemon dropped", v)
	}

	cmd = build(append(os.Environ(), quality.EnvChangedPackages+"=daemon-pkgs"))
	if v, ok := gateCmdEnv(cmd, quality.EnvChangedPackages); !ok || v != "daemon-pkgs" {
		t.Errorf("copy of the daemon: %s = %q (set %v), want the appended entry with the value of the daemon", quality.EnvChangedPackages, v, ok)
	}

	cmd = build(nil)
	for _, name := range []string{quality.EnvChangedPackages, "PILOT_TASK_ID"} {
		if v, ok := gateCmdEnv(cmd, name); ok {
			t.Errorf("nil env: %s = %q, want the entry of the daemon dropped", name, v)
		}
	}
}

// TestRunAsUserGateStartSitesGuarded: every checker the executor makes from
// qualityCheckerFactory goes through guardGateChecker, in each of the three
// gate starts: the timeout salvage route, the gates of the run and the gates
// after the review fix. A start that skips the wrap would run the gates as
// the daemon user with the option set and the default builder in place.
//
// mutation pin: the timeout salvage route (attemptBackendTimeoutSalvage, runner.go) starts the gates without guardGateChecker -> this test fails
func TestRunAsUserGateStartSitesGuarded(t *testing.T) {
	want := map[string]bool{"attemptBackendTimeoutSalvage": false, "executeWithOptions": false, "runReviewFix": false}
	fset := token.NewFileSet()
	for _, name := range []string{"runner.go", "pre_pr_fix.go"} {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			guarded := map[ast.Expr]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "guardGateChecker" && len(call.Args) == 1 {
					guarded[call.Args[0]] = true
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "qualityCheckerFactory" {
					if !guarded[call] {
						t.Errorf("%s: %s calls qualityCheckerFactory without guardGateChecker", fset.Position(call.Pos()), fn.Name.Name)
					} else if _, named := want[fn.Name.Name]; named {
						want[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	for fn, found := range want {
		if !found {
			t.Errorf("%s has no gate start through guardGateChecker", fn)
		}
	}
}

// gateEnvNames returns the names of the `env` output lines.
func gateEnvNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name, _, ok := strings.Cut(line, "="); ok && name != "" && !strings.ContainsAny(name, " \t(){}") {
			names = append(names, name)
		}
	}
	return names
}

// TestRunAsUserGateTimeoutKillsThroughSudo: a gate past its time limit is
// ended through the kill of the agent route and the result is the time limit
// error.
func TestRunAsUserGateTimeoutKillsThroughSudo(t *testing.T) {
	f := newGateFixture(t, mockSudoKill)
	gate := &quality.Gate{Name: "slow", Type: quality.GateCustom, Command: "sleep 30", Timeout: time.Second}
	start := time.Now()
	res, err := quality.NewRunner(gateConfig(gate), f.ws).RunGate(context.Background(), "slow")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Err, quality.ErrGateTimeout) {
		t.Errorf("Err = %v, want ErrGateTimeout", res.Err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("gate took %s: the kill did not end it", d)
	}
	if kills := agentKillCalls(sudoCalls(t, f.sudoLog)); len(kills) == 0 {
		t.Errorf("no kill through sudo:\n%s", f.log(t))
	}
}

// TestRunAsUserGateMakefileFIFO: a FIFO in place of the Makefile gives the
// time limit error of the gate; the Makefile is read through sudo, never by
// the daemon.
func TestRunAsUserGateMakefileFIFO(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed: the gate does not read the Makefile")
	}
	f := newGateFixture(t, mockSudoKill)
	makefile := filepath.Join(f.ws, "Makefile")
	if err := syscall.Mkfifo(makefile, 0o644); err != nil {
		t.Fatal(err)
	}
	gate := &quality.Gate{Name: "build", Type: quality.GateBuild, Command: "make build", Timeout: 2 * time.Second}
	res, err := quality.NewRunner(gateConfig(gate), f.ws).RunGate(context.Background(), "build")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(res.Err, quality.ErrGateTimeout) {
		t.Errorf("Err = %v, want ErrGateTimeout", res.Err)
	}
	if log := f.log(t); !strings.Contains(log, agentReadScript+" sh "+makefile) {
		t.Errorf("the Makefile was not read through sudo:\n%s", log)
	}
	// Give the blocked read of the agent side a Makefile with the target: the
	// command lookup, which outlives the gate, ends with no further read.
	if w, err := os.OpenFile(makefile, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		_, _ = w.WriteString("build:\n\ttrue\n")
		_ = w.Close()
	}
}

// TestRunAsUserGateReadsDoNotFollowDaemonLinks: a link in the workspace that
// points to a file or a directory of the daemon is resolved as the agent
// user, who may not read there (the mock refuses every path that resolves
// into the daemon directory), never as the daemon.
func TestRunAsUserGateReadsDoNotFollowDaemonLinks(t *testing.T) {
	daemonDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deny := fmt.Sprintf(`for a in "$@"; do
  r=$(realpath -- "$a" 2>/dev/null) || continue
  case "$r" in '%[1]s'|'%[1]s'/*) echo "mock sudo: permission denied" >&2; exit 1 ;; esac
done`, daemonDir)
	f := newGateFixture(t, deny)
	secret := filepath.Join(daemonDir, "secret.txt")
	writeTestFile(t, secret, "daemon secret\n")
	writeTestFile(t, filepath.Join(daemonDir, "sub", "secret.txt"), "daemon secret\n")
	fileLink := filepath.Join(f.ws, "file-link")
	dirLink := filepath.Join(f.ws, "dir-link")
	if err := os.Symlink(secret, fileLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(daemonDir, dirLink); err != nil {
		t.Fatal(err)
	}
	b := gateCommandBuilder{}

	if _, err := b.Stat(fileLink); err == nil {
		t.Error("Stat followed the link to the daemon file")
	}
	if _, err := b.ReadDir(dirLink); err == nil {
		t.Error("ReadDir followed the link to the daemon directory")
	}
	if got, err := b.EvalSymlinks(fileLink); err == nil {
		t.Errorf("EvalSymlinks resolved the link to %q", got)
	}
	if got, err := b.ReadFile(fileLink); err == nil {
		t.Errorf("ReadFile read %q through the link", got)
	}
	// A path below the link to the daemon directory: an error of each method,
	// never a read as the daemon user.
	below := filepath.Join(dirLink, "secret.txt")
	if _, err := b.Stat(below); err == nil {
		t.Error("Stat of a path below the link to the daemon directory gave no error")
	}
	if _, err := b.ReadDir(filepath.Join(dirLink, "sub")); err == nil {
		t.Error("ReadDir below the link to the daemon directory gave no error")
	}
	if got, err := b.EvalSymlinks(below); err == nil {
		t.Errorf("EvalSymlinks of a path below the link resolved to %q", got)
	}
	if got, err := b.ReadFile(below); err == nil {
		t.Errorf("ReadFile read %q below the link", got)
	}
	log := f.log(t)
	for _, p := range []string{fileLink, dirLink} {
		if !strings.Contains(log, p) {
			t.Errorf("no sudo call for %s:\n%s", p, log)
		}
	}

	// The workspace itself is read as the agent user: the links are entries
	// of type link, not followed.
	entries, err := b.ReadDir(f.ws)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]os.FileMode{}
	for _, e := range entries {
		types[e.Name()] = e.Type()
	}
	for _, name := range []string{"dir-link", "file-link"} {
		if types[name] != os.ModeSymlink {
			t.Errorf("entry %s type = %v, want a link (entries %v)", name, types[name], types)
		}
	}
	info, err := b.Stat(f.ws)
	if err != nil || !info.IsDir() {
		t.Errorf("Stat(workspace) = %v, %v; want a directory", info, err)
	}
	if got, err := b.EvalSymlinks(f.ws); err != nil || got != f.ws {
		t.Errorf("EvalSymlinks(workspace) = %q, %v", got, err)
	}
}

// TestRunAsUserGateReadsReturnWithinLimit: with a sudo that does not return,
// each read method ends through the kill of the agent route within its limit.
func TestRunAsUserGateReadsReturnWithinLimit(t *testing.T) {
	f := newGateFixture(t, mockSudoKill+"\nsleep 30")
	path := filepath.Join(f.ws, "go.mod")
	writeTestFile(t, path, "module example.com/gate\n")
	b := gateCommandBuilder{}
	reads := map[string]func() error{
		"ReadFile":     func() error { _, err := b.ReadFile(path); return err },
		"Stat":         func() error { _, err := b.Stat(path); return err },
		"ReadDir":      func() error { _, err := b.ReadDir(f.ws); return err },
		"EvalSymlinks": func() error { _, err := b.EvalSymlinks(path); return err },
	}
	var wg sync.WaitGroup
	for name, read := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := read()
			if d := time.Since(start); d > 12*time.Second {
				t.Errorf("%s returned after %s", name, d)
			}
			if err == nil {
				t.Errorf("%s returned no error", name)
			}
		}()
	}
	wg.Wait()
}

// TestRunAsUserGateReadsReturnWhenKillFails: with a sudo that does not return
// and ignores the kill (the kill through sudo fails), each read method returns
// the time limit error within its limit plus 3 seconds, with one Error line
// that names the method, the path and the process id.
func TestRunAsUserGateReadsReturnWhenKillFails(t *testing.T) {
	f := newGateFixture(t, `case "$1" in -[0-9]*) exit 1 ;; esac
trap '' TERM INT HUP
sleep 30`)
	logBuf := &syncBuffer{}
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLog) })
	path := filepath.Join(f.ws, "go.mod")
	writeTestFile(t, path, "module example.com/gate\n")
	b := gateCommandBuilder{}
	paths := map[string]string{"ReadFile": path, "Stat": path, "ReadDir": f.ws, "EvalSymlinks": path}
	reads := map[string]func() error{
		"ReadFile":     func() error { _, err := b.ReadFile(path); return err },
		"Stat":         func() error { _, err := b.Stat(path); return err },
		"ReadDir":      func() error { _, err := b.ReadDir(f.ws); return err },
		"EvalSymlinks": func() error { _, err := b.EvalSymlinks(path); return err },
	}
	var wg sync.WaitGroup
	for name, read := range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := read()
			if d := time.Since(start); d > gateReadTimeout+3*time.Second {
				t.Errorf("%s returned after %s", name, d)
			}
			if err == nil {
				t.Errorf("%s returned no error", name)
			}
		}()
	}
	wg.Wait()
	logs := logBuf.String()
	for name, p := range paths {
		var lines []string
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, "level=ERROR") && strings.Contains(line, "method="+name+" ") && strings.Contains(line, "path="+p+" ") {
				lines = append(lines, line)
			}
		}
		if len(lines) != 1 || strings.Contains(lines[0], "pid=0") || !strings.Contains(lines[0], "pid=") {
			t.Errorf("%s: want one Error line with the method, the path and the process id, got %q", name, lines)
		}
	}
}

// TestRunAsUserGateOptionEmpty: with the option empty the builder is the
// default: the gate command is `sh`, the reads are the os calls, and sudo
// never runs.
func TestRunAsUserGateOptionEmpty(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	sudoLog := mockSudoAgent(t, "", "")
	useGateCommandBuilderForTest(t)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Makefile"), "build:\n\ttrue\n")

	b := gateCommandBuilder{}
	cmd, err := b.Build(context.Background(), dir, nil, "sh", "-c", "true")
	if err != nil || cmd == nil {
		t.Fatalf("Build = %v, %v", cmd, err)
	}
	if cmd.Args[0] != "sh" || cmd.Dir != dir || cmd.Env != nil {
		t.Errorf("cmd args %q dir %q env %v", cmd.Args, cmd.Dir, cmd.Env)
	}

	gate := &quality.Gate{Name: "echo", Type: quality.GateCustom, Command: "echo ok", Timeout: time.Minute}
	res, err := quality.NewRunner(gateConfig(gate), dir).RunGate(context.Background(), "echo")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != quality.StatusPassed || !strings.Contains(res.Output, "ok") {
		t.Errorf("gate = %s %q", res.Status, res.Output)
	}
	if data, err := b.ReadFile(filepath.Join(dir, "Makefile")); err != nil || !strings.Contains(string(data), "build:") {
		t.Errorf("ReadFile = %q, %v", data, err)
	}
	if _, err := b.Stat(dir); err != nil {
		t.Error(err)
	}
	if _, err := b.ReadDir(dir); err != nil {
		t.Error(err)
	}
	if _, err := b.EvalSymlinks(dir); err != nil {
		t.Error(err)
	}
	if calls := sudoCalls(t, sudoLog); len(calls) != 0 {
		t.Errorf("sudo ran: %q", calls)
	}
}
