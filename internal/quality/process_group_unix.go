//go:build unix

package quality

import (
	"os/exec"
	"syscall"
	"time"
)

// gateWaitDelay bounds how long cmd.Run waits for the output pipes after the gate
// shell has been killed or has exited. A descendant that left the process group
// (for example through setsid) can still hold the pipes open.
const gateWaitDelay = 5 * time.Second

// configureGateProcessGroup runs the gate in its own process group and makes a
// timeout kill the whole group, not only the shell. Without this, descendants such
// as `npm test` workers outlive the shell, keep stdout/stderr open, and cmd.Run
// blocks until they exit on their own.
//
// Must be called before cmd.Start().
func configureGateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// The group is already gone, or was never created: kill the shell itself.
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = gateWaitDelay
}
