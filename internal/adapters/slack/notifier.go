package slack

import (
	"context"
	"fmt"
	"time"
)

// Config holds Slack adapter configuration
type Config struct {
	Enabled         bool                 `yaml:"enabled"`
	BotToken        string               `yaml:"bot_token"`
	AppToken        string               `yaml:"app_token"`
	Channel         string               `yaml:"channel"`
	SigningSecret   string               `yaml:"signing_secret"`
	SocketMode      bool                 `yaml:"socket_mode"`
	AllowedUsers    []string             `yaml:"allowed_users"`
	AllowedChannels []string             `yaml:"allowed_channels"`
	Approval        *ApprovalConfig      `yaml:"approval,omitempty"`
	LLMClassifier   *LLMClassifierConfig `yaml:"llm_classifier,omitempty"`
}

// LLMClassifierConfig configures LLM-based intent classification for Slack.
type LLMClassifierConfig struct {
	Enabled     bool          `yaml:"enabled"`
	APIKey      string        `yaml:"api_key"`
	HistorySize int           `yaml:"history_size"`
	HistoryTTL  time.Duration `yaml:"history_ttl"`
}

// DefaultConfig returns default Slack configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:         false,
		Channel:         "#dev-notifications",
		SocketMode:      false,
		AllowedUsers:    []string{},
		AllowedChannels: []string{},
		Approval:        DefaultApprovalConfig(),
	}
}

// Notifier sends notifications to Slack
type Notifier struct {
	client  *Client
	channel string
}

// NewNotifier creates a new Slack notifier
func NewNotifier(config *Config) *Notifier {
	return &Notifier{
		client:  NewClient(config.BotToken),
		channel: config.Channel,
	}
}

// TaskStarted notifies that a task has started
func (n *Notifier) TaskStarted(ctx context.Context, taskID, title string) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("🚀 *Pilot started task*\n`%s` %s", taskID, title),
				},
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskProgress notifies about task progress
func (n *Notifier) TaskProgress(ctx context.Context, taskID, status string, progress int) error {
	progressBar := generateProgressBar(progress)

	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("⏳ *Task Progress*\n`%s` %s\n%s %d%%", taskID, status, progressBar, progress),
				},
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskCompleted notifies that a task has completed
func (n *Notifier) TaskCompleted(ctx context.Context, taskID, title, prURL string) error {
	text := fmt.Sprintf("✅ *Pilot completed task*\n`%s` %s", taskID, title)
	if prURL != "" {
		text += fmt.Sprintf("\n\n<PR ready for review|%s>", prURL)
	}

	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: text,
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "good",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskFailed notifies that a task has failed
func (n *Notifier) TaskFailed(ctx context.Context, taskID, title, errorMsg string) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("❌ *Pilot task failed*\n`%s` %s\n\n```%s```", taskID, title, errorMsg),
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "danger",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// PRReady notifies that a PR is ready for review
func (n *Notifier) PRReady(ctx context.Context, taskID, title, prURL string, filesChanged int) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("🔔 *PR Ready for Review*\n`%s` %s\n\n<%s|View PR> • %d files changed", taskID, title, prURL, filesChanged),
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "#6366f1",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// generateProgressBar generates a text-based progress bar
func generateProgressBar(progress int) string {
	filled := progress / 10
	empty := 10 - filled
	bar := ""
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	return bar
}
