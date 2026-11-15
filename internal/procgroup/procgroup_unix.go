//go:build unix

package procgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

// ConfigureSession starts the subprocess in a new session (setsid), without
// a controlling terminal: it is then the leader of its session and of its
// process group, so pid, pgid and sid are equal and a signal to the negative
// pid reaches the group. Setpgid is not set: setsid fails for a process that
// is already a group leader. It does not touch WaitDelay.
//
// Must be called before cmd.Start().
func ConfigureSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Alive reports whether pid is a live process: one that exists and, on
// Linux, is not a zombie. A process of another user counts (EPERM).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// The state is the first field after the parenthesised comm.
	line := string(data)
	if i := strings.LastIndex(line, ")"); i >= 0 && i+2 < len(line) {
		switch line[i+2] {
		case 'Z', 'X':
			return false
		}
	}
	return true
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
