package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolveMemoryDBPath covers GH-4393: the daemon must log the
// symlink-resolved absolute DB path, not just the configured one, so a
// shadow-path open (a configured path that silently diverges from where it
// actually resolves on disk) is visible in daemon.log.
func TestResolveMemoryDBPath(t *testing.T) {
	t.Run("plain directory resolves to itself", func(t *testing.T) {
		dir := t.TempDir()
		// On macOS, t.TempDir() returns a path under /var, which is itself
		// a symlink to /private/var. resolveMemoryDBPath canonicalizes via
		// EvalSymlinks, so the expectation must be canonicalized too.
		canonicalDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", dir, err)
		}
		want := filepath.Join(canonicalDir, "pilot.db")
		if got := resolveMemoryDBPath(dir); got != want {
			t.Errorf("resolveMemoryDBPath(%q) = %q, want %q", dir, got, want)
		}
	})

	t.Run("nonexistent directory falls back to unresolved path", func(t *testing.T) {
		root := t.TempDir()
		missing := filepath.Join(root, "does-not-exist-yet")
		want := filepath.Join(missing, "pilot.db")
		if got := resolveMemoryDBPath(missing); got != want {
			t.Errorf("resolveMemoryDBPath(%q) = %q, want %q", missing, got, want)
		}
	})

	t.Run("symlinked directory resolves to its target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require elevated privileges on Windows")
		}
		root := t.TempDir()
		target := filepath.Join(root, "canonical")
		if err := os.MkdirAll(target, 0755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		link := filepath.Join(root, "configured")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}

		// Canonicalize target the same way resolveMemoryDBPath does, so this
		// still passes when root itself sits under a symlinked path (e.g.
		// macOS /var -> /private/var).
		canonicalTarget, err := filepath.EvalSymlinks(target)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", target, err)
		}
		want := filepath.Join(canonicalTarget, "pilot.db")
		if got := resolveMemoryDBPath(link); got != want {
			t.Errorf("resolveMemoryDBPath(%q) = %q, want %q (target of symlink)", link, got, want)
		}
	})
}
