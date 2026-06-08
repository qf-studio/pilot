package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/discord"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/intent"
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

			// Build LLM classifier + conversation store for comms.Handler
			var llmClassifier intent.Classifier
			var convStore *intent.ConversationStore
			if discordCfg.LLMClassifier != nil && discordCfg.LLMClassifier.Enabled {
				apiKey := discordCfg.LLMClassifier.APIKey
				if apiKey == "" {
					apiKey = os.Getenv("ANTHROPIC_API_KEY")
				}
				if apiKey != "" {
					client := intent.NewAnthropicClient(apiKey)
					if deps.Cfg.Executor != nil {
						if deps.Cfg.Executor.DefaultModel != "" {
							client.SetModel(deps.Cfg.Executor.DefaultModel)
						}
						if deps.Cfg.Executor.APIBaseURL != "" {
							client.SetAPIURL(deps.Cfg.Executor.APIBaseURL + "/v1/messages")
						}
					}
					llmClassifier = client
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

			discordCommsHandler := comms.NewHandler(&comms.HandlerConfig{
				Messenger:     sdkshim.MessengerToBridge(discordBridge),
				Runner:        deps.Runner,
				Projects:      config.NewProjectSource(deps.Cfg),
				ProjectPath:   deps.ProjectPath,
				LLMClassifier: llmClassifier,
				ConvStore:     convStore,
				TaskIDPrefix:  "DISCORD",
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
