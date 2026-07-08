// Package upgrade provides self-update functionality for Pilot.
// This file sends the drain signal on UNIX-like systems.

//go:build !windows

package upgrade

import (
	"fmt"
	"os"
	"syscall"
)

// drainSignal is the OS signal used to ask a target process to enter drain
// mode. SIGUSR1 is not used elsewhere in Pilot and is safe to repurpose for
// this handshake.
const drainSignal = syscall.SIGUSR1

// SignalDrain asks the process at pid to enter drain mode by sending
// drainSignal. The target process is expected to have installed a handler
// (via signal.Notify with drainSignal) that begins reporting its in-flight
// count with ReportDrainStatus at DefaultDrainStatusPath (or whatever path
// the caller and target have agreed on).
func SignalDrain(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}
	if err := proc.Signal(drainSignal); err != nil {
		return fmt.Errorf("failed to signal drain to pid %d: %w", pid, err)
	}
	return nil
}
