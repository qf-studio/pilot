package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHosted(t *testing.T) {
	t.Run("Unset", func(t *testing.T) {
		if IsHosted() {
			t.Error("IsHosted() = true, want false when PILOT_HOSTED is unset")
		}
	})

	t.Run("SetToOne", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		if !IsHosted() {
			t.Error("IsHosted() = false, want true when PILOT_HOSTED=1")
		}
	})

	t.Run("SetToOtherValue", func(t *testing.T) {
		// Only the literal "1" enables hosted mode — "true"/"yes" do not,
		// matching the exact contract in the issue (PILOT_HOSTED=1).
		t.Setenv(hostedEnvVar, "true")
		if IsHosted() {
			t.Error("IsHosted() = true, want false when PILOT_HOSTED=true (only \"1\" enables hosted mode)")
		}
	})
}

// TestSave_HostedModeNoOp proves PILOT_HOSTED=1 makes Save() a no-op: the
// target file is neither created nor modified, and a WARN log names the
// caller (GH-4274 acceptance criterion).
func TestSave_HostedModeNoOp(t *testing.T) {
	t.Run("NewFile_NeverCreated", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		var logBuf bytes.Buffer
		originalOutput := log.Writer()
		log.SetOutput(&logBuf)
		defer log.SetOutput(originalOutput)

		if err := Save(DefaultConfig(), configPath); err != nil {
			t.Fatalf("Save should no-op (not error) in hosted mode, got: %v", err)
		}

		if _, err := os.Stat(configPath); !os.IsNotExist(err) {
			t.Errorf("expected %s to not exist after a hosted-mode Save(), stat err = %v", configPath, err)
		}

		logged := logBuf.String()
		if !strings.Contains(logged, "WARN") || !strings.Contains(logged, "PILOT_HOSTED") {
			t.Errorf("expected a WARN log mentioning PILOT_HOSTED, got: %q", logged)
		}
		if !strings.Contains(logged, "hosted_test.go") {
			t.Errorf("expected the WARN log to name the calling file:line, got: %q", logged)
		}
	})

	t.Run("ExistingFile_ContentAndMtimeUnchanged", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Write the initial file while NOT hosted, so we have a real baseline.
		initial := DefaultConfig()
		initial.Version = "pre-hosted-version"
		if err := Save(initial, configPath); err != nil {
			t.Fatalf("initial (non-hosted) save failed: %v", err)
		}

		before, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat before hosted Save() failed: %v", err)
		}
		beforeContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read before hosted Save() failed: %v", err)
		}

		t.Setenv(hostedEnvVar, "1")
		attempted := DefaultConfig()
		attempted.Version = "attempted-hosted-overwrite"
		if err := Save(attempted, configPath); err != nil {
			t.Fatalf("Save should no-op (not error) in hosted mode, got: %v", err)
		}

		after, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat after hosted Save() failed: %v", err)
		}
		afterContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read after hosted Save() failed: %v", err)
		}

		if !before.ModTime().Equal(after.ModTime()) {
			t.Errorf("mtime changed: before=%v after=%v", before.ModTime(), after.ModTime())
		}
		if string(beforeContent) != string(afterContent) {
			t.Errorf("content changed after hosted-mode Save() attempt")
		}
		if strings.Contains(string(afterContent), "attempted-hosted-overwrite") {
			t.Error("hosted-mode Save() wrote the attempted content to disk")
		}
	})
}

// TestSave_UnhostedModeUnchanged proves zero behavior change when
// PILOT_HOSTED is unset (GH-4274 acceptance criterion) — Save() still
// writes normally.
func TestSave_UnhostedModeUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Version = "unhosted-write"
	if err := Save(cfg, configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Version != "unhosted-write" {
		t.Errorf("Version = %q, want %q — Save() should write normally when PILOT_HOSTED is unset", loaded.Version, "unhosted-write")
	}
}

// TestAssertHostedInvariants covers the hard invariants of the hosted
// profile (GH-4274): auto_hot_upgrade must be false and tunnel must be
// disabled when PILOT_HOSTED=1.
func TestAssertHostedInvariants(t *testing.T) {
	t.Run("NotHosted_ViolationsAllowed", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Upgrade.AutoHotUpgrade = true
		cfg.Tunnel.Enabled = true
		if err := cfg.AssertHostedInvariants(); err != nil {
			t.Errorf("AssertHostedInvariants() should be a no-op when PILOT_HOSTED is unset, got: %v", err)
		}
	})

	t.Run("Hosted_AutoHotUpgradeTrue_Fails", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		cfg := DefaultConfig()
		cfg.Upgrade.AutoHotUpgrade = true
		cfg.Tunnel.Enabled = false

		err := cfg.AssertHostedInvariants()
		if err == nil {
			t.Fatal("expected an error when hosted + upgrade.auto_hot_upgrade=true, got nil")
		}
		if !strings.Contains(err.Error(), "auto_hot_upgrade") {
			t.Errorf("error = %q, want it to mention auto_hot_upgrade", err.Error())
		}
	})

	t.Run("Hosted_TunnelEnabled_Fails", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		cfg := DefaultConfig()
		cfg.Upgrade.AutoHotUpgrade = false
		cfg.Tunnel.Enabled = true

		err := cfg.AssertHostedInvariants()
		if err == nil {
			t.Fatal("expected an error when hosted + tunnel.enabled=true, got nil")
		}
		if !strings.Contains(err.Error(), "tunnel") {
			t.Errorf("error = %q, want it to mention tunnel", err.Error())
		}
	})

	t.Run("Hosted_Compliant_Passes", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		cfg := DefaultConfig()
		cfg.Upgrade.AutoHotUpgrade = false
		cfg.Tunnel.Enabled = false

		if err := cfg.AssertHostedInvariants(); err != nil {
			t.Errorf("AssertHostedInvariants() should pass for a compliant hosted config, got: %v", err)
		}
	})
}

// TestLoad_HostedModeAssertions proves the assertions fire at Load() —
// the daemon boot chokepoint — both for an explicit violating config file
// and for the zero-config default path (GH-4274 acceptance criterion:
// "hosted + auto_hot_upgrade=true fails boot with a clear error").
func TestLoad_HostedModeAssertions(t *testing.T) {
	t.Run("ViolatingConfigFile_FailsBoot", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
tunnel:
  enabled: false
upgrade:
  auto_hot_upgrade: true
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write test config: %v", err)
		}

		_, err := Load(configPath)
		if err == nil {
			t.Fatal("expected Load() to fail boot for hosted config with auto_hot_upgrade=true")
		}
		if !strings.Contains(err.Error(), "auto_hot_upgrade") {
			t.Errorf("error = %q, want it to clearly mention auto_hot_upgrade", err.Error())
		}
	})

	t.Run("ViolatingTunnelConfigFile_FailsBoot", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
tunnel:
  enabled: true
upgrade:
  auto_hot_upgrade: false
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write test config: %v", err)
		}

		_, err := Load(configPath)
		if err == nil {
			t.Fatal("expected Load() to fail boot for hosted config with tunnel.enabled=true")
		}
		if !strings.Contains(err.Error(), "tunnel") {
			t.Errorf("error = %q, want it to clearly mention tunnel", err.Error())
		}
	})

	t.Run("CompliantConfigFile_Boots", func(t *testing.T) {
		t.Setenv(hostedEnvVar, "1")
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
tunnel:
  enabled: false
upgrade:
  auto_hot_upgrade: false
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write test config: %v", err)
		}

		if _, err := Load(configPath); err != nil {
			t.Errorf("expected a compliant hosted config to load, got: %v", err)
		}
	})

	t.Run("NoConfigFile_DefaultsViolateInvariant_FailsBoot", func(t *testing.T) {
		// DefaultConfig() sets upgrade.auto_hot_upgrade=true (GH-3790), so a
		// hosted instance booting with no config file at all must still fail
		// loud rather than silently running against a non-compliant default.
		t.Setenv(hostedEnvVar, "1")
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "does-not-exist.yaml")

		_, err := Load(configPath)
		if err == nil {
			t.Fatal("expected Load() to fail boot when hosted + no config file (defaults violate hosted invariants)")
		}
	})

	t.Run("NotHosted_ViolatingConfigStillLoads", func(t *testing.T) {
		// Zero behavior change when PILOT_HOSTED is unset.
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
tunnel:
  enabled: true
upgrade:
  auto_hot_upgrade: true
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write test config: %v", err)
		}

		if _, err := Load(configPath); err != nil {
			t.Errorf("expected non-hosted Load() to ignore hosted invariants, got: %v", err)
		}
	})
}
