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
		{"data dir 0750", 0o750, nil, []string{"gives the group or other users read or write", "0750"}},
		{"data dir 0711", 0o711, nil, nil},
		{"data dir 0700", 0o700, nil, nil},
		{"entry 0755", 0o711, entry("logs", 0o755), []string{"logs", "open to other users", "0755"}},
		{"entry 0700", 0o711, entry("logs", 0o700), nil},
		{"entry 0701", 0o711, entry("logs", 0o701), []string{"logs", "0701"}},
		{"entry with group bits, the shared group", 0o711, func(t *testing.T, data string, s *runAsSettings) {
			p := mkdirMode(t, filepath.Join(data, "state"), 0o750)
			s.gid = fileGID(t, p)
		}, []string{"state", "open to run_as_group pilot-agents", "0750"}},
		{"entry 0710, the shared group", 0o711, func(t *testing.T, data string, s *runAsSettings) {
			p := mkdirMode(t, filepath.Join(data, "state"), 0o710)
			s.gid = fileGID(t, p)
		}, []string{"state", "open to run_as_group pilot-agents", "0710"}},
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
		{"an entry agentx next to the workspace entry agent is checked", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			mkdirMode(t, filepath.Join(data, "agent", "workspaces"), 0o770)
			mkdirMode(t, filepath.Join(data, "agentx"), 0o755)
		}, []string{filepath.Join("data", "agentx"), "open to other users", "0755"}},
		{"a link to an open directory", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			open := mkdirMode(t, filepath.Join(data, "zz-open"), 0o755)
			if err := os.Symlink(open, filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, []string{"logs", "0755"}},
		{"a link to a closed directory", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			mkdirMode(t, filepath.Join(data, "closed"), 0o700)
			if err := os.Symlink("closed", filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a link out to an open directory", 0o711, func(t *testing.T, data string, _ *runAsSettings) {
			open := mkdirMode(t, filepath.Join(t.TempDir(), "open"), 0o755)
			if err := os.Symlink(open, filepath.Join(data, "logs")); err != nil {
				t.Fatal(err)
			}
		}, []string{"logs", "a link out of run_as_data_dir"}},
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

// runAsLinkCheck runs the check on a data directory with mode 0700, the
// workspace root <data>/agent/workspaces (agent root 0700) and one top-level
// link "logs" to the target the case returns.
func runAsLinkCheck(t *testing.T, link func(t *testing.T, data string) (target string)) (got, entry string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	data := mkdirMode(t, filepath.Join(t.TempDir(), "data"), 0o700)
	mkdirMode(t, filepath.Join(data, "agent", "workspaces"), 0o700)
	mkdirMode(t, filepath.Join(data, "agent"), 0o700)
	entry = filepath.Join(data, "logs")
	if err := os.Symlink(link(t, data), entry); err != nil {
		t.Fatal(err)
	}
	s := &runAsSettings{user: "pilot-agent", group: "pilot-agents", gid: -1, dataDir: data,
		workspaceRoot: filepath.Join(data, "agent", "workspaces")}
	return checkRunAsDataDirModes(s), entry
}

// mutation pin: a top-level link out of the data directory to a closed target passes in Go while the script refuses it -> this test fails
func TestRunAsDataDirLinkOutToClosedTargetFails(t *testing.T) {
	for name, target := range map[string]func(t *testing.T, data string) string{
		"absolute": func(t *testing.T, _ string) string { return mkdirMode(t, filepath.Join(t.TempDir(), "closed"), 0o700) },
		"relative": func(t *testing.T, data string) string {
			mkdirMode(t, filepath.Join(filepath.Dir(data), "closed"), 0o700)
			return "../closed"
		},
		"through a link in the data dir": func(t *testing.T, data string) string {
			closed := mkdirMode(t, filepath.Join(t.TempDir(), "closed"), 0o700)
			if err := os.Symlink(closed, filepath.Join(data, "zz-hop")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(data, "zz-hop", ".")
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, entry := runAsLinkCheck(t, target)
			if !strings.Contains(got, entry) || !strings.Contains(got, "a link out of run_as_data_dir") {
				t.Errorf("check = %q, want %s named as a link out of the data directory", got, entry)
			}
		})
	}
}

// mutation pin: a dangling top-level link out of the data directory is skipped, so it opens once the agent user creates the target -> this test fails
func TestRunAsDataDirDanglingLinkOutFails(t *testing.T) {
	for name, target := range map[string]func(t *testing.T, data string) string{
		"absolute": func(t *testing.T, _ string) string { return filepath.Join(t.TempDir(), "later") },
		"relative": func(*testing.T, string) string { return "../later/deeper" },
		"to a dangling link": func(t *testing.T, data string) string {
			if err := os.Symlink(filepath.Join(t.TempDir(), "later"), filepath.Join(data, "zz-hop")); err != nil {
				t.Fatal(err)
			}
			return "zz-hop"
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, entry := runAsLinkCheck(t, target)
			if !strings.Contains(got, entry) || !strings.Contains(got, "a link out of run_as_data_dir") {
				t.Errorf("check = %q, want the dangling %s named as a link out of the data directory", got, entry)
			}
		})
	}
	// A dangling link that stays in the data directory reaches nothing the
	// agent user can create: still skipped.
	got, _ := runAsLinkCheck(t, func(*testing.T, string) string { return "gone" })
	if got != "" {
		t.Errorf("a dangling link inside the data directory failed: %q", got)
	}
}

// mutation pin: a second top-level entry that links to the agent root or to the data directory is judged by the mode of its target, not flagged as the script does -> this test fails
func TestRunAsDataDirLinkToAgentRootOrDataDirFails(t *testing.T) {
	for name, target := range map[string]func(t *testing.T, data string) string{
		"the data dir":           func(_ *testing.T, data string) string { return data },
		"the data dir, relative": func(*testing.T, string) string { return "." },
		"the agent root":         func(_ *testing.T, data string) string { return filepath.Join(data, "agent") },
		"the workspace root":     func(*testing.T, string) string { return "agent/workspaces" },
		"a missing path in the agent root": func(_ *testing.T, data string) string {
			return filepath.Join(data, "agent", "workspaces", "later")
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, entry := runAsLinkCheck(t, target)
			if !strings.Contains(got, entry) || !strings.Contains(got, "resolves to run_as_data_dir or to the agent root") {
				t.Errorf("check = %q, want %s named as a link to the data directory or the agent root", got, entry)
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
// alone runs the preflight again.
//
// mutation pin: the reload subcase changes the workspace root along with run_as_data_dir, so it does not pin the dataDir term of the short cut -> this test fails
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
			ws := filepath.Join(t.TempDir(), "agent", "workspaces")
			PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
				RunAsWorkspaceDir: ws, RunAsDataDir: data}, "", true, 0)
			err := RunAsUserPreflightError()
			if tc.wantCheck == "" {
				if err != nil {
					t.Fatalf("preflight failed: %v", err)
				}
				if strings.Join(order, ",") != "workspace,data" {
					t.Errorf("checks ran as %v, want the data directory after the workspace root", order)
				}
				// A reload to another data directory, every other option
				// the same: the preflight runs again and refuses.
				open := mkdirMode(t, filepath.Join(t.TempDir(), "open"), 0o755)
				PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
					RunAsWorkspaceDir: ws, RunAsDataDir: open}, "", true, 0)
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

// TestRunAsDataDirReloadRunsPreflight: a change of run_as_data_dir alone,
// every other option the same, runs the preflight again with the new data
// directory; the same options again keep the result.
func TestRunAsDataDirReloadRunsPreflight(t *testing.T) {
	setRunAsStateForTest(t, nil, "")
	_ = mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	var checked []string
	runAsDataDirCheck = func(s *runAsSettings) string { checked = append(checked, s.dataDir); return "" }
	ws := filepath.Join(t.TempDir(), "agent", "workspaces")
	first, second := closedTempDir(t), closedTempDir(t)
	cfg := &ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents", RunAsWorkspaceDir: ws, RunAsDataDir: first}
	PreflightRunAsUser(cfg, "", true, 0)
	PreflightRunAsUser(cfg, "", true, 0)
	cfg.RunAsDataDir = second
	PreflightRunAsUser(cfg, "", true, 0)
	if want := []string{first, second}; strings.Join(checked, ",") != strings.Join(want, ",") {
		t.Errorf("data directory checks ran for %v, want %v", checked, want)
	}
	if s := runAsSettingsNow(); s == nil || s.dataDir != second {
		t.Errorf("settings after the reload = %+v, want run_as_data_dir %s", s, second)
	}
}

// TestRunAsWorkspaceRootNotDataDir: the workspace root is not run_as_data_dir
// or a parent of it, as given or resolved; the refusal names both paths. The
// layout of the setup script (the root below the agent entry of the data
// directory) passes.
func TestRunAsWorkspaceRootNotDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("links")
	}
	prev := daemonHomeDir
	daemonHomeDir = func() (string, error) { return filepath.Join(t.TempDir(), "home", "daemon"), nil }
	t.Cleanup(func() { daemonHomeDir = prev })
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := mkdirMode(t, filepath.Join(base, "var", "lib", "pilot"), 0o711)
	linkToData := filepath.Join(base, "data-link")
	if err := os.Symlink(data, linkToData); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		root, dataDir string
		refused       bool
	}{
		{"root is the data directory", data, data, true},
		{"root is the data directory, trailing slash", data + "/", data, true},
		{"root is the parent of the data directory", filepath.Dir(data), data, true},
		{"root is a grand-parent of the data directory", filepath.Join(base, "var"), data, true},
		{"root is a link to the data directory", linkToData, data, true},
		{"data directory given through a link, root its target", data, linkToData, true},
		{"default layout: the root below the agent entry", filepath.Join(data, "agent", "workspaces"), data, false},
		{"a root next to the data directory", filepath.Join(base, "var", "lib", "pilot-agent"), data, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := refusedRunAsWorkspaceRoot(tc.root, tc.dataDir, &ClaudeCodeConfig{})
			if !tc.refused {
				if got != "" {
					t.Errorf("refused: %q, want a pass", got)
				}
				return
			}
			if !strings.Contains(got, "is run_as_data_dir") || !strings.Contains(got, tc.root) || !strings.Contains(got, tc.dataDir) {
				t.Errorf("check = %q, want the refusal naming %s and %s", got, tc.root, tc.dataDir)
			}
		})
	}

	// Through the preflight: one Error line and no spawn.
	setRunAsStateForTest(t, nil, "")
	_ = mockSudo(t, 0)
	stubRunAsHost(t, "linux", "1000", map[string]string{"pilot-agent": t.TempDir()})
	buf := captureLog(t)
	PreflightRunAsUser(&ClaudeCodeConfig{RunAsUser: "pilot-agent", RunAsGroup: "pilot-agents",
		RunAsWorkspaceDir: filepath.Dir(data), RunAsDataDir: data}, "", true, 0)
	if err := RunAsUserPreflightError(); err == nil || !strings.Contains(err.Error(), "is run_as_data_dir "+data) {
		t.Errorf("preflight error = %v, want the parent of the data directory refused", err)
	}
	if n := strings.Count(buf.String(), "level=ERROR"); n != 1 {
		t.Errorf("want one Error line, got:\n%s", buf.String())
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
