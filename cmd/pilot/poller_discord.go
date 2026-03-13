package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/adapters/discord"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/logging"
)

func discordPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "discord",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			handler := discord.NewHandler(&discord.HandlerConfig{
				BotToken:        deps.Cfg.Adapters.Discord.BotToken,
				BotID:           deps.Cfg.Adapters.Discord.BotID,
				AllowedGuilds:   deps.Cfg.Adapters.Discord.AllowedGuilds,
				AllowedChannels: deps.Cfg.Adapters.Discord.AllowedChannels,
				ProjectPath:     deps.ProjectPath,
				LLMClassifier:   deps.Cfg.Adapters.Discord.LLMClassifier,
			}, deps.Runner)

			// GH-2132: Wire notifier for task lifecycle messages
			discordClient := discord.NewClient(deps.Cfg.Adapters.Discord.BotToken)
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
