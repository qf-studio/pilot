//go:build unix

package executor

import (
	"os/exec"
	"syscall"
	"time"
)

// processGroupWaitDelay bounds how long cmd.Wait() will block on inherited
// stdout/stderr pipe fds after the tracked process has exited or been
// signalled. Without it, a surviving grandchild that still holds a pipe fd
// open (e.g. a backgrounded Bash-tool child — GH-4357's task_started/
// task_notification events prove Claude Code forks these) can hang
// cmd.Wait() forever: os/exec's io-copy goroutines block on Read() until
// every fd referencing the pipe's write end is closed, and Go never closes
// them on its own unless WaitDelay is set (GH-4503, pilot-console#24 —
// gen-0's claude process survived its session kill for 1h14m).
const processGroupWaitDelay = 5 * time.Second

// configureProcessGroup puts the subprocess in its own process group
// (setpgid) so killProcessGroup can reach any children it forks — rather
// than just the single tracked PID — and sets WaitDelay so cmd.Wait()
// cannot hang forever on inherited pipe fds (GH-4503).
//
// Must be called before cmd.Start().
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = processGroupWaitDelay
}

// killProcessGroup signals the entire process group led by the subprocess
// (negative PID convention) instead of just the tracked PID, so
// grandchildren backgrounded by the subprocess (e.g. Claude Code's Bash
// tool — GH-4357) are reached by the same signal. This is only effective
// when the cmd was started with configureProcessGroup applied (setpgid),
// making it its own group leader.
//
// If the group signal fails for any reason — the group already exited
// (ESRCH), or cmd was never made its own group leader so -pid was never a
// valid pgid to begin with — this falls back to signalling only the
// tracked PID, so callers always get a result no worse than the
// pre-GH-4503 single-PID behavior instead of a silent no-op.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
		return nil
	}
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
