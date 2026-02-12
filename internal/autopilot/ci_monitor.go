package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sync"
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

	// CI checks configuration (auto-discovery)
	ciChecks *CIChecksConfig

	// Discovery state for auto mode
	discoveredChecks map[string][]string  // sha -> check names
	discoveryStart   map[string]time.Time // sha -> when discovery started
	mu               sync.RWMutex
}

// NewCIMonitor creates a CI monitor with configuration from Config.
// Uses DevCITimeout for dev environment, CIWaitTimeout for stage/prod.
// Handles both legacy RequiredChecks and new CIChecks configuration.
func NewCIMonitor(ghClient *github.Client, owner, repo string, cfg *Config) *CIMonitor {
	timeout := cfg.CIWaitTimeout
	if cfg.Environment == EnvDev && cfg.DevCITimeout > 0 {
		timeout = cfg.DevCITimeout
	}

	// Determine CI checks configuration
	var ciChecks *CIChecksConfig
	var requiredChecks []string

	if cfg.CIChecks != nil {
		ciChecks = cfg.CIChecks
		// If manual mode, use the Required list
		if ciChecks.Mode == "manual" && len(ciChecks.Required) > 0 {
			requiredChecks = ciChecks.Required
		}
	} else if len(cfg.RequiredChecks) > 0 {
		// Legacy: if RequiredChecks is set, use manual mode
		ciChecks = &CIChecksConfig{
			Mode:     "manual",
			Required: cfg.RequiredChecks,
		}
		requiredChecks = cfg.RequiredChecks
	} else {
		// Default: auto mode
		ciChecks = &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 60 * time.Second,
		}
	}

	// Ensure grace period has a default
	if ciChecks.DiscoveryGracePeriod == 0 {
		ciChecks.DiscoveryGracePeriod = 60 * time.Second
	}

	return &CIMonitor{
		ghClient:         ghClient,
		owner:            owner,
		repo:             repo,
		pollInterval:     cfg.CIPollInterval,
		waitTimeout:      timeout,
		requiredChecks:   requiredChecks,
		ciChecks:         ciChecks,
		discoveredChecks: make(map[string][]string),
		discoveryStart:   make(map[string]time.Time),
		log:              slog.Default().With("component", "ci-monitor"),
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

	// Store discovered check names for later retrieval (filtered by exclusions in auto mode)
	if len(checkRuns.CheckRuns) > 0 {
		m.mu.RLock()
		_, hasDiscovered := m.discoveredChecks[sha]
		m.mu.RUnlock()

		if !hasDiscovered {
			names := make([]string, 0, len(checkRuns.CheckRuns))
			for _, run := range checkRuns.CheckRuns {
				// In auto mode, filter out excluded checks
				if m.ciChecks != nil && m.ciChecks.Mode == "auto" && m.matchesExclude(run.Name) {
					continue
				}
				names = append(names, run.Name)
			}
			if len(names) > 0 {
				m.SetDiscoveredChecks(sha, names)
				m.log.Info("discovered CI checks", "sha", ShortSHA(sha), "checks", names, "mode", m.ciChecks.Mode)
			}
		}
	}

	// Auto mode: use discovered checks with exclusions and grace period
	if m.ciChecks != nil && m.ciChecks.Mode == "auto" {
		return m.checkAutoDiscoveredRuns(sha, checkRuns)
	}

	// Manual mode: If no required checks configured, check all runs
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

// checkAutoDiscoveredRuns checks CI status in auto mode with exclusion filtering.
// It waits during the grace period if no checks are found yet.
func (m *CIMonitor) checkAutoDiscoveredRuns(sha string, checkRuns *github.CheckRunsResponse) (CIStatus, error) {
	// Filter checks by exclusion patterns
	var filteredRuns []github.CheckRun
	for _, run := range checkRuns.CheckRuns {
		if !m.matchesExclude(run.Name) {
			filteredRuns = append(filteredRuns, run)
		}
	}

	// Handle grace period for check discovery
	if len(filteredRuns) == 0 {
		m.mu.Lock()
		startTime, exists := m.discoveryStart[sha]
		if !exists {
			// First check: start the grace period
			m.discoveryStart[sha] = time.Now()
			m.mu.Unlock()
			m.log.Debug("no CI checks found, starting grace period",
				"sha", ShortSHA(sha),
				"grace_period", m.ciChecks.DiscoveryGracePeriod,
			)
			return CIPending, nil
		}
		m.mu.Unlock()

		// Check if grace period has expired
		elapsed := time.Since(startTime)
		if elapsed < m.ciChecks.DiscoveryGracePeriod {
			m.log.Debug("waiting for CI checks during grace period",
				"sha", ShortSHA(sha),
				"elapsed", elapsed,
				"remaining", m.ciChecks.DiscoveryGracePeriod-elapsed,
			)
			return CIPending, nil
		}

		// Grace period expired with no checks - treat as success (no CI configured)
		m.log.Info("grace period expired with no CI checks, treating as success",
			"sha", ShortSHA(sha),
			"grace_period", m.ciChecks.DiscoveryGracePeriod,
		)
		return CISuccess, nil
	}

	// Clear discovery start since we found checks
	m.mu.Lock()
	delete(m.discoveryStart, sha)
	m.mu.Unlock()

	// Aggregate status from filtered runs
	hasFailure := false
	hasPending := false

	for _, run := range filteredRuns {
		status := m.mapCheckStatus(run.Status, run.Conclusion)
		switch status {
		case CIFailure:
			hasFailure = true
		case CIPending, CIRunning:
			hasPending = true
		}
	}

	if hasFailure {
		return CIFailure, nil
	}
	if hasPending {
		return CIPending, nil
	}
	return CISuccess, nil
}

// matchesExclude checks if a check name matches any exclusion pattern.
// Supports glob patterns using path.Match (e.g., "codecov/*", "*.optional").
func (m *CIMonitor) matchesExclude(name string) bool {
	if m.ciChecks == nil || len(m.ciChecks.Exclude) == 0 {
		return false
	}

	for _, pattern := range m.ciChecks.Exclude {
		// Try exact match first
		if pattern == name {
			return true
		}
		// Try glob match
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
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

// GetDiscoveredChecks returns the check names discovered for a SHA.
// Returns nil if no checks have been discovered yet.
func (m *CIMonitor) GetDiscoveredChecks(sha string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.discoveredChecks[sha]
}

// SetDiscoveredChecks stores discovered check names for a SHA.
// Called during CI status checks when checks are first seen.
func (m *CIMonitor) SetDiscoveredChecks(sha string, checks []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.discoveredChecks[sha] = checks
}

// ClearDiscovery removes discovery state for a SHA.
// Should be called when a PR is removed from tracking.
func (m *CIMonitor) ClearDiscovery(sha string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.discoveredChecks, sha)
	delete(m.discoveryStart, sha)
}
