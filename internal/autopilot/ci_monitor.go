package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
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
// The effective CI wait timeout is the minimum of CIWaitTimeout (user override) and
// the environment-specific CITimeout. This lets environments define shorter timeouts
// (e.g. dev uses 5m) while still respecting explicit user overrides in tests or configs.
// Handles both legacy RequiredChecks and new CIChecks configuration.
func NewCIMonitor(ghClient *github.Client, owner, repo string, cfg *Config) *CIMonitor {
	timeout := cfg.CIWaitTimeout
	envCITimeout := cfg.ResolvedEnv().CITimeout
	if envCITimeout > 0 && (timeout == 0 || envCITimeout < timeout) {
		timeout = envCITimeout
	}

	// Determine CI checks configuration
	var ciChecks *CIChecksConfig
	var requiredChecks []string

	if cfg.CIChecks != nil {
		ciChecks = cfg.CIChecks
		// GH-4307: honor a non-empty Required allowlist regardless of Mode.
		// Previously this was gated on Mode == "manual", so an operator running
		// auto-discovery with an explicit Required list (e.g. to shield against
		// unrelated scheduled/canary checks landing on the same SHA) had that
		// list silently ignored — checkStatus fell through to
		// checkAutoDiscoveredRuns, which aggregates every non-excluded check.
		if len(ciChecks.Required) > 0 {
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

			status, err := m.checkStatus(ctx, sha, false)
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
// skipGrace, when true, bypasses the no-CI discovery grace period entirely
// (see checkAutoDiscoveredRuns) for callers that already know CI resolved
// once for this SHA earlier in the PR lifecycle.
func (m *CIMonitor) checkStatus(ctx context.Context, sha string, skipGrace bool) (CIStatus, error) {
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

	// GH-4307: a non-empty required-checks allowlist always wins, even in auto
	// mode. Auto-discovery aggregates every check run on the SHA (minus
	// Exclude patterns), so an always-on scheduled workflow (e.g. a canary)
	// that happens to attach a check run to the same merge SHA can flip status
	// to CIFailure despite being unrelated to the PR. An explicit Required
	// list scopes status to exactly those checks, sidestepping that class of
	// false positive without requiring every such workflow to be Exclude-d.
	if len(m.requiredChecks) > 0 {
		return m.checkRequiredChecks(checkRuns), nil
	}

	// Auto mode: use discovered checks with exclusions and grace period
	if m.ciChecks != nil && m.ciChecks.Mode == "auto" {
		return m.checkAutoDiscoveredRuns(ctx, sha, checkRuns, skipGrace)
	}

	// Manual mode with no required checks configured: check all runs
	return m.checkAllRuns(checkRuns), nil
}

// checkRequiredChecks aggregates status from only the checks named in
// m.requiredChecks, ignoring every other check run on the SHA.
func (m *CIMonitor) checkRequiredChecks(checkRuns *github.CheckRunsResponse) CIStatus {
	requiredStatus := make(map[string]CIStatus)
	for _, name := range m.requiredChecks {
		requiredStatus[name] = CIPending
	}

	for _, run := range checkRuns.CheckRuns {
		if _, ok := requiredStatus[run.Name]; ok {
			requiredStatus[run.Name] = m.mapCheckStatus(run.Status, run.Conclusion)
		}
	}

	return m.aggregateStatus(requiredStatus)
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

	// B4 (TASK-345): only declare failure once no check is still pending — a
	// fail-fast matrix leg or flaky check can report failure before siblings
	// finish; wait (CIPending) so the suite completes / flaky checks auto-rerun.
	if hasFailure && !hasPending {
		return CIFailure
	}
	if hasPending {
		return CIPending
	}
	return CISuccess
}

// checkAutoDiscoveredRuns checks CI status in auto mode with exclusion filtering.
// It waits during the grace period if no checks are found yet, then falls back
// to the commit-status API before treating a SHA as having no CI configured.
// skipGrace bypasses the discoveryStart wait/eviction entirely and goes straight
// to the commit-status fallback — for callers (verifyCIBeforeMerge, via
// GetCIStatus) that only run after this SHA already resolved to CISuccess once
// earlier in the PR lifecycle, so re-discovering "no CI" from scratch would
// incorrectly restart the grace period (GH-3873).
func (m *CIMonitor) checkAutoDiscoveredRuns(ctx context.Context, sha string, checkRuns *github.CheckRunsResponse, skipGrace bool) (CIStatus, error) {
	// Filter checks by exclusion patterns
	var filteredRuns []github.CheckRun
	for _, run := range checkRuns.CheckRuns {
		if !m.matchesExclude(run.Name) {
			filteredRuns = append(filteredRuns, run)
		}
	}

	// Handle grace period for check discovery
	if len(filteredRuns) == 0 {
		if !skipGrace {
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

			// TASK-357 (B6b): the grace period is over and this is a terminal decision for
			// the SHA — evict its discoveryStart entry so it does not leak for the daemon
			// lifetime. Previously only the "checks found" path below deleted it, so every
			// no-CI SHA (and every superseded intermediate commit) leaked an entry.
			m.mu.Lock()
			delete(m.discoveryStart, sha)
			m.mu.Unlock()
		}

		// Grace period expired (or skipped) with no check runs — query commit-status
		// API before concluding that no CI is configured. Providers like CircleCI,
		// Jenkins, Travis, and Buildkite report exclusively via the statuses API.
		combined, err := m.ghClient.GetCombinedStatus(ctx, m.owner, m.repo, sha)
		if err != nil {
			m.log.Warn("grace period expired; combined-status lookup failed, treating as no CI",
				"sha", ShortSHA(sha),
				"error", err,
			)
			return CISuccess, nil
		}
		status := m.mapCombinedStatus(combined)
		m.log.Info("grace period expired with no check runs; using commit-status API",
			"sha", ShortSHA(sha),
			"combined_state", combined.State,
			"total_count", combined.TotalCount,
			"status", status,
		)
		return status, nil
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

	// B4 (TASK-345): only declare failure once no check is still pending — a
	// fail-fast matrix leg or flaky check can report failure before siblings
	// finish; wait (CIPending) so the suite completes / flaky checks auto-rerun.
	if hasFailure && !hasPending {
		return CIFailure, nil
	}
	if hasPending {
		return CIPending, nil
	}
	return CISuccess, nil
}

// mapCombinedStatus converts a GitHub combined commit-status response into CIStatus.
// TotalCount==0 means no status contexts exist → genuine no-CI repo → CISuccess.
func (m *CIMonitor) mapCombinedStatus(combined *github.CombinedStatus) CIStatus {
	if combined.TotalCount == 0 {
		return CISuccess
	}
	switch combined.State {
	case github.StatusFailure, github.StatusError:
		return CIFailure
	case github.StatusPending:
		return CIPending
	default:
		return CISuccess
	}
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

	// B4 (TASK-345): only declare failure once no check is still pending — a
	// fail-fast matrix leg or flaky check can report failure before siblings
	// finish; wait (CIPending) so the suite completes / flaky checks auto-rerun.
	if hasFailure && !hasPending {
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
	status, err := m.checkStatus(ctx, sha, false)
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
// Its sole production caller (verifyCIBeforeMerge) only runs after the PR
// state machine already reached StageCIPassed, proving CI resolved CISuccess
// once on this SHA — so it skips the no-CI discovery grace period rather than
// restarting it (GH-3873).
// Deprecated: Use CheckCI instead for clarity.
func (m *CIMonitor) GetCIStatus(ctx context.Context, sha string) (CIStatus, error) {
	return m.checkStatus(ctx, sha, true)
}

// GetFailedChecks returns names of failed checks for a SHA. It applies the
// same scoping as checkStatus (GH-4307): with a required-checks allowlist,
// only failures among those names are reported; otherwise, in auto mode,
// Exclude-matched checks (e.g. an always-on scheduled canary) are dropped so
// fix-issue bodies don't attribute an unrelated failure to this SHA's CI.
func (m *CIMonitor) GetFailedChecks(ctx context.Context, sha string) ([]string, error) {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		return nil, err
	}

	var failed []string
	for _, run := range checkRuns.CheckRuns {
		if run.Conclusion != github.ConclusionFailure {
			continue
		}
		if !m.isScopedCheck(run.Name) {
			continue
		}
		failed = append(failed, run.Name)
	}
	return failed, nil
}

// isScopedCheck reports whether a check name is in scope for CI status/failure
// attribution: within the required-checks allowlist when one is configured,
// otherwise not matching an auto-mode Exclude pattern.
func (m *CIMonitor) isScopedCheck(name string) bool {
	if len(m.requiredChecks) > 0 {
		for _, required := range m.requiredChecks {
			if required == name {
				return true
			}
		}
		return false
	}
	if m.ciChecks != nil && m.ciChecks.Mode == "auto" && m.matchesExclude(name) {
		return false
	}
	return true
}

// GetFailedCheckLogs fetches logs for all failed check runs and returns them
// as a combined string. Each check's logs are prefixed with the check name.
// Logs are truncated to maxLen total characters to keep issues readable.
// GH-1567: Include actual CI error output in fix issues.
func (m *CIMonitor) GetFailedCheckLogs(ctx context.Context, sha string, maxLen int) string {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		m.log.Warn("failed to list check runs for log fetch", "sha", ShortSHA(sha), "error", err)
		return ""
	}

	var combined strings.Builder
	for _, run := range checkRuns.CheckRuns {
		if run.Conclusion != github.ConclusionFailure {
			continue
		}
		if !m.isScopedCheck(run.Name) {
			continue
		}

		logs, err := m.ghClient.GetJobLogs(ctx, m.owner, m.repo, run.ID)
		if err != nil {
			m.log.Warn("failed to fetch logs for check run",
				"check", run.Name,
				"id", run.ID,
				"error", err,
			)
			continue
		}

		if combined.Len() > 0 {
			combined.WriteString("\n\n")
		}
		combined.WriteString(fmt.Sprintf("=== %s ===\n", run.Name))
		combined.WriteString(logs)

		if combined.Len() >= maxLen {
			break
		}
	}

	result := combined.String()
	if len(result) > maxLen {
		result = result[:maxLen]
	}
	return result
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
