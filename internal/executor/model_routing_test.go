package executor

import (
	"testing"
	"time"
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
				Simple:  "claude-sonnet",
				Medium:  "claude-sonnet",
				Complex: "claude-opus",
			},
			task:     &Task{Description: "Add field to struct"},
			expected: "claude-sonnet",
		},
		{
			name: "complex task returns opus",
			config: &ModelRoutingConfig{
				Enabled: true,
				Trivial: "claude-haiku",
				Simple:  "claude-sonnet",
				Medium:  "claude-sonnet",
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
		Simple:  "sonnet",
		Medium:  "sonnet",
		Complex: "opus",
	}
	router := NewModelRouter(config, nil)

	tests := []struct {
		complexity Complexity
		expected   string
	}{
		{ComplexityTrivial, "haiku"},
		{ComplexitySimple, "sonnet"},
		{ComplexityMedium, "sonnet"},
		{ComplexityComplex, "opus"},
		{Complexity("unknown"), "sonnet"}, // Default to medium
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
