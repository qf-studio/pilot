package executor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TASK-651 host layout: the data directory of the daemon, the agent root
// below it and the Go paths of the agent home. No test needs root: the
// directories are temporary and the modes are set by the test.

// mkdirMode makes dir with exactly mode (t.TempDir and os.Mkdir follow the
// umask).
func mkdirMode(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunAsDataDirModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	// layout fills the data directory and may change the settings to check.
	// The workspace root is <data>/agent/workspaces, the layout of the
	// setup script; the gid of run_as_group is -1 unless the case sets it.
	type layout func(t *testing.T, data string, s *runAsSettings)
	entry := func(name string, mode os.FileMode) layout {
		return func(t *testing.T, data string, _ *runAsSettings) { mkdirMode(t, filepath.Join(data, name), mode) }
	}
	tests := []struct {
		name     string
		dataMode os.FileMode
		layout   layout
		want     []string // substrings of the failed check; nil: passes
	}{
		{"data dir 0755", 0o755, nil, []string{"gives the group or other users read or write", "0755"}},
		{"data dir 0711", 0o711, nil, nil},
		{"data dir 0700", 0o700, nil, nil},
		{"entry 0755", 0o711, entry("logs", 0o755), []string{"logs", "open to other users", "0755"}},
		{"entry 0700", 0o711, entry("logs", 0o700), nil},
		{"entry 0701", 0o711, entry("logs", 0o701), []string{"logs", "0701"}},
		{"entry with group bits, the shared group", 0o711, func(t *testing.T, data string, s *runAsSettings) {
			p := mkdirMode(t, filepath.Join(data, "state"), 0o750)
			s.gid = fileGID(t, p)
		}, []string{"state", "open to run_as_group pilot-agents", "0750"}},
		{"entry with group bits, another group", 0o711, entry("state", 0o750), nil},
		{"a file entry 0644", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			if err := os.WriteFile(filepath.Join(data, "pilot.db"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(data, "pilot.db"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, []string{"pilot.db", "0644"}},
		{"the entry that holds the workspace root is skipped", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			mkdirMode(t, filepath.Join(data, "agent"), 0o755)
			mkdirMode(t, filepath.Join(data, "agent", "workspaces"), 0o770)
		}, nil},
		{"a link to an open directory", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			open := mkdirMode(t, filepath.Join(t.TempDir(), "open"), 0o755)
			if err := os.Symlink(open, filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, []string{"logs", "0755"}},
		{"a link to a closed directory", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			closed := mkdirMode(t, filepath.Join(t.TempDir(), "closed"), 0o700)
			if err := os.Symlink(closed, filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a dangling link reaches nothing", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			if err := os.Symlink(filepath.Join(data, "gone"), filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"the first entry that fails is named", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			mkdirMode(t, filepath.Join(data, "a-closed"), 0o700)
			mkdirMode(t, filepath.Join(data, "b-open"), 0o755)
			mkdirMode(t, filepath.Join(data, "c-open"), 0o777)
		}, []string{"b-open", "0755"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := mkdirMode(t, filepath.Join(t.TempDir(), "data"), 0o700)
			s := &runAsSettings{user: "pilot-agent", group: "pilot-agents", gid: -1, dataDir: data,
				workspaceRoot: filepath.Join(data, "agent", "workspaces")}
			if tc.layout != nil {
				tc.layout(t, data, s)
			}
			// Last: a data dir of 0755 would not change the entries.
			mkdirMode(t, data, tc.dataMode)
			got := checkRunAsDataDirModes(s)
			if tc.want == nil {
				if got != "" {
					t.Fatalf("check failed: %q, want a pass", got)
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("check = %q, want one containing %q", got, w)
				}
			}
		})
	}
}

func TestRunAsDataDirMissingFails(t *testing.T) {
	s := &runAsSettings{user: "pilot-agent", gid: -1, dataDir: filepath.Join(t.TempDir(), "gone")}
	if got := checkRunAsDataDirModes(s); !strings.Contains(got, "run_as_data_dir exists") {
		t.Errorf("check = %q, want the missing data directory named", got)
	}
}

// TestRunAsDataDirPreflight: the preflight runs the check after the workspace
// root check and refuses the option on a failure; a change of run_as_data_dir
// runs the preflight again.
func TestRunAsDataDirPreflight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	tests := []struct {
		name      string
		dataDir   func(t *testing.T) string
		wantCheck string // empty: passes
	}{
		{"closed data dir", func(t *testing.T) string { return closedTempDir(t) }, ""},
		{"open data dir", func(t *testing.T) string { return mkdirMode(t, filepath.Join(t.TempDir(), "d"), 0o755) }, "run_as_data_dir"},
		{"open entry", func(t *testing.T) string {
			d := closedTempDir(t)
			mkdirMode(t, filepath.Join(d, "logs"), 0o755)
			return d
		}, "logs is open to other users (mode 0755)"},
		{"missing data dir", func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone") }, "run_as_data_dir exists"},
		{"relative data dir", func(*testing.T) string { return "var/lib/pilot" }, "run_as_data_dir is not an absolute path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRunAsStateForTest(t, nil, "")
			_ = mockSudo(t, 0)
			stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
			var order []string
			runAsWorkspaceCheck = func(*runAsSettings) string { order = append(order, "workspace"); return "" }
			runAsDataDirCheck = func(s *runAsSettings) string { order = append(order, "data"); return checkRunAsDataDirModes(s) }
			buf := captureLog(t)
			data := tc.dataDir(t)
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
				RunAsWorkspaceDir: filepath.Join(t.TempDir(), "agent", "workspaces"), RunAsDataDir: data}, "", true, 0)
			err := RunAsUserPreflightError()
			if tc.wantCheck == "" {
				if err != nil {
					t.Fatalf("preflight failed: %v", err)
				}
				if strings.Join(order, ",") != "workspace,data" {
					t.Errorf("checks ran as %v, want the data directory after the workspace root", order)
				}
				// A daemon entry opened later and a reload to another data
				// directory: the preflight runs again and refuses.
				open := mkdirMode(t, filepath.Join(t.TempDir(), "open"), 0o755)
				PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
					RunAsWorkspaceDir: filepath.Join(t.TempDir(), "agent", "workspaces"), RunAsDataDir: open}, "", true, 0)
				if RunAsUserPreflightError() == nil {
					t.Error("a reload to an open run_as_data_dir kept the passing result")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantCheck) {
				t.Fatalf("preflight error = %v, want one naming %q", err, tc.wantCheck)
			}
			if n := strings.Count(buf.String(), "level=ERROR"); n != 1 {
				t.Errorf("want one Error line, got:\n%s", buf.String())
			}
			if cmd, err := agentCommand(context.Background(), "claude"); err == nil || cmd != nil {
				t.Errorf("spawn after a refused data directory: cmd=%v err=%v", cmd, err)
			}
		})
	}
}

// TestRunAsDataDirCheckedPerRun: the preflight result lives as long as the
// process, so an entry the daemon opens after it fails the next workspace
// creation, and no workspace is made.
func TestRunAsDataDirCheckedPerRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	e := newAgentWorkspaceEnv(t)
	s := runAsSettingsNow()
	m := NewWorktreeManager(e.repo)

	ws, cleanup, err := m.createAgentWorkspace(context.Background(), "GH-20", "pilot/GH-20", "HEAD")
	if err != nil {
		t.Fatalf("first run with a closed data directory: %v", err)
	}
	cleanup()
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Fatalf("workspace %s left after the cleanup: %v", ws, err)
	}

	// The daemon creates a log directory at run time with mode 0755.
	logs := mkdirMode(t, filepath.Join(s.dataDir, "logs"), 0o755)
	ws, cleanup, err = m.createAgentWorkspace(context.Background(), "GH-21", "pilot/GH-21", "HEAD")
	if err == nil {
		cleanup()
		t.Fatalf("a run started with %s open to the agent user (workspace %s)", logs, ws)
	}
	if !strings.Contains(err.Error(), logs) || !strings.Contains(err.Error(), "0755") {
		t.Errorf("error = %v, want the entry and its mode named", err)
	}
	if ws != "" || cleanup != nil {
		t.Errorf("a failed check returned workspace %q and a cleanup", ws)
	}
	if got := e.rootEntries(t); len(got) != 0 {
		t.Errorf("workspace root after the refused run holds %v, want nothing", got)
	}
}

// TestRunAsUserEnvGoPaths: with the option set the agent gets the Go paths
// below its home and never a value of the daemon for these names.
func TestRunAsUserEnvGoPaths(t *testing.T) {
	stubRunAsGOOS(t)
	s := testRunAsSettings(t)
	setRunAsStateForTest(t, s, "")
	daemon := []string{"PATH=/usr/bin", "GOCACHE=/home/daemon/.cache/go-build", "GOMODCACHE=/home/daemon/go/pkg/mod",
		"GOPATH=/home/daemon/go", "GOFLAGS=-mod=mod"}
	cmd, err := agentCommand(context.Background(), "claude", "-p", "x")
	if err != nil {
		t.Fatal(err)
	}
	// A caller that appends a daemon value after the scrub is no different.
	setAgentEnv(cmd, append(daemon, "GOCACHE=/home/daemon/late"))

	want := map[string]string{
		"GOPATH":     filepath.Join(s.home, "go"),
		"GOCACHE":    filepath.Join(s.home, "go", "cache"),
		"GOMODCACHE": filepath.Join(s.home, "go", "mod"),
	}
	count := map[string]int{}
	for _, kv := range cmd.Env {
		name, value, _ := strings.Cut(kv, "=")
		if strings.Contains(value, "/home/daemon") && name != "PATH" {
			t.Errorf("a daemon value reached the agent: %s", kv)
		}
		if w, ok := want[name]; ok {
			count[name]++
			if value != w {
				t.Errorf("%s = %q, want %q", name, value, w)
			}
		}
	}
	for name := range want {
		if count[name] != 1 {
			t.Errorf("%s appears %d times in %v, want once", name, count[name], cmd.Env)
		}
	}
	var flag string
	for _, a := range cmd.Args {
		if strings.HasPrefix(a, runAsPreserveFlag) {
			flag = strings.TrimPrefix(a, runAsPreserveFlag)
		}
	}
	for name := range want {
		if !strings.Contains(","+flag+",", ","+name+",") {
			t.Errorf("--preserve-env=%s lacks %s", flag, name)
		}
	}
}

// TestRunAsUserOptionEmptyKeepsLayout: with the option empty the environment
// is the daemon's, byte for byte, and neither directory option is read.
func TestRunAsUserOptionEmptyKeepsLayout(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	logPath := mockSudo(t, 0)
	prev := runAsDataDirCheck
	called := false
	runAsDataDirCheck = func(*runAsSettings) string { called = true; return "unexpected" }
	t.Cleanup(func() { runAsDataDirCheck = prev })

	PreflightRunAsUser(&ClaudeCodeConfig{RunAsDataDir: "/nowhere", RunAsWorkspaceDir: "/nowhere/ws"}, "", true, 0)
	if called || runAsSettingsNow() != nil || RunAsUserPreflightError() != nil {
		t.Errorf("the empty option ran the data directory check (%v) or set state (%v, %v)",
			called, runAsSettingsNow(), RunAsUserPreflightError())
	}
	if got := sudoArgv(t, logPath); got != nil {
		t.Errorf("the empty option ran sudo: %q", got)
	}
	env := []string{"HOME=/home/daemon", "GOCACHE=/home/daemon/.cache/go-build", "GOMODCACHE=/m", "GOPATH=/g"}
	cmd, err := agentCommand(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	setAgentEnv(cmd, env)
	if strings.Join(cmd.Env, "\x00") != strings.Join(env, "\x00") {
		t.Errorf("env = %v, want %v", cmd.Env, env)
	}
}

// TestRunAsUserLayoutDefaults pins the defaults to the layout of
// scripts/box/setup-agent-user.sh: the data directory /var/lib/pilot and the
// workspaces below its agent root.
func TestRunAsUserLayoutDefaults(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	_ = mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents"}, "", true, 0)
	s := runAsSettingsNow()
	if s == nil {
		t.Fatal("no settings")
	}
	if s.dataDir != "/var/lib/pilot" || s.workspaceRoot != "/var/lib/pilot/agent/workspaces" {
		t.Errorf("defaults = %q, %q; want /var/lib/pilot and /var/lib/pilot/agent/workspaces", s.dataDir, s.workspaceRoot)
	}
}
