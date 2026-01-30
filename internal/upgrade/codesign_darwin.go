//go:build darwin

package upgrade

import (
	"os/exec"
)

// PrepareForExecution removes quarantine attribute and ad-hoc signs the binary on macOS.
// This ensures the binary can run without being killed by Gatekeeper.
func PrepareForExecution(binaryPath string) error {
	// Remove quarantine attribute (ignore errors - may not exist)
	_ = exec.Command("xattr", "-d", "com.apple.quarantine", binaryPath).Run()

	// Ad-hoc code sign
	return exec.Command("codesign", "-s", "-", binaryPath).Run()
}
