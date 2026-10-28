package main

import (
	"log/slog"
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

func postMergeReviewCfg(enabled bool, claudeCmd string) *config.Config {
	return &config.Config{
		Orchestrator: &config.OrchestratorConfig{
			Autopilot: &autopilot.Config{PostMergeReview: &autopilot.PostMergeReviewConfig{Enabled: enabled}},
		},
		Executor: &executor.BackendConfig{ClaudeCode: &executor.ClaudeCodeConfig{Command: claudeCmd}},
	}
}

// GH-5861: the reviewer is only built when the knob is on and the claude
// binary resolves; otherwise handleMerged keeps its pre-GH-5861 behavior.
func TestBuildPostMergeReviewer(t *testing.T) {
	log := slog.Default()
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nil config", nil, false},
		{"no orchestrator", &config.Config{}, false},
		{"knob off", postMergeReviewCfg(false, "sh"), false},
		{"knob on, binary missing", postMergeReviewCfg(true, "definitely-not-a-real-binary-5861"), false},
		{"knob on, binary found", postMergeReviewCfg(true, "sh"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildPostMergeReviewer(tt.cfg, log) != nil; got != tt.want {
				t.Fatalf("built reviewer = %v, want %v", got, tt.want)
			}
		})
	}
}

// GH-5873: one controller runs per project; wiring must hand them all the same
// daily budget so daily_cap is daemon-wide.
func TestPostMergeReviewer_SharedBudgetAcrossControllers(t *testing.T) {
	cfg := postMergeReviewCfg(true, "sh")
	cfg.Orchestrator.Autopilot.PostMergeReview.DailyCap = 2

	c1 := autopilot.NewController(autopilot.DefaultConfig(), nil, nil, "o", "r1")
	c2 := autopilot.NewController(autopilot.DefaultConfig(), nil, nil, "o", "r2")
	wirePostMergeReviewer(cfg, slog.Default(), c1, c2)

	if c1.PostMergeReviewBudget() != c2.PostMergeReviewBudget() {
		t.Fatal("controllers got separate budgets; daily_cap would multiply by project count")
	}
}

// executor.memory_injection.enabled is the one switch for review context too.
func TestMemoryInjectionEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{"nil config", nil, false},
		{"no executor", &config.Config{}, false},
		{"no memory_injection block", &config.Config{Executor: &executor.BackendConfig{}}, false},
		{"disabled", &config.Config{Executor: &executor.BackendConfig{MemoryInjection: &executor.MemoryInjectionConfig{Enabled: false}}}, false},
		{"enabled", &config.Config{Executor: &executor.BackendConfig{MemoryInjection: executor.DefaultMemoryInjectionConfig()}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryInjectionEnabled(tt.cfg); got != tt.want {
				t.Errorf("memoryInjectionEnabled = %v, want %v", got, tt.want)
			}
		})
	}
}
