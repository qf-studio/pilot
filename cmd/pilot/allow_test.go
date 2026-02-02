package main

import (
	"path/filepath"
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
)

func TestAllowCommand_ValidateUserID(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "valid numeric ID",
			args:    []string{"123456789"},
			wantErr: false,
		},
		{
			name:    "invalid non-numeric ID",
			args:    []string{"@username"},
			wantErr: true,
		},
		{
			name:    "invalid empty",
			args:    []string{""},
			wantErr: true,
		},
		{
			name:    "valid negative ID (groups) with separator",
			args:    []string{"--", "-123456789"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newAllowCmd()
			cmd.SetArgs(tt.args)

			// Create temp config for test
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			// Create minimal config
			cfg := config.DefaultConfig()
			if err := config.Save(cfg, configPath); err != nil {
				t.Fatalf("failed to create test config: %v", err)
			}

			// Set the config file path
			oldCfgFile := cfgFile
			cfgFile = configPath
			defer func() { cfgFile = oldCfgFile }()

			err := cmd.Execute()
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAddAllowedUser(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create minimal config
	cfg := config.DefaultConfig()
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// Add a user
	if err := addAllowedUser(123456789); err != nil {
		t.Fatalf("addAllowedUser() error = %v", err)
	}

	// Reload and verify
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if cfg.Adapters == nil || cfg.Adapters.Telegram == nil {
		t.Fatal("Telegram config not initialized")
	}

	if len(cfg.Adapters.Telegram.AllowedIDs) != 1 {
		t.Errorf("expected 1 allowed ID, got %d", len(cfg.Adapters.Telegram.AllowedIDs))
	}

	if cfg.Adapters.Telegram.AllowedIDs[0] != 123456789 {
		t.Errorf("expected ID 123456789, got %d", cfg.Adapters.Telegram.AllowedIDs[0])
	}
}

func TestAddAllowedUser_PreventsDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config with existing user
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram.AllowedIDs = []int64{123456789}
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// Try to add the same user again (should not error, just skip)
	if err := addAllowedUser(123456789); err != nil {
		t.Fatalf("addAllowedUser() error = %v", err)
	}

	// Reload and verify no duplicate
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if len(cfg.Adapters.Telegram.AllowedIDs) != 1 {
		t.Errorf("expected 1 allowed ID (no duplicate), got %d", len(cfg.Adapters.Telegram.AllowedIDs))
	}
}

func TestRemoveAllowedUser(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config with users
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram.AllowedIDs = []int64{111, 222, 333}
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// Remove middle user
	if err := removeAllowedUser(222); err != nil {
		t.Fatalf("removeAllowedUser() error = %v", err)
	}

	// Reload and verify
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if len(cfg.Adapters.Telegram.AllowedIDs) != 2 {
		t.Errorf("expected 2 allowed IDs after removal, got %d", len(cfg.Adapters.Telegram.AllowedIDs))
	}

	// Verify correct users remain
	expected := map[int64]bool{111: true, 333: true}
	for _, id := range cfg.Adapters.Telegram.AllowedIDs {
		if !expected[id] {
			t.Errorf("unexpected ID %d in allowed list", id)
		}
	}
}

func TestRemoveAllowedUser_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config with users
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram.AllowedIDs = []int64{111, 222}
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// Try to remove non-existent user (should not error)
	if err := removeAllowedUser(999); err != nil {
		t.Fatalf("removeAllowedUser() error = %v", err)
	}

	// Verify list unchanged
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if len(cfg.Adapters.Telegram.AllowedIDs) != 2 {
		t.Errorf("expected 2 allowed IDs (unchanged), got %d", len(cfg.Adapters.Telegram.AllowedIDs))
	}
}

func TestListAllowedUsers(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config with users
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram.AllowedIDs = []int64{111, 222, 333}
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// List should succeed (output goes to stdout)
	if err := listAllowedUsers(); err != nil {
		t.Fatalf("listAllowedUsers() error = %v", err)
	}
}

func TestListAllowedUsers_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Create config with no users
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram.AllowedIDs = []int64{}
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	// Set the config file path
	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	// List should succeed with empty message
	if err := listAllowedUsers(); err != nil {
		t.Fatalf("listAllowedUsers() error = %v", err)
	}
}

func TestAllowCommand_NoArgs(t *testing.T) {
	cmd := newAllowCmd()
	cmd.SetArgs([]string{})

	// Should error without --list flag
	err := cmd.Execute()
	if err == nil {
		t.Error("expected error when no args and no --list flag")
	}
}

func TestAllowCommand_ListFlag(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := config.DefaultConfig()
	if err := config.Save(cfg, configPath); err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}

	oldCfgFile := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = oldCfgFile }()

	cmd := newAllowCmd()
	cmd.SetArgs([]string{"--list"})

	// Should succeed with --list flag
	if err := cmd.Execute(); err != nil {
		t.Errorf("Execute() with --list error = %v", err)
	}
}
