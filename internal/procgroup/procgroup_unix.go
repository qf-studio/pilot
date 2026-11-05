//go:build unix

package procgroup

import (
	"os/exec"
	"syscall"
	"time"
)

// Configure puts the subprocess in its own process group (setpgid) so Kill
// can reach any children it forks — rather than just the single tracked PID —
// and sets WaitDelay so cmd.Wait() cannot hang forever on inherited pipe fds
// (GH-4503). waitDelay is optional and defaults to DefaultWaitDelay.
//
// Must be called before cmd.Start().
func Configure(cmd *exec.Cmd, waitDelay ...time.Duration) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = DefaultWaitDelay
	if len(waitDelay) > 0 {
		cmd.WaitDelay = waitDelay[0]
	}
}

// Kill signals the entire process group led by the subprocess (negative PID
// convention) instead of just the tracked PID, so grandchildren backgrounded
// by the subprocess (e.g. Claude Code's Bash tool — GH-4357) are reached by
// the same signal. This is only effective when the cmd was started with
// Configure applied (setpgid), making it its own group leader.
//
// If the group signal fails for any reason — the group already exited
// (ESRCH), or cmd was never made its own group leader so -pid was never a
// valid pgid to begin with — this falls back to signalling only the
// tracked PID, so callers always get a result no worse than the
// pre-GH-4503 single-PID behavior instead of a silent no-op.
func Kill(cmd *exec.Cmd, sig syscall.Signal) error {
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
