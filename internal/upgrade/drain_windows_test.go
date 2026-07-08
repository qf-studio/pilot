// Package upgrade provides self-update functionality for Pilot.
// This file tests the Windows drain-signal stub.

//go:build windows

package upgrade

import (
	"os"
	"testing"
)

func TestSignalDrain_UnsupportedOnWindows(t *testing.T) {
	err := SignalDrain(os.Getpid())
	if err == nil {
		t.Fatal("SignalDrain() expected an unsupported-platform error on Windows, got nil")
	}
}
