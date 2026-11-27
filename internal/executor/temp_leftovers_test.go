package executor

import (
	"os"
	"strings"
	"testing"
)

// GH-6733: the quality gates run the Go tests on a box whose temporary
// directory is memory, and every leftover there accumulates across runs.
// These tests read os.TempDir() directly, so they must not run in parallel
// with other tests (no t.Parallel).

// TestTempLeftovers_SetupTestRepoWithRemote pins that setupTestRepoWithRemote
// removes both of its directories when the calling test ends, also for a
// caller that drops a path with the blank identifier.
func TestTempLeftovers_SetupTestRepoWithRemote(t *testing.T) {
	var local, remote string
	t.Run("caller", func(t *testing.T) {
		local, remote = setupTestRepoWithRemote(t)
	})
	if local == "" || remote == "" {
		t.Fatalf("setupTestRepoWithRemote returned an empty path: local=%q remote=%q", local, remote)
	}
	for _, p := range []string{local, remote} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %q to be removed after the subtest ended, stat err=%v", p, err)
		}
	}
}

// TestTempLeftovers_GH5399WorktreeIsRemoved pins that the GH-5399 push-failure
// test removes the worktree it proves is preserved: no new
// pilot-worktree-GH-5399- entry stays in os.TempDir(). Entries that were
// there before are not a failure.
func TestTempLeftovers_GH5399WorktreeIsRemoved(t *testing.T) {
	const prefix = "pilot-worktree-GH-5399-"
	before := tempDirEntriesWithPrefix(t, prefix)

	t.Run("GH5399", runGH5399PushFailurePreservesWorktree)

	for name := range tempDirEntriesWithPrefix(t, prefix) {
		if !before[name] {
			t.Errorf("leftover %q in %s after the GH-5399 test ended", name, os.TempDir())
		}
	}
}

func tempDirEntriesWithPrefix(t *testing.T, prefix string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read %s: %v", os.TempDir(), err)
	}
	names := make(map[string]bool)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			names[e.Name()] = true
		}
	}
	return names
}
