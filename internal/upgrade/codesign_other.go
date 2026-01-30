//go:build !darwin

package upgrade

// PrepareForExecution is a no-op on non-macOS platforms.
func PrepareForExecution(binaryPath string) error {
	return nil
}
