package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/config"
)

func TestExtractConfigFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no config flag", []string{"--github", "--replace"}, ""},
		{"space form", []string{"--config", "custom.yaml", "--github"}, "custom.yaml"},
		{"equals form", []string{"--config=custom.yaml", "--github"}, "custom.yaml"},
		{"space form at end, no value", []string{"--github", "--config"}, ""},
		{"empty args", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractConfigFlag(tt.args); got != tt.want {
				t.Errorf("extractConfigFlag(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestStopDaemonLockDir(t *testing.T) {
	t.Run("uses configured Memory.Path", func(t *testing.T) {
		cfg := &config.Config{Memory: &config.MemoryConfig{Path: "/custom/memory/path"}}
		if got := stopDaemonLockDir(cfg); got != "/custom/memory/path" {
			t.Errorf("stopDaemonLockDir() = %q, want /custom/memory/path", got)
		}
	})

	t.Run("falls back to ~/.pilot/data when Memory is nil", func(t *testing.T) {
		// Pin HOME rather than reading the ambient os.UserHomeDir(): other
		// tests in this package (e.g. TestAddProjectToConfig_NoDuplicate)
		// mutate the process-wide HOME env var without fully restoring it,
		// which otherwise makes this assertion order-dependent.
		cfg := &config.Config{}
		home := t.TempDir()
		t.Setenv("HOME", home)
		want := filepath.Join(home, ".pilot", "data")
		if got := stopDaemonLockDir(cfg); got != want {
			t.Errorf("stopDaemonLockDir() = %q, want %q", got, want)
		}
	})
}

func TestStopDaemonNoLockFile(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

	if err := stopDaemon(cfg, time.Second); err != nil {
		t.Fatalf("stopDaemon with no lock file should be a no-op, got: %v", err)
	}
}

func TestStopDaemonStalePIDReleasesImmediately(t *testing.T) {
	// A lock file with a pid recorded but not actually flock'd (e.g. left
	// behind by a process that crashed before the OS reclaimed the file
	// handle across a reboot/restore) should be treated as free: SIGTERM to
	// the dead pid is a harmless no-op, and stopDaemon must not block for
	// the full timeout waiting on a lock nobody holds.
	dir := t.TempDir()
	lockPath := dir + "/pilot.lock"
	if err := os.WriteFile(lockPath, []byte("999999999"), 0o644); err != nil {
		t.Fatalf("write stale lock file: %v", err)
	}

	cfg := &config.Config{Memory: &config.MemoryConfig{Path: dir}}

	start := time.Now()
	if err := stopDaemon(cfg, 5*time.Second); err != nil {
		t.Fatalf("stopDaemon with stale pid should succeed, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("stopDaemon took %s, expected it to release almost immediately (lock wasn't actually held)", elapsed)
	}
}
