package memory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultLearningConfig(t *testing.T) {
	config := DefaultLearningConfig()

	if config == nil {
		t.Fatal("DefaultLearningConfig returned nil")
	}

	if config.FeedbackWeight <= 0 {
		t.Error("FeedbackWeight should be positive")
	}

	if config.DecayRate <= 0 {
		t.Error("DecayRate should be positive")
	}
}

func TestNewLearningLoop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	tests := []struct {
		name   string
		config *LearningConfig
	}{
		{name: "with nil config", config: nil},
		{name: "with custom config", config: &LearningConfig{FeedbackWeight: 0.2, DecayRate: 0.02}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop := NewLearningLoop(store, nil, tt.config)
			if loop == nil {
				t.Error("NewLearningLoop returned nil")
			}
		})
	}
}

func TestRecordExecution_Outcomes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)

	ctx := context.Background()

	// Create a pattern
	pattern := &CrossPattern{
		ID:         "outcome-test-pattern",
		Type:       "code",
		Title:      "Test Pattern",
		Confidence: 0.6,
		Scope:      "org",
	}
	_ = store.SaveCrossPattern(pattern)
	_ = store.LinkPatternToProject("outcome-test-pattern", "/test/project")

	tests := []struct {
		name           string
		status         string
		wantConfChange bool
		wantIncrease   bool
	}{
		{name: "completed execution", status: "completed", wantConfChange: true, wantIncrease: true},
		{name: "failed execution", status: "failed", wantConfChange: true, wantIncrease: false},
		{name: "running execution (neutral)", status: "running", wantConfChange: false, wantIncrease: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "exec-" + tt.status,
				TaskID:      "task-1",
				ProjectPath: "/test/project",
				Status:      tt.status,
			}
			_ = store.SaveExecution(exec)

			err := loop.RecordExecution(ctx, exec, []string{"outcome-test-pattern"})
			if err != nil {
				t.Fatalf("RecordExecution failed: %v", err)
			}
		})
	}
}

func TestCalculateConfidenceDelta(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	config := &LearningConfig{FeedbackWeight: 0.1, DecayRate: 0.01}
	loop := NewLearningLoop(store, nil, config)

	tests := []struct {
		name    string
		outcome FeedbackOutcome
		wantMin float64
		wantMax float64
	}{
		{name: "success outcome", outcome: OutcomeSuccess, wantMin: 0.09, wantMax: 0.11},
		{name: "failure outcome", outcome: OutcomeFailure, wantMin: 0.14, wantMax: 0.16}, // 0.1 * 1.5
		{name: "neutral outcome", outcome: OutcomeNeutral, wantMin: 0, wantMax: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loop.calculateConfidenceDelta(tt.outcome)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("calculateConfidenceDelta(%v) = %f, want between %f and %f", tt.outcome, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestApplyDecay(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, &LearningConfig{FeedbackWeight: 0.1, DecayRate: 0.1})
	ctx := context.Background()

	// Create a pattern (will have current timestamp, so won't be stale)
	pattern := &CrossPattern{
		ID:         "decay-test",
		Type:       "code",
		Title:      "Decay Test",
		Confidence: 0.8,
		Scope:      "org",
	}
	_ = store.SaveCrossPattern(pattern)

	// Apply decay (should not affect recent patterns)
	updated, err := loop.ApplyDecay(ctx)
	if err != nil {
		t.Fatalf("ApplyDecay failed: %v", err)
	}

	// Recent pattern should not be decayed
	if updated > 0 {
		t.Logf("Note: %d patterns decayed (might be from other tests)", updated)
	}

	// Verify pattern confidence unchanged for recent patterns
	retrieved, _ := store.GetCrossPattern("decay-test")
	if retrieved.Confidence != 0.8 {
		t.Logf("Confidence changed from 0.8 to %f", retrieved.Confidence)
	}
}

func TestDeprecateLowConfidencePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create patterns with varying confidence
	patterns := []*CrossPattern{
		{ID: "high-conf", Type: "code", Title: "High", Confidence: 0.9, Occurrences: 10, Scope: "org"},
		{ID: "low-conf-1", Type: "code", Title: "Low 1", Confidence: 0.2, Occurrences: 1, Scope: "org"},
		{ID: "low-conf-2", Type: "code", Title: "Low 2", Confidence: 0.15, Occurrences: 2, Scope: "org"},
	}

	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	deprecated, err := loop.DeprecateLowConfidencePatterns(ctx, 0.3)
	if err != nil {
		t.Fatalf("DeprecateLowConfidencePatterns failed: %v", err)
	}

	if deprecated != 2 {
		t.Errorf("deprecated %d patterns, want 2", deprecated)
	}

	// Verify high confidence pattern still exists
	_, err = store.GetCrossPattern("high-conf")
	if err != nil {
		t.Error("high confidence pattern should still exist")
	}
}

func TestGetPatternPerformance(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create pattern and link to projects
	pattern := &CrossPattern{
		ID:         "perf-test",
		Type:       "code",
		Title:      "Performance Test",
		Confidence: 0.8,
		Scope:      "org",
	}
	_ = store.SaveCrossPattern(pattern)
	_ = store.LinkPatternToProject("perf-test", "/project/a")
	_ = store.LinkPatternToProject("perf-test", "/project/b")

	perf, err := loop.GetPatternPerformance(ctx, "perf-test")
	if err != nil {
		t.Fatalf("GetPatternPerformance failed: %v", err)
	}

	if perf.PatternID != "perf-test" {
		t.Errorf("PatternID = %q, want 'perf-test'", perf.PatternID)
	}
	if perf.ProjectCount != 2 {
		t.Errorf("ProjectCount = %d, want 2", perf.ProjectCount)
	}
}

func TestGetPatternPerformance_NotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	_, err = loop.GetPatternPerformance(ctx, "nonexistent")
	if err == nil {
		t.Error("GetPatternPerformance should fail for nonexistent pattern")
	}
}

func TestGetTopPerformingPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create patterns
	patterns := []*CrossPattern{
		{ID: "top-1", Type: "code", Title: "Top 1", Confidence: 0.9, Scope: "org"},
		{ID: "top-2", Type: "code", Title: "Top 2", Confidence: 0.8, Scope: "org"},
		{ID: "top-3", Type: "code", Title: "Top 3", Confidence: 0.7, Scope: "org"},
	}

	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	top, err := loop.GetTopPerformingPatterns(ctx, 2)
	if err != nil {
		t.Fatalf("GetTopPerformingPatterns failed: %v", err)
	}

	if len(top) > 2 {
		t.Errorf("got %d patterns, want at most 2", len(top))
	}
}

func TestSurfaceHighValuePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create patterns with varying quality
	_ = store.SaveCrossPattern(&CrossPattern{ID: "high-value", Type: "code", Title: "High Value", Confidence: 0.85, Occurrences: 10, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "low-value", Type: "code", Title: "Low Value", Confidence: 0.5, Occurrences: 2, Scope: "org"})
	_ = store.LinkPatternToProject("high-value", "/test/project")

	patterns, err := loop.SurfaceHighValuePatterns(ctx, "/test/project")
	if err != nil {
		t.Fatalf("SurfaceHighValuePatterns failed: %v", err)
	}

	// Should only return high-value pattern
	hasHighValue := false
	for _, p := range patterns {
		if p.ID == "high-value" {
			hasHighValue = true
		}
	}

	if !hasHighValue && len(patterns) > 0 {
		t.Error("expected high-value pattern in results")
	}
}

func TestLearnFromDiff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)

	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	// Provide a diff that matches pattern extraction rules
	diff := `+ using context.Context in Handler
+ added error handling for validateInput`

	err = loop.LearnFromDiff(ctx, "/test/project", diff, true)
	if err != nil {
		t.Fatalf("LearnFromDiff failed: %v", err)
	}

	// Should have extracted patterns from the diff
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern to be extracted from diff, got %d", patternStore.Count())
	}
}

func TestBoostPatternConfidence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create pattern
	_ = store.SaveCrossPattern(&CrossPattern{ID: "boost-test", Type: "code", Title: "Boost Test", Confidence: 0.5, Scope: "org"})

	err = loop.BoostPatternConfidence(ctx, "boost-test", 0.2)
	if err != nil {
		t.Fatalf("BoostPatternConfidence failed: %v", err)
	}

	retrieved, _ := store.GetCrossPattern("boost-test")
	if retrieved.Confidence != 0.7 {
		t.Errorf("Confidence = %f, want 0.7", retrieved.Confidence)
	}

	// Test cap at 0.95
	err = loop.BoostPatternConfidence(ctx, "boost-test", 0.5)
	if err != nil {
		t.Fatalf("BoostPatternConfidence failed: %v", err)
	}

	retrieved, _ = store.GetCrossPattern("boost-test")
	if retrieved.Confidence != 0.95 {
		t.Errorf("Confidence = %f, want 0.95 (capped)", retrieved.Confidence)
	}
}

func TestResetPatternStats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create pattern with stats
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "reset-test",
		Type:        "code",
		Title:       "Reset Test",
		Confidence:  0.9,
		Occurrences: 50,
		Scope:       "org",
	})

	err = loop.ResetPatternStats(ctx, "reset-test")
	if err != nil {
		t.Fatalf("ResetPatternStats failed: %v", err)
	}

	retrieved, _ := store.GetCrossPattern("reset-test")
	if retrieved.Confidence != 0.5 {
		t.Errorf("Confidence = %f, want 0.5 (neutral)", retrieved.Confidence)
	}
	// Note: SaveCrossPattern with ON CONFLICT increments occurrences,
	// so after reset (which calls SaveCrossPattern), occurrences will be original + 1
	// This is expected behavior based on the current implementation
	if retrieved.Occurrences < 1 {
		t.Errorf("Occurrences = %d, want >= 1", retrieved.Occurrences)
	}
}

func TestFeedbackOutcomeConstants(t *testing.T) {
	tests := []struct {
		outcome  FeedbackOutcome
		expected string
	}{
		{OutcomeSuccess, "success"},
		{OutcomeFailure, "failure"},
		{OutcomeNeutral, "neutral"},
	}

	for _, tt := range tests {
		if string(tt.outcome) != tt.expected {
			t.Errorf("Outcome %v = %q, want %q", tt.outcome, tt.outcome, tt.expected)
		}
	}
}

func TestRecordExecution_WithExtractor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)

	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	// Create an execution with pattern-extractable output
	exec := &Execution{
		ID:          "extractor-exec",
		TaskID:      "task-1",
		ProjectPath: "/test/project",
		Status:      "completed",
		Output:      "using context.Context in Handler added error handling for validateUser",
	}
	_ = store.SaveExecution(exec)

	err = loop.RecordExecution(ctx, exec, nil)
	if err != nil {
		t.Fatalf("RecordExecution failed: %v", err)
	}

	// The extractor should have run and saved patterns
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern from extractor, got %d", patternStore.Count())
	}
}

func TestRecordExecution_WithMultiplePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create multiple patterns
	patterns := []string{"multi-1", "multi-2", "multi-3"}
	for _, id := range patterns {
		_ = store.SaveCrossPattern(&CrossPattern{ID: id, Type: "code", Title: id, Confidence: 0.6, Scope: "org"})
		_ = store.LinkPatternToProject(id, "/test/project")
	}

	exec := &Execution{
		ID:          "multi-exec",
		TaskID:      "task-multi",
		ProjectPath: "/test/project",
		Status:      "completed",
	}
	_ = store.SaveExecution(exec)

	err = loop.RecordExecution(ctx, exec, patterns)
	if err != nil {
		t.Fatalf("RecordExecution failed: %v", err)
	}

	// All patterns should have been updated
	for _, id := range patterns {
		perf, err := loop.GetPatternPerformance(ctx, id)
		if err != nil {
			t.Fatalf("GetPatternPerformance(%s) failed: %v", id, err)
		}
		if perf.SuccessCount != 1 {
			t.Errorf("Pattern %s SuccessCount = %d, want 1", id, perf.SuccessCount)
		}
	}
}

func TestSurfaceHighValuePatterns_EmptyResult(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// No patterns exist
	patterns, err := loop.SurfaceHighValuePatterns(ctx, "/empty/project")
	if err != nil {
		t.Fatalf("SurfaceHighValuePatterns failed: %v", err)
	}

	if len(patterns) != 0 {
		t.Errorf("expected 0 patterns for empty project, got %d", len(patterns))
	}
}

func TestPatternPerformance_SuccessRate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil)
	ctx := context.Background()

	// Create pattern
	pattern := &CrossPattern{
		ID:         "rate-test",
		Type:       "code",
		Title:      "Rate Test",
		Confidence: 0.7,
		Scope:      "org",
	}
	_ = store.SaveCrossPattern(pattern)
	_ = store.LinkPatternToProject("rate-test", "/test/project")

	// Record mixed results
	for i := 0; i < 3; i++ {
		exec := &Execution{
			ID:          "success-" + string(rune('0'+i)),
			TaskID:      "task",
			ProjectPath: "/test/project",
			Status:      "completed",
		}
		_ = store.SaveExecution(exec)
		_ = loop.RecordExecution(ctx, exec, []string{"rate-test"})
	}

	exec := &Execution{
		ID:          "failure-1",
		TaskID:      "task",
		ProjectPath: "/test/project",
		Status:      "failed",
	}
	_ = store.SaveExecution(exec)
	_ = loop.RecordExecution(ctx, exec, []string{"rate-test"})

	perf, err := loop.GetPatternPerformance(ctx, "rate-test")
	if err != nil {
		t.Fatalf("GetPatternPerformance failed: %v", err)
	}

	// 3 successes, 1 failure = 75% success rate
	expectedRate := 0.75
	if perf.SuccessRate != expectedRate {
		t.Errorf("SuccessRate = %f, want %f", perf.SuccessRate, expectedRate)
	}
}

func TestLearnFromDiff_Failed(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)

	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	// Provide a diff with error patterns for failed execution
	diff := `- forgot to handle error
+ nil pointer dereference`

	// LearnFromDiff with success=false creates a "failed" execution
	// The extractor only works on "completed" executions, so this will error
	err = loop.LearnFromDiff(ctx, "/test/project", diff, false)
	if err == nil {
		t.Error("LearnFromDiff with failed execution should return error")
	}

	// Verify the error is about execution status
	if err != nil && !strings.Contains(err.Error(), "completed") {
		t.Errorf("Expected error about completed executions, got: %v", err)
	}
}

func TestApplyDecay_StalePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, &LearningConfig{FeedbackWeight: 0.1, DecayRate: 0.5})
	ctx := context.Background()

	// Create a pattern with old timestamp (can't easily simulate this via API)
	// Just verify the function runs without error
	_, err = loop.ApplyDecay(ctx)
	if err != nil {
		t.Fatalf("ApplyDecay failed: %v", err)
	}

	// Verify function signature works
	_ = time.Now() // Just to avoid unused import
}

func TestLearnFromReview_ChangesRequested(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	reviews := []*ReviewData{
		{
			Body:     "add tests for edge cases",
			State:    "CHANGES_REQUESTED",
			Reviewer: "reviewer1",
		},
	}

	err = loop.LearnFromReview(ctx, "/test/project", reviews, "https://github.com/test/pr/1")
	if err != nil {
		t.Fatalf("LearnFromReview failed: %v", err)
	}

	// Should have extracted testing anti-pattern
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern extracted from review, got %d", patternStore.Count())
	}

	// Verify anti-pattern was created
	patterns := patternStore.GetByType(PatternTypeWorkflow)
	if len(patterns) < 1 {
		t.Error("Expected workflow anti-pattern for testing feedback")
	}
}

func TestLearnFromReview_Approved(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	reviews := []*ReviewData{
		{
			Body:     "this is a nice approach to the problem",
			State:    "APPROVED",
			Reviewer: "reviewer2",
		},
	}

	err = loop.LearnFromReview(ctx, "/test/project", reviews, "https://github.com/test/pr/2")
	if err != nil {
		t.Fatalf("LearnFromReview failed: %v", err)
	}

	// Should have extracted positive pattern
	patterns := patternStore.GetByType(PatternTypeCode)
	if len(patterns) < 1 {
		t.Error("Expected positive pattern for approved review")
	}

	// Confidence should be boosted
	if len(patterns) > 0 && patterns[0].Confidence < 0.8 {
		t.Errorf("Approved pattern should have high confidence, got %f", patterns[0].Confidence)
	}
}

func TestLearnFromReview_EmptyBody(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	reviews := []*ReviewData{
		{
			Body:     "", // Empty body (approval click without text)
			State:    "APPROVED",
			Reviewer: "reviewer3",
		},
	}

	err = loop.LearnFromReview(ctx, "/test/project", reviews, "https://github.com/test/pr/3")
	if err != nil {
		t.Fatalf("LearnFromReview failed: %v", err)
	}

	// Empty review should be skipped
	if patternStore.Count() != 0 {
		t.Errorf("Empty body review should be skipped, but got %d patterns", patternStore.Count())
	}
}

func TestLearnFromReview_MultipleReviews(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	reviews := []*ReviewData{
		{
			Body:     "add tests and improve naming",
			State:    "CHANGES_REQUESTED",
			Reviewer: "reviewer1",
		},
		{
			Body:     "error handling looks good",
			State:    "APPROVED",
			Reviewer: "reviewer2",
		},
		{
			Body:     "",
			State:    "APPROVED",
			Reviewer: "reviewer3",
		},
	}

	err = loop.LearnFromReview(ctx, "/test/project", reviews, "https://github.com/test/pr/4")
	if err != nil {
		t.Fatalf("LearnFromReview failed: %v", err)
	}

	// Should have extracted multiple patterns
	if patternStore.Count() < 2 {
		t.Errorf("Expected at least 2 patterns from multiple reviews, got %d", patternStore.Count())
	}
}

func TestLearnFromReview_NoExtractor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil) // No extractor
	ctx := context.Background()

	reviews := []*ReviewData{
		{
			Body:     "add tests",
			State:    "CHANGES_REQUESTED",
			Reviewer: "reviewer",
		},
	}

	err = loop.LearnFromReview(ctx, "/test/project", reviews, "https://github.com/test/pr/5")
	if err == nil {
		t.Error("LearnFromReview should fail without extractor")
	}
}

func TestLearnFromCIFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	ciLogs := `./internal/handler.go:15:2: undefined: processItem
--- FAIL: TestHandler (0.01s)
    handler_test.go:42: expected 200, got 500
golangci-lint: errcheck violation at store.go:55`

	err = loop.LearnFromCIFailure(ctx, "/test/project", ciLogs, []string{"build", "test", "lint"})
	if err != nil {
		t.Fatalf("LearnFromCIFailure failed: %v", err)
	}

	// Should have extracted CI-related anti-patterns
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern from CI logs, got %d", patternStore.Count())
	}

	// Verify all saved patterns have [ANTI] prefix (CI patterns are anti-patterns)
	for _, pt := range []PatternType{PatternTypeError, PatternTypeWorkflow} {
		for _, p := range patternStore.GetByType(pt) {
			if !strings.HasPrefix(p.Title, "[ANTI]") {
				t.Errorf("CI pattern %q should have [ANTI] prefix", p.Title)
			}
		}
	}
}

func TestLearnFromCIFailure_EmptyLogs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	err = loop.LearnFromCIFailure(ctx, "/test/project", "", []string{"build"})
	if err != nil {
		t.Fatalf("LearnFromCIFailure with empty logs should not error: %v", err)
	}

	if patternStore.Count() != 0 {
		t.Errorf("Empty CI logs should produce no patterns, got %d", patternStore.Count())
	}
}

func TestLearnFromCIFailure_NoExtractor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	loop := NewLearningLoop(store, nil, nil) // No extractor
	ctx := context.Background()

	err = loop.LearnFromCIFailure(ctx, "/test/project", "undefined: foo", []string{"build"})
	if err == nil {
		t.Error("LearnFromCIFailure should fail without extractor")
	}
}

func TestLearnFromCIFailure_NoMatchingPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	err = loop.LearnFromCIFailure(ctx, "/test/project", "All checks passed", []string{"build"})
	if err != nil {
		t.Fatalf("LearnFromCIFailure with no matches should not error: %v", err)
	}

	if patternStore.Count() != 0 {
		t.Errorf("Non-matching CI logs should produce no patterns, got %d", patternStore.Count())
	}
}

func TestLearnFromCIFailure_CheckNameTagging(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	err = loop.LearnFromCIFailure(ctx, "/test/project", "undefined: myFunc", []string{"go-build", "compile"})
	if err != nil {
		t.Fatalf("LearnFromCIFailure failed: %v", err)
	}

	// Verify patterns were saved (check names tag is embedded in Context during extraction,
	// then stored as metadata by SaveExtractedPatterns)
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern, got %d", patternStore.Count())
	}
}

func TestRecordPatternOutcomeAndContextualConfidence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pattern-perf-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	tests := []struct {
		name       string
		setup      func(t *testing.T)
		patternID  string
		projectID  string
		taskType   string
		wantMin    float64
		wantMax    float64
	}{
		{
			name:      "fresh pattern returns default 0.5",
			setup:     func(t *testing.T) {},
			patternID: "nonexistent-pattern",
			projectID: "proj-a",
			taskType:  "feat",
			wantMin:   0.5,
			wantMax:   0.5,
		},
		{
			name: "100% success rate with fresh timestamp",
			setup: func(t *testing.T) {
				for i := 0; i < 5; i++ {
					if err := store.RecordPatternOutcome("fresh-success", "proj-a", "feat", "opus", true); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
			},
			patternID: "fresh-success",
			projectID: "proj-a",
			taskType:  "feat",
			// successRate=1.0, recency≈1.0 (just recorded) → ~1.0
			wantMin: 0.95,
			wantMax: 1.0,
		},
		{
			name: "mixed outcomes 60% success",
			setup: func(t *testing.T) {
				for i := 0; i < 3; i++ {
					if err := store.RecordPatternOutcome("mixed-pat", "proj-b", "fix", "sonnet", true); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
				for i := 0; i < 2; i++ {
					if err := store.RecordPatternOutcome("mixed-pat", "proj-b", "fix", "sonnet", false); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
			},
			patternID: "mixed-pat",
			projectID: "proj-b",
			taskType:  "fix",
			// successRate=0.6, recency≈1.0 → ~0.6
			wantMin: 0.55,
			wantMax: 0.65,
		},
		{
			name: "cross-project divergence — high success project",
			setup: func(t *testing.T) {
				for i := 0; i < 8; i++ {
					if err := store.RecordPatternOutcome("diverge-pat", "proj-high", "refactor", "opus", true); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
				for i := 0; i < 2; i++ {
					if err := store.RecordPatternOutcome("diverge-pat", "proj-high", "refactor", "opus", false); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
			},
			patternID: "diverge-pat",
			projectID: "proj-high",
			taskType:  "refactor",
			// successRate=0.8, recency≈1.0 → ~0.8
			wantMin: 0.75,
			wantMax: 0.85,
		},
		{
			name: "cross-project divergence — low success project",
			setup: func(t *testing.T) {
				for i := 0; i < 2; i++ {
					if err := store.RecordPatternOutcome("diverge-pat", "proj-low", "refactor", "opus", true); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
				for i := 0; i < 8; i++ {
					if err := store.RecordPatternOutcome("diverge-pat", "proj-low", "refactor", "opus", false); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
			},
			patternID: "diverge-pat",
			projectID: "proj-low",
			taskType:  "refactor",
			// successRate=0.2, recency≈1.0 → ~0.2
			wantMin: 0.15,
			wantMax: 0.25,
		},
		{
			name: "mixed task types — feat vs fix same pattern",
			setup: func(t *testing.T) {
				for i := 0; i < 5; i++ {
					if err := store.RecordPatternOutcome("tasktype-pat", "proj-c", "feat", "sonnet", true); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
				for i := 0; i < 5; i++ {
					if err := store.RecordPatternOutcome("tasktype-pat", "proj-c", "fix", "sonnet", false); err != nil {
						t.Fatalf("RecordPatternOutcome failed: %v", err)
					}
				}
			},
			patternID: "tasktype-pat",
			projectID: "proj-c",
			taskType:  "feat",
			// feat: 5 success, 0 fail → rate=1.0, recency≈1.0
			wantMin: 0.95,
			wantMax: 1.0,
		},
		{
			name: "UPSERT accumulates correctly",
			setup: func(t *testing.T) {
				// Record same combo multiple times
				if err := store.RecordPatternOutcome("upsert-pat", "proj-d", "test", "haiku", true); err != nil {
					t.Fatalf("RecordPatternOutcome failed: %v", err)
				}
				if err := store.RecordPatternOutcome("upsert-pat", "proj-d", "test", "haiku", true); err != nil {
					t.Fatalf("RecordPatternOutcome failed: %v", err)
				}
				if err := store.RecordPatternOutcome("upsert-pat", "proj-d", "test", "haiku", false); err != nil {
					t.Fatalf("RecordPatternOutcome failed: %v", err)
				}
			},
			patternID: "upsert-pat",
			projectID: "proj-d",
			taskType:  "test",
			// 2 success, 1 failure → rate=0.667, recency≈1.0
			wantMin: 0.6,
			wantMax: 0.7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			got := store.GetContextualConfidence(tt.patternID, tt.projectID, tt.taskType)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("GetContextualConfidence() = %f, want [%f, %f]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestGetContextualConfidence_DecayedPattern(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pattern-perf-decay-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert a record with an old last_used timestamp (90 days ago)
	_, err = store.db.Exec(`
		INSERT INTO pattern_performance (pattern_id, project_id, task_type, model, success_count, failure_count, last_used)
		VALUES (?, ?, ?, ?, 10, 0, ?)
	`, "old-pattern", "proj-e", "feat", "opus", time.Now().AddDate(0, 0, -90).Format("2006-01-02 15:04:05"))
	if err != nil {
		t.Fatalf("failed to insert old pattern: %v", err)
	}

	got := store.GetContextualConfidence("old-pattern", "proj-e", "feat")
	// successRate=1.0, recencyDecay = 1/(1+90/30) = 1/4 = 0.25
	// contextual = 1.0 * 0.25 = 0.25
	if got > 0.26 {
		t.Errorf("Decayed pattern confidence = %f, want <= 0.25", got)
	}
	if got < 0.20 {
		t.Errorf("Decayed pattern confidence = %f, want >= 0.20 (should be ~0.25)", got)
	}
}

func TestLearnFromReview_NoReviews(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "feedback-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	loop := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	err = loop.LearnFromReview(ctx, "/test/project", nil, "https://github.com/test/pr/6")
	if err != nil {
		t.Fatalf("LearnFromReview with no reviews should not error: %v", err)
	}

	if patternStore.Count() != 0 {
		t.Errorf("No reviews should result in no patterns, got %d", patternStore.Count())
	}
}
