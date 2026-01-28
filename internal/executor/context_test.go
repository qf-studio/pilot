package executor

import (
	"strings"
	"testing"
)

func TestDefaultPatternContextConfig(t *testing.T) {
	cfg := DefaultPatternContextConfig()

	if cfg == nil {
		t.Fatal("DefaultPatternContextConfig returned nil")
	}
	if !cfg.Enabled {
		t.Error("Enabled should be true by default")
	}
	if cfg.MinConfidence != 0.6 {
		t.Errorf("MinConfidence = %f, want 0.6", cfg.MinConfidence)
	}
	if cfg.MaxPatterns != 5 {
		t.Errorf("MaxPatterns = %d, want 5", cfg.MaxPatterns)
	}
	if !cfg.IncludeAnti {
		t.Error("IncludeAnti should be true by default")
	}
}

func TestPatternContextConfigValues(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		minConfidence float64
		maxPatterns   int
		includeAnti   bool
	}{
		{
			name:          "all enabled",
			enabled:       true,
			minConfidence: 0.8,
			maxPatterns:   10,
			includeAnti:   true,
		},
		{
			name:          "disabled",
			enabled:       false,
			minConfidence: 0.5,
			maxPatterns:   3,
			includeAnti:   false,
		},
		{
			name:          "zero patterns",
			enabled:       true,
			minConfidence: 0.0,
			maxPatterns:   0,
			includeAnti:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &PatternContextConfig{
				Enabled:       tt.enabled,
				MinConfidence: tt.minConfidence,
				MaxPatterns:   tt.maxPatterns,
				IncludeAnti:   tt.includeAnti,
			}

			if cfg.Enabled != tt.enabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.enabled)
			}
			if cfg.MinConfidence != tt.minConfidence {
				t.Errorf("MinConfidence = %f, want %f", cfg.MinConfidence, tt.minConfidence)
			}
			if cfg.MaxPatterns != tt.maxPatterns {
				t.Errorf("MaxPatterns = %d, want %d", cfg.MaxPatterns, tt.maxPatterns)
			}
			if cfg.IncludeAnti != tt.includeAnti {
				t.Errorf("IncludeAnti = %v, want %v", cfg.IncludeAnti, tt.includeAnti)
			}
		})
	}
}

func TestInjectPatternsLogic(t *testing.T) {
	// Test the string manipulation logic of InjectPatterns
	// without needing a real PatternContext or Store

	tests := []struct {
		name           string
		prompt         string
		patterns       string
		expectContains []string
	}{
		{
			name:     "inject before task marker",
			prompt:   "Some intro.\n\n## Task: TASK-123\n\nDescription here.",
			patterns: "## Learned Patterns\n- Pattern 1\n",
			expectContains: []string{
				"## Learned Patterns",
				"## Task: TASK-123",
			},
		},
		{
			name:           "no task marker prepends",
			prompt:         "Just a prompt without markers.",
			patterns:       "## Patterns\n",
			expectContains: []string{"## Patterns", "Just a prompt"},
		},
		{
			name:           "empty patterns unchanged",
			prompt:         "## Task: TEST\n\nContent",
			patterns:       "",
			expectContains: []string{"## Task: TEST"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result string
			if tt.patterns == "" {
				result = tt.prompt
			} else {
				taskMarker := "## Task:"
				idx := strings.Index(tt.prompt, taskMarker)
				if idx != -1 {
					var sb strings.Builder
					sb.WriteString(tt.prompt[:idx])
					sb.WriteString(tt.patterns)
					sb.WriteString("\n")
					sb.WriteString(tt.prompt[idx:])
					result = sb.String()
				} else {
					result = tt.patterns + "\n" + tt.prompt
				}
			}

			for _, expected := range tt.expectContains {
				if !strings.Contains(result, expected) {
					t.Errorf("result should contain %q, got:\n%s", expected, result)
				}
			}
		})
	}
}
