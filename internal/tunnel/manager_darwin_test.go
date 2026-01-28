//go:build darwin

package tunnel

import "testing"

func TestLaunchdConstants(t *testing.T) {
	if launchdLabel != "com.pilot.cloudflare-tunnel" {
		t.Errorf("launchdLabel = %q, want %q", launchdLabel, "com.pilot.cloudflare-tunnel")
	}
}

func TestServiceConfigStruct(t *testing.T) {
	cfg := ServiceConfig{
		Label:           "test-label",
		CloudflaredPath: "/usr/local/bin/cloudflared",
		TunnelID:        "abc123",
		LogPath:         "/var/log",
		WorkingDir:      "/home/user",
	}

	if cfg.Label != "test-label" {
		t.Errorf("Label = %q, want %q", cfg.Label, "test-label")
	}
	if cfg.CloudflaredPath != "/usr/local/bin/cloudflared" {
		t.Errorf("CloudflaredPath = %q, want %q", cfg.CloudflaredPath, "/usr/local/bin/cloudflared")
	}
	if cfg.TunnelID != "abc123" {
		t.Errorf("TunnelID = %q, want %q", cfg.TunnelID, "abc123")
	}
	if cfg.LogPath != "/var/log" {
		t.Errorf("LogPath = %q, want %q", cfg.LogPath, "/var/log")
	}
	if cfg.WorkingDir != "/home/user" {
		t.Errorf("WorkingDir = %q, want %q", cfg.WorkingDir, "/home/user")
	}
}
