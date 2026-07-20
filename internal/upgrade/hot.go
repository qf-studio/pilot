package upgrade

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// HotUpgrader orchestrates the hot upgrade flow, combining graceful task
// completion, binary download/install, and process restart.
type HotUpgrader struct {
	graceful    *GracefulUpgrader
	taskChecker TaskChecker
}

// HotUpgradeConfig configures the hot upgrade behavior
type HotUpgradeConfig struct {
	// WaitForTasks waits for running tasks to complete before upgrade
	WaitForTasks bool

	// TaskTimeout is the max time to wait for tasks (default: 2 minutes)
	TaskTimeout time.Duration

	// FlushSession is called before restart to persist any in-memory state
	FlushSession func() error

	// OnProgress callback for progress updates (0-100%)
	OnProgress func(pct int, msg string)
}

// DefaultHotUpgradeConfig returns sensible defaults for hot upgrade
func DefaultHotUpgradeConfig() *HotUpgradeConfig {
	return &HotUpgradeConfig{
		WaitForTasks: true,
		TaskTimeout:  2 * time.Minute,
	}
}

// progressLogHygieneInterval is the minimum gap between two INFO-level
// "upgrade progress" log lines for the same run, unless a 10% boundary or
// the 0%/100% endpoints are crossed. downloadAsset reports progress on every
// 32KB chunk — for a ~30MB release that's roughly a thousand callbacks in a
// few hundred milliseconds, which without throttling turns into hundreds of
// log lines per second (GH-4468 incident: daemon.log was too noisy to
// contain the one ERROR line that mattered).
const progressLogHygieneInterval = time.Second

// progressLogThrottle decides whether a given progress update is significant
// enough to log at INFO. It does not gate the OnProgress callback itself —
// UI consumers (the TUI progress bar) still get every update; only the log
// line is throttled.
type progressLogThrottle struct {
	clock   clock
	started bool
	lastAt  time.Time
	lastPct int
}

func newProgressLogThrottle(clk clock) *progressLogThrottle {
	if clk == nil {
		clk = realClock{}
	}
	return &progressLogThrottle{clock: clk}
}

// shouldLog reports whether pct should produce a log line, then (if so)
// records it as the new baseline. Always logs the first call and the
// 0%/100% endpoints so start and completion are never dropped.
func (t *progressLogThrottle) shouldLog(pct int) bool {
	now := t.clock.Now()

	log := !t.started ||
		pct <= 0 || pct >= 100 ||
		pct/10 != t.lastPct/10 ||
		now.Sub(t.lastAt) >= progressLogHygieneInterval

	if log {
		t.started = true
		t.lastAt = now
		t.lastPct = pct
	}

	return log
}

// NewHotUpgrader creates a new HotUpgrader instance
func NewHotUpgrader(currentVersion string, taskChecker TaskChecker) (*HotUpgrader, error) {
	graceful, err := NewGracefulUpgrader(currentVersion, taskChecker)
	if err != nil {
		return nil, err
	}

	return &HotUpgrader{
		graceful:    graceful,
		taskChecker: taskChecker,
	}, nil
}

// PerformHotUpgrade performs a complete hot upgrade:
// 1. Waits for running tasks to complete
// 2. Flushes session state
// 3. Downloads and installs the new binary
// 4. Restarts the process via syscall.Exec
//
// This function only returns if an error occurs. On success, the process
// is replaced with the new binary and this function never returns.
func (h *HotUpgrader) PerformHotUpgrade(ctx context.Context, release *Release, cfg *HotUpgradeConfig) error {
	if cfg == nil {
		cfg = DefaultHotUpgradeConfig()
	}

	currentVersion := h.graceful.GetUpgrader().currentVersion

	logThrottle := newProgressLogThrottle(nil)
	progress := func(pct int, msg string) {
		if logThrottle.shouldLog(pct) {
			slog.Info("upgrade progress", slog.Int("percent", pct), slog.String("msg", msg))
		}
		if cfg.OnProgress != nil {
			cfg.OnProgress(pct, msg)
		}
	}

	// Step 1: Wait for running tasks (if any)
	if cfg.WaitForTasks && h.taskChecker != nil {
		runningTasks := h.taskChecker.GetRunningTaskIDs()
		if len(runningTasks) > 0 {
			progress(5, fmt.Sprintf("Waiting for %d task(s) to complete...", len(runningTasks)))

			waitCtx, cancel := context.WithTimeout(ctx, cfg.TaskTimeout)
			defer cancel()

			if err := h.taskChecker.WaitForTasks(waitCtx, cfg.TaskTimeout); err != nil {
				return fmt.Errorf("timeout waiting for tasks: %w", err)
			}
			progress(15, "Tasks completed")
		}
	}

	// Step 2: Flush session state (if callback provided)
	if cfg.FlushSession != nil {
		progress(20, "Flushing session state...")
		if err := cfg.FlushSession(); err != nil {
			slog.Warn("failed to flush session state", slog.Any("error", err))
			// Non-fatal - continue with upgrade
		}
	}

	// Step 3: Perform the upgrade (download, backup, install)
	progress(25, "Downloading update...")

	upgradeOpts := &UpgradeOptions{
		WaitForTasks: false, // Already handled above
		Force:        true,  // Skip task check in graceful upgrader
		// GH-3600: a restart is still ahead (exec below, or a manual restart on
		// Windows) — state must read awaiting_restart, not completed, until the
		// restarted process verifies the running version at boot.
		MarkAwaitingRestart: true,
		OnProgress: func(pct int, msg string) {
			// Scale from 25-90%
			scaledPct := 25 + (pct * 65 / 100)
			progress(scaledPct, msg)
		},
	}

	if err := h.graceful.PerformUpgrade(ctx, release, upgradeOpts); err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	// Step 4: Restart if supported, otherwise notify user
	if !canHotRestart() {
		progress(100, "Upgrade installed! Please restart Pilot manually.")
		slog.Info("upgrade installed, manual restart required (Windows)",
			slog.String("previous_version", currentVersion),
			slog.String("new_version", release.TagName),
		)
		return nil
	}

	progress(95, "Preparing to restart...")

	binaryPath := h.graceful.GetUpgrader().BinaryPath()
	args := os.Args

	slog.Info("restarting with new binary",
		slog.String("path", binaryPath),
		slog.String("previous_version", currentVersion),
		slog.String("new_version", release.TagName),
	)

	progress(100, "Restarting...")

	// Give a moment for progress to be displayed
	time.Sleep(100 * time.Millisecond)

	// Step 5: Exec into new binary (this never returns on success)
	if err := RestartWithNewBinary(binaryPath, args, currentVersion); err != nil {
		// GH-3600: persist the failure — without this the state file keeps
		// claiming the upgrade landed while the old binary is still running.
		if st, lerr := LoadState(h.graceful.statePath); lerr == nil && st != nil {
			st.MarkRestartFailed(err)
			_ = st.Save(h.graceful.statePath)
		}
		slog.Error("hot restart FAILED — daemon still running previous version",
			slog.String("running_version", currentVersion),
			slog.String("installed_version", release.TagName),
			slog.Any("error", err),
		)
		return fmt.Errorf("restart failed: %w", err)
	}

	// This line is never reached on success
	return nil
}

// canHotRestart is an overridable seam (mirrors runSmokeTest) so tests can
// exercise the manual-restart branch on any platform.
var canHotRestart = CanHotRestart

// GetUpgrader returns the underlying Upgrader for version checks
func (h *HotUpgrader) GetUpgrader() *Upgrader {
	return h.graceful.GetUpgrader()
}

// GetGracefulUpgrader returns the underlying GracefulUpgrader
func (h *HotUpgrader) GetGracefulUpgrader() *GracefulUpgrader {
	return h.graceful
}

// CheckVersion checks if a new version is available
func (h *HotUpgrader) CheckVersion(ctx context.Context) (*VersionInfo, error) {
	return h.graceful.GetUpgrader().CheckVersion(ctx)
}
