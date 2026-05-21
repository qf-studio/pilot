//go:build !linux && !darwin

package executor

// readRSSMB returns 0 on unsupported platforms.
func readRSSMB(_ int) int { return 0 }
