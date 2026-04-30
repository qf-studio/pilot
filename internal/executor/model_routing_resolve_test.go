package executor

import "testing"

// TestResolveExecutionModel_GH2450 covers the regression where default_model
// clobbered model_routing results for the claude-code backend.
func TestResolveExecutionModel_GH2450(t *testing.T) {
	simpleTask := &Task{Description: "Add field to struct"}

	tests := []struct {
		name     string
		router   *ModelRouter
		cfg      *BackendConfig
		task     *Task
		expected string
	}{
		{
			name: "model_routing wins over default_model on claude-code backend",
			router: NewModelRouter(&ModelRoutingConfig{
				Enabled: true,
				Trivial: "claude-haiku",
				Simple:  "claude-sonnet-4-6",
				Medium:  "claude-sonnet-4-6",
				Complex: "claude-opus",
			}, nil),
			cfg: &BackendConfig{
				Type:         BackendTypeClaudeCode,
				DefaultModel: "claude-opus-4-5",
			},
			task:     simpleTask,
			expected: "claude-sonnet-4-6",
		},
		{
			name:   "claude-code backend with routing disabled keeps empty passthrough",
			router: NewModelRouter(&ModelRoutingConfig{Enabled: false}, nil),
			cfg: &BackendConfig{
				Type:         BackendTypeClaudeCode,
				DefaultModel: "claude-opus-4-5",
			},
			task:     simpleTask,
			expected: "",
		},
		{
			name:   "non-CC backend falls back to default_model when router empty",
			router: NewModelRouter(&ModelRoutingConfig{Enabled: false}, nil),
			cfg: &BackendConfig{
				Type:         "anthropic",
				DefaultModel: "claude-opus-4-5",
			},
			task:     simpleTask,
			expected: "claude-opus-4-5",
		},
		{
			name:   "no config and disabled routing returns empty",
			router: NewModelRouter(&ModelRoutingConfig{Enabled: false}, nil),
			cfg:    nil,
			task:   simpleTask,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveExecutionModel(tt.router, tt.cfg, tt.task)
			if got != tt.expected {
				t.Errorf("resolveExecutionModel() = %q, want %q", got, tt.expected)
			}
		})
	}
}
