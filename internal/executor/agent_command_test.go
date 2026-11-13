package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
)

// TASK-651 leg U1: run_as_user. None of these tests needs root: sudo is a mock
// script on PATH and the user lookups are stubbed.

// setRunAsStateForTest installs the run_as_user state for one test and puts
// the empty option back afterwards.
func setRunAsStateForTest(t *testing.T, s *runAsSettings, failed string) {
	t.Helper()
	runAsMu.Lock()
	prev := runAsCur
	runAsCur = runAsState{settings: s, failed: failed, checked: s != nil}
	runAsMu.Unlock()
	t.Cleanup(func() {
		runAsMu.Lock()
		runAsCur = prev
		runAsMu.Unlock()
	})
}

// stubRunAsGOOS makes the platform Linux for one test: agentCommand builds the
// sudo form on Linux only, and these tests check that form on any host.
func stubRunAsGOOS(t *testing.T) {
	t.Helper()
	prev := runAsGOOS
	runAsGOOS = "linux"
	t.Cleanup(func() { runAsGOOS = prev })
}

func testRunAsSettings(t *testing.T) *runAsSettings {
	t.Helper()
	return &runAsSettings{user: "pilot-agent", home: t.TempDir(), gid: -1}
}

// mockSudo puts a fake sudo first on PATH. It records its own name and its
// arguments, NUL separated, in the returned file and exits with exitCode. The
// terminal probe of the preflight (sh -c 'ps -o tty= -p $$') is recorded in
// the file plus ".tty" instead and prints $MOCK_SUDO_TTY, "?" when unset.
func mockSudo(t *testing.T, exitCode int) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock sudo is a shell script")
	}
	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "sudo-argv")
	body := fmt.Sprintf(`#!/bin/sh
case "$*" in
*"ps -o tty= -p"*)
  printf '%%s\0' "${0##*/}" "$@" > '%[1]s.tty'
  printf '%%s\n' "${MOCK_SUDO_TTY-?}"
  ;;
*)
  printf '%%s\0' "${0##*/}" "$@" > '%[1]s'
  ;;
esac
exit %[2]d
`, logPath, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// sudoArgv returns what the mock sudo recorded (its name first), nil when it
// never ran. The log is removed so the next call sees only a new run.
func sudoArgv(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	_ = os.Remove(logPath)
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

// assertSudoSpawn checks the recorded argv: sudo first, the binary right
// after the "--".
func assertSudoSpawn(t *testing.T, argv []string, user, binary string) {
	t.Helper()
	if len(argv) == 0 {
		t.Fatal("sudo never ran: the spawn did not go through agentCommand")
	}
	if argv[0] != "sudo" {
		t.Fatalf("first argv element = %q, want sudo (argv %q)", argv[0], argv)
	}
	if len(argv) < 4 || argv[1] != "-n" || argv[2] != "-u" || argv[3] != user {
		t.Errorf("argv does not start with sudo -n -u %s: %q", user, argv)
	}
	dd := -1
	for i, a := range argv {
		if a == "--" {
			dd = i
			break
		}
	}
	if dd < 0 || dd+1 >= len(argv) || argv[dd+1] != binary {
		t.Errorf("model binary %q does not follow --: %q", binary, argv)
	}
}

func TestAgentCommandOptionEmptyIsPlainCommand(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	ctx := context.Background()
	got, err := agentCommand(ctx, "claude", "--print", "-p", "x")
	if err != nil {
		t.Fatal(err)
	}
	want := exec.CommandContext(ctx, "claude", "--print", "-p", "x")
	if got.Path != want.Path || strings.Join(got.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Errorf("empty option: got %v (%s), want %v (%s)", got.Args, got.Path, want.Args, want.Path)
	}

	env := []string{"A=1", "GITHUB_TOKEN=keep-me", "FOO_TOKEN=x", "A=2"}
	setAgentEnv(got, env)
	if strings.Join(got.Env, "\x00") != strings.Join(env, "\x00") {
		t.Errorf("empty option changed the env: %v", got.Env)
	}
}

func TestAgentCommandRunAsUserArgv(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the sudo form is Linux only")
	}
	tests := []struct {
		name  string
		group string
		want  []string
	}{
		{"user only", "", []string{"sudo", "-n", "-u", "pilot-agent"}},
		{"user and group", "pilot-agents", []string{"sudo", "-n", "-u", "pilot-agent", "-g", "pilot-agents"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testRunAsSettings(t)
			s.group = tc.group
			setRunAsStateForTest(t, s, "")

			cmd, err := agentCommand(context.Background(), "claude", "--print", "-p", "the prompt")
			if err != nil {
				t.Fatal(err)
			}
			secret := "s3cr3t-value-for-test"
			setAgentEnv(cmd, []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=" + secret, "FOO_TOKEN=" + secret})

			args := cmd.Args
			if len(args) < len(tc.want)+4 {
				t.Fatalf("argv too short: %q", args)
			}
			for i, w := range tc.want {
				if args[i] != w {
					t.Errorf("argv[%d] = %q, want %q (argv %q)", i, args[i], w, args)
				}
			}
			rest := args[len(tc.want):]
			if !strings.HasPrefix(rest[0], "--preserve-env=") {
				t.Errorf("no --preserve-env flag after the user/group: %q", rest)
			}
			if rest[1] != "--" || rest[2] != "claude" || strings.Join(rest[3:], " ") != "--print -p the prompt" {
				t.Errorf("tail of argv = %q, want -- claude --print -p the prompt", rest[1:])
			}
			for _, a := range args {
				if strings.Contains(a, secret) {
					t.Errorf("an environment value is in argv: %q", a)
				}
			}
		})
	}
}

func TestAgentCommandPreserveListEqualsEnvNames(t *testing.T) {
	stubRunAsGOOS(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	setAgentEnv(cmd, []string{
		"PATH=/bin", "ANTHROPIC_MODEL=a", "ANTHROPIC_MODEL=b", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=5",
		"HOME=/home/daemon", "GITHUB_TOKEN=x", "PILOT_EXECUTOR=1",
	})
	var flag string
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, "--preserve-env=") {
			flag = strings.TrimPrefix(a, "--preserve-env=")
		}
	}
	var names []string
	seen := map[string]bool{}
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		if seen[name] {
			t.Errorf("name %s twice in cmd.Env: %v", name, cmd.Env)
		}
		seen[name] = true
		names = append(names, name)
	}
	if flag != strings.Join(names, ",") {
		t.Errorf("--preserve-env=%s does not equal the env names %v", flag, names)
	}
	for _, kv := range cmd.Env {
		if kv == "ANTHROPIC_MODEL=a" {
			t.Errorf("the earlier duplicate won: %v", cmd.Env)
		}
	}
}

func TestRunAsUserEnvAllowlist(t *testing.T) {
	s := testRunAsSettings(t)
	in := []string{
		"PATH=/usr/bin", "LANG=C", "LC_ALL=C", "TERM=xterm", "TZ=UTC", "TMPDIR=/tmp",
		"GOCACHE=/c", "GOMODCACHE=/m", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOPROXY=off",
		"GONOSUMDB=x", "GOPRIVATE=y", "NODE_OPTIONS=--max-old-space-size=1", "PILOT_EXECUTOR=1",
		"ANTHROPIC_API_KEY=k", "ANTHROPIC_BASE_URL=u", "CLAUDE_CODE_OAUTH_TOKEN=o",
		"HOME=/home/daemon", "CLAUDE_CONFIG_DIR=/home/daemon/.claude",
		// dropped
		"GITHUB_TOKEN=g", "GH_TOKEN=g", "AWS_ACCESS_KEY_ID=a", "AWS_SECRET_ACCESS_KEY=a",
		"AWS_SESSION_TOKEN=a", "FOO_TOKEN=f", "SSH_AUTH_SOCK=/s", "XDG_RUNTIME_DIR=/r",
	}
	got := map[string]string{}
	for _, kv := range s.agentEnvFor(in) {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = value
	}
	for _, name := range []string{
		"PATH", "LANG", "LC_ALL", "TERM", "TZ", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN",
		"GOPROXY", "GONOSUMDB", "GOPRIVATE", "NODE_OPTIONS", "PILOT_EXECUTOR",
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
	} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s was dropped, the allowlist keeps it", name)
		}
	}
	for _, name := range []string{
		"GITHUB_TOKEN", "GH_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"FOO_TOKEN", "SSH_AUTH_SOCK", "XDG_RUNTIME_DIR",
	} {
		if _, ok := got[name]; ok {
			t.Errorf("%s reached the agent, the allowlist drops it", name)
		}
	}
	if got["HOME"] != s.home {
		t.Errorf("HOME = %q, want the agent home %q", got["HOME"], s.home)
	}
	if got["CLAUDE_CONFIG_DIR"] != filepath.Join(s.home, ".claude") {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want under the agent home", got["CLAUDE_CONFIG_DIR"])
	}
}

// With the option empty the deny-list result is what the CLI gets, byte for
// byte: setAgentEnv is a plain assignment.
func TestModelEnvRunAsUserEmptyKeepsDenyListResult(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	base := []string{"HOME=/home/daemon", "GITHUB_TOKEN=g", "TELEGRAM_BOT_TOKEN=t", "FOO=1", "ANTHROPIC_MODEL=m"}
	want := modelSubprocessEnvFor(context.Background(), base)
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	setAgentEnv(cmd, modelSubprocessEnvFor(context.Background(), base))
	if strings.Join(cmd.Env, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("env = %v, want %v", cmd.Env, want)
	}
}

func TestRunAsUserKillCommand(t *testing.T) {
	tests := []struct {
		sig  syscall.Signal
		want string
	}{
		{syscall.SIGKILL, "KILL"},
		{syscall.SIGTERM, "TERM"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			cmd := agentKillCommand(context.Background(), "pilot-agent", 4242, tc.sig)
			want := []string{"sudo", "-n", "-u", "pilot-agent", "kill", "-s", tc.want, "--", "-4242"}
			if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
				t.Errorf("kill argv = %q, want %q", cmd.Args, want)
			}
		})
	}
}

func TestRunAsUserKillProcessGroupGoesThroughSudo(t *testing.T) {
	stubRunAsGOOS(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	logPath := mockSudo(t, 0)
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Process = &os.Process{Pid: 4242}

	if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}
	got := sudoArgv(t, logPath)
	want := []string{"sudo", "-n", "-u", "pilot-agent", "kill", "-s", "TERM", "--", "-4242"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("kill ran %q, want %q", got, want)
	}
}

func TestRunAsUserKillFailureLogsOneErrorWithPgid(t *testing.T) {
	stubRunAsGOOS(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	mockSudo(t, 1)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	pid := startLiveProcess(t)
	cmd.Process = &os.Process{Pid: pid}
	if err := killProcessGroup(cmd, syscall.SIGKILL); err == nil {
		t.Fatal("a failed sudo kill returned nil")
	}
	var errLines []string
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.Contains(l, "level=ERROR") {
			errLines = append(errLines, l)
		}
	}
	if want := fmt.Sprintf("pgid=%d", pid); len(errLines) != 1 || !strings.Contains(errLines[0], want) {
		t.Errorf("want one Error line with %s, got %q", want, errLines)
	}
}

func TestRunAsUserKillOptionEmptyIsDirect(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 0)
	cmd := exec.Command("sleep", "30")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
		t.Fatalf("direct kill: %v", err)
	}
	_ = cmd.Wait()
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("the empty option killed through sudo: %q", got)
	}
}

// stubRunAsHost replaces the host lookups of the preflight. Every user it
// knows has uid 5000; the group "pilot-agents" has gid 6000 and the daemon is
// a member of it. Tests change lookupGrp and daemonGroups for the group cases.
func stubRunAsHost(t *testing.T, goos string, daemonUID string, users map[string]string) {
	t.Helper()
	pg, pu, pc, po, pd := lookupGrp, lookupUser, currentUID, runAsGOOS, daemonGroups
	runAsGOOS = goos
	currentUID = func() (string, error) { return daemonUID, nil }
	lookupUser = func(name string) (*user.User, error) {
		home, ok := users[name]
		if !ok {
			return nil, fmt.Errorf("unknown user %s", name)
		}
		return &user.User{Uid: "5000", HomeDir: home}, nil
	}
	lookupGrp = func(name string) (*user.Group, error) {
		if name != "pilot-agents" {
			return nil, fmt.Errorf("unknown group %s", name)
		}
		return &user.Group{Gid: "6000", Name: name}, nil
	}
	daemonGroups = func() ([]int, error) { return []int{1000, 6000}, nil }
	t.Cleanup(func() { lookupGrp, lookupUser, currentUID, runAsGOOS, daemonGroups = pg, pu, pc, po, pd })
}

func TestRunAsUserPreflightFailsClosed(t *testing.T) {
	home := t.TempDir()
	tests := []struct {
		name      string
		goos      string
		user      string
		daemonUID string
		sudoExit  int
		homeDir   string
		wantCheck string
	}{
		{"missing user", "linux", "nobody-such", "1000", 0, home, "user exists"},
		{"the daemon user", "linux", "pilot-agent", "5000", 0, home, "user differs from the daemon user"},
		{"sudo exits non-zero", "linux", "pilot-agent", "1000", 1, home, "sudo -n -u pilot-agent -g pilot-agents true"},
		{"home missing", "linux", "pilot-agent", "1000", 0, filepath.Join(home, "gone"), "home exists"},
		{"not linux", "darwin", "pilot-agent", "1000", 0, home, "linux only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			logPath := mockSudo(t, tc.sudoExit)
			stubRunAsHost(t, tc.goos, tc.daemonUID, map[string]string{"pilot-agent": tc.homeDir})
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: tc.user, RunAsGroup: "pilot-agents"}, "")
			_ = sudoArgv(t, logPath) // the preflight's own probe

			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), tc.wantCheck) {
				t.Errorf("want one Error line naming %q, got:\n%s", tc.wantCheck, buf.String())
			}
			cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
			if err == nil || cmd != nil {
				t.Fatalf("spawn after a failed preflight: cmd=%v err=%v, want an error and no command", cmd, err)
			}
			if want := "run_as_user preflight failed: " + tc.wantCheck; err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("a process started after a failed preflight: %q", got)
			}
		})
	}
}

func TestRunAsUserPreflightPasses(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 0)
	home := t.TempDir()
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": home})

	PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}, "")
	got := sudoArgv(t, logPath)
	if strings.Join(got, " ") != "sudo -n -u pilot-agent -g pilot-agents true" {
		t.Errorf("probe = %q", got)
	}
	got = sudoArgv(t, logPath+".tty")
	if strings.Join(got, "\x00") != strings.Join([]string{"sudo", "-n", "-u", "pilot-agent", "-g", "pilot-agents", "sh", "-c", "ps -o tty= -p $$"}, "\x00") {
		t.Errorf("terminal probe = %q", got)
	}
	cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
	if err != nil {
		t.Fatalf("spawn after a passing preflight: %v", err)
	}
	setAgentEnv(cmd, []string{"HOME=/home/daemon"})
	if cmd.Env[0] != "HOME="+home {
		t.Errorf("HOME = %v, want the home resolved by the preflight", cmd.Env)
	}
}

func TestRunAsUserPreflightOptionEmptyDoesNothing(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 1)
	PreflightRunAsUser(nil, "")
	PreflightRunAsUser(&ClaudeCodeConfig{}, "")
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("the empty option ran sudo: %q", got)
	}
	if err := runAsSpawnError(); err != nil {
		t.Errorf("the empty option set the flag: %v", err)
	}
}

func TestRunAsUserPreflightEmptyClearsEarlierFailure(t *testing.T) {
	setRunAsStateForTest(t, testRunAsSettings(t), "sudo -n -u pilot-agent true")
	PreflightRunAsUser(&ClaudeCodeConfig{}, "")
	if _, err := agentCommand(context.Background(), "claude"); err != nil {
		t.Errorf("reloading an empty option left the flag set: %v", err)
	}
}

func TestRunAsUserGuardFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	modeOf := func(t *testing.T, p string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	spec := &readGuardSpec{Home: t.TempDir(), ConfigPath: filepath.Join(t.TempDir(), "config.yaml")}

	t.Run("option empty", func(t *testing.T) {
		setRunAsStateForTest(t, nil, "")
		settings, cleanup, err := setupRunGuards(t.TempDir(), t.TempDir(), spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		dir := filepath.Dir(settings)
		if m := modeOf(t, dir); m != 0o700 {
			t.Errorf("guard dir mode = %o, want 700", m)
		}
		if m := modeOf(t, settings); m != 0o600 {
			t.Errorf("settings mode = %o, want 600", m)
		}
		shim, _, shimCleanup, err := setupGhGuardShim("")
		if err != nil {
			t.Fatal(err)
		}
		defer shimCleanup()
		if m := modeOf(t, shim); m != 0o700 {
			t.Errorf("gh shim dir mode = %o, want 700", m)
		}
	})

	t.Run("option set", func(t *testing.T) {
		s := testRunAsSettings(t)
		// A group the daemon user is a member of, other than the primary one.
		if groups, err := os.Getgroups(); err == nil {
			for _, g := range groups {
				if g != os.Getegid() {
					s.gid = g
					break
				}
			}
		}
		setRunAsStateForTest(t, s, "")
		settings, cleanup, err := setupRunGuards(t.TempDir(), t.TempDir(), spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		dir := filepath.Dir(settings)
		check := func(p string, want os.FileMode) {
			t.Helper()
			if m := modeOf(t, p); m != want {
				t.Errorf("%s mode = %o, want %o", filepath.Base(p), m, want)
			}
			if s.gid >= 0 {
				if gid := fileGID(t, p); gid != s.gid {
					t.Errorf("%s gid = %d, want %d", filepath.Base(p), gid, s.gid)
				}
			}
		}
		check(dir, 0o750)
		check(settings, 0o640)
		check(filepath.Join(dir, readGuardName), 0o750)
		check(filepath.Join(dir, writeBoundaryGuardName), 0o750)

		shim, _, shimCleanup, err := setupGhGuardShim("")
		if err != nil {
			t.Fatal(err)
		}
		defer shimCleanup()
		check(shim, 0o750)
		check(filepath.Join(shim, "gh"), 0o750)
	})
}

func fileGID(t *testing.T, p string) int {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no stat_t on this platform")
	}
	return int(st.Gid)
}

// The gh guard shim leads PATH, so the names the guard process reads must
// survive the allowlist: with the shim directory lost, the fallback search of
// ghguard.ResolveFallbackGh finds the shim's own gh and recurses.
func TestRunAsUserEnvKeepsGhGuardNames(t *testing.T) {
	s := testRunAsSettings(t)
	shimDir := t.TempDir()
	realDir := t.TempDir()
	for _, d := range []string{shimDir, realDir} {
		if err := os.WriteFile(filepath.Join(d, "gh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	journal := filepath.Join(shimDir, "journal.jsonl")
	env := prependPathEnv([]string{"PATH=" + realDir, "GITHUB_TOKEN=g"}, shimDir)
	env = append(env, ghGuardTaskEnv(ExecuteOptions{SourceIssueID: "42", SourceRepo: "o/r", Branch: "pilot/GH-42"},
		filepath.Join(realDir, "gh"), shimDir, journal)...)

	got := map[string]string{}
	for _, kv := range s.agentEnvFor(env) {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = value
	}
	if _, ok := got["GITHUB_TOKEN"]; ok {
		t.Error("GITHUB_TOKEN reached the agent")
	}
	getenv := func(k string) string { return got[k] }

	id := ghguard.IdentityFromEnv(getenv)
	if id.TaskIssue != "42" || id.TaskRepo != "o/r" || id.TaskBranch != "pilot/GH-42" || id.RealGh != filepath.Join(realDir, "gh") {
		t.Errorf("identity lost through the allowlist: %+v", id)
	}
	if getenv(ghguard.EnvJournalPath) != journal {
		t.Errorf("journal path = %q, want %q", getenv(ghguard.EnvJournalPath), journal)
	}
	if getenv(ghguard.EnvShimDir) != shimDir {
		t.Fatalf("shim dir = %q, want %q", getenv(ghguard.EnvShimDir), shimDir)
	}

	// Real counterpart: the fallback search the guard runs when RealGh is
	// empty must skip the shim directory that leads the agent's PATH.
	t.Setenv("PATH", got["PATH"])
	fallback, err := ghguard.ResolveFallbackGh(getenv(ghguard.EnvShimDir))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(realDir, "gh"); fallback != want {
		t.Errorf("fallback gh = %q, want %q (the shim must be skipped)", fallback, want)
	}
}
