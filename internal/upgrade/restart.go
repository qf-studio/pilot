// Package upgrade provides self-update functionality for Pilot.
// This file contains the process restart logic using syscall.Exec.

//go:build !windows

package upgrade

import (
	"fmt"
	"os"
	"syscall"
)

// RestartWithNewBinary replaces the current process with the new binary.
// This uses syscall.Exec which replaces the current process in-place,
// preserving the PID and terminal attachment.
//
// IMPORTANT: This function never returns on success. On failure, it returns an error.
//
// The function:
// 1. Flushes any buffered output
// 2. Copies the current environment
// 3. Adds PILOT_RESTARTED=1 marker for logging
// 4. Adds PILOT_PREVIOUS_VERSION for upgrade notification
// 5. Calls syscall.Exec to replace the process
func RestartWithNewBinary(binaryPath string, args []string, previousVersion string) error {
	// Validate binary exists and is executable
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("binary not found: %w", err)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("binary is not executable: %s", binaryPath)
	}

	// Flush any buffered output
	_ = os.Stdout.Sync()
	_ = os.Stderr.Sync()

	// Get current environment
	env := os.Environ()

	// Add marker to indicate this is a restart (for logging/state restoration)
	env = append(env, "PILOT_RESTARTED=1")

	// Add previous version for upgrade notification
	if previousVersion != "" {
		env = append(env, fmt.Sprintf("PILOT_PREVIOUS_VERSION=%s", previousVersion))
	}

	// Replace current process with the new binary
	// syscall.Exec never returns on success
	return syscall.Exec(binaryPath, args, env)
}

// CanHotRestart returns true if hot restart is supported on this platform.
// Hot restart is supported on Unix-like systems (Linux, macOS, BSD).
func CanHotRestart() bool {
	return true
}
