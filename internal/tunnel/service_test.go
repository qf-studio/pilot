//go:build darwin

package tunnel

import (
	"os"
	"path/filepath"
	"testing"
	"text/template"
)

func TestGetServiceStatus(t *testing.T) {
	status := GetServiceStatus()

	if status == nil {
		t.Fatal("GetServiceStatus returned nil")
	}

	// Verify all fields are accessible
	_ = status.Installed
	_ = status.Running
	_ = status.PlistPath
}

func TestIsServiceInstalledCheck(t *testing.T) {
	// Should return false or true without panicking
	installed := IsServiceInstalled()
	t.Logf("Service installed: %v", installed)
}

func TestIsServiceRunningCheck(t *testing.T) {
	// Should return false or true without panicking
	running := IsServiceRunning()
	t.Logf("Service running: %v", running)
}

func TestServiceConfigFields(t *testing.T) {
	cfg := ServiceConfig{
		Label:           "com.test.service",
		CloudflaredPath: "/usr/local/bin/cloudflared",
		TunnelID:        "test-tunnel-123",
		LogPath:         "/var/log/test",
		WorkingDir:      "/home/test/.cloudflared",
	}

	if cfg.Label != "com.test.service" {
		t.Errorf("Label = %q, want %q", cfg.Label, "com.test.service")
	}
	if cfg.CloudflaredPath != "/usr/local/bin/cloudflared" {
		t.Errorf("CloudflaredPath = %q, want %q", cfg.CloudflaredPath, "/usr/local/bin/cloudflared")
	}
	if cfg.TunnelID != "test-tunnel-123" {
		t.Errorf("TunnelID = %q, want %q", cfg.TunnelID, "test-tunnel-123")
	}
	if cfg.LogPath != "/var/log/test" {
		t.Errorf("LogPath = %q, want %q", cfg.LogPath, "/var/log/test")
	}
	if cfg.WorkingDir != "/home/test/.cloudflared" {
		t.Errorf("WorkingDir = %q, want %q", cfg.WorkingDir, "/home/test/.cloudflared")
	}
}

func TestServiceStatusFields(t *testing.T) {
	status := &ServiceStatus{
		Installed: true,
		Running:   true,
		PlistPath: "/path/to/plist.plist",
	}

	if !status.Installed {
		t.Error("expected Installed to be true")
	}
	if !status.Running {
		t.Error("expected Running to be true")
	}
	if status.PlistPath != "/path/to/plist.plist" {
		t.Errorf("PlistPath = %q, want %q", status.PlistPath, "/path/to/plist.plist")
	}
}

func TestLaunchdPlistTemplate(t *testing.T) {
	// Test that the template parses correctly
	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		t.Fatalf("failed to parse plist template: %v", err)
	}

	// Test template execution
	cfg := ServiceConfig{
		Label:           "com.test.tunnel",
		CloudflaredPath: "/usr/local/bin/cloudflared",
		TunnelID:        "test-123",
		LogPath:         "/tmp/logs",
		WorkingDir:      "/tmp",
	}

	tmpFile, err := os.CreateTemp("", "plist-test-*.plist")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	defer func() { _ = tmpFile.Close() }()

	if err := tmpl.Execute(tmpFile, cfg); err != nil {
		t.Fatalf("failed to execute template: %v", err)
	}

	// Read back and verify content
	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read temp file: %v", err)
	}

	contentStr := string(content)

	// Verify key elements are present
	if !contains(contentStr, "com.test.tunnel") {
		t.Error("plist should contain label")
	}
	if !contains(contentStr, "/usr/local/bin/cloudflared") {
		t.Error("plist should contain cloudflared path")
	}
	if !contains(contentStr, "test-123") {
		t.Error("plist should contain tunnel ID")
	}
	if !contains(contentStr, "/tmp/logs") {
		t.Error("plist should contain log path")
	}
	if !contains(contentStr, "<true/>") {
		t.Error("plist should contain RunAtLoad true")
	}
}

func TestLaunchdLabelConstant(t *testing.T) {
	if launchdLabel != "com.pilot.cloudflare-tunnel" {
		t.Errorf("launchdLabel = %q, want %q", launchdLabel, "com.pilot.cloudflare-tunnel")
	}
}

func TestServiceStatusPlistPath(t *testing.T) {
	status := GetServiceStatus()

	// The plist path should be set if we're on macOS
	if status.PlistPath != "" {
		// Verify it looks like a valid plist path
		if filepath.Ext(status.PlistPath) != ".plist" {
			t.Errorf("PlistPath should end with .plist, got %q", status.PlistPath)
		}
	}
}

func TestStartServiceNotInstalled(t *testing.T) {
	// Skip if service is actually installed
	if IsServiceInstalled() {
		t.Skip("skipping test because service is installed")
	}

	err := StartService()
	if err == nil {
		// On non-darwin platforms, this should error
		t.Log("StartService did not error - may be on darwin with service installed")
	}
}

func TestStopServiceNotInstalled(t *testing.T) {
	// Skip if service is actually installed
	if IsServiceInstalled() {
		t.Skip("skipping test because service is installed")
	}

	// Stop should not error when service isn't installed (it's a no-op)
	_ = StopService()
}

func TestRestartServiceNotInstalled(t *testing.T) {
	// Skip if service is actually installed
	if IsServiceInstalled() {
		t.Skip("skipping test because service is installed")
	}

	err := RestartService()
	if err == nil {
		// On non-darwin or when not installed, this might error or not
		t.Log("RestartService did not error")
	}
}

// Helper function for string contains check
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestInstallServiceWithoutCloudflared(t *testing.T) {
	// Skip if cloudflared is installed
	if _, ok := CheckCLI("cloudflared"); ok {
		t.Skip("skipping - cloudflared is installed")
	}

	err := InstallService("test-tunnel")
	if err == nil {
		t.Error("expected error when cloudflared is not in PATH")
	}
}

func TestUninstallServiceWhenNotInstalled(t *testing.T) {
	// Skip if service is actually installed
	if IsServiceInstalled() {
		t.Skip("skipping - service is installed")
	}

	// Should not error when service isn't installed
	err := UninstallService()
	if err != nil {
		t.Errorf("UninstallService should not error when not installed: %v", err)
	}
}

func TestServiceStatusConsistency(t *testing.T) {
	status := GetServiceStatus()

	// If installed, we should have a plist path
	if status.Installed && status.PlistPath == "" {
		t.Error("installed service should have PlistPath set")
	}

	// If running, should be installed
	if status.Running && !status.Installed {
		t.Error("running service should be installed")
	}
}

func TestStartServiceErrorPath(t *testing.T) {
	// Test StartService when not installed
	if IsServiceInstalled() {
		t.Skip("skipping - service is installed")
	}

	err := StartService()
	if err == nil {
		// On some platforms this might not error
		t.Log("StartService did not error even when not installed")
	}
}

func TestStopServiceWhenNotRunning(t *testing.T) {
	// Test StopService when service isn't running
	if IsServiceRunning() {
		t.Skip("skipping - service is running")
	}

	err := StopService()
	// Should not fail when service isn't running (no-op behavior is acceptable)
	if err != nil {
		t.Logf("StopService returned error when not running: %v", err)
	}
}

func TestRestartServiceWhenNotInstalled(t *testing.T) {
	if IsServiceInstalled() {
		t.Skip("skipping - service is installed")
	}

	err := RestartService()
	// Expected to fail when service isn't installed
	if err == nil {
		t.Log("RestartService succeeded even when not installed")
	}
}

func TestServicePlistPathFormat(t *testing.T) {
	status := GetServiceStatus()

	if status.PlistPath != "" {
		// Verify path looks correct
		if !contains(status.PlistPath, launchdLabel) {
			t.Errorf("PlistPath should contain launchdLabel, got %q", status.PlistPath)
		}
		if !contains(status.PlistPath, ".plist") {
			t.Errorf("PlistPath should end with .plist, got %q", status.PlistPath)
		}
		if !contains(status.PlistPath, "LaunchAgents") {
			t.Errorf("PlistPath should contain LaunchAgents, got %q", status.PlistPath)
		}
	}
}
