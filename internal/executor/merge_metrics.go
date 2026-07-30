package executor

// MergeMetricsRecorder is an interface for recording PR merges that the
// executor discovers outside the autopilot controller's own merge flow —
// e.g. self-heal noticing a task's branch was already merged on GitHub
// (boot orphan heal, stale-running heal) or the pre-execute short-circuit
// skipping a queued task whose branch merged while it waited. Without this,
// pilot_prs_merged_total undercounts every merge that ships via one of
// these paths instead of the controller's handleMerging (GH-4390).
//
// Satisfied by autopilot.Controller and autopilot.MultiControllerMergeRecorder.
// Kept as a mirrored interface here (rather than importing autopilot
// directly) to avoid an import cycle — autopilot already imports executor.
type MergeMetricsRecorder interface {
	// RecordExternalMerge records a merge for the given project path and PR
	// number, deduping against any merge already recorded for that PR
	// (e.g. by the controller's own handleMerging or an external-merge scan)
	// so the same PR is never double-counted across paths.
	RecordExternalMerge(projectPath string, prNumber int)
}
