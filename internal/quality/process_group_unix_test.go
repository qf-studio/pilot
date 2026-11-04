//go:build unix

package quality

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A timeout must end the gate even when the command's descendants keep the
// stdout/stderr pipes open: `sleep 8 & sleep 8` leaves two sleeps holding them.
func TestExecuteCommand_TimeoutBoundsWallTime(t *testing.T) {
	g := &Gate{Name: "orphan", Type: GateTest, Timeout: 500 * time.Millisecond}
	r := NewRunner(&Config{Enabled: true, Gates: []*Gate{g}}, t.TempDir())

	start := time.Now()
	_, _, err := r.executeCommand(context.Background(), g, "sleep 8 & sleep 8", &changeScope{full: true})

	if !errors.Is(err, ErrGateTimeout) {
		t.Errorf("err = %v, want ErrGateTimeout", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s with a 500ms timeout; the gate waited for its descendants", d.Round(time.Millisecond))
	}
}
