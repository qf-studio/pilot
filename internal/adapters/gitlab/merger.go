package gitlab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// MergeWaitResult represents the outcome of waiting for an MR to merge
type MergeWaitResult struct {
	Merged       bool   // MR was successfully merged
	Closed       bool   // MR was closed without merging
	HasConflicts bool   // MR has merge conflicts
	TimedOut     bool   // Wait timed out
	MRNumber     int    // The MR IID
	MRURL        string // The MR URL
	Message      string // Human-readable status message
}

// MergeWaiterConfig holds configuration for the merge waiter
type MergeWaiterConfig struct {
	PollInterval time.Duration // How often to check MR status
	Timeout      time.Duration // Max time to wait for merge
}

// DefaultMergeWaiterConfig returns sensible defaults
func DefaultMergeWaiterConfig() *MergeWaiterConfig {
	return &MergeWaiterConfig{
		PollInterval: 30 * time.Second,
		Timeout:      1 * time.Hour,
	}
}

// MergeWaiter waits for an MR to be merged
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
		logger: logging.WithComponent("gitlab-merge-waiter"),
	}
}

// Common errors
var (
	ErrMRClosed       = errors.New("MR was closed without merging")
	ErrMRConflict     = errors.New("MR has merge conflicts")
	ErrMergeTimeout   = errors.New("timed out waiting for MR merge")
	ErrPipelineFailed = errors.New("pipeline failed")
)

// WaitForMerge polls the MR status until it's merged, closed, or times out
func (m *MergeWaiter) WaitForMerge(ctx context.Context, mrIID int) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for MR merge",
		slog.Int("mr_iid", mrIID),
		slog.Duration("timeout", m.config.Timeout),
		slog.Duration("poll_interval", m.config.PollInterval),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	// Check immediately, then on ticker
	for {
		result, err := m.checkMRStatus(ctx, mrIID)
		if err != nil {
			return nil, fmt.Errorf("failed to check MR status: %w", err)
		}

		// If we have a terminal state, return
		if result.Merged || result.Closed || result.HasConflicts {
			return result, nil
		}

		// Check if context is done
		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				MRNumber: mrIID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		default:
		}

		// Check timeout
		if time.Now().After(deadline) {
			m.logger.Warn("MR merge timed out",
				slog.Int("mr_iid", mrIID),
				slog.Duration("timeout", m.config.Timeout),
			)
			return &MergeWaitResult{
				MRNumber: mrIID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out waiting for MR !%d to merge after %s", mrIID, m.config.Timeout),
			}, ErrMergeTimeout
		}

		// Wait for next tick
		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				MRNumber: mrIID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		case <-ticker.C:
			m.logger.Debug("Polling MR status",
				slog.Int("mr_iid", mrIID),
				slog.Duration("remaining", time.Until(deadline)),
			)
		}
	}
}

// checkMRStatus fetches and interprets the current MR status
func (m *MergeWaiter) checkMRStatus(ctx context.Context, mrIID int) (*MergeWaitResult, error) {
	mr, err := m.client.GetMergeRequest(ctx, mrIID)
	if err != nil {
		return nil, err
	}

	result := &MergeWaitResult{
		MRNumber: mrIID,
		MRURL:    mr.WebURL,
	}

	// Check if merged
	if mr.State == MRStateMerged {
		m.logger.Info("MR merged successfully",
			slog.Int("mr_iid", mrIID),
		)
		result.Merged = true
		result.Message = fmt.Sprintf("MR !%d was merged", mrIID)
		return result, nil
	}

	// Check if closed without merge
	if mr.State == MRStateClosed {
		m.logger.Warn("MR closed without merging",
			slog.Int("mr_iid", mrIID),
		)
		result.Closed = true
		result.Message = fmt.Sprintf("MR !%d was closed without merging", mrIID)
		return result, nil
	}

	// Check for merge conflicts
	if mr.HasConflicts {
		m.logger.Warn("MR has merge conflicts",
			slog.Int("mr_iid", mrIID),
		)
		result.HasConflicts = true
		result.Message = fmt.Sprintf("MR !%d has merge conflicts", mrIID)
		return result, nil
	}

	// Check pipeline status if available
	if mr.HeadPipeline != nil {
		switch mr.HeadPipeline.Status {
		case PipelineFailed:
			result.Message = fmt.Sprintf("MR !%d pipeline failed", mrIID)
		case PipelineRunning, PipelinePending:
			result.Message = fmt.Sprintf("MR !%d pipeline in progress", mrIID)
		case PipelineSuccess:
			result.Message = fmt.Sprintf("MR !%d ready for merge", mrIID)
		}
		return result, nil
	}

	// Still open and no conflicts
	result.Message = fmt.Sprintf("MR !%d is open, waiting for merge...", mrIID)
	return result, nil
}

// WaitWithCallback is like WaitForMerge but calls the callback on each poll
// This allows the caller to update UI or logs with current status
func (m *MergeWaiter) WaitWithCallback(ctx context.Context, mrIID int, onPoll func(result *MergeWaitResult)) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for MR merge with callback",
		slog.Int("mr_iid", mrIID),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkMRStatus(ctx, mrIID)
		if err != nil {
			return nil, fmt.Errorf("failed to check MR status: %w", err)
		}

		// Call the callback with current status
		if onPoll != nil {
			onPoll(result)
		}

		// If we have a terminal state, return
		if result.Merged || result.Closed || result.HasConflicts {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				MRNumber: mrIID,
				Message:  "Context cancelled",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return &MergeWaitResult{
				MRNumber: mrIID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out after %s", m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				MRNumber: mrIID,
				Message:  "Context cancelled",
			}, ctx.Err()
		case <-ticker.C:
			// Continue polling
		}
	}
}

// WaitForPipeline waits for the MR's pipeline to complete
func (m *MergeWaiter) WaitForPipeline(ctx context.Context, mrIID int) (string, error) {
	m.logger.Info("Waiting for pipeline",
		slog.Int("mr_iid", mrIID),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		mr, err := m.client.GetMergeRequest(ctx, mrIID)
		if err != nil {
			return "", fmt.Errorf("failed to get MR: %w", err)
		}

		if mr.HeadPipeline != nil {
			switch mr.HeadPipeline.Status {
			case PipelineSuccess:
				return PipelineSuccess, nil
			case PipelineFailed:
				return PipelineFailed, ErrPipelineFailed
			case PipelineCanceled:
				return PipelineCanceled, fmt.Errorf("pipeline was canceled")
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return "", ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			// Continue polling
		}
	}
}
