//go:build darwin

package executor

import (
	"log/slog"
	"sync"

	"github.com/qf-studio/pilot/internal/logging"
)

var darwinLimitWarnOnce sync.Once

// applyResourceLimits is best-effort on macOS: prlimit64 is a Linux-only syscall
// and macOS RLIMIT_AS support is unreliable. We log a one-time warning and degrade
// to telemetry-only mode (RSS sampler still runs).
func applyResourceLimits(pid int, cfg *SubprocessLimitsConfig) {
	if cfg == nil || !cfg.Enabled || cfg.MaxRSSMB <= 0 {
		return
	}
	darwinLimitWarnOnce.Do(func() {
		logging.WithComponent("executor.resource_limits").Warn(
			"subprocess_limits.enabled=true but memory cap is not supported on macOS; running in telemetry-only mode",
			slog.Int("max_rss_mb", cfg.MaxRSSMB),
		)
	})
}
