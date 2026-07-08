// Package upgrade provides self-update functionality for Pilot.
// This file sends the drain signal on UNIX-like systems.

//go:build !windows

package upgrade

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// drainSignal is the OS signal used to ask a target process to enter drain
// mode. SIGUSR1 is not used elsewhere in Pilot and is safe to repurpose for
// this handshake.
const drainSignal = syscall.SIGUSR1

// SignalDrain asks the process at pid to enter drain mode by sending
// drainSignal. The target process is expected to have installed a handler
// (via NotifyDrain) that begins reporting its in-flight count with
// ReportDrainStatus at DefaultDrainStatusPath (or whatever path the caller
// and target have agreed on).
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

// NotifyDrain registers c to receive drainSignal, i.e. the receiving half of
// the SignalDrain handshake. Without this call, drainSignal (SIGUSR1) is
// ignored by Go's runtime by default — so a target process that never calls
// NotifyDrain simply never sees the request, and any RequestDrain waiting on
// it always times out.
func NotifyDrain(c chan<- os.Signal) {
	signal.Notify(c, drainSignal)
}
