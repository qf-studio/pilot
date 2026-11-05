//go:build unix

package quality

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// TestExecuteCommand_TimeoutBoundsWallTime: a gate whose shell backgrounds a
// descendant that inherits the output pipes must still return at the gate
// timeout. Before GH-6081 only sh was killed and cmd.Run blocked until the
// sleeps exited.
func TestExecuteCommand_TimeoutBoundsWallTime(t *testing.T) {
	gate := &Gate{Name: "bounds", Type: GateCustom, Timeout: 500 * time.Millisecond}
	runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())

	start := time.Now()
	_, output, err := runner.executeCommand(context.Background(), gate, "echo started; sleep 8 & sleep 8", &changeScope{full: true})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrGateTimeout) {
		t.Fatalf("err = %v, want ErrGateTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("executeCommand took %v, want < 3s (timeout must bound wall time)", elapsed)
	}
	if output == "" {
		t.Error("output captured before the timeout must still be returned")
	}
}

// TestExecuteCommand_WaitDelayCountsAsTimeout: a child that leaves the group
// (setsid) survives the group kill and holds the pipe; WaitDelay must release
// the wait and the result must still be ErrGateTimeout.
func TestExecuteCommand_WaitDelayCountsAsTimeout(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	old := gateWaitDelay
	gateWaitDelay = 500 * time.Millisecond
	t.Cleanup(func() { gateWaitDelay = old })

	gate := &Gate{Name: "waitdelay", Type: GateCustom, Timeout: 500 * time.Millisecond}
	runner := NewRunner(&Config{Enabled: true, Gates: []*Gate{gate}}, t.TempDir())

	start := time.Now()
	_, _, err := runner.executeCommand(context.Background(), gate, "echo started; setsid sleep 6 & sleep 6", &changeScope{full: true})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrGateTimeout) {
		t.Fatalf("err = %v, want ErrGateTimeout", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("executeCommand took %v, want < 3s (WaitDelay must release the pipe wait)", elapsed)
	}
}
