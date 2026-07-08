package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/upgrade"
)

// newKillBotTestStore creates a real, file-backed memory.Store for
// killExistingTelegramBot/terminateTarget tests (GH-4107).
func newKillBotTestStore(t *testing.T) *memory.Store {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-kill-bot-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

// stubRequestDrain swaps requestDrainFn for the duration of the test and
// restores the original on cleanup.
func stubRequestDrain(t *testing.T, fn func(ctx context.Context, pid int, cfg *upgrade.DrainConfig) (upgrade.DrainOutcome, error)) {
	t.Helper()
	orig := requestDrainFn
	requestDrainFn = fn
	t.Cleanup(func() { requestDrainFn = orig })
}

// spawnSleeper starts a real, short-lived process this test owns so
// terminateTarget can signal a genuine PID without touching anything else on
// the machine. The caller must Wait() on the returned cmd.
func spawnSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start sleeper process: %v", err)
	}
	return cmd
}

// waitSignal waits for cmd to exit and returns the signal that killed it (nil
// if it didn't die from a signal).
func waitSignal(t *testing.T, cmd *exec.Cmd) os.Signal {
	t.Helper()
	err := cmd.Wait()
	if err == nil {
		return nil
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal()
	}
	t.Fatalf("process did not exit via signal: %v", err)
	return nil
}

// TestKillExistingTelegramBot_IdleDaemon pins the unchanged-behavior path: no
// matching "pilot start"/"pilot telegram" process means killExistingTelegramBot
// returns immediately with no error and never touches the drain handshake or
// the store — the common case (no existing bot to replace) must behave
// exactly as before this GH-4107 change.
func TestKillExistingTelegramBot_IdleDaemon(t *testing.T) {
	stubRequestDrain(t, func(ctx context.Context, pid int, cfg *upgrade.DrainConfig) (upgrade.DrainOutcome, error) {
		t.Fatalf("requestDrainFn must not be called when no target process exists (pid=%d)", pid)
		return upgrade.DrainUnknown, nil
	})

	if err := killExistingTelegramBot(context.Background(), nil, time.Second); err != nil {
		t.Fatalf("killExistingTelegramBot() with no running instance = %v, want nil", err)
	}
}

// TestTerminateTarget_DrainSucceeds pins the (c) branch of GH-4107: when the
// target reports Drained, terminateTarget proceeds with the existing graceful
// shutdown (SIGTERM) and does not touch the execution store.
func TestTerminateTarget_DrainSucceeds(t *testing.T) {
	cmd := spawnSleeper(t)
	pid := cmd.Process.Pid

	stubRequestDrain(t, func(ctx context.Context, gotPID int, cfg *upgrade.DrainConfig) (upgrade.DrainOutcome, error) {
		if gotPID != pid {
			t.Errorf("requestDrainFn pid = %d, want %d", gotPID, pid)
		}
		return upgrade.Drained, nil
	})

	store := newKillBotTestStore(t)
	const execID = "exec-drain-succeeds"
	if err := store.SaveExecution(&memory.Execution{ID: execID, TaskID: "GH-1", ProjectPath: "/project", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	terminateTarget(context.Background(), pid, store, time.Second)

	sig := waitSignal(t, cmd)
	if sig != syscall.SIGTERM {
		t.Fatalf("target received signal %v, want SIGTERM (graceful shutdown on Drained)", sig)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}
	if exec.Status != "running" {
		t.Errorf("execution status = %q, want unchanged %q — Drained path must not touch the store", exec.Status, "running")
	}
}

// TestTerminateTarget_DrainTimesOut pins the (d) branch of GH-4107: when the
// target does not report Drained in time, terminateTarget force-kills it
// (SIGKILL) AND stamps an explicit "terminated by --replace restart"
// classification on any execution row still marked running, so it does not
// fall through to the exit-137 OOM heuristic (reclassifyLegacyOutcomes,
// GH-4105) on the next daemon boot.
func TestTerminateTarget_DrainTimesOut(t *testing.T) {
	cmd := spawnSleeper(t)
	pid := cmd.Process.Pid

	stubRequestDrain(t, func(ctx context.Context, gotPID int, cfg *upgrade.DrainConfig) (upgrade.DrainOutcome, error) {
		return upgrade.TimedOut, nil
	})

	store := newKillBotTestStore(t)
	const execID = "exec-drain-times-out"
	if err := store.SaveExecution(&memory.Execution{ID: execID, TaskID: "GH-2", ProjectPath: "/project", Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	terminateTarget(context.Background(), pid, store, 50*time.Millisecond)

	sig := waitSignal(t, cmd)
	if sig != syscall.SIGKILL {
		t.Fatalf("target received signal %v, want SIGKILL (force-kill on TimedOut)", sig)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}
	if exec.Status != "infra" {
		t.Errorf("execution status = %q, want %q so the exit-137 OOM heuristic never sees this row", exec.Status, "infra")
	}
	if !strings.Contains(exec.Error, "--replace restart") {
		t.Errorf("execution error = %q, want it to explicitly mention --replace restart", exec.Error)
	}
	if strings.Contains(exec.Error, "SIGKILL") || strings.Contains(exec.Error, "exit code 137") {
		t.Errorf("execution error = %q must not contain oom-heuristic signature substrings (SIGKILL/exit code 137)", exec.Error)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one execution_event recorded for the force-kill classification")
	}
	last := events[len(events)-1]
	if last.Stage != memory.StageFailed {
		t.Errorf("last event stage = %q, want %q", last.Stage, memory.StageFailed)
	}
	if !strings.Contains(last.Detail, "--replace restart") {
		t.Errorf("last event detail = %q, want it to mention --replace restart", last.Detail)
	}
}

// TestDrainHandshake_EndToEnd exercises the real GH-4106/GH-4107 handshake
// end-to-end (no stubbing): startDrainResponder installs the receiving half
// in this test process, then a real GracefulUpgrader.RequestDrain signals
// this same process (self-signal, since a test process is a perfectly valid
// SignalDrain target) and polls the on-disk status file it writes back. This
// pins the fix for the gap where nothing in the daemon ever answered the
// drain signal, so RequestDrain always timed out regardless of in-flight
// count.
func TestDrainHandshake_EndToEnd(t *testing.T) {
	tmpHome, err := os.MkdirTemp("", "pilot-test-drain-e2e-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpHome) })
	t.Setenv("HOME", tmpHome) // redirects upgrade.DefaultDrainStatusPath()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// nil dispatcher/runner: an explicitly supported startDrainResponder path
	// (store/runner unavailable) — in-flight count is always 0, so the
	// responder should report Drained on its very first status write.
	startDrainResponder(ctx, nil, nil)

	g, err := upgrade.NewGracefulUpgrader("test-version", &upgrade.NoOpTaskChecker{})
	if err != nil {
		t.Fatalf("NewGracefulUpgrader failed: %v", err)
	}

	outcome, err := g.RequestDrain(context.Background(), os.Getpid(), &upgrade.DrainConfig{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("RequestDrain failed: %v", err)
	}
	if outcome != upgrade.Drained {
		t.Fatalf("RequestDrain outcome = %v, want Drained", outcome)
	}
}
