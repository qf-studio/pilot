//go:build !linux && !darwin

package executor

// applyResourceLimits is a no-op on unsupported platforms.
func applyResourceLimits(_ int, _ *SubprocessLimitsConfig) {}
