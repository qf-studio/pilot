package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// fakeProc builds a fabricated /proc-shaped directory tree under t.TempDir()
// so checkDBFD can be exercised without a live daemon process. procs maps
// pid -> the fd target it should have open for "pilot.db" (or "" to fake an
// unreadable/missing fd table — INCONCLUSIVE).
func fakeProc(t *testing.T, procs map[int]string) string {
	t.Helper()
	root := t.TempDir()

	for pid, target := range procs {
		pidDir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(pidDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pidDir, "comm"), []byte("pilot\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if target == "" {
			continue // no fd/ dir at all — simulates a permission-denied read
		}
		fdDir := filepath.Join(pidDir, "fd")
		if err := os.MkdirAll(fdDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A real process has many fds; only one points at the DB. Add a
		// couple of unrelated ones to make sure the scan skips them.
		if err := os.Symlink("/dev/null", filepath.Join(fdDir, "0")); err != nil {
			t.Skipf("symlinks unsupported on this platform: %v", err)
		}
		if err := os.Symlink(filepath.Join(filepath.Dir(target), "pilot.lock"), filepath.Join(fdDir, "1")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(fdDir, "2")); err != nil {
			t.Fatal(err)
		}
	}

	// Add a non-pilot process to make sure the comm-name filter works.
	otherDir := filepath.Join(root, "99999")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "comm"), []byte("sshd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a non-numeric entry to make sure the pid filter works.
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}

	return root
}

// GH-4393: config and the daemon's own startup logs both looked correct
// while it silently wrote to a shadow ledger for 3 hours. checkDBFD must
// catch that class of drift by comparing against the daemon's actual open
// fd, not anything the process could have gotten wrong about itself.
func TestCheckDBFD(t *testing.T) {
	t.Run("daemon fd matches configured path — no mismatch", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}
		want := resolvedDBPath(cfg)

		procRoot := fakeProc(t, map[int]string{4242: want})

		res, err := checkDBFD(cfg, procRoot)
		if err != nil {
			t.Fatalf("checkDBFD() error = %v", err)
		}
		if len(res.Mismatched) != 0 {
			t.Errorf("Mismatched = %v, want none", res.Mismatched)
		}
		if len(res.Inconclusive) != 0 {
			t.Errorf("Inconclusive = %v, want none", res.Inconclusive)
		}
		if got := res.OpenDBPath[4242]; got != want {
			t.Errorf("OpenDBPath[4242] = %q, want %q", got, want)
		}
	})

	t.Run("daemon fd points at a different path — mismatch flagged", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

		shadowDir := realpath(t, t.TempDir())
		shadowPath := filepath.Join(shadowDir, "pilot.db")

		procRoot := fakeProc(t, map[int]string{4242: shadowPath})

		res, err := checkDBFD(cfg, procRoot)
		if err != nil {
			t.Fatalf("checkDBFD() error = %v", err)
		}
		if len(res.Mismatched) != 1 || res.Mismatched[0] != 4242 {
			t.Errorf("Mismatched = %v, want [4242]", res.Mismatched)
		}
		if got := res.OpenDBPath[4242]; got != shadowPath {
			t.Errorf("OpenDBPath[4242] = %q, want %q (the shadow path actually open)", got, shadowPath)
		}
	})

	t.Run("fd table unreadable — inconclusive, not silently OK", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

		procRoot := fakeProc(t, map[int]string{4242: ""})

		res, err := checkDBFD(cfg, procRoot)
		if err != nil {
			t.Fatalf("checkDBFD() error = %v", err)
		}
		if len(res.Inconclusive) != 1 || res.Inconclusive[0] != 4242 {
			t.Errorf("Inconclusive = %v, want [4242]", res.Inconclusive)
		}
		if len(res.Mismatched) != 0 {
			t.Errorf("Mismatched = %v, want none", res.Mismatched)
		}
	})

	t.Run("no daemon process found — empty result, not an error", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

		procRoot := fakeProc(t, map[int]string{})

		res, err := checkDBFD(cfg, procRoot)
		if err != nil {
			t.Fatalf("checkDBFD() error = %v", err)
		}
		if len(res.DaemonPIDs) != 0 {
			t.Errorf("DaemonPIDs = %v, want none", res.DaemonPIDs)
		}
	})

	t.Run("non-pilot process is ignored", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

		procRoot := fakeProc(t, map[int]string{})

		pids, err := findDaemonPIDs(procRoot, os.Getpid())
		if err != nil {
			t.Fatalf("findDaemonPIDs() error = %v", err)
		}
		for _, p := range pids {
			if p == 99999 {
				t.Errorf("findDaemonPIDs() included sshd pid 99999, comm filter failed")
			}
		}
		_ = cfg
	})
}
