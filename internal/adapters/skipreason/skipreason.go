// Package skipreason defines shared constants for poller dispatch-skip label values.
// All three pollers (GitHub, GitLab, Azure DevOps) use these constants so that
// the pilot_poller_skipped_total{reason} label is consistent across adapters.
package skipreason

const (
	ReasonInProgress         = "in_progress"
	ReasonDone               = "done"
	ReasonBlocked            = "blocked"
	ReasonNeedsClarification = "needs_clarification"
	ReasonSuperseded         = "superseded"
	ReasonFailedSkip         = "failed_skip"
	ReasonRetryReadySkip     = "retry_ready_skip"
	ReasonProcessedGrace     = "processed_grace"
	ReasonCompletedExecution = "completed_execution"
)
