package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TASK-651, gates leg part 4: with run_as_user set the lint run and the lint
// fix of autoFixLint run as the agent user in the workspace; a failed restore
// of the settings file fails the run. sudo is the mock of the package
// (mockSudoAgent); no test needs root.

// lintDaemonEnv are the names of the daemon the lint commands never get.
var lintDaemonEnv = map[string]string{
	"DAEMON_ONLY_SECRET":       "daemon-only-secret",
	"ANTHROPIC_API_KEY":        "fake-api-key",
	"ANTHROPIC_AUTH_TOKEN":     "fake-auth-token",
	"CLAUDE_CODE_OAUTH_TOKEN":  "fake-oauth-token",
	"GITHUB_TOKEN":             testutil.FakeGitHubToken,
	"ANTHROPIC_CUSTOM_HEADERS": "X-Daemon: daemon-header",
}

// toolsOnlyPath makes a directory with links to sh and git only and returns
// it: a PATH without the lint program of the host.
func toolsOnlyPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"sh", "git"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not found: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// mockLint puts a fake golangci-lint first on PATH. Each call writes its
// working directory and its environment to <out>/<check|fix>.pwd and .env. A
// check reports an issue until a fix ran; a fix exits 0.
func mockLint(t *testing.T) (out string) {
	t.Helper()
	dir := t.TempDir()
	out = t.TempDir()
	body := fmt.Sprintf(`#!/bin/sh
out='%[1]s'
kind=check
for a in "$@"; do [ "$a" = "--fix" ] && kind=fix; done
pwd > "$out/$kind.pwd"
env > "$out/$kind.env"
if [ $kind = fix ]; then
  : > "$out/fixed"
  exit 0
fi
[ -f "$out/fixed" ] && exit 0
echo "a.go:1:1: lint issue"
exit 1
`, out)
	if err := os.WriteFile(filepath.Join(dir, "golangci-lint"), []byte(body), 0o755); err != nil { //nolint:gosec // mock must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// lintWorkspace is a git repository with a go.mod in a workspace below a new
// workspace root; it returns the root and the workspace.
func lintWorkspace(t *testing.T) (root, ws string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws = filepath.Join(root, "ws-1")
	writeTestFile(t, filepath.Join(ws, "go.mod"), "module example.com/lint\n\ngo 1.21\n")
	wsGit(t, ws, "init", "-q", "-b", "main")
	// The identity is in the config of the repository: the amend of autoFixLint
	// commits without -c, and a host that cannot name itself gives git no
	// identity to guess (exit 128).
	wsGit(t, ws, "config", "user.email", "t@example.com")
	wsGit(t, ws, "config", "user.name", "T")
	wsGit(t, ws, "add", ".")
	wsGit(t, ws, "commit", "-q", "-m", "init")
	return root, ws
}

func setLintDaemonEnv(t *testing.T) {
	t.Helper()
	clearModelEnvForTest(t)
	for name, v := range lintDaemonEnv {
		t.Setenv(name, v)
	}
}

func TestAutoFixLintRunsAsAgent(t *testing.T) {
	isolateHostGitIdentity(t)
	t.Run("lint and fix run as the agent user", func(t *testing.T) {
		stubRunAsGOOS(t)
		root, ws := lintWorkspace(t)
		out := mockLint(t)
		sudoLog := mockSudoAgent(t, "", "")
		setRunAsStateForTest(t, gitRouteSettings(t, root), "")
		setLintDaemonEnv(t)

		res := NewGitOperations(ws).autoFixLint(context.Background(), "main")
		if !res.FixedAll || !res.Clean {
			t.Fatalf("autoFixLint = %+v, want the fix applied", res)
		}

		calls := strings.Join(sudoCalls(t, sudoLog), "\n")
		for _, want := range []string{
			"-n -u pilot-agent",
			" -- golangci-lint run --new-from-rev=origin/main ./...",
			" -- golangci-lint run --fix ./...",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("sudo log lacks %q:\n%s", want, calls)
			}
		}
		for _, kind := range []string{"check", "fix"} {
			if got := strings.TrimSpace(readTestFile(t, filepath.Join(out, kind+".pwd"))); got != ws {
				t.Errorf("%s command ran in %q, want the task directory %q", kind, got, ws)
			}
			env := readTestFile(t, filepath.Join(out, kind+".env"))
			for _, line := range strings.Split(env, "\n") {
				name, _, _ := strings.Cut(line, "=")
				if _, daemon := lintDaemonEnv[name]; daemon {
					t.Errorf("%s command environment has %s of the daemon", kind, name)
				}
			}
			for name, v := range lintDaemonEnv {
				if strings.Contains(env, v) {
					t.Errorf("%s command environment has the value of %s of the daemon", kind, name)
				}
			}
		}
	})

	t.Run("a missing lint program gives the result of today", func(t *testing.T) {
		stubRunAsGOOS(t)
		root, ws := lintWorkspace(t)
		t.Setenv("PATH", toolsOnlyPath(t))
		sudoLog := mockSudoAgent(t, "", "")
		g := NewGitOperations(ws)

		want := g.autoFixLint(context.Background(), "main")
		if calls := sudoCalls(t, sudoLog); len(calls) != 0 {
			t.Fatalf("option empty: sudo ran: %q", calls)
		}
		if want.Clean || want.FixedAll {
			t.Fatalf("option empty: a missing program gave %+v", want)
		}

		setRunAsStateForTest(t, gitRouteSettings(t, root), "")
		got := g.autoFixLint(context.Background(), "main")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("option set: autoFixLint = %+v, want the result of the option empty %+v", got, want)
		}
		if calls := strings.Join(sudoCalls(t, sudoLog), "\n"); !strings.Contains(calls, " -- golangci-lint run") {
			t.Errorf("option set: the lint program did not run through sudo:\n%s", calls)
		}
	})

	t.Run("option empty: no sudo call", func(t *testing.T) {
		_, ws := lintWorkspace(t)
		out := mockLint(t)
		sudoLog := mockSudoAgent(t, "", "")
		setRunAsStateForTest(t, nil, "")

		res := NewGitOperations(ws).autoFixLint(context.Background(), "main")
		if !res.FixedAll {
			t.Fatalf("autoFixLint = %+v, want the fix applied", res)
		}
		if calls := sudoCalls(t, sudoLog); len(calls) != 0 {
			t.Errorf("sudo ran with the option empty: %q", calls)
		}
		if got := strings.TrimSpace(readTestFile(t, filepath.Join(out, "fix.pwd"))); got != ws {
			t.Errorf("fix command ran in %q, want %q", got, ws)
		}
	})
}

// TestRestoreHookSettingsFailureFailsRunAsAgent: with the option set a restore
// that fails makes the result of the run a failure that names the file; with
// it empty the warning of before stays and the result is untouched.
func TestRestoreHookSettingsFailureFailsRunAsAgent(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settingsPath := filepath.Join(t.TempDir(), ".claude", "settings.json")
	failing := func() error {
		return RestoreHookSettings(settingsPath, t.TempDir(), func() error { return errors.New("write as the agent user failed") }, true)
	}

	t.Run("option set", func(t *testing.T) {
		stubRunAsGOOS(t)
		setRunAsStateForTest(t, testRunAsSettings(t), "")
		err := hookSettingsRestore(log, settingsPath, runAsSettingsNow() != nil, failing)()
		if err == nil {
			t.Fatal("the restore error was dropped")
		}
		res, runErr := failRunOnSettingsRestore(log, "GH-1", &ExecutionResult{TaskID: "GH-1", Success: true}, nil, err)
		if res.Success {
			t.Error("the run succeeded after a failed restore")
		}
		if !strings.Contains(res.Error, settingsPath) {
			t.Errorf("result error %q does not name %s", res.Error, settingsPath)
		}
		if runErr == nil || !strings.Contains(runErr.Error(), settingsPath) {
			t.Errorf("run error = %v, want one that names %s", runErr, settingsPath)
		}

		res, _ = failRunOnSettingsRestore(log, "GH-1", nil, errors.New("earlier"), err)
		if res == nil || res.Success || res.TaskID != "GH-1" {
			t.Errorf("a nil result = %+v, want a failure of the task", res)
		}
	})

	t.Run("option empty", func(t *testing.T) {
		setRunAsStateForTest(t, nil, "")
		if err := hookSettingsRestore(log, settingsPath, runAsSettingsNow() != nil, failing)(); err != nil {
			t.Errorf("option empty: restore returned %v, want the warning only", err)
		}
	})
}
