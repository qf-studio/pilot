package executor

import (
	"testing"
	"time"
)

// TestPlanningTimeoutDefault verifies that DefaultBackendConfig sets PlanningTimeout to 2 minutes.
func TestPlanningTimeoutDefault(t *testing.T) {
	tests := []struct {
		name     string
		config   *BackendConfig
		expected time.Duration
	}{
		{
			name:     "default config has 2m planning timeout",
			config:   DefaultBackendConfig(),
			expected: 2 * time.Minute,
		},
		{
			name: "custom config preserves zero value",
			config: &BackendConfig{
				PlanningTimeout: 0,
			},
			expected: 0,
		},
		{
			name: "custom config preserves custom value",
			config: &BackendConfig{
				PlanningTimeout: 5 * time.Minute,
			},
			expected: 5 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.PlanningTimeout != tt.expected {
				t.Errorf("PlanningTimeout = %v, want %v", tt.config.PlanningTimeout, tt.expected)
			}
		})
	}
}

// TestHasNoPlanKeyword verifies [no-plan] keyword detection in task title and description.
func TestHasNoPlanKeyword(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		expected bool
	}{
		{
			name: "no-plan in title",
			task: &Task{
				Title:       "Add feature [no-plan]",
				Description: "Some description",
			},
			expected: true,
		},
		{
			name: "no-plan in description",
			task: &Task{
				Title:       "Add feature",
				Description: "Implement this [no-plan] without epic planning",
			},
			expected: true,
		},
		{
			name: "no-plan case insensitive",
			task: &Task{
				Title:       "Add feature [No-Plan]",
				Description: "",
			},
			expected: true,
		},
		{
			name: "no keyword present",
			task: &Task{
				Title:       "Add feature",
				Description: "Normal task description",
			},
			expected: false,
		},
		{
			name: "empty title and description",
			task: &Task{
				Title:       "",
				Description: "",
			},
			expected: false,
		},
		{
			name: "partial match does not trigger",
			task: &Task{
				Title:       "no-plan without brackets",
				Description: "",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasNoPlanKeyword(tt.task)
			if got != tt.expected {
				t.Errorf("HasNoPlanKeyword() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// TestNoPlanKeywordConstant verifies the constant value.
func TestNoPlanKeywordConstant(t *testing.T) {
	if NoPlanKeyword != "[no-plan]" {
		t.Errorf("NoPlanKeyword = %q, want %q", NoPlanKeyword, "[no-plan]")
	}
}
