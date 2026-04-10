package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/discord"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/intent"
	"github.com/qf-studio/pilot/internal/logging"
)

func discordPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "discord",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			discordCfg := deps.Cfg.Adapters.Discord

			// Build LLM classifier + conversation store for comms.Handler
			var llmClassifier intent.Classifier
			var convStore *intent.ConversationStore
			if discordCfg.LLMClassifier != nil && discordCfg.LLMClassifier.Enabled {
				apiKey := discordCfg.LLMClassifier.APIKey
				if apiKey == "" {
					apiKey = os.Getenv("ANTHROPIC_API_KEY")
				}
				if apiKey != "" {
					llmClassifier = intent.NewAnthropicClient(apiKey)
					historySize := 10
					if discordCfg.LLMClassifier.HistorySize > 0 {
						historySize = discordCfg.LLMClassifier.HistorySize
					}
					historyTTL := 30 * time.Minute
					if discordCfg.LLMClassifier.HistoryTTL > 0 {
						historyTTL = discordCfg.LLMClassifier.HistoryTTL
					}
					convStore = intent.NewConversationStore(historySize, historyTTL)
				}
			}

			// Build DiscordMessenger + comms.Handler
			discordClient := discord.NewClient(discordCfg.BotToken)
			messenger := discord.NewMessenger(discordClient)

			commsHandler := comms.NewHandler(&comms.HandlerConfig{
				Messenger:     messenger,
				Runner:        deps.Runner,
				Projects:      config.NewProjectSource(deps.Cfg),
				ProjectPath:   deps.ProjectPath,
				LLMClassifier: llmClassifier,
				ConvStore:     convStore,
				TaskIDPrefix:  "DISCORD",
			})

			handler := discord.NewHandler(&discord.HandlerConfig{
				BotToken:        discordCfg.BotToken,
				BotID:           discordCfg.BotID,
				AllowedGuilds:   discordCfg.AllowedGuilds,
				AllowedChannels: discordCfg.AllowedChannels,
			}, commsHandler)

			// GH-2132: Wire notifier for task lifecycle messages
			handler.SetNotifier(discord.NewNotifier(discordClient))

			go func() {
				if err := handler.StartListening(ctx); err != nil {
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
