//go:build unix

package executor

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestConfigureProcessGroupDelegates pins the executor wrapper to the shared
// helper: own process group and the executor's 5 s WaitDelay (GH-4503).
func TestConfigureProcessGroupDelegates(t *testing.T) {
	cmd := exec.Command("true")
	configureProcessGroup(cmd)

	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("configureProcessGroup must set Setpgid")
	}
	if cmd.WaitDelay != 5*time.Second {
		t.Fatalf("WaitDelay = %v, want 5s", cmd.WaitDelay)
	}
}

// TestKillProcessGroupDelegates proves the wrapper reaches a grandchild: the
// group kill must take the shell and its backgrounded sleep down together.
func TestKillProcessGroupDelegates(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid

	if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
		t.Fatalf("killProcessGroup: %v", err)
	}
	_ = cmd.Wait()

	// The whole group is gone once signalling -pid fails with ESRCH.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process group %d still alive after killProcessGroup", pid)
}
