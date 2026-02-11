package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
)

// Cleaner handles automatic cleanup of stale pilot labels (pilot-in-progress and pilot-failed).
// When Pilot crashes or is killed, labels remain on issues. This cleaner
// periodically checks for such orphaned labels and removes them.
type Cleaner struct {
	client          *Client
	store           *memory.Store
	owner           string
	repo            string
	interval        time.Duration
	threshold       time.Duration
	failedThreshold time.Duration
	logger          *slog.Logger

	// OnFailedCleaned is called when a pilot-failed label is removed.
	// Used to clear the issue from the poller's processed map.
	OnFailedCleaned func(issueNumber int)

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

// WithOnFailedCleaned sets the callback for when a pilot-failed label is removed.
// The callback receives the issue number and should clear it from the poller's processed map.
func WithOnFailedCleaned(fn func(issueNumber int)) CleanerOption {
	return func(c *Cleaner) {
		c.OnFailedCleaned = fn
	}
}

// NewCleaner creates a new stale label cleaner.
// The repo parameter should be in "owner/repo" format.
func NewCleaner(client *Client, store *memory.Store, repo string, config *StaleLabelCleanupConfig, opts ...CleanerOption) (*Cleaner, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format, expected owner/repo: %s", repo)
	}

	interval := config.Interval
	if interval == 0 {
		interval = 30 * time.Minute
	}

	threshold := config.Threshold
	if threshold == 0 {
		threshold = 1 * time.Hour
	}

	failedThreshold := config.FailedThreshold
	if failedThreshold == 0 {
		failedThreshold = 24 * time.Hour
	}

	c := &Cleaner{
		client:          client,
		store:           store,
		owner:           parts[0],
		repo:            parts[1],
		interval:        interval,
		threshold:       threshold,
		failedThreshold: failedThreshold,
		logger:          logging.WithComponent("github-cleanup"),
		stopCh:          make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
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
		slog.String("repo", c.owner+"/"+c.repo),
		slog.Duration("interval", c.interval),
		slog.Duration("in_progress_threshold", c.threshold),
		slog.Duration("failed_threshold", c.failedThreshold),
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
// 1. Lists all issues with pilot-in-progress label and removes stale ones
// 2. Lists all issues with pilot-failed label and removes stale ones
// 3. Cross-references with active executions in memory store
func (c *Cleaner) Cleanup(ctx context.Context) error {
	c.logger.Debug("Running stale label cleanup")

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

	// Clean up stale pilot-in-progress labels
	inProgressCleaned, err := c.cleanupLabel(ctx, LabelInProgress, c.threshold, activeTaskIDs)
	if err != nil {
		return fmt.Errorf("failed to cleanup in-progress labels: %w", err)
	}

	// Clean up stale pilot-failed labels
	failedCleaned, err := c.cleanupLabel(ctx, LabelFailed, c.failedThreshold, activeTaskIDs)
	if err != nil {
		return fmt.Errorf("failed to cleanup failed labels: %w", err)
	}

	totalCleaned := inProgressCleaned + failedCleaned
	if totalCleaned > 0 {
		c.logger.Info("Stale label cleanup completed",
			slog.Int("in_progress_cleaned", inProgressCleaned),
			slog.Int("failed_cleaned", failedCleaned),
		)
	}

	return nil
}

// cleanupLabel cleans up a specific label type and returns count of cleaned issues
func (c *Cleaner) cleanupLabel(ctx context.Context, label string, threshold time.Duration, activeTaskIDs map[string]bool) (int, error) {
	issues, err := c.client.ListIssues(ctx, c.owner, c.repo, &ListIssuesOptions{
		Labels: []string{label},
		State:  StateOpen,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to list issues with %s label: %w", label, err)
	}

	if len(issues) == 0 {
		c.logger.Debug("No issues found with label", slog.String("label", label))
		return 0, nil
	}

	c.logger.Debug("Found issues with label",
		slog.String("label", label),
		slog.Int("count", len(issues)),
	)

	cleanedCount := 0
	for _, issue := range issues {
		// Check if there's an active execution for this issue
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		if activeTaskIDs[taskID] {
			c.logger.Debug("Issue has active execution, skipping",
				slog.Int("issue", issue.Number),
				slog.String("task_id", taskID),
				slog.String("label", label),
			)
			continue
		}

		// Check if the issue's label update is older than threshold
		if time.Since(issue.UpdatedAt) < threshold {
			c.logger.Debug("Issue recently updated, skipping",
				slog.Int("issue", issue.Number),
				slog.Duration("age", time.Since(issue.UpdatedAt)),
				slog.Duration("threshold", threshold),
				slog.String("label", label),
			)
			continue
		}

		// Remove the stale label
		c.logger.Info("Removing stale label",
			slog.String("label", label),
			slog.Int("issue", issue.Number),
			slog.String("title", issue.Title),
			slog.Duration("age", time.Since(issue.UpdatedAt)),
		)

		if err := c.client.RemoveLabel(ctx, c.owner, c.repo, issue.Number, label); err != nil {
			c.logger.Warn("Failed to remove stale label",
				slog.Int("issue", issue.Number),
				slog.String("label", label),
				slog.Any("error", err),
			)
			continue
		}

		// Add a comment explaining the cleanup
		var comment string
		switch label {
		case LabelInProgress:
			comment = "🧹 **Pilot cleanup**: Removed stale `pilot-in-progress` label.\n\n" +
				"This issue was marked as in-progress but no active Pilot execution was found. " +
				"This can happen if Pilot was interrupted or crashed. The issue is now available for processing again."
		case LabelFailed:
			comment = "🧹 **Pilot cleanup**: Removed stale `pilot-failed` label.\n\n" +
				"This issue was marked as failed but has been stale for over 24 hours. " +
				"The label has been removed to allow Pilot to retry this issue automatically."
		}

		if _, err := c.client.AddComment(ctx, c.owner, c.repo, issue.Number, comment); err != nil {
			c.logger.Warn("Failed to add cleanup comment",
				slog.Int("issue", issue.Number),
				slog.Any("error", err),
			)
		}

		// For failed labels, notify callback to clear from processed map
		if label == LabelFailed && c.OnFailedCleaned != nil {
			c.OnFailedCleaned(issue.Number)
		}

		cleanedCount++
	}

	return cleanedCount, nil
}

// CleanupStaleLabels is a convenience method that performs a single cleanup
// without starting the periodic loop. Useful for one-off cleanup operations.
func (c *Cleaner) CleanupStaleLabels(ctx context.Context) error {
	return c.Cleanup(ctx)
}
