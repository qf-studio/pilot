package executor

import "context"

// QualityOutcome represents the result of quality gate checks.
// This mirrors quality.ExecutionOutcome to avoid import cycles.
type QualityOutcome struct {
	Passed        bool
	ShouldRetry   bool
	RetryFeedback string // Error feedback to send to Claude for retry
	Attempt       int
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
type QualityCheckerFactory func(taskID, projectPath string) QualityChecker
