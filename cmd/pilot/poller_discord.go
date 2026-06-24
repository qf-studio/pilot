package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/qf-studio/pilot/internal/adapters/discord"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"
	sdkDiscord "github.com/qf-studio/studio-sdk/sdk/integrations/discord"
)

func discordPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "discord",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			discordCfg := deps.Cfg.Adapters.Discord

			var discordClassifierCfg *comms.ClassifierConfig
			if discordCfg.LLMClassifier != nil {
				discordClassifierCfg = &comms.ClassifierConfig{
					Enabled:     discordCfg.LLMClassifier.Enabled,
					APIKey:      discordCfg.LLMClassifier.APIKey,
					HistorySize: discordCfg.LLMClassifier.HistorySize,
					HistoryTTL:  discordCfg.LLMClassifier.HistoryTTL,
				}
			}

			// discordChatHandler is the core.MessageHandler: it shims SDK events
			// into comms.IncomingMessage and forwards them to discordCommsHandler.
			// SetCommsHandler is called after the bridge messenger is created to
			// break the bridge ↔ Messenger circular dependency.
			discordChatHandler := discord.NewHandler(&discord.HandlerConfig{
				BotToken:        discordCfg.BotToken,
				BotID:           discordCfg.BotID,
				AllowedGuilds:   discordCfg.AllowedGuilds,
				AllowedChannels: discordCfg.AllowedChannels,
			}, nil)

			discordBridge := sdkDiscord.New(sdkDiscord.Config{
				BotToken:        discordCfg.BotToken,
				BotID:           discordCfg.BotID,
				AllowedGuilds:   discordCfg.AllowedGuilds,
				AllowedChannels: discordCfg.AllowedChannels,
			}, nil).NewChatBridge(sdkCore.ChatDeps{Handler: discordChatHandler})

			discordCommsHandler := comms.BuildHandler(comms.HandlerDeps{
				Messenger:       sdkshim.MessengerToBridge(discordBridge),
				Runner:          deps.Runner,
				Projects:        config.NewProjectSource(deps.Cfg),
				ProjectPath:     deps.ProjectPath,
				RateLimit:       discordRateLimitToComms(discordCfg.RateLimit),
				Classifier:      discordClassifierCfg,
				Store:           deps.Store,
				TaskIDPrefix:    "DISCORD",
				ExecutorBackend: deps.Cfg.Executor,
			})
			discordChatHandler.SetCommsHandler(discordCommsHandler)

			go func() {
				if err := discordBridge.Start(ctx); err != nil {
					logging.WithComponent("discord").Error("Discord listener error",
						slog.Any("error", err),
					)
				}
			}()
			fmt.Println("🎮 Discord bot started")
			logging.WithComponent("start").Info("Discord bot started")
		},
	}
}

// discordRateLimitToComms converts discord.RateLimitConfig units to comms.RateLimitConfig.
// discord uses per-second messages and per-minute tasks; comms uses per-minute and per-hour.
func discordRateLimitToComms(rl *discord.RateLimitConfig) *comms.RateLimitConfig {
	if rl == nil {
		return nil
	}
	return &comms.RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: rl.MessagesPerSecond * 60,
		TasksPerHour:      rl.TasksPerMinute * 60,
		BurstSize:         comms.DefaultRateLimitConfig().BurstSize,
	}
}
