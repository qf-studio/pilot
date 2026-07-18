//go:build !linux && !darwin

package executor

// applyResourceLimits is a no-op on unsupported platforms.
func applyResourceLimits(_ int, _ *SubprocessLimitsConfig) func() { return func() {} }

// CgroupV2MemoryAvailable always reports unavailable on unsupported platforms.
func CgroupV2MemoryAvailable() (bool, string) {
	return false, "cgroup v2 is a Linux-only mechanism; not supported on this platform"
}
