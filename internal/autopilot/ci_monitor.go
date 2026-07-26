package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// StepLogClient is the minimal in-tree GitHub client surface CIMonitor needs
// to resolve a failed check run down to its actual failing step (GH-4460):
// the jobs API for the step breakdown, and check-run annotations as a
// fallback for check runs that aren't backed by a GitHub Actions job (e.g.
// third-party Checks-API integrations report annotations but have no
// job/step breakdown). This is deliberately a separate, in-tree client
// (internal/adapters/github) rather than the studio-sdk client CIMonitor
// otherwise uses — the SDK does not yet expose the jobs/annotations APIs.
type StepLogClient interface {
	GetWorkflowJob(ctx context.Context, owner, repo string, jobID int64) (*ghadapter.WorkflowJob, error)
	GetCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]ghadapter.CheckRunAnnotation, error)
	// GetWorkflowRunIDForJob and RerunFailedJobs back the GH-4533
	// infra-failure auto-retry path: a failed check run only carries a job
	// ID, but the rerun-failed-jobs API operates on the owning workflow run.
	GetWorkflowRunIDForJob(ctx context.Context, owner, repo string, jobID int64) (int64, error)
	RerunFailedJobs(ctx context.Context, owner, repo string, runID int64) error
}

// CIMonitor watches GitHub CI status for PRs.
type CIMonitor struct {
	ghClient       *github.Client
	owner          string
	repo           string
	pollInterval   time.Duration
	waitTimeout    time.Duration
	requiredChecks []string
	log            *slog.Logger

	// stepLogClient is optional (GH-4460). When unset, GetFailedCheckExcerpts
	// falls back to whole-job-log tails instead of resolving the specific
	// failing step.
	stepLogClient StepLogClient

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
	envCITimeout := cfg.ResolvedEnvOrDefault().CITimeout
	if envCITimeout > 0 && (timeout == 0 || envCITimeout < timeout) {
		timeout = envCITimeout
	}

	// Determine CI checks configuration
	var ciChecks *CIChecksConfig
	var requiredChecks []string
	startupLog := slog.Default().With("component", "ci-monitor")

	switch {
	case cfg.CIChecks != nil && len(cfg.CIChecks.Required) > 0:
		// GH-4307: honor a non-empty Required allowlist regardless of Mode.
		// Previously this was gated on Mode == "manual", so an operator running
		// auto-discovery with an explicit Required list (e.g. to shield against
		// unrelated scheduled/canary checks landing on the same SHA) had that
		// list silently ignored — checkStatus fell through to
		// checkAutoDiscoveredRuns, which aggregates every non-excluded check.
		ciChecks = cfg.CIChecks
		requiredChecks = ciChecks.Required
	case len(cfg.RequiredChecks) > 0:
		// GH-4333: ci_checks.required is empty (or ci_checks is unset) but the
		// legacy required_checks allowlist is populated. Before this fix, any
		// non-nil cfg.CIChecks (which DefaultConfig always sets) silently
		// dropped the legacy list here with zero warning, leaving operators
		// who believed required_checks was shielding them with no allowlist
		// active at all (RCA of #4331).
		mode := "manual"
		var exclude []string
		var grace time.Duration
		if cfg.CIChecks != nil {
			exclude = cfg.CIChecks.Exclude
			grace = cfg.CIChecks.DiscoveryGracePeriod
		}
		ciChecks = &CIChecksConfig{
			Mode:                 mode,
			Exclude:              exclude,
			Required:             cfg.RequiredChecks,
			DiscoveryGracePeriod: grace,
		}
		requiredChecks = cfg.RequiredChecks
		startupLog.Warn("falling back to deprecated required_checks allowlist because ci_checks.required is empty; migrate to ci_checks.required",
			"required_checks", cfg.RequiredChecks)
	default:
		// Both ci_checks.required and the legacy required_checks are empty:
		// no allowlist is active, so every non-excluded check on the SHA
		// participates in the CI gate. This is valid (e.g. intentional
		// auto-discovery), but it's also exactly the trap an operator hits
		// when they believe an allowlist is shielding them and it silently
		// isn't (RCA of #4331) — so make it visible at startup.
		if cfg.CIChecks != nil {
			ciChecks = cfg.CIChecks
		} else {
			ciChecks = &CIChecksConfig{
				Mode:                 "auto",
				DiscoveryGracePeriod: 60 * time.Second,
			}
		}
		startupLog.Warn("no CI required-checks allowlist configured (ci_checks.required and required_checks are both empty); all non-excluded checks on the SHA will gate CI status")
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
		log:              startupLog,
	}
}

// SetStepLogClient injects the in-tree GitHub client used to resolve a
// failed check run down to its specific failing step (GH-4460). Optional:
// without it, GetFailedCheckExcerpts falls back to whole-job-log tails.
func (m *CIMonitor) SetStepLogClient(c StepLogClient) {
	m.stepLogClient = c
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
//
// GH-4384: once check-runs have been discovered for a SHA (m.discoveredChecks
// is non-empty), check-runs IS the completion source of truth for that SHA —
// the legacy combined-status endpoint must never be consulted again for it.
// GitHub Actions only ever writes check-runs, never legacy commit statuses, so
// combined-status permanently reports state=pending/total_count=0 for an
// Actions-only repo. Discovery (top of checkStatus) and completion (here) must
// read the same API for the same SHA, or a repo with zero required_checks and
// an empty combined-status response looks identical to "CI still pending"
// forever, even though the discovered check-runs already completed green
// (observed on qf-studio/pointer PRs #5/#6/#7: 30m CI timeouts with green
// checks). A transient empty ListCheckRuns response on a later poll — GitHub's
// check-runs listing is eventually consistent — must not be mistaken for "no
// CI configured" once this SHA is already known to report via check-runs.
func (m *CIMonitor) checkAutoDiscoveredRuns(ctx context.Context, sha string, checkRuns *github.CheckRunsResponse, skipGrace bool) (CIStatus, error) {
	// Filter checks by exclusion patterns
	var filteredRuns []github.CheckRun
	for _, run := range checkRuns.CheckRuns {
		if !m.matchesExclude(run.Name) {
			filteredRuns = append(filteredRuns, run)
		}
	}

	if len(filteredRuns) == 0 {
		m.mu.RLock()
		alreadyDiscovered := len(m.discoveredChecks[sha]) > 0
		m.mu.RUnlock()

		if alreadyDiscovered {
			// Check-runs already resolved as the source of truth for this SHA;
			// a momentarily empty response is a transient read, not evidence
			// that CI stopped existing. Never fall through to combined-status.
			m.log.Debug("check-runs momentarily empty for a SHA with prior discovery; staying pending rather than consulting combined-status",
				"sha", ShortSHA(sha),
			)
			return CIPending, nil
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

// FailedCheckLog pairs one failed, in-scope check run's name and job ID with
// its raw job log — GH-4533's infra-vs-code classifier
// (classifyCheckFailure) needs to evaluate each failed check's log
// independently rather than the single concatenated blob GetFailedCheckLogs
// produces, and the auto-retry path needs each check's job ID to resolve its
// owning workflow run via GetWorkflowRunIDForJob.
type FailedCheckLog struct {
	CheckName string
	JobID     int64
	Logs      string
}

// GetFailedCheckLogsByCheck fetches raw job logs for each failed, in-scope
// check run individually, scoped by isScopedCheck (same scoping as
// GetFailedChecks, GH-4307). It reuses the GetJobLogs path from
// GetFailedCheckLogs above but keeps each check's log (and job ID) separate
// instead of concatenating them into one budget-capped blob, so callers
// (GH-4533) can classify each failure independently.
//
// A log-fetch failure for one check still yields an entry for that check,
// with an empty Logs field, rather than dropping it silently — every failed,
// in-scope check must be accounted for before the caller can conclude "every
// check classifies infra", and classifyCheckFailure already treats empty
// logs as FailureClassCode (fail-safe).
func (m *CIMonitor) GetFailedCheckLogsByCheck(ctx context.Context, sha string) []FailedCheckLog {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		m.log.Warn("failed to list check runs for per-check log fetch", "sha", ShortSHA(sha), "error", err)
		return nil
	}

	var results []FailedCheckLog
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
		}
		results = append(results, FailedCheckLog{CheckName: run.Name, JobID: run.ID, Logs: logs})
	}
	return results
}

// GetFailedCheckExcerpts fetches, for each failed check run on sha, the tail
// of its actual failing step (GH-4460) rather than the whole job log, and
// assembles them into a single budget-capped, self-contained block: a
// heading, the tail excerpt, and a permalink fallback per check. Returns ""
// when there are no in-scope failed checks or the check-run list can't be
// fetched.
//
// This replaces GetFailedCheckLogs in the fix-issue creation path: that
// method's small maxLen (2000 chars, applied to the head of the *combined*
// blob) let one check's runner-setup preamble consume the entire budget
// before the real failure line was ever reached, which is what made every
// GH-4415 continuation (4444/4446/4449/4453) bounce at preflight with
// "provide only runner setup information".
func (m *CIMonitor) GetFailedCheckExcerpts(ctx context.Context, sha string) string {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		m.log.Warn("failed to list check runs for excerpt fetch", "sha", ShortSHA(sha), "error", err)
		return ""
	}

	var excerpts []FailingStepExcerpt
	for _, run := range checkRuns.CheckRuns {
		if run.Conclusion != github.ConclusionFailure {
			continue
		}
		if !m.isScopedCheck(run.Name) {
			continue
		}
		excerpts = append(excerpts, m.buildFailingStepExcerpt(ctx, run))
	}

	body := AssembleFailureExcerptsBody(excerpts, failedCheckExcerptBudgetChars)
	if body == "" {
		return ""
	}
	return ciExcerptSentinel + body
}

// buildFailingStepExcerpt resolves one failed check run down to its actual
// failing step and returns the trailing lines of that step's log. It falls
// through progressively coarser signal when the finer one is unavailable:
//  1. jobs API step breakdown + timestamp-sliced job log tail (GitHub
//     Actions-backed checks — the common case)
//  2. check-run annotations (checks posted directly via the Checks API by
//     third-party CI apps have no job/step breakdown at all)
//  3. whole job log, last N lines (old behavior, still an improvement over
//     the previous head-of-combined-blob truncation)
func (m *CIMonitor) buildFailingStepExcerpt(ctx context.Context, run github.CheckRun) FailingStepExcerpt {
	excerpt := FailingStepExcerpt{
		CheckName:    run.Name,
		PermalinkURL: run.DetailsURL,
	}

	if m.stepLogClient != nil {
		job, jobErr := m.stepLogClient.GetWorkflowJob(ctx, m.owner, m.repo, run.ID)
		if jobErr != nil {
			m.log.Debug("jobs API lookup failed, falling back", "check", run.Name, "id", run.ID, "error", jobErr)
		} else if step, found := resolveFailingStep(job.Steps); found {
			if jobLog, logErr := m.ghClient.GetJobLogs(ctx, m.owner, m.repo, run.ID); logErr == nil {
				if window, ok := sliceLogByStepWindow(jobLog, step); ok {
					excerpt.StepName = step.Name
					excerpt.Tail = tailLines(window, failedCheckExcerptMaxLines)
					excerpt.Source = "step"
					return excerpt
				}
			}
		}

		if anns, annErr := m.stepLogClient.GetCheckRunAnnotations(ctx, m.owner, m.repo, run.ID); annErr == nil {
			var sb strings.Builder
			for _, a := range anns {
				if a.AnnotationLevel != "failure" {
					continue
				}
				fmt.Fprintf(&sb, "%s:%d: %s\n", a.Path, a.StartLine, a.Message)
			}
			if sb.Len() > 0 {
				excerpt.Tail = sb.String()
				excerpt.Source = "annotations"
				return excerpt
			}
		}
	}

	// Fallback: whole job log, tail only (still better than head-of-log).
	jobLog, err := m.ghClient.GetJobLogs(ctx, m.owner, m.repo, run.ID)
	if err != nil {
		m.log.Warn("failed to fetch logs for check run", "check", run.Name, "id", run.ID, "error", err)
		return excerpt
	}
	excerpt.Tail = tailLines(jobLog, failedCheckExcerptMaxLines)
	excerpt.Source = "job"
	return excerpt
}

// GetCheckLogs fetches logs for all completed, in-scope check runs for sha and
// returns them combined, each prefixed with the check name. Unlike
// GetFailedCheckLogs, this includes successful/skipped runs too — the
// test-evidence gate (GH-4329) needs to see the test job's own passing output
// to tell a rigorous run from one that silently skipped everything.
func (m *CIMonitor) GetCheckLogs(ctx context.Context, sha string, maxLen int) string {
	checkRuns, err := m.ghClient.ListCheckRuns(ctx, m.owner, m.repo, sha)
	if err != nil {
		m.log.Warn("failed to list check runs for log fetch", "sha", ShortSHA(sha), "error", err)
		return ""
	}

	var combined strings.Builder
	for _, run := range checkRuns.CheckRuns {
		if run.Status != github.CheckRunCompleted {
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
