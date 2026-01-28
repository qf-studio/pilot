//go:build !darwin

package tunnel

import "fmt"

// InstallService installs the tunnel service (not implemented for this platform)
func InstallService(tunnelID string) error {
	return fmt.Errorf("service installation not supported on this platform (macOS only)")
}

// UninstallService removes the service (not implemented for this platform)
func UninstallService() error {
	return fmt.Errorf("service uninstallation not supported on this platform (macOS only)")
}

// IsServiceInstalled checks if service is installed
func IsServiceInstalled() bool {
	return false
}

// IsServiceRunning checks if service is running
func IsServiceRunning() bool {
	return false
}

// StartService starts the service
func StartService() error {
	return fmt.Errorf("service management not supported on this platform (macOS only)")
}

// StopService stops the service
func StopService() error {
	return fmt.Errorf("service management not supported on this platform (macOS only)")
}

// RestartService restarts the service
func RestartService() error {
	return fmt.Errorf("service management not supported on this platform (macOS only)")
}

// ServiceStatus represents service status
type ServiceStatus struct {
	Installed bool
	Running   bool
	PlistPath string
}

// GetServiceStatus returns the current service status
func GetServiceStatus() *ServiceStatus {
	return &ServiceStatus{
		Installed: false,
		Running:   false,
		PlistPath: "",
	}
}
