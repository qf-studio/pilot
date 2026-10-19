package main

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
)

// buildLifecycleNotifier returns the Telegram-backed PR lifecycle notifier
// when orchestrator.autopilot.lifecycle_notifications is true and the Telegram
// adapter is usable (GH-5759). The second return value names the missing
// setting when the key is on but Telegram cannot be used; it is empty
// otherwise. A nil notifier means "no notifier" — callers must not pass it to
// SetNotifier.
func buildLifecycleNotifier(cfg *config.Config) (autopilot.Notifier, string) {
	if cfg == nil || cfg.Orchestrator == nil || cfg.Orchestrator.Autopilot == nil ||
		!cfg.Orchestrator.Autopilot.LifecycleNotifications {
		return nil, ""
	}
	var tg *telegram.Config
	if cfg.Adapters != nil {
		tg = cfg.Adapters.Telegram
	}
	switch {
	case tg == nil || !tg.Enabled:
		return nil, "adapters.telegram.enabled"
	case tg.BotToken == "":
		return nil, "adapters.telegram.bot_token"
	case tg.ChatID == "":
		return nil, "adapters.telegram.chat_id"
	}
	return autopilot.NewTelegramNotifier(telegram.NewClient(tg.BotToken), tg.ChatID, tg.MessageThreadID), ""
}

// wireLifecycleNotifier installs the lifecycle notifier on every controller and
// logs exactly one startup line: INFO naming the active notifier, or a WARN
// naming the missing Telegram setting when the opt-in key is on but unusable.
// With the key off it is silent and leaves every controller without a notifier.
func wireLifecycleNotifier(cfg *config.Config, log *slog.Logger, controllers ...*autopilot.Controller) {
	if cfg == nil || cfg.Orchestrator == nil || cfg.Orchestrator.Autopilot == nil {
		return
	}
	n, missing := buildLifecycleNotifier(cfg)
	if n == nil {
		if missing != "" {
			log.Warn("autopilot.lifecycle_notifications is true but Telegram is not usable — PR lifecycle notices stay off",
				slog.String("missing", missing))
		} else {
			log.Info("autopilot lifecycle notifier: none (orchestrator.autopilot.lifecycle_notifications is off)")
		}
		return
	}
	wired := 0
	for _, c := range controllers {
		if c == nil {
			continue
		}
		c.SetNotifier(n)
		wired++
	}
	log.Info("autopilot lifecycle notifier: telegram",
		slog.String("chat_id", cfg.Adapters.Telegram.ChatID),
		slog.Int("controllers", wired))
}
