package autopilot

import (
	"os"
	"strings"
	"testing"
)

// mutation pin: reconcileLaneStarvation doc comment uses the dropped word "due" -> this test fails
func TestReconcileLaneStarvationDocDropsDue(t *testing.T) {
	src, err := os.ReadFile("lane_starvation.go")
	if err != nil {
		t.Fatalf("read lane_starvation.go: %v", err)
	}
	if strings.Contains(string(src), "and is due (cooldown") {
		t.Error("reconcileLaneStarvation doc comment still says \"is due (cooldown\"; use \"the cooldown (per lane) is clear\"")
	}
}
