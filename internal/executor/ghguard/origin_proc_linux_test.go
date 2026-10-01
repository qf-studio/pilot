//go:build linux

package ghguard

import (
	"os"
	"testing"
)

// GH-5553: the /proc/<pid>/cmdline detection path (Linux only).
func TestParentIsTestProcess_ProcPath(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("/proc not mounted in this environment")
	}
	if got := runWithParent(t, "-test.v"); !got.proc {
		t.Errorf("/proc path: parent with -test.v not detected")
	}
	if got := runWithParent(t, "--plain"); got.proc {
		t.Errorf("/proc path: plain parent detected as test process")
	}
}
