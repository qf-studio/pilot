package executor

import (
	"context"

	"github.com/alekspetrov/pilot/internal/memory"
)

// LearningRecorder records execution outcomes for pattern learning.
// This interface is satisfied by memory.LearningLoop and allows the executor
// to record executions without tight coupling to the memory package implementation.
type LearningRecorder interface {
	RecordExecution(ctx context.Context, exec *memory.Execution, appliedPatterns []string) error
}
