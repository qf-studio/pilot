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

// SelfReviewExtractor extracts and persists patterns from self-review output.
// This interface is satisfied by memory.PatternExtractor and allows the executor
// to extract patterns without tight coupling to the memory package implementation.
type SelfReviewExtractor interface {
	ExtractFromSelfReview(ctx context.Context, selfReviewOutput string, projectPath string) (*memory.ExtractionResult, error)
	SaveExtractedPatterns(ctx context.Context, result *memory.ExtractionResult) error
}
