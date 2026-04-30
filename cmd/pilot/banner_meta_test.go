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
// from config into the dashboard banner so renderBanner() shows real values
// instead of placeholders.
func TestApplyDashboardBannerMeta(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		wantSubstrs []string
		notSubstrs  []string
	}{
		{
			name: "full config — env + model stack + multi adapters",
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
			wantSubstrs: []string{
				"STAGE",      // env uppercased in banner
				"OPUS-4-7",   // plan model
				"SONNET-4-6", // exec model
				"GH",         // github abbreviated
				"TG",         // telegram abbreviated
				"SLACK",      // slack full
				"DAEMON",     // always shown
			},
		},
		{
			name: "single model — no slash separator",
			cfg: &config.Config{
				Executor: &executor.BackendConfig{DefaultModel: "sonnet-4-6"},
				Adapters: &config.AdaptersConfig{
					Discord: &discord.Config{Enabled: true},
				},
			},
			wantSubstrs: []string{"SONNET-4-6", "DISCORD"},
			notSubstrs:  []string{" / "},
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
			wantSubstrs: []string{"OPUS-4-7"},
			notSubstrs:  []string{" / "},
		},
		{
			name: "empty config — no panic, banner still renders version + clock",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{},
			},
			wantSubstrs: []string{"v9.9.9", "UTC"},
		},
		{
			name: "configured-but-disabled adapter still appears as inactive chip",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub:   &ghadapter.Config{Enabled: false},
					Telegram: &telegram.Config{Enabled: true},
				},
			},
			// Both chips render — Active vs inactive only changes the dot,
			// not whether the adapter name appears.
			wantSubstrs: []string{"GH", "TG"},
		},
		{
			name: "no Adapters config — only DAEMON chip + version",
			cfg:  &config.Config{
				// Adapters: nil intentionally
			},
			wantSubstrs: []string{"DAEMON", "v9.9.9"},
			notSubstrs:  []string{"GH", "TG", "SLACK"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := dashboard.NewModel("9.9.9")
			applyDashboardBannerMeta(&model, tt.cfg, nil)

			out := model.RenderBannerForTest()

			for _, want := range tt.wantSubstrs {
				if !strings.Contains(out, want) {
					t.Errorf("banner missing %q\noutput:\n%s", want, out)
				}
			}
			for _, no := range tt.notSubstrs {
				if strings.Contains(out, no) {
					t.Errorf("banner unexpectedly contains %q\noutput:\n%s", no, out)
				}
			}
		})
	}
}
