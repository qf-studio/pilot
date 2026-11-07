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

	// clock drives the passive wait's poll loop; nil means realClock{}.
	// Swapped for a fake in tests.
	clock clock
}

// DefaultMaxWait is how long the passive wait (see drainRunningTasks) runs before it alerts that a long task blocks the drain.
const DefaultMaxWait = 6 * time.Hour

// HotUpgradeConfig configures the hot upgrade behavior
type HotUpgradeConfig struct {
	// WaitForTasks waits for running tasks to complete before upgrade
	WaitForTasks bool

	// TaskTimeout is the max time to wait for tasks (default: 2 minutes)
	TaskTimeout time.Duration

	// MaxWait caps the passive wait that follows a drain timeout (default:
	// DefaultMaxWait). On expiry OnMaxWait fires once and the wait goes on —
	// a task is never killed for an upgrade.
	MaxWait time.Duration

	// PollInterval is how often the passive wait looks at the running set
	// (default: DefaultCheckInterval).
	PollInterval time.Duration

	// PauseAdmission stops the dispatcher from starting new executions. It is
	// called for the first drain window and again only once the running set
	// is empty; nil disables pausing.
	PauseAdmission func()

	// ResumeAdmission undoes PauseAdmission. PerformHotUpgrade calls it when a
	// drain window times out, so admission stays open during the passive wait.
	// After a successful drain admission stays paused: the caller resumes it
	// if the install fails.
	ResumeAdmission func()

	// OnMaxWait is called once when the passive wait outlasts MaxWait, with
	// the time waited and the task IDs still blocking the drain.
	OnMaxWait func(waited time.Duration, blockedBy []string)

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
		MaxWait:      DefaultMaxWait,
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
		if err := h.drainRunningTasks(ctx, cfg, progress); err != nil {
			return err
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

// drainRunningTasks pauses admission and waits for the running set to empty.
//
// Decision (GH-6187, founder 2026-11-07): the first drain window is paused for
// TaskTimeout; if one long task outlasts it, admission is resumed and the
// upgrade waits passively — polling without pausing — until the running set is
// empty, then pauses, re-checks and proceeds. Re-pausing every cooldown kept
// every project on the box paused for most of 90 minutes on 2026-11-06 while a
// single 2 h task ran. The cost is accepted: the box may run the old binary
// until the longest task ends. Returns nil with admission paused.
func (h *HotUpgrader) drainRunningTasks(ctx context.Context, cfg *HotUpgradeConfig, progress func(int, string)) error {
	clk := h.clock
	if clk == nil {
		clk = realClock{}
	}
	pause := func() {
		if cfg.PauseAdmission != nil {
			cfg.PauseAdmission()
		}
	}
	resume := func() {
		if cfg.ResumeAdmission != nil {
			cfg.ResumeAdmission()
		}
	}

	pause()
	runningTasks := h.taskChecker.GetRunningTaskIDs()
	if len(runningTasks) == 0 {
		return nil
	}

	progress(5, fmt.Sprintf("Waiting for %d task(s) to complete...", len(runningTasks)))
	waitCtx, cancel := context.WithTimeout(ctx, cfg.TaskTimeout)
	err := h.taskChecker.WaitForTasks(waitCtx, cfg.TaskTimeout)
	cancel()
	if err == nil {
		progress(15, "Tasks completed")
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("timeout waiting for tasks: %w", err)
	}

	// Drain window timed out: reopen admission and wait for a natural gap.
	resume()
	maxWait := cfg.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = DefaultCheckInterval
	}
	waitStart := clk.Now()
	maxWaitFired := false
	announce := true

	for {
		blockedBy := h.taskChecker.GetRunningTaskIDs()
		if len(blockedBy) == 0 {
			// Close the race with admission: pause, then look again.
			pause()
			blockedBy = h.taskChecker.GetRunningTaskIDs()
			if len(blockedBy) == 0 {
				progress(15, "Tasks completed")
				return nil
			}
			resume()
			announce = true
		}

		if announce {
			slog.Info("self-upgrade: passive wait, admission open",
				slog.Any("drainBlockedBy", blockedBy),
				slog.Duration("max_wait", maxWait),
			)
			progress(5, fmt.Sprintf("Waiting for %d long-running task(s); admission open", len(blockedBy)))
			announce = false
		}

		if waited := clk.Now().Sub(waitStart); !maxWaitFired && waited >= maxWait {
			maxWaitFired = true
			slog.Warn("self-upgrade: passive wait exceeded max_wait, still waiting",
				slog.Any("drainBlockedBy", blockedBy),
				slog.Duration("waited", waited),
			)
			if cfg.OnMaxWait != nil {
				cfg.OnMaxWait(waited, blockedBy)
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for tasks: %w", ctx.Err())
		case <-clk.After(poll):
		}
	}
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
