package executor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

func TestModelRouter_SelectModel(t *testing.T) {
	tests := []struct {
		name     string
		config   *ModelRoutingConfig
		task     *Task
		expected string
	}{
		{
			name:     "routing disabled returns empty",
			config:   &ModelRoutingConfig{Enabled: false, Trivial: "haiku"},
			task:     &Task{Description: "Fix typo"},
			expected: "",
		},
		{
			name: "trivial task returns haiku",
			config: &ModelRoutingConfig{
				Enabled: true,
				Trivial: "claude-haiku",
				Simple:  "claude-sonnet",
				Medium:  "claude-sonnet",
				Complex: "claude-opus",
			},
			task:     &Task{Description: "Fix typo in README"},
			expected: "claude-haiku",
		},
		{
			name: "simple task returns sonnet",
			config: &ModelRoutingConfig{
				Enabled: true,
				Trivial: "claude-haiku",
				Simple:  "claude-sonnet-4-6",
				Medium:  "claude-sonnet-4-6",
				Complex: "claude-opus",
			},
			task:     &Task{Description: "Add field to struct"},
			expected: "claude-sonnet-4-6",
		},
		{
			name: "complex task returns opus",
			config: &ModelRoutingConfig{
				Enabled: true,
				Trivial: "claude-haiku",
				Simple:  "claude-sonnet-4-6",
				Medium:  "claude-sonnet-4-6",
				Complex: "claude-opus",
			},
			task:     &Task{Description: "Refactor the authentication system"},
			expected: "claude-opus",
		},
		{
			name:     "nil config returns empty",
			config:   nil,
			task:     &Task{Description: "Any task"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewModelRouter(tt.config, nil)
			got := router.SelectModel(tt.task)
			if got != tt.expected {
				t.Errorf("SelectModel() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestModelRouter_SelectTimeout(t *testing.T) {
	config := &TimeoutConfig{
		Default: "30m",
		Trivial: "5m",
		Simple:  "10m",
		Medium:  "30m",
		Complex: "60m",
	}

	tests := []struct {
		name     string
		task     *Task
		expected time.Duration
	}{
		{
			name:     "trivial task gets 5m timeout",
			task:     &Task{Description: "Fix typo"},
			expected: 5 * time.Minute,
		},
		{
			name:     "simple task gets 10m timeout",
			task:     &Task{Description: "Add field to struct"},
			expected: 10 * time.Minute,
		},
		{
			name:     "medium task gets 30m timeout",
			task:     &Task{Description: "Implement new endpoint for user data with validation and error handling"},
			expected: 30 * time.Minute,
		},
		{
			name:     "complex task gets 60m timeout",
			task:     &Task{Description: "Refactor the authentication system"},
			expected: 60 * time.Minute,
		},
	}

	router := NewModelRouter(nil, config)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := router.SelectTimeout(tt.task)
			if got != tt.expected {
				t.Errorf("SelectTimeout() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestModelRouter_NilConfigs(t *testing.T) {
	router := NewModelRouter(nil, nil)

	// Should use defaults
	if router.modelConfig == nil {
		t.Error("Expected default model config")
	}
	if router.timeoutConfig == nil {
		t.Error("Expected default timeout config")
	}

	// Default model routing is disabled
	if router.IsRoutingEnabled() {
		t.Error("Expected routing to be disabled by default")
	}

	// Should still return a valid timeout
	task := &Task{Description: "Any task"}
	timeout := router.SelectTimeout(task)
	if timeout == 0 {
		t.Error("Expected non-zero timeout")
	}
}

func TestModelRouter_InvalidTimeoutFormat(t *testing.T) {
	config := &TimeoutConfig{
		Default: "30m",
		Trivial: "invalid",
		Simple:  "10m",
		Medium:  "30m",
		Complex: "60m",
	}

	router := NewModelRouter(nil, config)

	// Should fall back to default
	task := &Task{Description: "Fix typo"}
	timeout := router.SelectTimeout(task)
	if timeout != 30*time.Minute {
		t.Errorf("Expected fallback to 30m, got %v", timeout)
	}
}

func TestModelRouter_GetModelForComplexity(t *testing.T) {
	config := &ModelRoutingConfig{
		Enabled: true,
		Trivial: "haiku",
		Simple:  "claude-sonnet-4-6",
		Medium:  "claude-sonnet-4-6",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)

	tests := []struct {
		complexity Complexity
		expected   string
	}{
		{ComplexityTrivial, "haiku"},
		{ComplexitySimple, "claude-sonnet-4-6"},
		{ComplexityMedium, "claude-sonnet-4-6"},
		{ComplexityComplex, "opus"},
		{Complexity("unknown"), "claude-sonnet-4-6"}, // Default to medium
	}

	for _, tt := range tests {
		t.Run(string(tt.complexity), func(t *testing.T) {
			got := router.GetModelForComplexity(tt.complexity)
			if got != tt.expected {
				t.Errorf("GetModelForComplexity(%s) = %v, want %v", tt.complexity, got, tt.expected)
			}
		})
	}
}

func TestModelRouter_SelectEffort(t *testing.T) {
	tests := []struct {
		name     string
		config   *EffortRoutingConfig
		task     *Task
		expected string
	}{
		{
			name:     "effort routing disabled returns empty",
			config:   &EffortRoutingConfig{Enabled: false, Trivial: "low"},
			task:     &Task{Description: "Fix typo"},
			expected: "",
		},
		{
			name: "trivial task returns low",
			config: &EffortRoutingConfig{
				Enabled: true,
				Trivial: "low",
				Simple:  "medium",
				Medium:  "high",
				Complex: "max",
			},
			task:     &Task{Description: "Fix typo in README"},
			expected: "low",
		},
		{
			name: "complex task returns max",
			config: &EffortRoutingConfig{
				Enabled: true,
				Trivial: "low",
				Simple:  "medium",
				Medium:  "high",
				Complex: "max",
			},
			task:     &Task{Description: "Refactor the authentication system"},
			expected: "max",
		},
		{
			name:     "nil config returns empty",
			config:   nil,
			task:     &Task{Description: "Any task"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewModelRouterWithEffort(nil, nil, tt.config)
			got := router.SelectEffort(tt.task)
			if got != tt.expected {
				t.Errorf("SelectEffort() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestModelRouter_GetEffortForComplexity(t *testing.T) {
	config := &EffortRoutingConfig{
		Enabled: true,
		Trivial: "low",
		Simple:  "medium",
		Medium:  "high",
		Complex: "max",
	}
	router := NewModelRouterWithEffort(nil, nil, config)

	tests := []struct {
		complexity Complexity
		expected   string
	}{
		{ComplexityTrivial, "low"},
		{ComplexitySimple, "medium"},
		{ComplexityMedium, "high"},
		{ComplexityComplex, "max"},
		{Complexity("unknown"), "high"}, // Default to medium complexity
	}

	for _, tt := range tests {
		t.Run(string(tt.complexity), func(t *testing.T) {
			got := router.GetEffortForComplexity(tt.complexity)
			if got != tt.expected {
				t.Errorf("GetEffortForComplexity(%s) = %v, want %v", tt.complexity, got, tt.expected)
			}
		})
	}
}

func TestModelRouter_IsEffortRoutingEnabled(t *testing.T) {
	// Disabled by default
	router := NewModelRouter(nil, nil)
	if router.IsEffortRoutingEnabled() {
		t.Error("Expected effort routing to be disabled by default")
	}

	// Enabled with config
	router = NewModelRouterWithEffort(nil, nil, &EffortRoutingConfig{Enabled: true, Trivial: "low"})
	if !router.IsEffortRoutingEnabled() {
		t.Error("Expected effort routing to be enabled")
	}
}

func TestModelRouter_GetTimeoutForComplexity(t *testing.T) {
	config := &TimeoutConfig{
		Default: "30m",
		Trivial: "5m",
		Simple:  "10m",
		Medium:  "30m",
		Complex: "60m",
	}
	router := NewModelRouter(nil, config)

	tests := []struct {
		complexity Complexity
		expected   time.Duration
	}{
		{ComplexityTrivial, 5 * time.Minute},
		{ComplexitySimple, 10 * time.Minute},
		{ComplexityMedium, 30 * time.Minute},
		{ComplexityComplex, 60 * time.Minute},
		{Complexity("unknown"), 30 * time.Minute}, // Default
	}

	for _, tt := range tests {
		t.Run(string(tt.complexity), func(t *testing.T) {
			got := router.GetTimeoutForComplexity(tt.complexity)
			if got != tt.expected {
				t.Errorf("GetTimeoutForComplexity(%s) = %v, want %v", tt.complexity, got, tt.expected)
			}
		})
	}
}

func TestModelRouter_SelectEffortWithLLMClassifier(t *testing.T) {
	// Test that LLM classifier overrides static mapping
	config := &EffortRoutingConfig{
		Enabled: true,
		Trivial: "low",
		Simple:  "medium",
		Medium:  "high",
		Complex: "max",
	}

	router := NewModelRouterWithEffort(nil, nil, config)

	// Attach a mock classifier that returns "high"
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "security sensitive"))
	router.SetEffortClassifier(classifier)

	// This task looks trivial by heuristic, but LLM says "high"
	task := &Task{
		ID:          "GH-100",
		Description: "Fix typo in auth module", // Heuristic: trivial (typo), LLM: high
	}

	got := router.SelectEffort(task)
	if got != "high" {
		t.Errorf("Expected LLM classification 'high' to override static mapping, got %q", got)
	}
}

func TestModelRouter_SelectEffortFallsBackOnLLMFailure(t *testing.T) {
	// Test that static mapping is used when LLM fails
	config := &EffortRoutingConfig{
		Enabled: true,
		Trivial: "low",
		Simple:  "medium",
		Medium:  "high",
		Complex: "max",
	}

	router := NewModelRouterWithEffort(nil, nil, config)

	// Attach a mock classifier that fails
	classifier := newEffortClassifierWithRunner(func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("subprocess failed")
	})
	router.SetEffortClassifier(classifier)

	// This task is trivial by heuristic
	task := &Task{
		ID:          "GH-200",
		Description: "Fix typo in README",
	}

	got := router.SelectEffort(task)
	if got != "low" {
		t.Errorf("Expected fallback to static mapping 'low', got %q", got)
	}
}

// newTestOutcomeTracker creates a tracker backed by a temporary SQLite store.
func newTestOutcomeTracker(t *testing.T) (*memory.ModelOutcomeTracker, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "routing-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}
	tracker := memory.NewModelOutcomeTracker(store)
	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return tracker, cleanup
}

func TestModelRouter_SelectModelEscalation(t *testing.T) {
	tracker, cleanup := newTestOutcomeTracker(t)
	defer cleanup()

	config := &ModelRoutingConfig{
		Enabled: true,
		Trivial: "haiku",
		Simple:  "sonnet",
		Medium:  "sonnet",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)
	router.SetOutcomeTracker(tracker)

	// Record enough failures to exceed 30% threshold (4 failures out of 10)
	for i := 0; i < 4; i++ {
		if err := tracker.RecordOutcome("trivial", "haiku", "failure", 1000, time.Minute); err != nil {
			t.Fatalf("failed to record outcome: %v", err)
		}
	}
	for i := 0; i < 6; i++ {
		if err := tracker.RecordOutcome("trivial", "haiku", "success", 1000, time.Minute); err != nil {
			t.Fatalf("failed to record outcome: %v", err)
		}
	}

	// Trivial task should be escalated from haiku to sonnet
	task := &Task{Description: "Fix typo in README"}
	got := router.SelectModel(task)
	if got != "sonnet" {
		t.Errorf("Expected escalation to 'sonnet', got %q", got)
	}
}

func TestModelRouter_SelectModelNoEscalationBelowThreshold(t *testing.T) {
	tracker, cleanup := newTestOutcomeTracker(t)
	defer cleanup()

	config := &ModelRoutingConfig{
		Enabled: true,
		Trivial: "haiku",
		Simple:  "sonnet",
		Medium:  "sonnet",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)
	router.SetOutcomeTracker(tracker)

	// Record outcomes below threshold (2 failures out of 10 = 20%)
	for i := 0; i < 2; i++ {
		if err := tracker.RecordOutcome("trivial", "haiku", "failure", 1000, time.Minute); err != nil {
			t.Fatalf("failed to record outcome: %v", err)
		}
	}
	for i := 0; i < 8; i++ {
		if err := tracker.RecordOutcome("trivial", "haiku", "success", 1000, time.Minute); err != nil {
			t.Fatalf("failed to record outcome: %v", err)
		}
	}

	// Should NOT escalate — failure rate 20% < 30% threshold
	task := &Task{Description: "Fix typo in README"}
	got := router.SelectModel(task)
	if got != "haiku" {
		t.Errorf("Expected no escalation (haiku), got %q", got)
	}
}

func TestModelRouter_SelectModelNilTrackerNoEscalation(t *testing.T) {
	config := &ModelRoutingConfig{
		Enabled: true,
		Trivial: "haiku",
		Simple:  "sonnet",
		Medium:  "sonnet",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)
	// No tracker set — should behave exactly as before

	task := &Task{Description: "Fix typo in README"}
	got := router.SelectModel(task)
	if got != "haiku" {
		t.Errorf("Expected 'haiku' without tracker, got %q", got)
	}
}

func TestResolveComplexity_LLMUpgradesHeuristic(t *testing.T) {
	router := NewModelRouter(nil, nil)
	// Attach LLM classifier returning "high"
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "large refactor"))
	router.SetEffortClassifier(classifier)

	// "remove unused" matches trivial heuristic, but LLM says high → complex
	task := &Task{ID: "GH-2131", Description: "remove unused imports"}
	got := router.resolveComplexity(task)
	if got != ComplexityComplex {
		t.Errorf("Expected ComplexityComplex (LLM floor), got %s", got)
	}
}

func TestResolveComplexity_HeuristicWinsWhenHigher(t *testing.T) {
	router := NewModelRouter(nil, nil)
	// Attach LLM classifier returning "low"
	classifier := newEffortClassifierWithRunner(mockEffortRunner("low", "trivial"))
	router.SetEffortClassifier(classifier)

	// Heuristic returns complex, LLM says low → heuristic wins
	task := &Task{ID: "GH-999", Description: "Refactor the authentication system"}
	got := router.resolveComplexity(task)
	if got != ComplexityComplex {
		t.Errorf("Expected ComplexityComplex (heuristic wins), got %s", got)
	}
}

func TestResolveComplexity_NoClassifier(t *testing.T) {
	router := NewModelRouter(nil, nil)
	// No LLM classifier — pure heuristic

	task := &Task{ID: "GH-1", Description: "Fix typo in README"}
	got := router.resolveComplexity(task)
	heuristic := DetectComplexity(task)
	if got != heuristic {
		t.Errorf("Expected heuristic %s, got %s", heuristic, got)
	}
}

func TestSelectModel_UsesResolvedComplexity(t *testing.T) {
	config := &ModelRoutingConfig{
		Enabled: true,
		Trivial: "haiku",
		Simple:  "sonnet",
		Medium:  "sonnet",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)
	// LLM says "high" → should select opus despite trivial heuristic
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "security sensitive"))
	router.SetEffortClassifier(classifier)

	task := &Task{ID: "GH-2145", Description: "remove unused imports"}
	got := router.SelectModel(task)
	if got != "opus" {
		t.Errorf("Expected 'opus' (LLM effort floor), got %q", got)
	}
}

func TestSelectTimeout_UsesResolvedComplexity(t *testing.T) {
	timeoutConfig := &TimeoutConfig{
		Default: "30m",
		Trivial: "5m",
		Simple:  "10m",
		Medium:  "30m",
		Complex: "60m",
	}
	router := NewModelRouter(nil, timeoutConfig)
	// LLM says "high" → should get 60m despite trivial heuristic
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "large refactor"))
	router.SetEffortClassifier(classifier)

	task := &Task{ID: "GH-2145", Description: "remove unused imports"}
	got := router.SelectTimeout(task)
	if got != 60*time.Minute {
		t.Errorf("Expected 60m (LLM effort floor), got %v", got)
	}
}

func TestModelRouter_SetOutcomeTracker(t *testing.T) {
	tracker, cleanup := newTestOutcomeTracker(t)
	defer cleanup()

	router := NewModelRouter(nil, nil)
	if router.outcomeTracker != nil {
		t.Error("Expected nil outcome tracker by default")
	}

	router.SetOutcomeTracker(tracker)
	if router.outcomeTracker == nil {
		t.Error("Expected outcome tracker to be set")
	}
}
