//go:build unix

package executor

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestRunAsUserProjectContextReadStopsWithRun: a FIFO at the README path
// blocks the read as the agent user; the read stops with the context of the
// run, not after agentFileTimeout, and the prompt goes without the context.
func TestRunAsUserProjectContextReadStopsWithRun(t *testing.T) {
	// The mock sudo stops at the first "--", which for the kill of the
	// group (`kill -s KILL -- -<pgid>`) leaves only the group: run that kill.
	f := newAgentFilesFixture(t, `case "$1" in -[0-9]*) kill -s KILL -- "$1"; exit $?;; esac`)
	agentDir := filepath.Join(f.ws, ".agent")
	mkdirMode(t, agentDir, 0o755)
	if err := syscall.Mkfifo(filepath.Join(agentDir, "DEVELOPMENT-README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.lock(t)
	_ = captureLog(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := loadProjectContextCtx(ctx, agentDir)
	if elapsed := time.Since(start); elapsed > runAsWaitDelay/2 {
		t.Errorf("the read took %v after the run ended, want it stopped with the run", elapsed)
	}
	if got != "" {
		t.Errorf("context = %q, want empty", got)
	}
}
