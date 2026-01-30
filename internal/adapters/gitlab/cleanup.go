package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
)

// Cleaner handles automatic cleanup of stale pilot-in-progress labels.
// When Pilot crashes or is killed, labels remain on issues. This cleaner
// periodically checks for such orphaned labels and removes them.
type Cleaner struct {
	client    *Client
	store     *memory.Store
	interval  time.Duration
	threshold time.Duration
	logger    *slog.Logger

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// CleanerOption configures a Cleaner
type CleanerOption func(*Cleaner)

// WithCleanerLogger sets the logger for the cleaner
func WithCleanerLogger(logger *slog.Logger) CleanerOption {
	return func(c *Cleaner) {
		c.logger = logger
	}
}

// NewCleaner creates a new stale label cleaner.
func NewCleaner(client *Client, store *memory.Store, config *StaleLabelCleanupConfig, opts ...CleanerOption) *Cleaner {
	interval := config.Interval
	if interval == 0 {
		interval = 30 * time.Minute
	}

	threshold := config.Threshold
	if threshold == 0 {
		threshold = 1 * time.Hour
	}

	c := &Cleaner{
		client:    client,
		store:     store,
		interval:  interval,
		threshold: threshold,
		logger:    logging.WithComponent("gitlab-cleanup"),
		stopCh:    make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Start begins the periodic cleanup loop.
// It runs in the background and can be stopped with Stop().
func (c *Cleaner) Start(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.stopCh = make(chan struct{})
	c.mu.Unlock()

	c.logger.Info("Starting stale label cleaner",
		slog.Duration("interval", c.interval),
		slog.Duration("threshold", c.threshold),
	)

	// Run initial cleanup
	if err := c.Cleanup(ctx); err != nil {
		c.logger.Warn("Initial cleanup failed", slog.Any("error", err))
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Stale label cleaner stopped (context cancelled)")
			return
		case <-c.stopCh:
			c.logger.Info("Stale label cleaner stopped")
			return
		case <-ticker.C:
			if err := c.Cleanup(ctx); err != nil {
				c.logger.Warn("Cleanup failed", slog.Any("error", err))
			}
		}
	}
}

// Stop stops the periodic cleanup loop
func (c *Cleaner) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		close(c.stopCh)
		c.running = false
	}
}

// Cleanup performs a single cleanup pass:
// 1. Lists all issues with pilot-in-progress label
// 2. Cross-references with active executions in memory store
// 3. Removes label from issues with no matching active execution
func (c *Cleaner) Cleanup(ctx context.Context) error {
	c.logger.Debug("Running stale label cleanup")

	// Get all issues with in-progress label
	issues, err := c.client.ListIssues(ctx, &ListIssuesOptions{
		Labels: []string{LabelInProgress},
		State:  StateOpened,
	})
	if err != nil {
		return fmt.Errorf("failed to list issues with in-progress label: %w", err)
	}

	if len(issues) == 0 {
		c.logger.Debug("No issues with in-progress label found")
		return nil
	}

	c.logger.Debug("Found issues with in-progress label", slog.Int("count", len(issues)))

	// Get active executions from memory store
	activeExecutions, err := c.store.GetActiveExecutions()
	if err != nil {
		return fmt.Errorf("failed to get active executions: %w", err)
	}

	// Build a map of active task IDs for quick lookup
	activeTaskIDs := make(map[string]bool)
	for _, exec := range activeExecutions {
		activeTaskIDs[exec.TaskID] = true
	}

	c.logger.Debug("Active executions found", slog.Int("count", len(activeExecutions)))

	// Check each issue
	cleanedCount := 0
	for _, issue := range issues {
		// Check if there's an active execution for this issue
		// Task IDs are typically formatted as "GL-<iid>" for GitLab
		taskID := fmt.Sprintf("GL-%d", issue.IID)
		if activeTaskIDs[taskID] {
			c.logger.Debug("Issue has active execution, skipping",
				slog.Int("issue_iid", issue.IID),
				slog.String("task_id", taskID),
			)
			continue
		}

		// Check if the issue's label update is older than threshold
		// Use UpdatedAt as a proxy for when the label was added
		if time.Since(issue.UpdatedAt) < c.threshold {
			c.logger.Debug("Issue recently updated, skipping",
				slog.Int("issue_iid", issue.IID),
				slog.Duration("age", time.Since(issue.UpdatedAt)),
			)
			continue
		}

		// Remove the stale label
		c.logger.Info("Removing stale in-progress label",
			slog.Int("issue_iid", issue.IID),
			slog.String("title", issue.Title),
			slog.Duration("age", time.Since(issue.UpdatedAt)),
		)

		if err := c.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress); err != nil {
			c.logger.Warn("Failed to remove stale label",
				slog.Int("issue_iid", issue.IID),
				slog.Any("error", err),
			)
			continue
		}

		// Optionally add a comment explaining the cleanup
		comment := "🧹 **Pilot cleanup**: Removed stale `pilot-in-progress` label.\n\n" +
			"This issue was marked as in-progress but no active Pilot execution was found. " +
			"This can happen if Pilot was interrupted or crashed. The issue is now available for processing again."

		if _, err := c.client.AddIssueNote(ctx, issue.IID, comment); err != nil {
			c.logger.Warn("Failed to add cleanup comment",
				slog.Int("issue_iid", issue.IID),
				slog.Any("error", err),
			)
		}

		cleanedCount++
	}

	if cleanedCount > 0 {
		c.logger.Info("Stale label cleanup completed",
			slog.Int("cleaned", cleanedCount),
			slog.Int("total_checked", len(issues)),
		)
	}

	return nil
}

// CleanupStaleLabels is a convenience method that performs a single cleanup
// without starting the periodic loop. Useful for one-off cleanup operations.
func (c *Cleaner) CleanupStaleLabels(ctx context.Context) error {
	return c.Cleanup(ctx)
}
