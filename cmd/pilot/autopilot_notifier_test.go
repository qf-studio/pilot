package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/testutil"
)

func lifecycleCfg(on bool, tg *telegram.Config) *config.Config {
	return &config.Config{
		Adapters: &config.AdaptersConfig{Telegram: tg},
		Orchestrator: &config.OrchestratorConfig{
			Autopilot: &autopilot.Config{Enabled: true, LifecycleNotifications: on},
		},
	}
}

func TestWireLifecycleNotifier(t *testing.T) {
	good := &telegram.Config{Enabled: true, BotToken: testutil.FakeTelegramBotToken, ChatID: "123", MessageThreadID: 7}

	tests := []struct {
		name         string
		cfg          *config.Config
		wantNotifier bool
		wantWarns    int
		wantInfoText string
		wantWarnText string
	}{
		{"key on, telegram configured", lifecycleCfg(true, good), true, 0, "notifier: telegram", ""},
		{"key off", lifecycleCfg(false, good), false, 0, "notifier: none", ""},
		{"key off, no telegram", lifecycleCfg(false, nil), false, 0, "notifier: none", ""},
		{"key on, telegram nil", lifecycleCfg(true, nil), false, 1, "", "adapters.telegram.enabled"},
		{"key on, telegram disabled", lifecycleCfg(true, &telegram.Config{BotToken: testutil.FakeTelegramBotToken, ChatID: "1"}), false, 1, "", "adapters.telegram.enabled"},
		{"key on, no bot token", lifecycleCfg(true, &telegram.Config{Enabled: true, ChatID: "1"}), false, 1, "", "adapters.telegram.bot_token"},
		{"key on, no chat id", lifecycleCfg(true, &telegram.Config{Enabled: true, BotToken: testutil.FakeTelegramBotToken}), false, 1, "", "adapters.telegram.chat_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrls := []*autopilot.Controller{
				autopilot.NewController(tt.cfg.Orchestrator.Autopilot, nil, nil, "o", "a"),
				autopilot.NewController(tt.cfg.Orchestrator.Autopilot, nil, nil, "o", "b"),
			}
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			wireLifecycleNotifier(tt.cfg, log, ctrls...)

			for i, c := range ctrls {
				if c.HasNotifier() != tt.wantNotifier {
					t.Errorf("controller %d HasNotifier = %v, want %v", i, c.HasNotifier(), tt.wantNotifier)
				}
			}
			out := buf.String()
			if got := strings.Count(out, "level=WARN"); got != tt.wantWarns {
				t.Errorf("WARN lines = %d, want %d\n%s", got, tt.wantWarns, out)
			}
			if got := strings.Count(out, "level=INFO"); got > 1 {
				t.Errorf("INFO lines = %d, want at most 1\n%s", got, out)
			}
			if tt.wantInfoText != "" && !strings.Contains(out, tt.wantInfoText) {
				t.Errorf("log missing %q\n%s", tt.wantInfoText, out)
			}
			if tt.wantWarnText != "" && !strings.Contains(out, tt.wantWarnText) {
				t.Errorf("WARN missing %q\n%s", tt.wantWarnText, out)
			}
		})
	}
}

func TestBuildLifecycleNotifier_NilSafe(t *testing.T) {
	if n, missing := buildLifecycleNotifier(nil); n != nil || missing != "" {
		t.Errorf("nil cfg: got (%v, %q), want (nil, \"\")", n, missing)
	}
	cfg := &config.Config{Orchestrator: &config.OrchestratorConfig{Autopilot: &autopilot.Config{LifecycleNotifications: true}}}
	if n, missing := buildLifecycleNotifier(cfg); n != nil || missing != "adapters.telegram.enabled" {
		t.Errorf("nil Adapters: got (%v, %q)", n, missing)
	}
}
