package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
)

// GH-4393: an absolute Memory.Path in config silently bypassed a host
// directory shim, leaving the daemon writing to a shadow ledger for hours
// undetected. resolvedDBPath must always return an absolute,
// symlink-evaluated path so that drift is visible in the startup banner.
func TestResolvedDBPath(t *testing.T) {
	t.Run("uses configured Memory.Path, joins pilot.db", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}
		want := filepath.Join(dir, "pilot.db")
		if got := resolvedDBPath(cfg); got != want {
			t.Errorf("resolvedDBPath() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.pilot/data when Memory is nil", func(t *testing.T) {
		// Pin HOME rather than reading the ambient os.UserHomeDir() (mirrors
		// TestStopDaemonLockDir's rationale — other tests mutate HOME).
		cfg := &config.Config{}
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)
		want := filepath.Join(home, ".pilot", "data", "pilot.db")
		if got := resolvedDBPath(cfg); got != want {
			t.Errorf("resolvedDBPath() = %q, want %q", got, want)
		}
	})

	t.Run("resolves relative Memory.Path to absolute", func(t *testing.T) {
		dir := realpath(t, t.TempDir())
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(wd, dir)
		if err != nil {
			t.Skip("temp dir not relative to working directory on this platform")
		}
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: rel}}
		want := filepath.Join(dir, "pilot.db")
		if got := resolvedDBPath(cfg); got != want {
			t.Errorf("resolvedDBPath() = %q, want %q (must be absolute)", got, want)
		}
		if !filepath.IsAbs(resolvedDBPath(cfg)) {
			t.Errorf("resolvedDBPath() = %q, want absolute path", resolvedDBPath(cfg))
		}
	})

	t.Run("evaluates symlinked Memory.Path to the real directory (shim case)", func(t *testing.T) {
		realDir := realpath(t, t.TempDir())
		parent := t.TempDir()
		shim := filepath.Join(parent, "shimmed")
		if err := os.Symlink(realDir, shim); err != nil {
			t.Skipf("symlinks unsupported on this platform: %v", err)
		}
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: shim}}
		want := filepath.Join(realDir, "pilot.db")
		if got := resolvedDBPath(cfg); got != want {
			t.Errorf("resolvedDBPath() = %q, want %q (real path behind shim)", got, want)
		}
	})
}

// realpath returns the symlink-evaluated form of p, skipping the test if
// resolution fails. Some platforms (e.g. macOS, where /tmp is itself a
// symlink to /private/tmp) hand out temp dirs that aren't already in
// canonical form, which would otherwise make assertions about
// resolvedDBPath's symlink evaluation flaky.
func realpath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Skipf("could not resolve symlinks for %q: %v", p, err)
	}
	return real
}
