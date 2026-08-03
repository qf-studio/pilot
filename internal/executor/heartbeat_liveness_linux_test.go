//go:build linux

package executor

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestReadProcStatSelf sanity-checks the /proc/<pid>/stat parser against the
// current test process, whose pgrp is known independently via
// syscall.Getpgid.
func TestReadProcStatSelf(t *testing.T) {
	pid := os.Getpid()
	pgrp, _, _, err := readProcStat(pid)
	if err != nil {
		t.Fatalf("readProcStat(self) error: %v", err)
	}
	wantPgrp, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("syscall.Getpgid: %v", err)
	}
	if pgrp != wantPgrp {
		t.Errorf("pgrp = %d, want %d", pgrp, wantPgrp)
	}
}

// TestReadProcStatNonexistentPID verifies a nonexistent pid produces an
// error rather than a zero-valued success — probeProcessLiveness's "fail
// toward kill" contract (GH-4668 acceptance d) depends on read errors
// propagating rather than being silently swallowed as zero.
func TestReadProcStatNonexistentPID(t *testing.T) {
	const bogusPID = 2123456789 // far beyond any real pid_max
	if _, _, _, err := readProcStat(bogusPID); err == nil {
		t.Fatal("expected error reading /proc/<bogus>/stat, got nil")
	}
}

// TestProbeProcessLivenessWithChild spawns a real process group leader that
// forks a background child, mirroring how Claude Code's Bash tool backgrounds
// `make test` inside the group configureProcessGroup creates (GH-4503). It
// asserts probeProcessLiveness finds the descendant and accumulates CPU
// ticks — the exact signal GH-4668's heartbeat monitor relies on to avoid
// killing a healthy long-running local tool call.
func TestProbeProcessLivenessWithChild(t *testing.T) {
	// Busy-loop child so utime/stime reliably advance within the polling
	// window, instead of a `sleep` which accrues ~0 CPU time.
	cmd := exec.Command("sh", "-c", "(i=0; while [ $i -lt 100000000 ]; do i=$((i+1)); done) & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	var snap processLivenessSnapshot
	var err error
	for time.Now().Before(deadline) {
		snap, err = probeProcessLiveness(pgid)
		if err == nil && snap.descendants > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("probeProcessLiveness error: %v", err)
	}
	if snap.descendants == 0 {
		t.Fatal("expected at least 1 descendant (backgrounded busy-loop child), got 0")
	}

	// CPU ticks should advance across two probes while the busy loop spins.
	first := snap
	time.Sleep(200 * time.Millisecond)
	second, err := probeProcessLiveness(pgid)
	if err != nil {
		t.Fatalf("second probeProcessLiveness error: %v", err)
	}
	if second.cpuTicks <= first.cpuTicks {
		t.Errorf("cpuTicks did not advance: first=%d second=%d", first.cpuTicks, second.cpuTicks)
	}
}

// TestProbeProcessLivenessUnknownPGID verifies that a pgid with no matching
// /proc entries at all is reported as an error, not as a zero-activity
// snapshot — an empty scan means "couldn't observe this group", which must
// fail toward killing (acceptance d), not toward granting grace.
func TestProbeProcessLivenessUnknownPGID(t *testing.T) {
	const bogusPGID = 2123456789
	if _, err := probeProcessLiveness(bogusPGID); err == nil {
		t.Fatal("expected error for pgid with no /proc entries, got nil")
	}
}
