package executor

import (
	"context"

	"github.com/qf-studio/pilot/internal/memory"
)

// LearningRecorder records execution outcomes for pattern learning.
// This interface is satisfied by memory.LearningLoop and allows the executor
// to record executions without tight coupling to the memory package implementation.
type LearningRecorder interface {
	RecordExecution(ctx context.Context, exec *memory.Execution, appliedPatterns []string) error
}

// KnowledgeGraphRecorder records execution learnings into a knowledge graph.
// This interface is satisfied by memory.KnowledgeGraph and allows the executor
// to record learnings without tight coupling to the memory package implementation.
type KnowledgeGraphRecorder interface {
	AddExecutionLearning(title, content string, filesChanged []string, patterns []string, outcome string) error
	GetRelatedByKeywords(keywords []string) []*memory.GraphNode
}

// SelfReviewExtractor extracts and persists patterns from self-review output.
// This interface is satisfied by memory.PatternExtractor and allows the executor
// to extract patterns without tight coupling to the memory package implementation.
type SelfReviewExtractor interface {
	ExtractFromSelfReview(ctx context.Context, selfReviewOutput string, projectPath string) (*memory.ExtractionResult, error)
	SaveExtractedPatterns(ctx context.Context, result *memory.ExtractionResult) error
}
