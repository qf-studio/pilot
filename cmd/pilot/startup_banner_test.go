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
		want := filepath.Join(dir, "pilot.db")
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

		want := filepath.Join(target, "pilot.db")
		if got := resolveMemoryDBPath(link); got != want {
			t.Errorf("resolveMemoryDBPath(%q) = %q, want %q (target of symlink)", link, got, want)
		}
	})
}
