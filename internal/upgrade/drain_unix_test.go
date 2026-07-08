// Package upgrade provides self-update functionality for Pilot.
// This file tests the UNIX drain signal delivery.

//go:build !windows

package upgrade

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestSignalDrain_DeliversSignal sends the drain signal to the current
// process and verifies it arrives. A signal.Notify handler must be
// registered first — SIGUSR1's default disposition is to terminate the
// process, so an unhandled send here would kill the test binary.
func TestSignalDrain_DeliversSignal(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, drainSignal)
	defer signal.Stop(sigCh)

	if err := SignalDrain(os.Getpid()); err != nil {
		t.Fatalf("SignalDrain() error = %v", err)
	}

	select {
	case sig := <-sigCh:
		if sig != syscall.SIGUSR1 {
			t.Errorf("received signal = %v, want SIGUSR1", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for drain signal to be delivered")
	}
}

func TestSignalDrain_InvalidPID(t *testing.T) {
	// PID 0 and negative PIDs are never valid single-process targets; on most
	// UNIX systems this either errors on FindProcess/Signal or targets a
	// process group, so assert only that the exported PID lookup path (not a
	// crash) is exercised. A defensively huge PID is guaranteed unused.
	err := SignalDrain(1 << 30)
	if err == nil {
		t.Fatal("SignalDrain() expected error for a PID that does not exist, got nil")
	}
}

// TestGracefulUpgrader_RequestDrain_SignalAndPoll exercises the full
// handshake end-to-end: RequestDrain signals this process, a background
// goroutine acts as the "target" by catching the signal and reporting a
// drained status shortly after, and RequestDrain's poll loop picks it up.
func TestGracefulUpgrader_RequestDrain_SignalAndPoll(t *testing.T) {
	tc := &mockTaskChecker{}
	g, dir := newTestGracefulUpgrader(t, tc)
	g.clock = realClock{}

	statusPath := filepath.Join(dir, "drain-status.json")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, drainSignal)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh
		// Simulate the target finishing one in-flight execution after
		// acknowledging the drain request.
		time.Sleep(20 * time.Millisecond)
		_ = ReportDrainStatus(statusPath, os.Getpid(), true, 0)
	}()

	outcome, err := g.RequestDrain(context.Background(), os.Getpid(), &DrainConfig{
		StatusPath:   statusPath,
		Timeout:      2 * time.Second,
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RequestDrain() error = %v", err)
	}
	if outcome != Drained {
		t.Errorf("outcome = %v, want Drained", outcome)
	}
}

func TestGracefulUpgrader_RequestDrain_SignalFailurePropagates(t *testing.T) {
	tc := &mockTaskChecker{}
	g, _ := newTestGracefulUpgrader(t, tc)
	g.clock = realClock{}

	outcome, err := g.RequestDrain(context.Background(), 1<<30, nil)
	if err == nil {
		t.Fatal("RequestDrain() expected error for nonexistent PID, got nil")
	}
	if outcome != DrainUnknown {
		t.Errorf("outcome = %v, want DrainUnknown", outcome)
	}
}
