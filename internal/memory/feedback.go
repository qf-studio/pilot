package memory

import (
	"context"
	"fmt"
	"time"
)

// FeedbackOutcome represents the outcome when a pattern is applied
type FeedbackOutcome string

const (
	OutcomeSuccess FeedbackOutcome = "success"
	OutcomeFailure FeedbackOutcome = "failure"
	OutcomeNeutral FeedbackOutcome = "neutral"
)

// LearningLoop implements the pattern learning feedback loop
type LearningLoop struct {
	store          *Store
	extractor      *PatternExtractor
	feedbackWeight float64
	decayRate      float64
}

// LearningConfig configures the learning loop
type LearningConfig struct {
	FeedbackWeight float64 // How much each feedback affects confidence (default: 0.1)
	DecayRate      float64 // Monthly decay for unused patterns (default: 0.01)
}

// DefaultLearningConfig returns default learning configuration
func DefaultLearningConfig() *LearningConfig {
	return &LearningConfig{
		FeedbackWeight: 0.1,
		DecayRate:      0.01,
	}
}

// NewLearningLoop creates a new learning loop
func NewLearningLoop(store *Store, extractor *PatternExtractor, config *LearningConfig) *LearningLoop {
	if config == nil {
		config = DefaultLearningConfig()
	}

	return &LearningLoop{
		store:          store,
		extractor:      extractor,
		feedbackWeight: config.FeedbackWeight,
		decayRate:      config.DecayRate,
	}
}

// RecordExecution records an execution and updates patterns accordingly
func (l *LearningLoop) RecordExecution(ctx context.Context, exec *Execution, appliedPatterns []string) error {
	// Determine outcome based on execution status
	outcome := OutcomeNeutral
	if exec.Status == "completed" {
		outcome = OutcomeSuccess
	} else if exec.Status == "failed" {
		outcome = OutcomeFailure
	}

	// Record feedback for each applied pattern
	for _, patternID := range appliedPatterns {
		feedback := &PatternFeedback{
			PatternID:   patternID,
			ExecutionID: exec.ID,
			ProjectPath: exec.ProjectPath,
			Outcome:     string(outcome),
			ConfidenceDelta: l.calculateConfidenceDelta(outcome),
		}

		if err := l.store.RecordPatternFeedback(feedback); err != nil {
			return fmt.Errorf("failed to record feedback for pattern %s: %w", patternID, err)
		}
	}

	// If successful, extract new patterns from the execution
	if outcome == OutcomeSuccess && l.extractor != nil {
		if err := l.extractor.ExtractAndSave(ctx, exec); err != nil {
			// Log but don't fail - pattern extraction is optional
			_ = err
		}
	}

	return nil
}

// calculateConfidenceDelta calculates how much confidence should change
func (l *LearningLoop) calculateConfidenceDelta(outcome FeedbackOutcome) float64 {
	switch outcome {
	case OutcomeSuccess:
		return l.feedbackWeight
	case OutcomeFailure:
		return l.feedbackWeight * 1.5 // Failures have more impact
	default:
		return 0
	}
}

// ApplyDecay applies confidence decay to unused patterns
func (l *LearningLoop) ApplyDecay(ctx context.Context) (int, error) {
	// Get patterns that haven't been used recently
	staleThreshold := time.Now().AddDate(0, -3, 0) // 3 months

	patterns, err := l.store.GetTopCrossPatterns(1000, 0) // Get all patterns
	if err != nil {
		return 0, fmt.Errorf("failed to get patterns: %w", err)
	}

	updated := 0
	for _, p := range patterns {
		if p.UpdatedAt.Before(staleThreshold) {
			// Apply decay
			newConfidence := p.Confidence * (1 - l.decayRate)
			if newConfidence < 0.1 {
				// Pattern has decayed too much, mark for potential cleanup
				newConfidence = 0.1
			}

			p.Confidence = newConfidence
			if err := l.store.SaveCrossPattern(p); err != nil {
				return updated, fmt.Errorf("failed to update pattern %s: %w", p.ID, err)
			}
			updated++
		}
	}

	return updated, nil
}

// DeprecateLowConfidencePatterns marks or removes patterns with very low confidence
func (l *LearningLoop) DeprecateLowConfidencePatterns(ctx context.Context, threshold float64) (int, error) {
	patterns, err := l.store.GetTopCrossPatterns(1000, 0)
	if err != nil {
		return 0, err
	}

	deprecated := 0
	for _, p := range patterns {
		if p.Confidence < threshold && p.Occurrences < 3 {
			// Low confidence and rarely used - deprecate
			if err := l.store.DeleteCrossPattern(p.ID); err != nil {
				return deprecated, err
			}
			deprecated++
		}
	}

	return deprecated, nil
}

// GetPatternPerformance returns performance metrics for a pattern
func (l *LearningLoop) GetPatternPerformance(ctx context.Context, patternID string) (*PatternPerformance, error) {
	pattern, err := l.store.GetCrossPattern(patternID)
	if err != nil {
		return nil, err
	}

	links, err := l.store.GetProjectsForPattern(patternID)
	if err != nil {
		return nil, err
	}

	var totalUses, totalSuccess, totalFailure int
	for _, link := range links {
		totalUses += link.Uses
		totalSuccess += link.SuccessCount
		totalFailure += link.FailureCount
	}

	successRate := 0.0
	if totalSuccess+totalFailure > 0 {
		successRate = float64(totalSuccess) / float64(totalSuccess+totalFailure)
	}

	return &PatternPerformance{
		PatternID:    patternID,
		Title:        pattern.Title,
		Type:         pattern.Type,
		Confidence:   pattern.Confidence,
		TotalUses:    totalUses,
		SuccessCount: totalSuccess,
		FailureCount: totalFailure,
		SuccessRate:  successRate,
		ProjectCount: len(links),
		IsAntiPattern: pattern.IsAntiPattern,
	}, nil
}

// PatternPerformance holds performance metrics for a pattern
type PatternPerformance struct {
	PatternID     string
	Title         string
	Type          string
	Confidence    float64
	TotalUses     int
	SuccessCount  int
	FailureCount  int
	SuccessRate   float64
	ProjectCount  int
	IsAntiPattern bool
}

// GetTopPerformingPatterns returns patterns with the best success rates
func (l *LearningLoop) GetTopPerformingPatterns(ctx context.Context, limit int) ([]*PatternPerformance, error) {
	patterns, err := l.store.GetTopCrossPatterns(100, 0.5)
	if err != nil {
		return nil, err
	}

	var performances []*PatternPerformance
	for _, p := range patterns {
		perf, err := l.GetPatternPerformance(ctx, p.ID)
		if err != nil {
			continue
		}
		performances = append(performances, perf)
	}

	// Sort by success rate
	for i := 0; i < len(performances)-1; i++ {
		for j := i + 1; j < len(performances); j++ {
			if performances[j].SuccessRate > performances[i].SuccessRate {
				performances[i], performances[j] = performances[j], performances[i]
			}
		}
	}

	if len(performances) > limit {
		performances = performances[:limit]
	}

	return performances, nil
}

// SurfaceHighValuePatterns returns patterns that should be highlighted
func (l *LearningLoop) SurfaceHighValuePatterns(ctx context.Context, projectPath string) ([]*CrossPattern, error) {
	// Get patterns for the project
	patterns, err := l.store.GetCrossPatternsForProject(projectPath, true)
	if err != nil {
		return nil, err
	}

	// Filter to high-value patterns
	var highValue []*CrossPattern
	for _, p := range patterns {
		// High value = high confidence + multiple uses + successful across projects
		if p.Confidence >= 0.75 && p.Occurrences >= 5 {
			highValue = append(highValue, p)
		}
	}

	// Limit to top 5
	if len(highValue) > 5 {
		highValue = highValue[:5]
	}

	return highValue, nil
}

// LearnFromDiff analyzes a code diff and extracts potential patterns
func (l *LearningLoop) LearnFromDiff(ctx context.Context, projectPath, diff string, success bool) error {
	// Create a synthetic execution to extract patterns from
	exec := &Execution{
		ID:          fmt.Sprintf("diff_%d", time.Now().UnixNano()),
		ProjectPath: projectPath,
		Status:      "completed",
		Output:      diff,
	}

	if !success {
		exec.Status = "failed"
	}

	if l.extractor != nil {
		return l.extractor.ExtractAndSave(ctx, exec)
	}

	return nil
}

// BoostPatternConfidence manually boosts a pattern's confidence
func (l *LearningLoop) BoostPatternConfidence(ctx context.Context, patternID string, amount float64) error {
	pattern, err := l.store.GetCrossPattern(patternID)
	if err != nil {
		return err
	}

	pattern.Confidence = min(0.95, pattern.Confidence+amount)
	return l.store.SaveCrossPattern(pattern)
}

// ResetPatternStats resets a pattern's usage statistics
func (l *LearningLoop) ResetPatternStats(ctx context.Context, patternID string) error {
	pattern, err := l.store.GetCrossPattern(patternID)
	if err != nil {
		return err
	}

	pattern.Occurrences = 1
	pattern.Confidence = 0.5 // Reset to neutral
	return l.store.SaveCrossPattern(pattern)
}
