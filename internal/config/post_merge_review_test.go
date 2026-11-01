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

// GH-6007: file_minors and minor_issue_daily_cap.
func TestConfig_PostMergeReviewMinorFields(t *testing.T) {
	validate := func(pmr *autopilot.PostMergeReviewConfig) error {
		cfg := baseValidConfig()
		cfg.Orchestrator = &OrchestratorConfig{MaxConcurrent: 1, Autopilot: &autopilot.Config{PostMergeReview: pmr}}
		return cfg.Validate()
	}
	if err := validate(&autopilot.PostMergeReviewConfig{MinorIssueDailyCap: -1}); err == nil ||
		!strings.Contains(err.Error(), "post_merge_review.minor_issue_daily_cap must be >= 0") {
		t.Fatalf("negative minor_issue_daily_cap: error = %v, want a validation error", err)
	}
	if err := validate(&autopilot.PostMergeReviewConfig{FileMinors: true, MinorIssueDailyCap: 3}); err != nil {
		t.Fatalf("valid minors config rejected: %v", err)
	}

	for cap, want := range map[int]int{0: 1, 1: 1, 3: 3} {
		if got := (&autopilot.PostMergeReviewConfig{MinorIssueDailyCap: cap}).EffectiveMinorIssueDailyCap(); got != want {
			t.Errorf("EffectiveMinorIssueDailyCap(%d) = %d, want %d", cap, got, want)
		}
	}
	var nilCfg *autopilot.PostMergeReviewConfig
	if got := nilCfg.EffectiveMinorIssueDailyCap(); got != 1 {
		t.Errorf("nil config effective cap = %d, want 1", got)
	}

	var cfg autopilot.Config
	if err := yaml.Unmarshal([]byte("post_merge_review:\n  enabled: true\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.PostMergeReview.FileMinors {
		t.Error("file_minors must default to false")
	}
	if err := yaml.Unmarshal([]byte("post_merge_review:\n  file_minors: true\n  minor_issue_daily_cap: 2\n"), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.PostMergeReview.FileMinors || cfg.PostMergeReview.MinorIssueDailyCap != 2 {
		t.Errorf("post_merge_review = %+v", cfg.PostMergeReview)
	}
}
