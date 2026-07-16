// Package skipreason defines shared constants and interfaces for poller skip metrics.
// All three pollers (GitHub, GitLab, Azure DevOps) use these constants as the
// `reason` label value in pilot_poller_skipped_total.
package skipreason

// Reason constants — used as the `reason` label in pilot_poller_skipped_total.
const (
	ReasonInProgress         = "in_progress"
	ReasonDone               = "done"
	ReasonBlocked            = "blocked"
	ReasonNeedsClarification = "needs_clarification"
	ReasonSuperseded         = "superseded"
	ReasonFailedSkip         = "failed_skip"
	ReasonRetryReadySkip     = "retry_ready_skip"
	ReasonProcessedGrace     = "processed_grace"
	ReasonTaskQueued         = "task_queued"
	ReasonHasMergedWork      = "has_merged_work"
	ReasonHasOpenPR          = "has_open_pr" // TASK-341: PR created, awaiting merge
	ReasonPendingDependency  = "pending_dependency"
	ReasonCompletedExecution = "completed_execution"
	ReasonFreshLabelCheck    = "fresh_label_check"
	ReasonPreFlightReject    = "pre_flight_reject"
	ReasonStatusLabel        = "status_label" // GitLab combined in_progress/done/failed
	ReasonStatusTag          = "status_tag"   // Azure DevOps combined in_progress/done/failed
	ReasonClosedIssue        = "closed_issue" // GH-4183: closed issue is terminal, never re-arm
	// ReasonRepickStormBackoff (GH-4376) is recorded by the shared dispatch
	// chokepoint (cmd/pilot/handler_common.go), not by a poller directly —
	// a claim-lost or completed-but-open re-pick that's currently within its
	// per-issue backoff window, or that the chokepoint's own
	// HasTerminalCompletion re-check caught independently of the poller.
	ReasonRepickStormBackoff = "repick_storm_backoff"
)

// PollerMetricsRecorder records poller dispatch/skip counters.
// Implemented by *autopilot.Metrics; kept as an interface here so all three
// adapter packages can reference it without importing autopilot directly.
type PollerMetricsRecorder interface {
	RecordPollerSkipped(repo, reason string)
	RecordPollerDispatched(repo string)
	RecordPollerDeferredScopeOverlap(repo string)
}
