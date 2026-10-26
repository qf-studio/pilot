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
