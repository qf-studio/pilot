package executor

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/workflow"
	"github.com/qf-studio/pilot/internal/procgroup"
)

// mockSudoKillsGroup is the extra part of mockSudoWritable for the kill
// route: `sudo -n -u <user> kill -s KILL -- -<pgid>` leaves only the group in
// the arguments after the "--", and the mock signals it as the daemon user.
const mockSudoKillsGroup = `case "$1" in -[0-9]*) kill -s KILL -- "$1"; exit $?;; esac`

// TestRunWorkflowHookRunsAsAgent is GH-6602 (TASK-651): with run_as_user set
// a workflow hook runs through sudo as the agent user, with PILOT_TASK_ID of
// the task and without the secrets of the daemon, and a cancel kills its
// process group through sudo. With the option empty the hook gets the
// environment of the daemon as before, no sudo call is made, and a cancel
// still kills the children the script started in the background.
func TestRunWorkflowHookRunsAsAgent(t *testing.T) {
	const (
		secret = "daemon-only-value-6602"
		taskID = "GH-6602-hook"
	)

	t.Run("hook runs through sudo as the agent user", func(t *testing.T) {
		t.Setenv("DAEMON_ONLY_SECRET", secret)
		t.Setenv("PILOT_TASK_ID", "GH-daemon-value")
		f := newAgentFilesFixture(t, "")
		out := runHookForTest(t, t.Context(), f.ws, taskID, "env")
		if strings.Contains(out, "DAEMON_ONLY_SECRET") || strings.Contains(out, secret) {
			t.Errorf("hook output has the daemon secret:\n%s", out)
		}
		if !strings.Contains(out, "PILOT_TASK_ID="+taskID) {
			t.Errorf("hook output has no PILOT_TASK_ID=%s:\n%s", taskID, out)
		}
		if strings.Contains(out, "GH-daemon-value") {
			t.Errorf("hook output has the PILOT_TASK_ID of the daemon:\n%s", out)
		}
		if home := runAsSettingsNow().home; !strings.Contains(out, "HOME="+home) {
			t.Errorf("hook output has no HOME of the agent user %s:\n%s", home, out)
		}
		calls := f.calls(t)
		if len(calls) != 1 || !strings.Contains(calls[0], "-u pilot-agent ") || !strings.HasSuffix(calls[0], " -- bash -lc env") {
			t.Errorf("sudo calls = %q, want one call of bash -lc env as the agent user", calls)
		}
	})

	t.Run("hook gets no model credential and no request header", func(t *testing.T) {
		names := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS"}
		for _, n := range names {
			t.Setenv(n, "fake-value-6616")
		}
		f := newAgentFilesFixture(t, "")
		out := runHookForTest(t, t.Context(), f.ws, taskID, "env")
		if !strings.Contains(out, "PILOT_TASK_ID="+taskID) {
			t.Fatalf("hook output has no PILOT_TASK_ID=%s, the hook did not run:\n%s", taskID, out)
		}
		for _, n := range names {
			if strings.Contains(out, n+"=") {
				t.Errorf("hook environment has %s:\n%s", n, out)
			}
		}
		if strings.Contains(out, "fake-value-6616") {
			t.Errorf("hook output has a value of the daemon:\n%s", out)
		}
	})

	t.Run("hook runs in the task directory", func(t *testing.T) {
		f := newAgentFilesFixture(t, "")
		out := runHookForTest(t, t.Context(), f.ws, taskID, "pwd -P")
		if !strings.Contains(out, "output="+f.ws+"\n") {
			t.Errorf("hook output has no working directory %s:\n%s", f.ws, out)
		}
	})

	t.Run("command that is not a sudo command of the agent user does not run", func(t *testing.T) {
		f := newAgentFilesFixture(t, "")
		runAsGOOS = "darwin" // agentCommand builds a plain command; stubRunAsGOOS of the fixture restores
		sentinel := filepath.Join(t.TempDir(), "ran")
		out := runHookForTest(t, t.Context(), f.ws, taskID, "touch "+sentinel)
		if _, err := os.Stat(sentinel); err == nil {
			t.Error("the hook ran as the daemon user")
		}
		if !strings.Contains(out, "workflow hook failed") || !strings.Contains(out, "is not a sudo command of the agent user") {
			t.Errorf("hook log does not name the cause:\n%s", out)
		}
	})

	t.Run("option empty: the environment of the daemon, no sudo call", func(t *testing.T) {
		t.Setenv("DAEMON_ONLY_SECRET", secret)
		f := newAgentFilesFixture(t, "")
		setRunAsStateForTest(t, nil, "")
		out := runHookForTest(t, t.Context(), f.ws, taskID, "env")
		if !strings.Contains(out, "DAEMON_ONLY_SECRET="+secret) {
			t.Errorf("hook output has no DAEMON_ONLY_SECRET of the test process:\n%s", out)
		}
		if !strings.Contains(out, "PILOT_TASK_ID="+taskID) {
			t.Errorf("hook output has no PILOT_TASK_ID=%s:\n%s", taskID, out)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Errorf("sudo calls = %q, want none", calls)
		}
	})

	for _, empty := range []bool{true, false} {
		name := "cancel kills the background child (option set)"
		if empty {
			name = "cancel kills the background child (option empty)"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentFilesFixture(t, mockSudoKillsGroup)
			if empty {
				setRunAsStateForTest(t, nil, "")
			}
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			script := "sleep 300 >/dev/null 2>&1 & echo $! > " + pidFile + "; sleep 300"

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan string, 1)
			go func() { done <- runHookForTest(t, ctx, f.ws, taskID, script) }()

			child := waitForPidFile(t, pidFile)
			t.Cleanup(func() {
				if p, err := os.FindProcess(child); err == nil {
					_ = p.Kill()
				}
			})
			cancel()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("the hook did not return 30s after the cancel")
			}
			if !processGone(child, 5*time.Second) {
				t.Errorf("background child %d is alive after the cancel", child)
			}

			calls := f.calls(t)
			kills := sudoCallsWith(calls, " kill -s KILL -- -")
			if empty {
				if len(calls) != 0 {
					t.Errorf("sudo calls = %q, want none", calls)
				}
				return
			}
			if len(kills) != 1 {
				t.Errorf("sudo calls = %q, want one kill of the process group as the agent user", calls)
			}
		})
	}
}

// runHookForTest runs script as the after_create hook of the task in dir and
// returns what runWorkflowHook logged.
func runHookForTest(t *testing.T, ctx context.Context, dir, taskID, script string) string {
	t.Helper()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	runWorkflowHook(ctx, "after_create", workflow.HookValue{script}, dir, taskID, log)
	return buf.String()
}

// waitForPidFile returns the pid the hook wrote into path.
func waitForPidFile(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
			return pid
		}
	}
	t.Fatalf("the hook wrote no pid into %s", path)
	return 0
}

// processGone reports whether pid is gone within timeout.
func processGone(pid int, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !procgroup.Alive(pid) {
			return true
		}
	}
	return !procgroup.Alive(pid)
}
