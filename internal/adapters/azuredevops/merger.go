package azuredevops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// MergeWaitResult represents the outcome of waiting for a PR to merge
type MergeWaitResult struct {
	Merged       bool   // PR was successfully completed (merged)
	Abandoned    bool   // PR was abandoned without merging
	HasConflicts bool   // PR has merge conflicts
	TimedOut     bool   // Wait timed out
	PRNumber     int    // The PR ID
	PRURL        string // The PR URL
	Message      string // Human-readable status message
}

// MergeWaiterConfig holds configuration for the merge waiter
type MergeWaiterConfig struct {
	PollInterval time.Duration // How often to check PR status
	Timeout      time.Duration // Max time to wait for merge
}

// DefaultMergeWaiterConfig returns sensible defaults
func DefaultMergeWaiterConfig() *MergeWaiterConfig {
	return &MergeWaiterConfig{
		PollInterval: 30 * time.Second,
		Timeout:      1 * time.Hour,
	}
}

// MergeWaiter waits for a PR to be merged
type MergeWaiter struct {
	client *Client
	config *MergeWaiterConfig
	logger *slog.Logger
}

// NewMergeWaiter creates a new merge waiter
func NewMergeWaiter(client *Client, config *MergeWaiterConfig) *MergeWaiter {
	if config == nil {
		config = DefaultMergeWaiterConfig()
	}
	return &MergeWaiter{
		client: client,
		config: config,
		logger: logging.WithComponent("azuredevops-merge-waiter"),
	}
}

// Common errors
var (
	ErrPRAbandoned    = errors.New("PR was abandoned without merging")
	ErrPRConflict     = errors.New("PR has merge conflicts")
	ErrMergeTimeout   = errors.New("timed out waiting for PR merge")
)

// WaitForMerge polls the PR status until it's merged, abandoned, or times out
func (m *MergeWaiter) WaitForMerge(ctx context.Context, prID int) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge",
		slog.Int("pr_id", prID),
		slog.Duration("timeout", m.config.Timeout),
		slog.Duration("poll_interval", m.config.PollInterval),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	// Check immediately, then on ticker
	for {
		result, err := m.checkPRStatus(ctx, prID)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		// If we have a terminal state, return
		if result.Merged || result.Abandoned || result.HasConflicts {
			return result, nil
		}

		// Check if context is done
		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		default:
		}

		// Check timeout
		if time.Now().After(deadline) {
			m.logger.Warn("PR merge timed out",
				slog.Int("pr_id", prID),
				slog.Duration("timeout", m.config.Timeout),
			)
			return &MergeWaitResult{
				PRNumber: prID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out waiting for PR #%d to merge after %s", prID, m.config.Timeout),
			}, ErrMergeTimeout
		}

		// Wait for next tick
		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		case <-ticker.C:
			m.logger.Debug("Polling PR status",
				slog.Int("pr_id", prID),
				slog.Duration("remaining", time.Until(deadline)),
			)
		}
	}
}

// checkPRStatus fetches and interprets the current PR status
func (m *MergeWaiter) checkPRStatus(ctx context.Context, prID int) (*MergeWaitResult, error) {
	pr, err := m.client.GetPullRequest(ctx, prID)
	if err != nil {
		return nil, err
	}

	result := &MergeWaitResult{
		PRNumber: prID,
		PRURL:    m.client.GetPullRequestWebURL(prID),
	}

	// Check if completed (merged)
	if pr.Status == PRStateCompleted {
		m.logger.Info("PR merged successfully",
			slog.Int("pr_id", prID),
		)
		result.Merged = true
		result.Message = fmt.Sprintf("PR #%d was merged", prID)
		return result, nil
	}

	// Check if abandoned without merge
	if pr.Status == PRStateAbandoned {
		m.logger.Warn("PR abandoned without merging",
			slog.Int("pr_id", prID),
		)
		result.Abandoned = true
		result.Message = fmt.Sprintf("PR #%d was abandoned without merging", prID)
		return result, nil
	}

	// Check for merge conflicts
	if pr.MergeStatus == MergeStatusConflicts {
		m.logger.Warn("PR has merge conflicts",
			slog.Int("pr_id", prID),
		)
		result.HasConflicts = true
		result.Message = fmt.Sprintf("PR #%d has merge conflicts", prID)
		return result, nil
	}

	// Still active
	switch pr.MergeStatus {
	case MergeStatusQueued:
		result.Message = fmt.Sprintf("PR #%d merge is queued", prID)
	case MergeStatusFailure:
		result.Message = fmt.Sprintf("PR #%d merge failed (checks not passing)", prID)
	default:
		result.Message = fmt.Sprintf("PR #%d is active, waiting for merge...", prID)
	}

	return result, nil
}

// WaitWithCallback is like WaitForMerge but calls the callback on each poll
// This allows the caller to update UI or logs with current status
func (m *MergeWaiter) WaitWithCallback(ctx context.Context, prID int, onPoll func(result *MergeWaitResult)) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge with callback",
		slog.Int("pr_id", prID),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkPRStatus(ctx, prID)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		// Call the callback with current status
		if onPoll != nil {
			onPoll(result)
		}

		// If we have a terminal state, return
		if result.Merged || result.Abandoned || result.HasConflicts {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return &MergeWaitResult{
				PRNumber: prID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out after %s", m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled",
			}, ctx.Err()
		case <-ticker.C:
			// Continue polling
		}
	}
}
