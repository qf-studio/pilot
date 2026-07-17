package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/singleton"
)

// GH-4393: a config-supplied state dir that didn't exist yet was silently
// auto-created by singleton.Acquire's os.MkdirAll, and the daemon happily
// started writing a brand-new, empty ledger there — while the real ledger,
// with a lockfile-era history, sat untouched at a different path for 3
// hours before anyone noticed. checkShadowInit must refuse to let that
// auto-create happen unnoticed whenever the well-known default location
// already shows evidence of a prior run.
func TestCheckShadowInit(t *testing.T) {
	t.Run("dir already exists — not an auto-create, no error", func(t *testing.T) {
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)

		dir := filepath.Join(t.TempDir(), "configured")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := checkShadowInit(dir); err != nil {
			t.Errorf("checkShadowInit() = %v, want nil (dir already exists)", err)
		}
	})

	t.Run("genuine first run — neither configured dir nor default has history", func(t *testing.T) {
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)

		dir := filepath.Join(t.TempDir(), "configured", "data")

		if err := checkShadowInit(dir); err != nil {
			t.Errorf("checkShadowInit() = %v, want nil (nothing has ever run on this host)", err)
		}
	})

	t.Run("configured dir IS the default — nothing elsewhere to compare against", func(t *testing.T) {
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)

		dir := filepath.Join(home, ".pilot", "data")

		if err := checkShadowInit(dir); err != nil {
			t.Errorf("checkShadowInit() = %v, want nil (dir is the default itself)", err)
		}
	})

	t.Run("default has a pilot.lock — refuses with errShadowInit", func(t *testing.T) {
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)

		defaultDir := filepath.Join(home, ".pilot", "data")
		if err := os.MkdirAll(defaultDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(defaultDir, singleton.LockFileName), []byte("12345"), 0o644); err != nil {
			t.Fatal(err)
		}

		dir := filepath.Join(t.TempDir(), "configured", "data")

		err := checkShadowInit(dir)
		if err == nil {
			t.Fatal("checkShadowInit() = nil, want error (default location has a lockfile-era ledger)")
		}
		var shadowErr *errShadowInit
		if !errors.As(err, &shadowErr) {
			t.Fatalf("checkShadowInit() error type = %T, want *errShadowInit", err)
		}
		if !shadowErr.lockExists || shadowErr.dbExists {
			t.Errorf("errShadowInit = %+v, want lockExists=true dbExists=false", shadowErr)
		}
		if shadowErr.dir != dir || shadowErr.defaultDir != defaultDir {
			t.Errorf("errShadowInit dir/defaultDir = %q/%q, want %q/%q", shadowErr.dir, shadowErr.defaultDir, dir, defaultDir)
		}
	})

	t.Run("default has a pilot.db — refuses with errShadowInit", func(t *testing.T) {
		home := realpath(t, t.TempDir())
		t.Setenv("HOME", home)

		defaultDir := filepath.Join(home, ".pilot", "data")
		if err := os.MkdirAll(defaultDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(defaultDir, "pilot.db"), []byte("sqlite"), 0o644); err != nil {
			t.Fatal(err)
		}

		dir := filepath.Join(t.TempDir(), "configured", "data")

		err := checkShadowInit(dir)
		if err == nil {
			t.Fatal("checkShadowInit() = nil, want error (default location has a lockfile-era ledger)")
		}
		var shadowErr *errShadowInit
		if !errors.As(err, &shadowErr) {
			t.Fatalf("checkShadowInit() error type = %T, want *errShadowInit", err)
		}
		if shadowErr.lockExists || !shadowErr.dbExists {
			t.Errorf("errShadowInit = %+v, want lockExists=false dbExists=true", shadowErr)
		}
	})

	t.Run("errShadowInit message names both paths", func(t *testing.T) {
		e := &errShadowInit{dir: "/tmp/configured", defaultDir: "/tmp/default", lockExists: true, dbExists: false}
		msg := e.Error()
		if !strings.Contains(msg, "/tmp/configured") || !strings.Contains(msg, "/tmp/default") {
			t.Errorf("errShadowInit.Error() = %q, want it to mention both paths", msg)
		}
	})
}

// TestAcquireDaemonLockRefusesShadowInit exercises the guard through its
// real call site: acquireDaemonLock must surface checkShadowInit's error
// instead of proceeding to singleton.Acquire (which would MkdirAll the
// configured dir and start a fresh, empty ledger).
func TestAcquireDaemonLockRefusesShadowInit(t *testing.T) {
	home := realpath(t, t.TempDir())
	t.Setenv("HOME", home)

	defaultDir := filepath.Join(home, ".pilot", "data")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDir, singleton.LockFileName), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	configuredDir := filepath.Join(t.TempDir(), "configured", "data")
	cfg := &config.Config{Memory: &config.MemoryConfig{Path: configuredDir}}

	_, err := acquireDaemonLock(cfg, false)
	if err == nil {
		t.Fatal("acquireDaemonLock() = nil error, want refusal (shadow init guard should have fired)")
	}
	var shadowErr *errShadowInit
	if !errors.As(err, &shadowErr) {
		t.Fatalf("acquireDaemonLock() error type = %T, want *errShadowInit", err)
	}

	if fileExists(configuredDir) {
		t.Errorf("acquireDaemonLock() must not create %q when refusing", configuredDir)
	}
}
