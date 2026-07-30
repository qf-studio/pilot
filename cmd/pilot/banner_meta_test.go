package main

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/discord"
	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
)

// GH-2459: applyDashboardBannerMeta wires env, model stack, and adapter list
// from config into the dashboard. TASK-390 (grom redesign) split the surface:
// env + model stack render in the banner identity line; adapter chips render
// in the queue panel border legend (active adapters named, idle collapsed to
// a count, no daemon chip — daemon liveness is the banner wordmark dot).
func TestApplyDashboardBannerMeta(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantBanner []string
		notBanner  []string
		wantLegend []string
		notLegend  []string
	}{
		{
			name: "full config — env + model stack in banner, adapters in legend",
			cfg: &config.Config{
				Orchestrator: &config.OrchestratorConfig{
					Autopilot: &autopilot.Config{Environment: autopilot.Environment("stage")},
				},
				Executor: &executor.BackendConfig{
					DefaultModel: "sonnet-4-6",
					ModelRouting: &executor.ModelRoutingConfig{Complex: "opus-4-7"},
				},
				Adapters: &config.AdaptersConfig{
					GitHub:   &ghadapter.Config{Enabled: true},
					Telegram: &telegram.Config{Enabled: true},
					Slack:    &slack.Config{Enabled: true},
				},
			},
			wantBanner: []string{
				"stage",      // env lowercased in banner
				"opus-4-7",   // plan model
				"sonnet-4-6", // exec model
			},
			notBanner:  []string{"gh", "tg", "slack", "daemon"},
			wantLegend: []string{"gh", "tg", "slack"},
		},
		{
			name: "single model — no slash separator",
			cfg: &config.Config{
				Executor: &executor.BackendConfig{DefaultModel: "sonnet-4-6"},
				Adapters: &config.AdaptersConfig{
					Discord: &discord.Config{Enabled: true},
				},
			},
			wantBanner: []string{"sonnet-4-6"},
			notBanner:  []string{" / "},
			wantLegend: []string{"discord"},
		},
		{
			name: "complex == default — collapse to single label, no slash",
			cfg: &config.Config{
				Executor: &executor.BackendConfig{
					DefaultModel: "opus-4-7",
					ModelRouting: &executor.ModelRoutingConfig{Complex: "opus-4-7"},
				},
				Adapters: &config.AdaptersConfig{},
			},
			wantBanner: []string{"opus-4-7"},
			notBanner:  []string{" / "},
		},
		{
			name: "empty config — no panic, banner still renders version + clock",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{},
			},
			wantBanner: []string{"v9.9.9", "utc"},
		},
		{
			name: "configured-but-disabled adapter collapses to idle count",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub:   &ghadapter.Config{Enabled: false},
					Telegram: &telegram.Config{Enabled: true},
				},
			},
			// Active adapters are named; idle ones are config facts, not
			// live status — they collapse to "○ N idle" without names.
			wantLegend: []string{"tg", "1 idle"},
			notLegend:  []string{"gh"},
		},
		{
			name: "no Adapters config — empty legend, banner version only",
			cfg:  &config.Config{
				// Adapters: nil intentionally
			},
			wantBanner: []string{"v9.9.9"},
			notBanner:  []string{"daemon"},
			notLegend:  []string{"gh", "tg", "slack"},
		},
		{
			// GH-4611: default_environment set in config with no --env
			// override must resolve through EnvironmentName() (same path as
			// the "autopilot enabled" structured log), not the stale legacy
			// Environment field. Before the fix, the banner rendered the
			// legacy field's zero value ("") instead of "hosted".
			name: "default_environment with no --env override — banner shows resolved env",
			cfg: &config.Config{
				Orchestrator: &config.OrchestratorConfig{
					Autopilot: &autopilot.Config{
						DefaultEnvironment: "hosted",
						Environments: map[string]*autopilot.EnvironmentConfig{
							"hosted": {Branch: "main"},
						},
					},
				},
				Adapters: &config.AdaptersConfig{},
			},
			wantBanner: []string{"hosted"},
			notBanner:  []string{"stage"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := dashboard.NewModel("9.9.9")
			applyDashboardBannerMeta(&model, tt.cfg, nil)

			banner := model.RenderBannerForTest()
			legend := model.AdapterLegendForTest()

			for _, want := range tt.wantBanner {
				if !strings.Contains(banner, want) {
					t.Errorf("banner missing %q\noutput:\n%s", want, banner)
				}
			}
			for _, no := range tt.notBanner {
				if strings.Contains(banner, no) {
					t.Errorf("banner unexpectedly contains %q\noutput:\n%s", no, banner)
				}
			}
			for _, want := range tt.wantLegend {
				if !strings.Contains(legend, want) {
					t.Errorf("legend missing %q\noutput: %q", want, legend)
				}
			}
			for _, no := range tt.notLegend {
				if strings.Contains(legend, no) {
					t.Errorf("legend unexpectedly contains %q\noutput: %q", no, legend)
				}
			}
		})
	}
}

// TestApplyDashboardBannerMeta_ExplicitEnvOverride verifies GH-4611's
// single-source-of-truth requirement: with an explicit --env override
// (SetActiveEnvironment, as the CLI flag handler calls at main.go:430) and a
// conflicting default_environment in config, both the dashboard banner
// (applyDashboardBannerMeta, via EnvironmentName()) and the structured
// "autopilot enabled" log line (which also calls EnvironmentName() directly
// at main.go:2369) must report the overridden environment, not the config
// default.
func TestApplyDashboardBannerMeta_ExplicitEnvOverride(t *testing.T) {
	autopilotCfg := &autopilot.Config{
		DefaultEnvironment: "hosted",
		Environments: map[string]*autopilot.EnvironmentConfig{
			"hosted": {Branch: "main"},
		},
	}
	if err := autopilotCfg.SetActiveEnvironment("stage"); err != nil {
		t.Fatalf("SetActiveEnvironment(stage): %v", err)
	}

	cfg := &config.Config{
		Orchestrator: &config.OrchestratorConfig{Autopilot: autopilotCfg},
		Adapters:     &config.AdaptersConfig{},
	}

	model := dashboard.NewModel("9.9.9")
	applyDashboardBannerMeta(&model, cfg, nil)
	banner := model.RenderBannerForTest()

	if !strings.Contains(banner, "stage") {
		t.Errorf("banner missing %q (--env override)\noutput:\n%s", "stage", banner)
	}
	if strings.Contains(banner, "hosted") {
		t.Errorf("banner unexpectedly contains %q (stale default_environment)\noutput:\n%s", "hosted", banner)
	}

	// The structured "autopilot enabled" log line at main.go:2369 uses the
	// same EnvironmentName() call directly — assert it agrees with the
	// banner so both surfaces stay in sync.
	if got := cfg.Orchestrator.Autopilot.EnvironmentName(); got != "stage" {
		t.Errorf("EnvironmentName() = %q, want %q (must match banner)", got, "stage")
	}
}
