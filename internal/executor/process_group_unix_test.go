//go:build unix

package executor

import (
	"bufio"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillProcessGroupKillsGrandchild is the GH-4503 regression test. It
// reproduces the pilot-console GH-24 incident shape: a direct child
// backgrounds a grandchild (mirroring how Claude Code's Bash tool
// backgrounds tasks — GH-4357) and killProcessGroup must reach both, not
// just the tracked PID.
func TestKillProcessGroupKillsGrandchild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-group integration test in short mode")
	}

	// The direct child backgrounds a grandchild ("sleep 60 &"), prints its
	// PID so the test can observe it, then execs into a long-lived
	// foreground process of its own (keeping cmd.Process.Pid stable and
	// outliving the echo).
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; exec sleep 60")
	configureProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}

	type lineResult struct {
		line string
		err  error
	}
	lineCh := make(chan lineResult, 1)
	go func() {
		reader := bufio.NewReader(stdoutPipe)
		line, rerr := reader.ReadString('\n')
		lineCh <- lineResult{line: line, err: rerr}
	}()

	var grandchildPID int
	select {
	case res := <-lineCh:
		if strings.TrimSpace(res.line) == "" {
			t.Fatalf("failed to read grandchild pid (err=%v)", res.err)
		}
		pid, perr := strconv.Atoi(strings.TrimSpace(res.line))
		if perr != nil {
			t.Fatalf("failed to parse grandchild pid from %q: %v", res.line, perr)
		}
		grandchildPID = pid
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for grandchild pid from child stdout")
	}

	parentPID := cmd.Process.Pid

	if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
		t.Fatalf("killProcessGroup failed: %v", err)
	}

	// Reap the direct child so its PID is actually released rather than
	// lingering as a zombie forever (Go never auto-reaps).
	go func() { _ = cmd.Wait() }()

	waitForProcessGone(t, parentPID, 5*time.Second, "direct child")
	waitForProcessGone(t, grandchildPID, 5*time.Second, "grandchild (GH-4503/pilot-console#24 orphan risk)")
}

// TestConfigureProcessGroupWaitDelayBoundsCmdWait is the second GH-4503
// regression test: proves cmd.Wait() cannot hang forever when a grandchild
// inherits the stdout pipe and holds it open long after the tracked process
// itself has exited (the exact shape of the pilot-console GH-24 incident,
// minus an explicit kill — this is what happens on a clean exit too).
func TestConfigureProcessGroupWaitDelayBoundsCmdWait(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process-group integration test in short mode")
	}

	// The direct child backgrounds a long sleep — inheriting the stdout
	// pipe's write end — then exits immediately itself.
	cmd := exec.Command("sh", "-c", "sleep 20 & exit 0")
	configureProcessGroup(cmd)
	cmd.Stdout = io.Discard // non-*os.File writer forces exec to allocate a real pipe

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}
	// Best-effort cleanup of the backgrounded grandchild once the test ends.
	defer func() { _ = killProcessGroup(cmd, syscall.SIGKILL) }()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// cmd.Wait() returned — WaitDelay unblocked the pipe read even
		// though the grandchild is still holding it open (20s sleep).
	case <-time.After(processGroupWaitDelay + 3*time.Second):
		t.Fatalf("cmd.Wait() did not return within WaitDelay (%v) bound — grandchild pipe hang not fixed (GH-4503)", processGroupWaitDelay)
	}
}

// waitForProcessGone polls (bounded, no fixed sleep) until signalling pid
// with signal 0 fails — meaning the process no longer exists — or the
// timeout elapses.
func waitForProcessGone(t *testing.T, pid int, timeout time.Duration, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s (pid %d) still alive after %v — process-group kill did not reach it", label, pid, timeout)
}
