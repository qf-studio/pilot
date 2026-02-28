package wiring

import (
	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/quality"
)

// MinimalConfig returns a config with all optional components disabled.
// This is the baseline — only the executor and orchestrator are configured.
func MinimalConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot.Enabled = false
	cfg.Quality.Enabled = false
	cfg.Budget.Enabled = false
	cfg.Memory.Learning.Enabled = false
	return cfg
}

// WithAutopilot enables the autopilot subsystem with default settings.
func WithAutopilot(cfg *config.Config) *config.Config {
	cfg.Orchestrator.Autopilot.Enabled = true
	cfg.Orchestrator.Autopilot.AutoMerge = true
	cfg.Orchestrator.Autopilot.AutoReview = true
	return cfg
}

// WithLearning enables the learning loop and pattern context.
func WithLearning(cfg *config.Config) *config.Config {
	cfg.Memory.Learning.Enabled = true
	cfg.Memory.Learning.MinConfidence = 0.6
	cfg.Memory.Learning.MaxPatterns = 5
	cfg.Memory.Learning.IncludeAnti = true
	return cfg
}

// WithBudget enables budget enforcement with default limits.
func WithBudget(cfg *config.Config) *config.Config {
	cfg.Budget = budget.DefaultConfig()
	cfg.Budget.Enabled = true
	cfg.Budget.DailyLimit = 50.0
	cfg.Budget.MonthlyLimit = 500.0
	return cfg
}

// WithQuality enables quality gates with the minimal build gate.
func WithQuality(cfg *config.Config) *config.Config {
	cfg.Quality = quality.MinimalBuildGate()
	return cfg
}

// WithMultiRepo adds a second project to the config, simulating multi-repo setups.
func WithMultiRepo(cfg *config.Config) *config.Config {
	cfg.Projects = append(cfg.Projects, &config.ProjectConfig{
		Name: "secondary",
		Path: "/tmp/test-repo-2",
		GitHub: &config.ProjectGitHubConfig{
			Owner: "test-owner",
			Repo:  "test-repo-2",
		},
	})
	return cfg
}

// WithExecutorDefaults ensures the executor config is set to safe defaults
// suitable for wiring tests (no actual subprocess spawning).
func WithExecutorDefaults(cfg *config.Config) *config.Config {
	cfg.Executor = executor.DefaultBackendConfig()
	cfg.Executor.Type = "claude-code"
	return cfg
}

// FullConfig returns a config with all optional components enabled.
func FullConfig() *config.Config {
	cfg := MinimalConfig()
	WithAutopilot(cfg)
	WithLearning(cfg)
	WithBudget(cfg)
	WithQuality(cfg)
	return cfg
}
