package executor

import (
	"context"
	"errors"
	"fmt"
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

// TestRunAsUserGateEnvHasNoDaemonSecret: a gate that prints its environment
// runs through sudo as the agent user, prints no secret of the daemon and
// prints the scope variables; git and go list of the scope run through sudo.
func TestRunAsUserGateEnvHasNoDaemonSecret(t *testing.T) {
	f := newGateFixture(t, "")
	t.Setenv("DAEMON_ONLY_SECRET", "daemon-only-value")
	t.Setenv("GITHUB_TOKEN", testutil.FakeGitHubToken)
	t.Setenv("PILOT_TASK_ID", "GH-1")

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
	for _, name := range []string{"DAEMON_ONLY_SECRET", "GITHUB_TOKEN"} {
		if strings.Contains(res.Output, name) {
			t.Errorf("gate environment has %s; names: %v", name, names)
		}
	}
	for _, kv := range []string{
		quality.EnvChangedPackages + "=example.com/gate/a",
		quality.EnvChangedFull + "=0",
		quality.EnvChangedReason + "=",
		"PILOT_TASK_ID=GH-1",
	} {
		if !strings.Contains(res.Output, kv) {
			t.Errorf("gate environment lacks %q; names: %v", kv, names)
		}
	}

	log := f.log(t)
	for _, want := range []string{"-n -u pilot-agent", " -- sh -c env", " -- git rev-parse --show-toplevel", " -- git merge-base", " -- go list -f"} {
		if !strings.Contains(log, want) {
			t.Errorf("sudo log lacks %q:\n%s", want, log)
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
