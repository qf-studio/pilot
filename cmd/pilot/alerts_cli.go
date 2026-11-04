package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// cliAlertFlushTimeout bounds how long a one-shot CLI command waits for the
// alerts it raised to be delivered before it exits.
const cliAlertFlushTimeout = 5 * time.Second

// registerAlertChannels registers every configured outbound alert channel on
// the dispatcher. The daemon and the CLI runners build through here.
func registerAlertChannels(cfg *config.Config, alertsCfg *alerts.AlertConfig, dispatcher *alerts.Dispatcher) {
	// Register Slack channel if configured
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
		slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
		for _, ch := range alertsCfg.Channels {
			if ch.Type == "slack" && ch.Slack != nil {
				slackChannel := alerts.NewSlackChannel(ch.Name, slackClient, ch.Slack.Channel)
				dispatcher.RegisterChannel(slackChannel)
			}
		}
	}

	// Register Telegram channel if configured
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
		telegramClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		for _, ch := range alertsCfg.Channels {
			if ch.Type == "telegram" && ch.Telegram != nil {
				telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID, ch.Telegram.MessageThreadID)
				dispatcher.RegisterChannel(telegramChannel)
			}
		}
	}

	// Register webhook channels
	for _, ch := range alertsCfg.Channels {
		if ch.Type == "webhook" && ch.Enabled && ch.Webhook != nil {
			webhookChannel := alerts.NewWebhookChannel(ch.Name, &alerts.WebhookChannelConfig{
				URL:     ch.Webhook.URL,
				Method:  ch.Webhook.Method,
				Headers: ch.Webhook.Headers,
				Secret:  ch.Webhook.Secret,
			})
			dispatcher.RegisterChannel(webhookChannel)
		}
	}

	// Register email channels
	for _, ch := range alertsCfg.Channels {
		if ch.Type == "email" && ch.Enabled && ch.Email != nil && ch.Email.SMTPHost != "" {
			sender := alerts.NewSMTPSender(ch.Email.SMTPHost, ch.Email.SMTPPort, ch.Email.From, ch.Email.Username, ch.Email.Password)
			emailChannel := alerts.NewEmailChannel(ch.Name, sender, ch.Email)
			dispatcher.RegisterChannel(emailChannel)
		}
	}

	// Register PagerDuty channels
	for _, ch := range alertsCfg.Channels {
		if ch.Type == "pagerduty" && ch.Enabled && ch.PagerDuty != nil {
			pdChannel := alerts.NewPagerDutyChannel(ch.Name, ch.PagerDuty)
			dispatcher.RegisterChannel(pdChannel)
		}
	}
}

// newCLIAlertProcessor builds and starts the alert engine for a CLI runner and
// returns its processor plus a stop func that flushes pending deliveries. A nil
// processor means alerts are disabled or the engine failed to start. A seam for
// tests.
var newCLIAlertProcessor = func(cfg *config.Config) (executor.AlertEventProcessor, func()) {
	if cfg == nil {
		return nil, func() {}
	}
	alertsCfg := getAlertsConfig(cfg)
	if alertsCfg == nil || !alertsCfg.Enabled {
		return nil, func() {}
	}
	dispatcher := alerts.NewDispatcher(alertsCfg, alerts.WithDispatcherMetrics(alerts.NewAlertMetrics()))
	registerAlertChannels(cfg, alertsCfg, dispatcher)
	engine := alerts.NewEngine(alertsCfg, alerts.WithDispatcher(dispatcher))
	if err := engine.Start(context.Background()); err != nil {
		logging.WithComponent("alerts").Warn("alert engine failed to start for CLI runner, alerts dropped", slog.Any("error", err))
		return nil, func() {}
	}
	return alerts.NewEngineAdapter(engine), func() {
		engine.Flush(cliAlertFlushTimeout)
		engine.Stop()
	}
}

// installCLIAlertProcessor gives a CLI runner the alert processor the daemon
// gives its runner (GH-6050): without it emitAlertEvent logs one warning and
// drops the event, so a fail-closed `pilot navigator bootstrap` in api_key
// mode alerts nobody. With alerts disabled in cfg the runner keeps the
// warn-once drop. The returned stop func flushes and stops the engine; call it
// (deferred) once the command is done.
func installCLIAlertProcessor(runner *executor.Runner, cfg *config.Config) func() {
	processor, stop := newCLIAlertProcessor(cfg)
	if processor != nil {
		runner.SetAlertProcessor(processor)
	}
	return stop
}
