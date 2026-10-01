package executor

import (
	"context"
	"time"
)

// QualityGateDetail represents detailed information about a single gate check.
// This is used to pass gate results from the quality package to the executor
// without creating import cycles.
type QualityGateDetail struct {
	Name       string
	Passed     bool
	Duration   time.Duration
	RetryCount int
	Error      string
}

// QualityOutcome represents the result of quality gate checks.
// This mirrors quality.ExecutionOutcome to avoid import cycles.
type QualityOutcome struct {
	Passed        bool
	ShouldRetry   bool
	RetryFeedback string // Error feedback to send to Claude for retry
	Attempt       int
	// GateDetails contains detailed results for each gate
	GateDetails []QualityGateDetail
	// TotalDuration is the total time spent running all gates
	TotalDuration time.Duration
}

// QualityChecker is an interface for running quality gate checks.
// This interface allows the executor to run quality gates without
// importing the quality package directly, avoiding import cycles.
type QualityChecker interface {
	// Check runs all quality gates and returns the outcome
	Check(ctx context.Context) (*QualityOutcome, error)
}

// QualityCheckerFactory creates a QualityChecker for a specific task.
// This allows the runner to create quality checkers on demand with
// the correct task context without knowing about the quality package.
// The factory is typically implemented in main.go where both packages
// can be imported.
//
// GH-5577: projectPath is the task's project root (Task.ProjectPath) and is
// what per-project config lookups (e.g. a project's `quality:` override) must
// key on; executionPath is the directory the gates actually run in, which for
// an isolated-worktree execution is a throwaway worktree that matches no
// configured project. The two are equal when the task runs in place.
type QualityCheckerFactory func(taskID, projectPath, executionPath string) QualityChecker
