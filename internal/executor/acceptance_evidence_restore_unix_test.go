//go:build unix

package executor

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestEnvContextFIFOReturnsEmpty: with the option set a FIFO at the env
// context path is the empty text within 7 seconds, not a wait for a writer.
func TestEnvContextFIFOReturnsEmpty(t *testing.T) {
	// The mock sudo stops at the first "--", which for the kill of the
	// group (`kill -s KILL -- -<pgid>`) leaves only the group: run that kill.
	f := newAgentFilesFixture(t, `case "$1" in -[0-9]*) kill -s KILL -- "$1"; exit $?;; esac`)
	if err := syscall.Mkfifo(filepath.Join(f.ws, ".pilot-env-context.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.lock(t)

	done := make(chan string, 1)
	start := time.Now()
	go func() { done <- readEnvContext(f.ws) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("readEnvContext = %q, want the empty text", got)
		}
		if d := time.Since(start); d > 7*time.Second {
			t.Errorf("readEnvContext took %s, want under 7s", d)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("readEnvContext blocked on the FIFO for more than 7s")
	}
}
