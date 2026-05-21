//go:build linux

package executor

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/logging"
	"golang.org/x/sys/unix"
)

// applyResourceLimits calls prlimit64 on an already-started subprocess to cap its
// virtual address space. Must be called after cmd.Start() when pid is valid.
// RLIMIT_AS limits virtual address space; exceeding it causes malloc/mmap to return
// ENOMEM, producing a clean exit rather than a kernel SIGKILL with no stderr.
func applyResourceLimits(pid int, cfg *SubprocessLimitsConfig) {
	if cfg == nil || !cfg.Enabled || cfg.MaxRSSMB <= 0 {
		return
	}

	log := logging.WithComponent("executor.resource_limits")
	maxBytes := uint64(cfg.MaxRSSMB) * 1024 * 1024
	lim := unix.Rlimit{Cur: maxBytes, Max: maxBytes}

	if err := unix.Prlimit(pid, unix.RLIMIT_AS, &lim, nil); err != nil {
		log.Warn("Failed to apply RLIMIT_AS to subprocess (telemetry-only mode)",
			slog.Int("pid", pid),
			slog.Int("max_rss_mb", cfg.MaxRSSMB),
			slog.Any("error", err),
		)
		return
	}

	log.Debug("Applied RLIMIT_AS to subprocess",
		slog.Int("pid", pid),
		slog.Int("max_rss_mb", cfg.MaxRSSMB),
	)
}
