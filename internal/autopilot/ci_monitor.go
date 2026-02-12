package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// CIMonitor watches GitHub CI status for PRs.
type CIMonitor struct {
	ghClient       *github.Client
	owner          string
	repo           string
	pollInterval   time.Duration
	waitTimeout    time.Duration
	requiredChecks []string
	log            *slog.Logger
}

// NewCIMonitor creates a CI monitor with configuration from Config.
// Uses DevCITimeout for dev environment, CIWaitTimeout for stage/prod.
func NewCIMonitor(ghClient *github.Client, owner, repo string, cfg *Config) *CIMonitor {
	timeout := cfg.CIWaitTimeout
	if cfg.Environment == EnvDev && cfg.DevCITimeout > 0 {
		timeout = cfg.DevCITimeout
	}
	return &CIMonitor{
		ghClient:       ghClient,
		owner:          owner,
		repo:           repo,
		pollInterval:   cfg.CIPollInterval,
		waitTimeout:    timeout,
		requiredChecks: cfg.RequiredChecks,
		log:            slog.Default().With("component", "ci-monitor"),
	}
}

// WaitForCI polls until all required checks complete or timeout.
// Returns CISuccess if all checks pass, CIFailure if any fail,
// or error on context cancellation or timeout.
func (m *CIMonitor) WaitForCI(ctx context.Context, sha string) (CIStatus, error) {
	deadline := time.Now().Add(m.waitTimeout)
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	// Log initial status
	m.log.Info("waiting for CI", "sha", ShortSHA(sha), "timeout", m.waitTimeout, "required_checks", m.requiredChecks)

	for {
		select {
		case <-ctx.Done():
			return CIPending, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return CIPending, fmt.Errorf("CI timeout after %v", m.waitTimeout)
			}

			status, err := m.checkStatus(ctx, sha)
			if err != nil {
				m.log.Warn("CI status check failed", "error", err)
				continue
			}

			m.log.Info("CI status", "sha", ShortSHA(sha), "status", status)

			if status == CISuccess || status == CIFailure {
				return status, nil
			}
		}
	}
}

// checkStatus gets current CI status for a SHA.
func (m *CIMonitor) checkStatus(ctx context.Context, sha string) (CIStatus, error) {
	// Get check runs (GitHub Actions)
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		return CIPending, err
	}

	// If no required checks configured, check all runs
	if len(m.requiredChecks) == 0 {
		return m.checkAllRuns(checkRuns), nil
	}

	// Track required checks
	requiredStatus := make(map[string]CIStatus)
	for _, name := range m.requiredChecks {
		requiredStatus[name] = CIPending
	}

	// Map check runs to status
	for _, run := range checkRuns.CheckRuns {
		if _, ok := requiredStatus[run.Name]; ok {
			requiredStatus[run.Name] = m.mapCheckStatus(run.Status, run.Conclusion)
		}
	}

	// Determine overall status
	return m.aggregateStatus(requiredStatus), nil
}

// checkAllRuns returns aggregate status when no required checks are configured.
func (m *CIMonitor) checkAllRuns(checkRuns *github.CheckRunsResponse) CIStatus {
	if checkRuns.TotalCount == 0 {
		return CIPending
	}

	hasFailure := false
	hasPending := false

	for _, run := range checkRuns.CheckRuns {
		status := m.mapCheckStatus(run.Status, run.Conclusion)
		switch status {
		case CIFailure:
			hasFailure = true
		case CIPending, CIRunning:
			hasPending = true
		}
	}

	if hasFailure {
		return CIFailure
	}
	if hasPending {
		return CIPending
	}
	return CISuccess
}

// aggregateStatus determines overall status from individual check statuses.
func (m *CIMonitor) aggregateStatus(statuses map[string]CIStatus) CIStatus {
	hasFailure := false
	hasPending := false

	for _, status := range statuses {
		switch status {
		case CIFailure:
			hasFailure = true
		case CIPending, CIRunning:
			hasPending = true
		}
	}

	if hasFailure {
		return CIFailure
	}
	if hasPending {
		return CIPending
	}
	return CISuccess
}

// mapCheckStatus maps GitHub check status to CIStatus.
func (m *CIMonitor) mapCheckStatus(status, conclusion string) CIStatus {
	switch status {
	case github.CheckRunQueued, github.CheckRunInProgress:
		return CIRunning
	case github.CheckRunCompleted:
		switch conclusion {
		case github.ConclusionSuccess:
			return CISuccess
		case github.ConclusionFailure, github.ConclusionCancelled, github.ConclusionTimedOut:
			return CIFailure
		case github.ConclusionSkipped, github.ConclusionNeutral:
			// Skipped/neutral checks don't block
			return CISuccess
		default:
			return CIPending
		}
	default:
		return CIPending
	}
}

// CheckCI checks CI status once and returns immediately.
// This is the non-blocking alternative to WaitForCI.
// Returns CIPending/CIRunning if checks are still running.
func (m *CIMonitor) CheckCI(ctx context.Context, sha string) (CIStatus, error) {
	status, err := m.checkStatus(ctx, sha)
	if err != nil {
		m.log.Debug("CheckCI: status check failed",
			"sha", ShortSHA(sha),
			"error", err,
		)
		return status, err
	}

	m.log.Debug("CheckCI: status check complete",
		"sha", ShortSHA(sha),
		"status", status,
		"required_checks", m.requiredChecks,
	)
	return status, nil
}

// GetCIStatus returns the current overall CI status for a SHA.
// This is useful for point-in-time status checks without waiting.
// Deprecated: Use CheckCI instead for clarity.
func (m *CIMonitor) GetCIStatus(ctx context.Context, sha string) (CIStatus, error) {
	return m.checkStatus(ctx, sha)
}

// GetFailedChecks returns names of failed checks for a SHA.
func (m *CIMonitor) GetFailedChecks(ctx context.Context, sha string) ([]string, error) {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		return nil, err
	}

	var failed []string
	for _, run := range checkRuns.CheckRuns {
		if run.Conclusion == github.ConclusionFailure {
			failed = append(failed, run.Name)
		}
	}
	return failed, nil
}

// GetCheckStatus returns the current status of a specific check by name.
func (m *CIMonitor) GetCheckStatus(ctx context.Context, sha, checkName string) (CIStatus, error) {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		return CIPending, err
	}

	for _, run := range checkRuns.CheckRuns {
		if run.Name == checkName {
			return m.mapCheckStatus(run.Status, run.Conclusion), nil
		}
	}

	return CIPending, nil
}
