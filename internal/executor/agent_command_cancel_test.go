package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TASK-651 leg F1: the cancel of a context stops the model at every spawn
// site, the backend and group checks of the preflight, fail-closed guard files.

// mockSudoBackground puts a fake sudo first on PATH that behaves like a sudo
// the daemon user cannot signal: a spawn runs `sleep 30` in the background
// (the model) and ignores TERM, INT and HUP, so a signal to the sudo process
// alone leaves the model alive, holding the stdout and stderr pipes. A
// `sudo ... kill -s SIG -- -PGID` runs the kill. Every call is recorded, NUL
// separated, in its own file in the returned directory.
func mockSudoBackground(t *testing.T) (logDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock sudo is a shell script")
	}
	dir := t.TempDir()
	logDir = t.TempDir()
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "${0##*/}" "$@" > '%s'/argv.$$
shift 3
if [ "$1" = "-g" ]; then shift 2; fi
if [ "$1" = "kill" ]; then
  shift
  kill "$@"
  exit $?
fi
trap '' TERM INT HUP
sleep 30 &
wait
`, logDir)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logDir
}

// sudoKillPgids returns the process group ids of the recorded
// `sudo -n -u <user> kill -s <sig> -- -<pgid>` calls.
func sudoKillPgids(t *testing.T, logDir string) []int {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(logDir, "argv.*"))
	var pgids []int
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		n := len(argv)
		if n < 9 || argv[4] != "kill" || argv[5] != "-s" || argv[6] != "KILL" || argv[7] != "--" {
			continue
		}
		pgid, err := strconv.Atoi(argv[n-1])
		if err != nil || pgid >= -1 {
			t.Errorf("kill argv does not end in a negative process group id: %q", argv)
			continue
		}
		pgids = append(pgids, pgid)
	}
	return pgids
}

// timeoutSites is every spawn site of the run_as_user cancel defect plus the
// three sites that set their own cancel.
func timeoutSites(cli string) []agentSite {
	var sites []agentSite
	for _, s := range agentSites(cli) {
		if s.name != "opencode server" {
			sites = append(sites, s)
		}
	}
	return append(sites, agentSite{"post-merge reviewer runner", cli, func(ctx context.Context, t *testing.T, proj, _ string) {
		_, _ = NewIntentJudge(cli).runCmdInHome(ctx, proj, t.TempDir(), "--print", "-p", "x")
	}})
}

func TestRunAsUserContextTimeoutStopsModelAtEverySite(t *testing.T) {
	stubRunAsGOOS(t)
	cli := agentSiteEnv(t)
	for _, site := range timeoutSites(cli) {
		t.Run(site.name, func(t *testing.T) {
			setRunAsStateForTest(t, testRunAsSettings(t), "")
			logDir := mockSudoBackground(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			start := time.Now()
			site.run(ctx, t, t.TempDir(), logDir)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("a 1 second timeout returned after %s: the model outlived the context", d)
			}
			if pgids := sudoKillPgids(t, logDir); len(pgids) == 0 {
				t.Error("no sudo kill of a negative process group id was recorded")
			}
		})
	}
}

func TestRunAsUserOpenCodeServerStopKillsProcessGroup(t *testing.T) {
	stubRunAsGOOS(t)
	agentSiteEnv(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	logDir := mockSudoBackground(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if files, _ := filepath.Glob(filepath.Join(logDir, "argv.*")); len(files) == 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	b := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: srv.URL, AutoStartServer: true, ServerCommand: "opencode serve"})
	if err := b.startServer(context.Background()); err != nil {
		t.Fatal(err)
	}
	cmd := b.serverCmd
	if err := b.StopServer(); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	_ = cmd.Wait()

	pgids := sudoKillPgids(t, logDir)
	if len(pgids) != 1 || -pgids[0] != cmd.Process.Pid {
		t.Fatalf("recorded kills %v, want one of the group -%d", pgids, cmd.Process.Pid)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pgids[0], 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the process group of the server is still alive")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRunAsUserOptionEmptyConstructorSetsNothing(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	cmd, err := agentCommand(ctx, "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr != nil || cmd.WaitDelay != 0 || cmd.Path == "sudo" {
		t.Errorf("the empty option changed the command: SysProcAttr=%v WaitDelay=%v path=%s", cmd.SysProcAttr, cmd.WaitDelay, cmd.Path)
	}
	// The Cancel is the one of exec.CommandContext: it kills the process.
	start := time.Now()
	if err := cmd.Run(); err == nil {
		t.Error("the command was not stopped by the context")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the default cancel did not stop the command")
	}
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("the empty option ran sudo: %q", got)
	}
}

func TestRunAsUserSettingsSnapshotSurvivesReload(t *testing.T) {
	stubRunAsGOOS(t)
	first := testRunAsSettings(t)
	setRunAsStateForTest(t, first, "")
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	// A reload between the two calls: other home, then the option empty.
	for _, next := range []*runAsSettings{testRunAsSettings(t), nil} {
		runAsMu.Lock()
		runAsCur = runAsState{settings: next, checked: next != nil}
		runAsMu.Unlock()
	}
	setAgentEnv(cmd, []string{"PATH=/bin", "HOME=/home/daemon", "GITHUB_TOKEN=x"})
	var flag string
	for _, a := range cmd.Args {
		if v, ok := strings.CutPrefix(a, "--preserve-env="); ok {
			flag = v
		}
	}
	if flag != strings.Join(agentEnvNames(cmd.Env), ",") {
		t.Errorf("--preserve-env=%s does not equal the names of %v", flag, cmd.Env)
	}
	if !slices.Contains(cmd.Env, "HOME="+first.home) {
		t.Errorf("env %v does not carry the home the command was built with (%s)", cmd.Env, first.home)
	}
}

func TestRunAsUserKillFailureLevel(t *testing.T) {
	stubRunAsGOOS(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	mockSudo(t, 1)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Process = &os.Process{Pid: startLiveProcess(t)}
	kerr := killProcessGroup(cmd, syscall.SIGKILL)
	if kerr == nil {
		t.Fatal("a failed sudo kill returned nil")
	}
	if got := killFailureLevel(kerr); got != slog.LevelWarn {
		t.Errorf("a caller logs a failed sudo kill at %v, want Warn (the kill logged Error)", got)
	}
	if got := killFailureLevel(errors.New("other")); got != slog.LevelError {
		t.Errorf("a caller logs another kill failure at %v, want Error", got)
	}
}

func TestRunAsUserValidName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"pilot-agent", true}, {"_svc", true}, {"a", true}, {"agent_2", true},
		{strings.Repeat("a", 32), true}, {strings.Repeat("a", 33), false},
		{"", false}, {"-u root", false}, {"-agent", false}, {"a b", false}, {"#0", false},
		{"1agent", false}, {"Agent", false}, {"a.b", false}, {"a$b", false}, {"a,b", false},
	}
	for _, tc := range tests {
		if got := validRunAsName(tc.name); got != tc.want {
			t.Errorf("validRunAsName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunAsUserPreflightRefusals(t *testing.T) {
	home := t.TempDir()
	type stub struct{ uid0, gid0, uidErr, groupsErr bool }
	tests := []struct {
		name      string
		user      string
		group     string
		backend   string
		daemonGID []int
		stub      stub
		wantCheck string
	}{
		{"user root", "root", "pilot-agents", "", nil, stub{}, "run_as_user name is not valid"},
		{"user -u root", "-u root", "pilot-agents", "", nil, stub{}, "run_as_user name is not valid"},
		{"user with a space", "a b", "pilot-agents", "", nil, stub{}, "run_as_user name is not valid"},
		{"user #0", "#0", "pilot-agents", "", nil, stub{}, "run_as_user name is not valid"},
		{"user of 33 characters", strings.Repeat("a", 33), "pilot-agents", "", nil, stub{}, "run_as_user name is not valid"},
		{"group -u root", "pilot-agent", "-u root", "", nil, stub{}, "run_as_group name is not valid"},
		{"group #0", "pilot-agent", "#0", "", nil, stub{}, "run_as_group name is not valid"},
		{"group of 33 characters", "pilot-agent", strings.Repeat("a", 33), "", nil, stub{}, "run_as_group name is not valid"},
		{"group empty", "pilot-agent", "", "", nil, stub{}, "run_as_group is required with run_as_user"},
		{"anthropic-api backend", "pilot-agent", "pilot-agents", BackendTypeAnthropicAPI, nil, stub{}, "run_as_user is not supported with the anthropic-api backend"},
		{"openai-api backend", "pilot-agent", "pilot-agents", BackendTypeOpenAIAPI, nil, stub{}, "run_as_user is not supported with the openai-api backend"},
		{"missing group", "pilot-agent", "no-such-group", "", nil, stub{}, "group exists"},
		{"group without the daemon user", "pilot-agent", "pilot-agents", "", []int{1000}, stub{}, "daemon user is a member of the group"},
		{"daemon groups unreadable", "pilot-agent", "pilot-agents", "", nil, stub{groupsErr: true}, "daemon user is a member of the group"},
		{"user with uid 0", "pilot-agent", "pilot-agents", "", nil, stub{uid0: true}, "run_as_user has uid 0"},
		{"group with gid 0", "pilot-agent", "pilot-agents", "", nil, stub{gid0: true}, "run_as_group has gid 0"},
		{"daemon uid unknown", "pilot-agent", "pilot-agents", "", nil, stub{uidErr: true}, "user differs from the daemon user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			logPath := mockSudo(t, 0)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": home})
			if tc.daemonGID != nil {
				daemonGroups = func() ([]int, error) { return tc.daemonGID, nil }
			}
			if tc.stub.groupsErr {
				daemonGroups = func() ([]int, error) { return nil, errors.New("no groups") }
			}
			if tc.stub.uid0 {
				lookupUser = func(string) (*user.User, error) { return &user.User{Uid: "0", HomeDir: home}, nil }
			}
			if tc.stub.gid0 {
				lookupGrp = func(string) (*user.Group, error) { return &user.Group{Gid: "0"}, nil }
			}
			if tc.stub.uidErr {
				currentUID = func() (string, error) { return "", errors.New("no uid") }
			}
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: tc.user, RunAsGroup: tc.group}, tc.backend)

			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), tc.wantCheck) {
				t.Errorf("want one Error line naming %q, got:\n%s", tc.wantCheck, buf.String())
			}
			cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
			if err == nil || cmd != nil {
				t.Fatalf("spawn after a failed preflight: cmd=%v err=%v", cmd, err)
			}
			if want := "run_as_user preflight failed: " + tc.wantCheck; err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("sudo ran after a refused preflight: %q", got)
			}
		})
	}
}

func TestRunAsUserPreflightCacheKeysOnBackend(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	cfg := &ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}
	PreflightRunAsUser(cfg, "")
	if err := RunAsUserPreflightError(); err != nil {
		t.Fatalf("claude-code backend refused: %v", err)
	}
	PreflightRunAsUser(cfg, BackendTypeAnthropicAPI)
	if err := RunAsUserPreflightError(); err == nil {
		t.Error("a reload to the anthropic-api backend kept the passing result")
	}
}

func TestRunAsUserExecBashFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		s      *runAsSettings
		failed string
		runs   bool
	}{
		{"option empty", nil, "", true},
		{"option set", &runAsSettings{user: "pilot-agent", gid: -1}, "", false},
		{"option set, preflight failed", &runAsSettings{user: "pilot-agent", gid: -1}, "run_as_user is not supported with the anthropic-api backend", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, tc.s, tc.failed)
			dir := t.TempDir()
			out := execBash("touch marker; echo done", 5, dir)
			_, statErr := os.Stat(filepath.Join(dir, "marker"))
			if tc.runs {
				if statErr != nil || strings.TrimSpace(out) != "done" {
					t.Errorf("the empty option changed execBash: out=%q stat=%v", out, statErr)
				}
				return
			}
			if statErr == nil {
				t.Error("execBash ran a command with run_as_user set")
			}
			if !strings.Contains(out, "run_as_user is not supported") {
				t.Errorf("execBash result = %q, want the run_as_user error", out)
			}
		})
	}
}

// A guard file whose group cannot be changed stops the spawn: the agent user
// could not read it, so the model would run without the guard.
func TestRunAsUserGuardFilesFailClosed(t *testing.T) {
	stubRunAsGOOS(t)
	tests := []struct {
		name     string
		failPart string // part of the path whose chown fails
	}{
		{"guard directory", "pilot-runguard-"},
		{"settings file", "settings.json"},
		{"gh shim directory", "pilot-ghguard-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agentSiteEnv(t)
			s := testRunAsSettings(t)
			s.gid = 6000
			setRunAsStateForTest(t, s, "")
			logPath := mockSudo(t, 0)
			var failedPath string
			prev := guardChown
			guardChown = func(path string, uid, gid int) error {
				if strings.Contains(path, tc.failPart) && failedPath == "" {
					failedPath = path
					return syscall.EPERM
				}
				return nil
			}
			t.Cleanup(func() { guardChown = prev })

			b := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: "claude-model-cli"})
			b.SetGhGuard(true, "")
			_, err := b.Execute(context.Background(), ExecuteOptions{ProjectPath: t.TempDir(), Prompt: "p"})
			if failedPath == "" {
				t.Fatal("no guard file went through the change of group")
			}
			if err == nil {
				t.Fatal("a failed change of group of a guard file did not stop the spawn")
			}
			if want := "run_as_user: guard files not readable by the agent: " + failedPath; !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to contain %q", err, want)
			}
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("a process started after the guard file error: %q", got)
			}
		})
	}
}
