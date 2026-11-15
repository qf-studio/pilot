package executor

import (
	"os/exec"
	"syscall"

	"github.com/qf-studio/pilot/internal/procgroup"
)

// configureProcessGroup puts cmd in its own process group with the default
// WaitDelay (GH-4503). Implementation lives in internal/procgroup, shared
// with the quality gate runner (GH-6081).
//
// A command agentCommand built as the run_as_user user is left as it is: it
// already runs in a session of its own (Setsid, which Setpgid would break)
// with the wait delay of the sudo kill (TASK-651).
func configureProcessGroup(cmd *exec.Cmd) {
	if _, ok := agentCmdUser(cmd); ok {
		return
	}
	procgroup.Configure(cmd)
}

// killProcessGroup signals cmd's whole process group, falling back to the
// tracked PID (GH-4503). See procgroup.Kill.
//
// A command started as the run_as_user user (agentCommand) is signalled
// through sudo: the daemon user may not signal that user or sudo (TASK-651).
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if agentUser, ok := agentCmdUser(cmd); ok {
		return killAgentProcessGroup(cmd, agentUser, sig)
	}
	return procgroup.Kill(cmd, sig)
}
