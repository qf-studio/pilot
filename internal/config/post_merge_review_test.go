package config

import (
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/autopilot"
	"gopkg.in/yaml.v3"
)

// GH-5861: orchestrator.autopilot.post_merge_review validation and YAML shape.
func TestConfig_Validate_PostMergeReview(t *testing.T) {
	tests := []struct {
		name      string
		pmr       *autopilot.PostMergeReviewConfig
		errSubstr string
	}{
		{name: "absent", pmr: nil},
		{name: "defaults", pmr: &autopilot.PostMergeReviewConfig{Enabled: true}},
		{name: "explicit", pmr: &autopilot.PostMergeReviewConfig{Enabled: true, Model: "m", DailyCap: 5, Timeout: time.Minute}},
		{name: "negative cap", pmr: &autopilot.PostMergeReviewConfig{DailyCap: -1}, errSubstr: "post_merge_review.daily_cap must be >= 0"},
		{name: "negative timeout", pmr: &autopilot.PostMergeReviewConfig{Timeout: -time.Second}, errSubstr: "post_merge_review.timeout must be >= 0"},
		{name: "max diff chars", pmr: &autopilot.PostMergeReviewConfig{Enabled: true, MaxDiffChars: 50000}},
		{name: "two passes", pmr: &autopilot.PostMergeReviewConfig{Enabled: true, Passes: 2}},
		{name: "three passes", pmr: &autopilot.PostMergeReviewConfig{Passes: 3}, errSubstr: "post_merge_review.passes must be 1 or 2"},
		{name: "negative passes", pmr: &autopilot.PostMergeReviewConfig{Passes: -1}, errSubstr: "post_merge_review.passes must be 1 or 2"},
		{name: "negative max diff chars", pmr: &autopilot.PostMergeReviewConfig{MaxDiffChars: -1}, errSubstr: "post_merge_review.max_diff_chars must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Orchestrator = &OrchestratorConfig{
				MaxConcurrent: 1,
				Autopilot:     &autopilot.Config{PostMergeReview: tt.pmr},
			}
			err := cfg.Validate()
			switch {
			case tt.errSubstr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tt.errSubstr)):
				t.Fatalf("error = %v, want one containing %q", err, tt.errSubstr)
			}
		})
	}
}

func TestPostMergeReviewConfig_YAML(t *testing.T) {
	var cfg autopilot.Config
	doc := "post_merge_review:\n  enabled: true\n  model: some-model\n  daily_cap: 7\n  timeout: 90s\n  max_diff_chars: 123456\n  passes: 2\n"
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pmr := cfg.PostMergeReview
	if pmr == nil || !pmr.Enabled || pmr.Model != "some-model" || pmr.DailyCap != 7 || pmr.Timeout != 90*time.Second || pmr.MaxDiffChars != 123456 || pmr.Passes != 2 {
		t.Fatalf("post_merge_review = %+v", pmr)
	}
}
