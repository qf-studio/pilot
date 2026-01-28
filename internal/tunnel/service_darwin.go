//go:build darwin

package tunnel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const launchdLabel = "com.pilot.cloudflare-tunnel"

// launchdPlist is the template for macOS launchd service
const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.CloudflaredPath}}</string>
        <string>tunnel</string>
        <string>run</string>
        <string>{{.TunnelID}}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{.LogPath}}/cloudflared.out.log</string>
    <key>StandardErrorPath</key>
    <string>{{.LogPath}}/cloudflared.err.log</string>
    <key>WorkingDirectory</key>
    <string>{{.WorkingDir}}</string>
</dict>
</plist>
`

// ServiceConfig holds launchd service configuration
type ServiceConfig struct {
	Label           string
	CloudflaredPath string
	TunnelID        string
	LogPath         string
	WorkingDir      string
}

// InstallService installs the tunnel as a macOS launchd service
func InstallService(tunnelID string) error {
	// Find cloudflared binary
	cloudflaredPath, err := exec.LookPath("cloudflared")
	if err != nil {
		return fmt.Errorf("cloudflared not found in PATH: %w", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// Setup paths
	logPath := filepath.Join(homeDir, ".pilot", "logs")
	if err := os.MkdirAll(logPath, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	workingDir := filepath.Join(homeDir, ".cloudflared")
	if err := os.MkdirAll(workingDir, 0755); err != nil {
		return fmt.Errorf("failed to create working directory: %w", err)
	}

	// Generate plist
	config := ServiceConfig{
		Label:           launchdLabel,
		CloudflaredPath: cloudflaredPath,
		TunnelID:        tunnelID,
		LogPath:         logPath,
		WorkingDir:      workingDir,
	}

	tmpl, err := template.New("plist").Parse(launchdPlistTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse plist template: %w", err)
	}

	// Write plist file
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")

	// Create LaunchAgents directory if needed
	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("failed to create plist file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := tmpl.Execute(f, config); err != nil {
		return fmt.Errorf("failed to write plist: %w", err)
	}

	// Load the service
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("failed to load service: %w", err)
	}

	return nil
}

// UninstallService removes the launchd service
func UninstallService() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")

	// Check if service exists
	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return nil // Already uninstalled
	}

	// Unload the service (ignore error - service might not be loaded)
	_ = exec.Command("launchctl", "unload", plistPath).Run()

	// Remove plist file
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove plist file: %w", err)
	}

	return nil
}

// IsServiceInstalled checks if the launchd service is installed
func IsServiceInstalled() bool {
	homeDir, _ := os.UserHomeDir()
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")
	_, err := os.Stat(plistPath)
	return err == nil
}

// IsServiceRunning checks if the launchd service is running
func IsServiceRunning() bool {
	output, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), launchdLabel)
}

// StartService starts the launchd service
func StartService() error {
	if !IsServiceInstalled() {
		return fmt.Errorf("service not installed")
	}

	homeDir, _ := os.UserHomeDir()
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")

	// Load service (starts it due to RunAtLoad)
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("failed to start service: %w", err)
	}

	return nil
}

// StopService stops the launchd service
func StopService() error {
	if !IsServiceInstalled() {
		return nil
	}

	homeDir, _ := os.UserHomeDir()
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")

	// Unload service (stops it)
	if err := exec.Command("launchctl", "unload", plistPath).Run(); err != nil {
		return fmt.Errorf("failed to stop service: %w", err)
	}

	return nil
}

// RestartService restarts the launchd service
func RestartService() error {
	if err := StopService(); err != nil {
		return err
	}
	return StartService()
}

// ServiceStatus returns the current service status
type ServiceStatus struct {
	Installed bool
	Running   bool
	PlistPath string
}

// GetServiceStatus returns the current service status
func GetServiceStatus() *ServiceStatus {
	homeDir, _ := os.UserHomeDir()
	plistPath := filepath.Join(homeDir, "Library", "LaunchAgents", launchdLabel+".plist")

	return &ServiceStatus{
		Installed: IsServiceInstalled(),
		Running:   IsServiceRunning(),
		PlistPath: plistPath,
	}
}
