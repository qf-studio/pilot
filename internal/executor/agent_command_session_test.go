package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TASK-651 leg F2: the sudo of the option runs without a terminal, so the
// kill of its group reaches the agent; a failed kill of a live process is an
// error; the API backends are refused as a whole; a blank value fails; the gh
// guard journal is writable for the agent.

// captureAgentCommands records every command agentCommand and the preflight
// probes build, as the spawn sites leave them.
func captureAgentCommands(t *testing.T) func() []*exec.Cmd {
	t.Helper()
	var mu sync.Mutex
	var cmds []*exec.Cmd
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		c := prev(ctx, name, args...)
		mu.Lock()
		cmds = append(cmds, c)
		mu.Unlock()
		return c
	}
	t.Cleanup(func() { execCommandContext = prev })
	return func() []*exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		return append([]*exec.Cmd(nil), cmds...)
	}
}

// startLiveProcess starts `sleep 30` and returns its pid; it is killed at the
// end of the test.
func startLiveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// cancelName is the name of the function behind a Cancel.
func cancelName(f func() error) string {
	if f == nil {
		return ""
	}
	return runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
}

func sessionAttrs(cmd *exec.Cmd) (setsid, setpgid bool) {
	if cmd.SysProcAttr == nil {
		return false, false
	}
	return cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setpgid
}

func TestAgentCommandRunAsUserStartsSudoInNewSession(t *testing.T) {
	stubRunAsGOOS(t)
	tests := []struct {
		name      string
		set       bool
		setsid    bool
		setpgid   bool
		stdin     bool
		waitDelay time.Duration
	}{
		{"option set", true, true, false, true, runAsWaitDelay},
		{"option empty", false, false, false, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s *runAsSettings
			if tc.set {
				s = testRunAsSettings(t)
			}
			setRunAsStateForTest(t, s, "")
			cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
			if err != nil {
				t.Fatal(err)
			}
			setsid, setpgid := sessionAttrs(cmd)
			if !tc.set && cmd.SysProcAttr != nil {
				t.Errorf("the empty option set SysProcAttr %+v", cmd.SysProcAttr)
			}
			if setsid != tc.setsid || setpgid != tc.setpgid {
				t.Errorf("Setsid=%v Setpgid=%v, want Setsid=%v Setpgid=%v", setsid, setpgid, tc.setsid, tc.setpgid)
			}
			if (cmd.Stdin != nil) != tc.stdin {
				t.Errorf("Stdin = %v, want set: %v", cmd.Stdin, tc.stdin)
			}
			if f, ok := cmd.Stdin.(*os.File); tc.stdin && (!ok || f.Name() != os.DevNull) {
				t.Errorf("Stdin = %v, want %s", cmd.Stdin, os.DevNull)
			}
			if cmd.WaitDelay != tc.waitDelay {
				t.Errorf("WaitDelay = %v, want %v", cmd.WaitDelay, tc.waitDelay)
			}
		})
	}
}

// Real counterpart on Linux: the sudo process leads its own session and
// group, has no terminal and reads the null device.
func TestAgentCommandRunAsUserSudoProcessHasOwnSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "proc")
	body := fmt.Sprintf("#!/bin/sh\necho $$ > '%[1]s'\ncat /proc/$$/stat >> '%[1]s'\nreadlink /proc/$$/fd/0 >> '%[1]s'\n", out)
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("mock output %q", raw)
	}
	stat := lines[1]
	fields := strings.Fields(stat[strings.LastIndex(stat, ")")+2:])
	// state ppid pgrp session tty_nr
	if pid := lines[0]; fields[2] != pid || fields[3] != pid {
		t.Errorf("pid %s, pgrp %s, session %s: want all equal", pid, fields[2], fields[3])
	}
	if fields[4] != "0" {
		t.Errorf("tty_nr = %s, want 0 (no controlling terminal)", fields[4])
	}
	if lines[2] != os.DevNull {
		t.Errorf("stdin = %s, want %s", lines[2], os.DevNull)
	}
}

// The three sites that call configureProcessGroup after agentCommand.
var regroupSites = map[string]bool{"main backend": true, "qwen backend": true, "research subagent": true}

func TestRunAsUserSpawnSitesKeepSession(t *testing.T) {
	stubRunAsGOOS(t)
	cli := agentSiteEnv(t)
	for _, site := range agentSites(cli) {
		t.Run(site.name, func(t *testing.T) {
			setRunAsStateForTest(t, testRunAsSettings(t), "")
			logPath := mockSudo(t, 0)
			cmds := captureAgentCommands(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			site.run(ctx, t, t.TempDir(), logPath)
			got := cmds()
			if len(got) == 0 {
				t.Fatal("no command went through agentCommand")
			}
			for _, cmd := range got {
				if setsid, setpgid := sessionAttrs(cmd); !setsid || setpgid {
					t.Errorf("Setsid=%v Setpgid=%v, want a new session without Setpgid (%+v)", setsid, setpgid, cmd.SysProcAttr)
				}
				if cmd.Stdin == nil {
					t.Error("no standard input")
				}
				if cmd.WaitDelay != runAsWaitDelay {
					t.Errorf("WaitDelay = %v, want %v", cmd.WaitDelay, runAsWaitDelay)
				}
				if cmd.Cancel == nil {
					t.Error("no Cancel")
				}
			}
		})
	}
}

// With the option empty every site builds the command it built before.
func TestRunAsUserOptionEmptySpawnSitesUnchanged(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	cli := agentSiteEnv(t)
	defaultCancel := cancelName(exec.CommandContext(context.Background(), "x").Cancel)
	for _, site := range agentSites(cli) {
		t.Run(site.name, func(t *testing.T) {
			// The opencode health check is up once the fake binary ran.
			up := plantFakeBinaries(t, cli)
			cmds := captureAgentCommands(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			site.run(ctx, t, t.TempDir(), up)
			got := cmds()
			if len(got) == 0 {
				t.Fatal("no command went through agentCommand")
			}
			for _, cmd := range got {
				if cmd.Path == "sudo" || filepath.Base(cmd.Path) == "sudo" {
					t.Errorf("the empty option ran sudo: %q", cmd.Args)
				}
				name := cancelName(cmd.Cancel)
				if regroupSites[site.name] {
					if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Setsid {
						t.Errorf("SysProcAttr = %+v, want Setpgid only", cmd.SysProcAttr)
					}
					if cmd.WaitDelay != 5*time.Second {
						t.Errorf("WaitDelay = %v, want 5s", cmd.WaitDelay)
					}
					if name == defaultCancel || !strings.HasPrefix(name, "github.com/qf-studio/pilot/internal/executor.") {
						t.Errorf("Cancel = %s, want the group kill of the site", name)
					}
				} else {
					if cmd.SysProcAttr != nil || cmd.WaitDelay != 0 {
						t.Errorf("SysProcAttr = %+v WaitDelay = %v, want none", cmd.SysProcAttr, cmd.WaitDelay)
					}
					if name != defaultCancel {
						t.Errorf("Cancel = %s, want the one of exec.CommandContext (%s)", name, defaultCancel)
					}
				}
				_, isReader := cmd.Stdin.(*strings.Reader)
				if site.name == "intent judge" {
					if !isReader {
						t.Errorf("Stdin = %T, want the prompt reader", cmd.Stdin)
					}
				} else if cmd.Stdin != nil {
					t.Errorf("Stdin = %v, want nil", cmd.Stdin)
				}
			}
		})
	}
}

func TestRunAsUserPreflightTerminalProbe(t *testing.T) {
	tests := []struct {
		name      string
		tty       string
		wantCheck string // empty: passes
	}{
		{"no terminal", "?", ""},
		{"no terminal, BSD form", "??", ""},
		{"pts terminal", "pts/3", runAsTTYCheck},
		{"tty terminal", "tty1", runAsTTYCheck},
		{"no output", "", runAsTTYCheck},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			logPath := mockSudo(t, 0)
			t.Setenv("MOCK_SUDO_TTY", tc.tty)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			cmds := captureAgentCommands(t)
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}, "")

			if got := sudoArgv(t, logPath+".tty"); got == nil {
				t.Fatal("the terminal probe did not run")
			}
			// Both probes run detached like a spawn.
			probes := cmds()
			if len(probes) != 2 {
				t.Fatalf("%d probe commands, want 2", len(probes))
			}
			for _, p := range probes {
				if setsid, setpgid := sessionAttrs(p); !setsid || setpgid || p.Stdin == nil {
					t.Errorf("probe %q: Setsid=%v Setpgid=%v Stdin=%v, want the attributes of a spawn", p.Args, setsid, setpgid, p.Stdin)
				}
			}
			err := RunAsUserPreflightError()
			if tc.wantCheck == "" {
				if err != nil || strings.Contains(buf.String(), "level=ERROR") {
					t.Errorf("preflight failed for %q: %v\n%s", tc.tty, err, buf.String())
				}
				return
			}
			if want := "run_as_user preflight failed: " + tc.wantCheck; err == nil || err.Error() != want {
				t.Errorf("error = %v, want %q", err, want)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), "the kill would not reach it") {
				t.Errorf("want one Error line naming the terminal check, got:\n%s", buf.String())
			}
		})
	}
}

func TestRunAsUserPreflightBlankValue(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		group     string
		wantCheck string
	}{
		{"blank user", "   ", "pilot-agents", "run_as_user is blank"},
		{"tab user", "\t", "", "run_as_user is blank"},
		{"blank group", "pilot-agent", " \t ", "run_as_group is blank"},
		{"blank group, user empty", "", "  ", "run_as_group is blank"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			logPath := mockSudo(t, 0)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: tc.user, RunAsGroup: tc.group}, "")

			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), tc.wantCheck) {
				t.Errorf("want one Error line naming %q, got:\n%s", tc.wantCheck, buf.String())
			}
			cmd, err := agentCommand(context.Background(), "claude")
			if want := "run_as_user preflight failed: " + tc.wantCheck; err == nil || cmd != nil || err.Error() != want {
				t.Errorf("spawn: cmd=%v err=%v, want no command and %q", cmd, err, want)
			}
			if got := sudoArgv(t, logPath); got != nil {
				t.Errorf("sudo ran for a blank value: %q", got)
			}
		})
	}
}

func TestRunAsUserKillFailureWaitsForTheProcess(t *testing.T) {
	tests := []struct {
		name    string
		alive   bool
		wantErr bool
	}{
		{"process still alive", true, true},
		{"process gone", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stubRunAsGOOS(t)
			setRunAsStateForTest(t, testRunAsSettings(t), "")
			mockSudo(t, 1)
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			var pid int
			if tc.alive {
				pid = startLiveProcess(t)
			} else {
				gone := exec.Command("true")
				if err := gone.Run(); err != nil {
					t.Fatal(err)
				}
				pid = gone.Process.Pid
			}
			cmd, err := agentCommand(context.Background(), "claude")
			if err != nil {
				t.Fatal(err)
			}
			cmd.Process = &os.Process{Pid: pid}

			start := time.Now()
			kerr := killAgentProcessGroup(cmd, "pilot-agent", syscall.SIGKILL)
			errLines := strings.Count(buf.String(), "level=ERROR")
			if !tc.wantErr {
				if kerr != nil || errLines != 0 {
					t.Errorf("a failed kill of a gone process: err=%v, Error lines %d, want nil and none", kerr, errLines)
				}
				return
			}
			if kerr == nil {
				t.Fatal("a failed kill of a live process returned nil")
			}
			if d := time.Since(start); d < runAsKillGrace {
				t.Errorf("returned after %s, want the %s wait for the process", d, runAsKillGrace)
			}
			if want := fmt.Sprintf("pid=%d", pid); errLines != 1 || !strings.Contains(buf.String(), want) {
				t.Errorf("want one Error line with %s, got:\n%s", want, buf.String())
			}
		})
	}
}

// A kill without a context (the watchdog paths, StopServer) does not report a
// stop that did not happen.
func TestRunAsUserOpenCodeStopServerReportsFailedKill(t *testing.T) {
	stubRunAsGOOS(t)
	setRunAsStateForTest(t, testRunAsSettings(t), "")
	mockSudo(t, 1)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	cmd, err := agentCommand(context.Background(), "opencode", "serve")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Process = &os.Process{Pid: startLiveProcess(t)}
	b := NewOpenCodeBackend(&OpenCodeConfig{})
	b.serverCmd = cmd
	if err := b.StopServer(); err == nil {
		t.Error("StopServer reported a stop while the server is alive")
	}
}

// openAISSEStop is a minimal OpenAI stream that ends cleanly.
func openAISSEStop() string {
	return sseChunk(map[string]interface{}{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
		{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "done"}, "finish_reason": "stop"},
	}}) + sseDone()
}

func TestRunAsUserAPIBackendExecute(t *testing.T) {
	type backendCase struct {
		typ    string
		stream string
		build  func(t *testing.T, url string) Backend
	}
	backends := []backendCase{
		{BackendTypeAnthropicAPI, anthropicSSEStop(), func(t *testing.T, url string) Backend { return newTestAnthropicBackend(t, url) }},
		{BackendTypeOpenAIAPI, openAISSEStop(), func(t *testing.T, url string) Backend { return newTestOpenAIBackend(t, url) }},
	}
	for _, bc := range backends {
		for _, set := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s set=%v", bc.typ, set), func(t *testing.T) {
				var s *runAsSettings
				if set {
					s = testRunAsSettings(t)
				}
				setRunAsStateForTest(t, s, "")
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, bc.stream)
				}))
				defer srv.Close()

				_, err := bc.build(t, srv.URL).Execute(context.Background(), ExecuteOptions{Prompt: "p", ProjectPath: t.TempDir()})
				if !set {
					if err != nil || requests.Load() == 0 {
						t.Errorf("the empty option changed Execute: err=%v requests=%d", err, requests.Load())
					}
					return
				}
				if want := "run_as_user is not supported with the " + bc.typ + " backend"; err == nil || err.Error() != want {
					t.Errorf("err = %v, want %q", err, want)
				}
				if n := requests.Load(); n != 0 {
					t.Errorf("%d API requests with run_as_user set, want none", n)
				}
			})
		}
	}
}

func TestRunAsUserAPIBackendExecuteTool(t *testing.T) {
	const content = "line-of-the-file\n"
	tools := []struct {
		name  string
		input func(dir string) map[string]interface{}
		// ran reports whether the tool did its work, from its result and dir.
		ran func(t *testing.T, dir, result string) bool
	}{
		{"bash", func(dir string) map[string]interface{} {
			return map[string]interface{}{"command": "touch marker"}
		}, func(t *testing.T, dir, _ string) bool {
			_, err := os.Stat(filepath.Join(dir, "marker"))
			return err == nil
		}},
		{"read_file", func(dir string) map[string]interface{} {
			return map[string]interface{}{"path": filepath.Join(dir, "f.txt")}
		}, func(t *testing.T, _, result string) bool {
			return strings.Contains(result, "line-of-the-file")
		}},
		{"write_file", func(dir string) map[string]interface{} {
			return map[string]interface{}{"path": filepath.Join(dir, "new.txt"), "content": "x"}
		}, func(t *testing.T, dir, _ string) bool {
			_, err := os.Stat(filepath.Join(dir, "new.txt"))
			return err == nil
		}},
		{"edit_file", func(dir string) map[string]interface{} {
			return map[string]interface{}{"path": filepath.Join(dir, "f.txt"), "old_string": "line-of", "new_string": "edited"}
		}, func(t *testing.T, dir, _ string) bool {
			raw, err := os.ReadFile(filepath.Join(dir, "f.txt"))
			return err == nil && string(raw) != content
		}},
	}
	for _, typ := range []string{BackendTypeAnthropicAPI, BackendTypeOpenAIAPI} {
		for _, tool := range tools {
			for _, set := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s %s set=%v", typ, tool.name, set), func(t *testing.T) {
					var s *runAsSettings
					if set {
						s = testRunAsSettings(t)
					}
					setRunAsStateForTest(t, s, "")
					dir := t.TempDir()
					if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}

					result := executeTool(typ, tool.name, tool.input(dir), dir)
					ran := tool.ran(t, dir, result)
					if !set {
						if !ran {
							t.Errorf("the empty option stopped %s: %q", tool.name, result)
						}
						return
					}
					if ran {
						t.Errorf("%s ran as the daemon user with run_as_user set: %q", tool.name, result)
					}
					if !strings.Contains(result, "run_as_user is not supported") {
						t.Errorf("result = %q, want the run_as_user error", result)
					}
				})
			}
		}
	}
}

func TestRunAsUserNewBackendRefusesAPIBackends(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		user    string
		state   bool // the preflight ran for a run_as_user
		wantErr bool
	}{
		{"anthropic-api, option in config", BackendTypeAnthropicAPI, "pilot-agent", false, true},
		{"openai-api, option in config", BackendTypeOpenAIAPI, "pilot-agent", false, true},
		{"anthropic-api, blank option", BackendTypeAnthropicAPI, "  ", false, true},
		{"openai-api, option from the preflight", BackendTypeOpenAIAPI, "", true, true},
		{"anthropic-api, option empty", BackendTypeAnthropicAPI, "", false, false},
		{"openai-api, option empty", BackendTypeOpenAIAPI, "", false, false},
		{"claude-code, option set", BackendTypeClaudeCode, "pilot-agent", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s *runAsSettings
			if tc.state {
				s = testRunAsSettings(t)
			}
			setRunAsStateForTest(t, s, "")
			cfg := DefaultBackendConfig()
			cfg.Type = tc.typ
			cfg.ClaudeCode.RunAsUser = tc.user
			b, err := NewBackend(cfg)
			if !tc.wantErr {
				if err != nil || b == nil {
					t.Errorf("NewBackend: %v", err)
				}
				return
			}
			if want := "run_as_user is not supported with the " + tc.typ + " backend"; err == nil || err.Error() != want || b != nil {
				t.Errorf("NewBackend = %v, %v; want no backend and %q", b, err, want)
			}
		})
	}
}

func TestRunAsUserGhGuardJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	tests := []struct {
		name    string
		set     bool
		dirMode os.FileMode
	}{
		{"option set", true, 0o750},
		{"option empty", false, 0o700},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s *runAsSettings
			if tc.set {
				s = testRunAsSettings(t)
				// A group the daemon user is a member of, other than the primary one.
				if groups, err := os.Getgroups(); err == nil {
					for _, g := range groups {
						if g != os.Getegid() {
							s.gid = g
							break
						}
					}
				}
			}
			setRunAsStateForTest(t, s, "")
			shimDir, journal, cleanup, err := setupGhGuardShim("")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()

			di, err := os.Stat(shimDir)
			if err != nil {
				t.Fatal(err)
			}
			if di.Mode().Perm() != tc.dirMode {
				t.Errorf("shim dir mode = %o, want %o", di.Mode().Perm(), tc.dirMode)
			}
			if st, ok := di.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
				t.Errorf("shim dir owner = %d, want the daemon user %d", st.Uid, os.Geteuid())
			}
			fi, err := os.Stat(journal)
			if !tc.set {
				if err == nil {
					t.Error("the empty option created the journal")
				}
				return
			}
			if err != nil {
				t.Fatalf("journal not created: %v", err)
			}
			if fi.Size() != 0 || fi.Mode().Perm() != 0o660 {
				t.Errorf("journal size %d mode %o, want empty and 660", fi.Size(), fi.Mode().Perm())
			}
			if s.gid >= 0 {
				if gid := fileGID(t, journal); gid != s.gid {
					t.Errorf("journal gid = %d, want %d", gid, s.gid)
				}
			}
		})
	}
}
