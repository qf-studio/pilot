// Package upgrade provides self-update functionality for Pilot.
// This file contains the Windows drain-signal stub.

//go:build windows

package upgrade

import "fmt"

// SignalDrain is not supported on Windows: there is no SIGUSR1 equivalent
// exposed by the Go runtime for arbitrary target processes. Callers on
// Windows should coordinate draining out-of-band (e.g. the target process
// checking DefaultDrainStatusPath's directory for a request file written by
// the caller) before falling back to WaitForDrain-style polling.
func SignalDrain(pid int) error {
	return fmt.Errorf("signal-based drain is not supported on Windows")
}
