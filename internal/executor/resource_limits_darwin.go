//go:build darwin

package executor

import (
	"log/slog"
	"sync"

	"github.com/qf-studio/pilot/internal/logging"
)

var darwinLimitWarnOnce sync.Once

// applyResourceLimits is best-effort on macOS: cgroup v2 doesn't exist on
// Darwin and RLIMIT_AS is unreliable there too (and, per GH-4401, wrong even
// on Linux — it caps virtual address space, not RSS). We log a one-time
// warning and degrade to telemetry-only mode (RSS sampler still runs). The
// returned cleanup func is always a no-op.
func applyResourceLimits(_ int, cfg *SubprocessLimitsConfig) func() {
	if cfg == nil || !cfg.Enabled || cfg.MaxRSSMB <= 0 {
		return func() {}
	}
	darwinLimitWarnOnce.Do(func() {
		logging.WithComponent("executor.resource_limits").Warn(
			"subprocess_limits.enabled=true but memory cap is not supported on macOS; running in telemetry-only mode",
			slog.Int("max_rss_mb", cfg.MaxRSSMB),
		)
	})
	return func() {}
}

// CgroupV2MemoryAvailable always reports unavailable on macOS.
func CgroupV2MemoryAvailable() (bool, string) {
	return false, "cgroup v2 is a Linux-only mechanism; not applicable on macOS"
}
