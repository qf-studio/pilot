// Package upgrade provides self-update functionality for Pilot.
// This file contains Windows-specific restart stub.

//go:build windows

package upgrade

import (
	"fmt"
)

// RestartWithNewBinary on Windows is not supported with syscall.Exec.
// Windows users should restart the application manually.
func RestartWithNewBinary(binaryPath string, args []string, previousVersion string) error {
	return fmt.Errorf("hot restart is not supported on Windows; please restart Pilot manually")
}

// CanHotRestart returns false on Windows as syscall.Exec is not available.
func CanHotRestart() bool {
	return false
}
