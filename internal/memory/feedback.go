package memory

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// FeedbackOutcome represents the outcome when a pattern is applied
type FeedbackOutcome string

const (
	OutcomeSuccess FeedbackOutcome = "success"
	OutcomeFailure FeedbackOutcome = "failure"
	OutcomeNeutral FeedbackOutcome = "neutral"
)

// ReviewData represents a PR review comment
type ReviewData struct {
	Body     string // Review comment text
	State    string // "APPROVED", "CHANGES_REQUESTED", "COMMENTED"
	Reviewer string // Reviewer login
}

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
	var outcome FeedbackOutcome
	switch exec.Status {
	case "completed":
		outcome = OutcomeSuccess
	case "failed":
		outcome = OutcomeFailure
	default:
		outcome = OutcomeNeutral
	}

	// Record feedback for each applied pattern
	for _, patternID := range appliedPatterns {
		feedback := &PatternFeedback{
			PatternID:       patternID,
			ExecutionID:     exec.ID,
			ProjectPath:     exec.ProjectPath,
			Outcome:         string(outcome),
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
		PatternID:     patternID,
		Title:         pattern.Title,
		Type:          pattern.Type,
		Confidence:    pattern.Confidence,
		TotalUses:     totalUses,
		SuccessCount:  totalSuccess,
		FailureCount:  totalFailure,
		SuccessRate:   successRate,
		ProjectCount:  len(links),
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

// LearnFromReview processes PR review comments and extracts patterns.
// Approved reviews boost confidence of patterns used in the execution.
// Changes-requested reviews extract anti-patterns from reviewer feedback.
func (l *LearningLoop) LearnFromReview(ctx context.Context, projectPath string,
	reviews []*ReviewData, prURL string) error {
	if len(reviews) == 0 {
		return nil
	}

	if l.extractor == nil {
		return fmt.Errorf("pattern extractor is required for review learning")
	}

	// Collect all review comments for extraction
	comments := make([]string, 0)
	var approvedComments []string

	for _, review := range reviews {
		// Skip empty body reviews (approval clicks without text)
		if strings.TrimSpace(review.Body) == "" {
			continue
		}

		comments = append(comments, review.Body)

		if review.State == "APPROVED" {
			approvedComments = append(approvedComments, review.Body)
		}
	}

	if len(comments) == 0 {
		return nil // No meaningful reviews
	}

	// Extract patterns from review comments
	result, err := l.extractor.ExtractFromReviewComments(ctx, comments, projectPath)
	if err != nil {
		return err
	}

	// Mark change-requested reviews as anti-patterns
	for _, p := range result.AntiPatterns {
		p.Confidence = min(0.85, p.Confidence)
	}

	// Boost confidence for approved reviews with positive patterns
	if len(approvedComments) > 0 {
		for _, p := range result.Patterns {
			p.Confidence = min(0.95, p.Confidence+0.15)
		}
	}

	// Save extracted patterns
	if len(result.Patterns) > 0 || len(result.AntiPatterns) > 0 {
		return l.extractor.SaveExtractedPatterns(ctx, result)
	}

	return nil
}

// LearnFromCIFailure extracts patterns from CI failure logs and saves them.
// It builds a synthetic extraction result from the CI logs, tags patterns with
// CI check names, and persists them via the extractor's SaveExtractedPatterns.
func (l *LearningLoop) LearnFromCIFailure(ctx context.Context, projectPath string, ciLogs string, checkNames []string) error {
	if l.extractor == nil {
		return fmt.Errorf("pattern extractor is required for CI failure learning")
	}

	if strings.TrimSpace(ciLogs) == "" {
		return nil
	}

	// Extract CI-specific patterns (confidence 0.5, source:ci tagged, categorized)
	ciPatterns := l.extractor.extractCIErrorPatterns(ciLogs, checkNames...)
	if len(ciPatterns) == 0 {
		return nil
	}

	result := &ExtractionResult{
		ExecutionID:  fmt.Sprintf("ci_failure_%d", time.Now().UnixNano()),
		ProjectPath:  projectPath,
		Patterns:     make([]*ExtractedPattern, 0),
		AntiPatterns: ciPatterns,
		ExtractedAt:  time.Now(),
	}

	return l.extractor.SaveExtractedPatterns(ctx, result)
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

// RecordPatternOutcome records a success or failure for a pattern in a specific
// project/task-type context. Uses UPSERT on the (pattern_id, project_id, task_type)
// unique constraint to accumulate counts and refresh last_used.
func (s *Store) RecordPatternOutcome(patternID, projectID, taskType, model string, success bool) error {
	return s.withRetry("RecordPatternOutcome", func() error {
		if success {
			_, err := s.db.Exec(`
				INSERT INTO pattern_performance (pattern_id, project_id, task_type, model, success_count, failure_count, last_used)
				VALUES (?, ?, ?, ?, 1, 0, CURRENT_TIMESTAMP)
				ON CONFLICT(pattern_id, project_id, task_type) DO UPDATE SET
					success_count = pattern_performance.success_count + 1,
					model = excluded.model,
					last_used = CURRENT_TIMESTAMP
			`, patternID, projectID, taskType, model)
			return err
		}
		_, err := s.db.Exec(`
			INSERT INTO pattern_performance (pattern_id, project_id, task_type, model, success_count, failure_count, last_used)
			VALUES (?, ?, ?, ?, 0, 1, CURRENT_TIMESTAMP)
			ON CONFLICT(pattern_id, project_id, task_type) DO UPDATE SET
				failure_count = pattern_performance.failure_count + 1,
				model = excluded.model,
				last_used = CURRENT_TIMESTAMP
		`, patternID, projectID, taskType, model)
		return err
	})
}

// GetContextualConfidence computes a contextual confidence score for a pattern
// in a given project and task type. The score combines:
//   - Project-specific success rate (success / total outcomes)
//   - Recency decay: 1.0 / (1.0 + daysSinceLastUse/30)
//
// Returns 0.5 (neutral) when no outcome data exists.
func (s *Store) GetContextualConfidence(patternID, projectID, taskType string) float64 {
	var successCount, failureCount int
	var lastUsed time.Time

	err := s.db.QueryRow(`
		SELECT success_count, failure_count, last_used
		FROM pattern_performance
		WHERE pattern_id = ? AND project_id = ? AND task_type = ?
	`, patternID, projectID, taskType).Scan(&successCount, &failureCount, &lastUsed)
	if err != nil {
		return 0.5 // No data — neutral default
	}

	total := successCount + failureCount
	if total == 0 {
		return 0.5
	}

	successRate := float64(successCount) / float64(total)
	daysSince := time.Since(lastUsed).Hours() / 24.0
	recencyDecay := 1.0 / (1.0 + daysSince/30.0)

	return math.Min(1.0, successRate*recencyDecay)
}
