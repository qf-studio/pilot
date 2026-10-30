package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

func TestPrePRReviewConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *executor.PrePRReviewConfig
		errSubstr string
	}{
		{name: "nil block"},
		{name: "empty block is off", cfg: &executor.PrePRReviewConfig{}},
		{name: "off", cfg: &executor.PrePRReviewConfig{Mode: "off"}},
		{name: "shadow", cfg: &executor.PrePRReviewConfig{Mode: "shadow", Timeout: 6 * time.Minute}},
		{name: "enforce", cfg: &executor.PrePRReviewConfig{Mode: "enforce", MaxFixRounds: 2}},
		{name: "unknown mode", cfg: &executor.PrePRReviewConfig{Mode: "advisory"}, errSubstr: "pre_pr_review.mode"},
		{name: "mode is case sensitive", cfg: &executor.PrePRReviewConfig{Mode: "Shadow"}, errSubstr: "pre_pr_review.mode"},
		{name: "negative timeout", cfg: &executor.PrePRReviewConfig{Mode: "shadow", Timeout: -time.Second}, errSubstr: "pre_pr_review.timeout"},
		{name: "negative fix_timeout", cfg: &executor.PrePRReviewConfig{FixTimeout: -time.Second}, errSubstr: "pre_pr_review.fix_timeout"},
		{name: "negative max_fix_rounds", cfg: &executor.PrePRReviewConfig{MaxFixRounds: -1}, errSubstr: "pre_pr_review.max_fix_rounds"},
		{name: "two passes", cfg: &executor.PrePRReviewConfig{Mode: "shadow", Passes: 2}},
		{name: "three passes", cfg: &executor.PrePRReviewConfig{Passes: 3}, errSubstr: "pre_pr_review.passes"},
		{name: "negative passes", cfg: &executor.PrePRReviewConfig{Passes: -1}, errSubstr: "pre_pr_review.passes"},
		{name: "negative max_diff_chars", cfg: &executor.PrePRReviewConfig{MaxDiffChars: -1}, errSubstr: "pre_pr_review.max_diff_chars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Executor = &executor.BackendConfig{PrePRReview: tt.cfg}
			err := cfg.Validate()
			if tt.errSubstr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("err = %v, want substring %q", err, tt.errSubstr)
			}
		})
	}
}

func TestPrePRReviewConfig_Load(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "executor:\n  pre_pr_review:\n    mode: shadow\n    model: test-model\n    timeout: 7m\n    fix_timeout: 11m\n    max_fix_rounds: 2\n    max_diff_chars: 1234\n    passes: 2\n    skip_complexity: [trivial, simple]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Executor.PrePRReview
	if p == nil {
		t.Fatal("executor.pre_pr_review did not load")
	}
	if p.Mode != "shadow" || p.Model != "test-model" || p.Timeout != 7*time.Minute || p.FixTimeout != 11*time.Minute || p.MaxFixRounds != 2 || p.MaxDiffChars != 1234 || p.Passes != 2 {
		t.Errorf("loaded = %+v", p)
	}
	if !p.SkipsComplexity(executor.ComplexitySimple) || !p.SkipsComplexity(executor.ComplexityTrivial) || p.SkipsComplexity(executor.ComplexityMedium) {
		t.Errorf("skip_complexity = %v", p.SkipComplexity)
	}
	if !cfg.Executor.EffectiveSkipSelfReview() {
		t.Error("shadow mode must skip the self-review phase")
	}
}

func TestPrePRReviewConfig_DefaultsOff(t *testing.T) {
	d := executor.DefaultBackendConfig()
	if d.PrePRReview == nil || d.PrePRReviewMode() != "off" || d.EffectiveSkipSelfReview() {
		t.Fatalf("default config must leave the pre-PR review off: %+v", d.PrePRReview)
	}
	if d.PrePRReview.Timeout != 6*time.Minute || d.PrePRReview.FixTimeout != 10*time.Minute || d.PrePRReview.MaxFixRounds != 1 {
		t.Errorf("defaults = %+v", d.PrePRReview)
	}
	if err := validatePrePRReview(d.PrePRReview); err != nil {
		t.Errorf("default block must validate: %v", err)
	}
}
