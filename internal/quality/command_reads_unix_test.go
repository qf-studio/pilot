//go:build unix

package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMakefileFIFOReadIsBoundedByTheGateLimit: a FIFO in place of the
// Makefile blocks the read of the default builder; the gate still returns the
// time limit error within its limit (GH-6555).
func TestMakefileFIFOReadIsBoundedByTheGateLimit(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "Makefile")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	t.Cleanup(func() { unblockFIFOReaders(fifo) })

	const limit = 500 * time.Millisecond
	gate := &Gate{Name: "lint", Type: GateLint, Command: "make lint", Timeout: limit}
	r, _ := newTestRunnerWithLog(&Config{Enabled: true, Gates: []*Gate{gate}}, dir, true)

	start := time.Now()
	res, err := r.RunGate(context.Background(), "lint")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunGate: %v", err)
	}
	if elapsed > limit+2*time.Second {
		t.Fatalf("RunGate took %v, want < %v", elapsed, limit+2*time.Second)
	}
	if !errors.Is(res.Err, ErrGateTimeout) || res.Status != StatusFailed {
		t.Errorf("result = (%s, %v), want failed with ErrGateTimeout", res.Status, res.Err)
	}
}

// unblockFIFOReaders opens the FIFO for writing while a reader waits on it, so
// the blocked reads of the gate end.
func unblockFIFOReaders(fifo string) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		time.Sleep(10 * time.Millisecond)
	}
}
