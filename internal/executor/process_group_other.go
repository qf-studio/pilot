//go:build !unix

package executor

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup is a no-op on non-Unix platforms (GH-4503): there is
// no setpgid equivalent wired up here, so subprocess trees keep their
// current behavior — this file intentionally preserves pre-GH-4503 semantics
// rather than approximating process groups.
func configureProcessGroup(_ *exec.Cmd) {}

// killProcessGroup falls back to signalling only the tracked PID on
// non-Unix platforms, identical to the pre-GH-4503 single-PID behavior.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
