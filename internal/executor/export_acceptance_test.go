package executor

import "context"

// AppendAcceptanceEvidenceForTest exposes appendAcceptanceEvidence to the
// external executor_test package. The GH-5485 wiring test must import the
// github adapter (which itself imports executor), so it cannot live in
// package executor.
func AppendAcceptanceEvidenceForTest(ctx context.Context, r *Runner, task *Task, workDir, prBody string) string {
	return r.appendAcceptanceEvidence(ctx, task, workDir, prBody)
}
