//go:build !unix

package procgroup

import (
	"os/exec"
	"syscall"
	"time"
)

// Configure is a no-op on non-Unix platforms (GH-4503): there is no setpgid
// equivalent wired up here, so subprocess trees keep their current behavior —
// this file intentionally preserves pre-GH-4503 semantics rather than
// approximating process groups.
func Configure(_ *exec.Cmd, _ ...time.Duration) {}

// ConfigureSession is a no-op on non-Unix platforms.
func ConfigureSession(_ *exec.Cmd) {}

// Alive reports false on non-Unix platforms: there is no signal 0 here.
func Alive(_ int) bool { return false }

// Kill falls back to signalling only the tracked PID on non-Unix platforms,
// identical to the pre-GH-4503 single-PID behavior.
func Kill(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
