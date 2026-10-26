package main

import (
	"log/slog"
	"os/exec"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// buildPostMergeReviewer returns the post-merge reviewer when
// orchestrator.autopilot.post_merge_review.enabled is true and the claude
// binary is usable (GH-5861). The model defaults to the executor's configured
// model (executor.default_model); empty leaves the subprocess on its own
// default. A nil result means "no reviewer" — callers must not install it.
func buildPostMergeReviewer(cfg *config.Config, log *slog.Logger) *executor.PostMergeReviewer {
	if cfg == nil || cfg.Orchestrator == nil || !cfg.Orchestrator.Autopilot.PostMergeReviewEnabled() {
		return nil
	}
	pmr := cfg.Orchestrator.Autopilot.PostMergeReview

	claudeCmd := ""
	if cfg.Executor != nil && cfg.Executor.ClaudeCode != nil {
		claudeCmd = cfg.Executor.ClaudeCode.Command
	}
	if claudeCmd == "" {
		claudeCmd = "claude"
	}
	if _, err := exec.LookPath(claudeCmd); err != nil {
		log.Warn("autopilot.post_merge_review is enabled but the claude binary was not found — post-merge reviews stay off",
			slog.String("command", claudeCmd))
		return nil
	}

	model := pmr.Model
	if model == "" {
		model = cfg.Executor.ResolveModel("")
	}
	return executor.NewPostMergeReviewer(claudeCmd, model, pmr.Timeout)
}

// wirePostMergeReviewer installs the post-merge reviewer on every controller
// and logs one startup line. With the knob off it is silent.
func wirePostMergeReviewer(cfg *config.Config, log *slog.Logger, controllers ...*autopilot.Controller) {
	r := buildPostMergeReviewer(cfg, log)
	if r == nil {
		return
	}
	for _, c := range controllers {
		c.SetPostMergeReviewer(r)
	}
	pmr := cfg.Orchestrator.Autopilot.PostMergeReview
	log.Info("autopilot post-merge reviewer enabled",
		slog.Int("daily_cap", pmr.EffectiveDailyCap()),
		slog.String("model", pmr.Model),
	)
}
