package upgrade

import (
	"context"
	"fmt"
	"time"
)

// GracefulUpgrader handles graceful upgrade with task completion
type GracefulUpgrader struct {
	upgrader    *Upgrader
	statePath   string
	taskChecker TaskChecker
}

// TaskChecker interface for checking running tasks
type TaskChecker interface {
	// GetRunningTaskIDs returns IDs of currently running tasks
	GetRunningTaskIDs() []string

	// WaitForTasks waits for all tasks to complete or timeout
	WaitForTasks(ctx context.Context, timeout time.Duration) error
}

// NewGracefulUpgrader creates a new GracefulUpgrader
func NewGracefulUpgrader(currentVersion string, taskChecker TaskChecker) (*GracefulUpgrader, error) {
	upgrader, err := NewUpgrader(currentVersion)
	if err != nil {
		return nil, err
	}

	return &GracefulUpgrader{
		upgrader:    upgrader,
		statePath:   DefaultStatePath(),
		taskChecker: taskChecker,
	}, nil
}

// UpgradeOptions configures the upgrade behavior
type UpgradeOptions struct {
	// WaitForTasks waits for running tasks to complete before upgrade
	WaitForTasks bool

	// TaskTimeout is the max time to wait for tasks (default: 5 minutes)
	TaskTimeout time.Duration

	// Force skips task waiting
	Force bool

	// OnProgress callback for progress updates
	OnProgress func(pct int, msg string)
}

// DefaultUpgradeOptions returns default upgrade options
func DefaultUpgradeOptions() *UpgradeOptions {
	return &UpgradeOptions{
		WaitForTasks: true,
		TaskTimeout:  5 * time.Minute,
		Force:        false,
	}
}

// PerformUpgrade performs a graceful upgrade
func (g *GracefulUpgrader) PerformUpgrade(ctx context.Context, release *Release, opts *UpgradeOptions) error {
	if opts == nil {
		opts = DefaultUpgradeOptions()
	}

	// Initialize state
	state := &State{
		PreviousVersion: g.upgrader.currentVersion,
		NewVersion:      release.TagName,
		UpgradeStarted:  time.Now(),
		BackupPath:      g.upgrader.backupPath,
		Status:          StatusPending,
	}

	// Check for running tasks
	if g.taskChecker != nil && !opts.Force {
		runningTasks := g.taskChecker.GetRunningTaskIDs()
		if len(runningTasks) > 0 {
			state.PendingTasks = runningTasks
			state.Status = StatusWaiting

			if opts.OnProgress != nil {
				opts.OnProgress(5, fmt.Sprintf("Waiting for %d running task(s)...", len(runningTasks)))
			}

			if opts.WaitForTasks {
				waitCtx, cancel := context.WithTimeout(ctx, opts.TaskTimeout)
				defer cancel()

				if err := g.taskChecker.WaitForTasks(waitCtx, opts.TaskTimeout); err != nil {
					state.MarkFailed(err)
					_ = state.Save(g.statePath)
					return fmt.Errorf("timeout waiting for tasks: %w", err)
				}
			}
		}
	}

	// Save state before upgrade
	state.Status = StatusDownloading
	if err := state.Save(g.statePath); err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	// Perform upgrade
	state.Status = StatusInstalling
	_ = state.Save(g.statePath)

	if err := g.upgrader.Upgrade(ctx, release, opts.OnProgress); err != nil {
		state.MarkFailed(err)
		_ = state.Save(g.statePath)
		return err
	}

	// Mark completed
	state.MarkCompleted()
	if err := state.Save(g.statePath); err != nil {
		return fmt.Errorf("failed to save completion state: %w", err)
	}

	// Note: No auto-restart - user's next command will use the new binary
	// This avoids macOS Gatekeeper/codesign issues that cause restart loops (GH-272)
	if opts.OnProgress != nil {
		opts.OnProgress(100, "Update complete!")
	}

	return nil
}

// CheckAndRollback checks for failed upgrades and rolls back if needed
func (g *GracefulUpgrader) CheckAndRollback() (bool, error) {
	state, err := LoadState(g.statePath)
	if err != nil {
		return false, err
	}

	if state == nil {
		return false, nil
	}

	if state.NeedsRollback() {
		if err := g.upgrader.Rollback(); err != nil {
			return false, fmt.Errorf("rollback failed: %w", err)
		}

		state.MarkRolledBack()
		_ = state.Save(g.statePath)

		return true, nil
	}

	return false, nil
}

// CleanupState cleans up upgrade state after successful startup
func (g *GracefulUpgrader) CleanupState() error {
	state, err := LoadState(g.statePath)
	if err != nil {
		return err
	}

	if state == nil {
		return nil
	}

	// Only cleanup if upgrade completed successfully
	if state.Status == StatusCompleted {
		// Remove backup
		if err := g.upgrader.CleanupBackup(); err != nil {
			return err
		}

		// Clear state
		return ClearState(g.statePath)
	}

	return nil
}

// GetUpgrader returns the underlying upgrader
func (g *GracefulUpgrader) GetUpgrader() *Upgrader {
	return g.upgrader
}

// NoOpTaskChecker is a task checker that reports no running tasks
type NoOpTaskChecker struct{}

// GetRunningTaskIDs returns an empty slice
func (n *NoOpTaskChecker) GetRunningTaskIDs() []string {
	return nil
}

// WaitForTasks returns immediately
func (n *NoOpTaskChecker) WaitForTasks(ctx context.Context, timeout time.Duration) error {
	return nil
}
