package telegram

import (
	"context"
	"fmt"
)

// Config holds Telegram adapter configuration
type Config struct {
	Enabled    bool    `yaml:"enabled"`
	BotToken   string  `yaml:"bot_token"`
	ChatID     string  `yaml:"chat_id"`
	Polling    bool    `yaml:"polling"`     // Enable inbound polling
	AllowedIDs []int64 `yaml:"allowed_ids"` // User/chat IDs allowed to send tasks
}

// DefaultConfig returns default Telegram configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled: false,
	}
}

// Notifier sends notifications to Telegram
type Notifier struct {
	client *Client
	chatID string
}

// NewNotifier creates a new Telegram notifier
func NewNotifier(config *Config) *Notifier {
	return &Notifier{
		client: NewClient(config.BotToken),
		chatID: config.ChatID,
	}
}

// SendMessage sends a plain text message
func (n *Notifier) SendMessage(ctx context.Context, text string) error {
	_, err := n.client.SendMessage(ctx, n.chatID, text, "")
	return err
}

// SendTaskStarted notifies that a task has started
func (n *Notifier) SendTaskStarted(ctx context.Context, taskID, title string) error {
	text := fmt.Sprintf("🚀 *Pilot started task*\n`%s` %s", taskID, escapeMarkdown(title))
	_, err := n.client.SendMessage(ctx, n.chatID, text, "Markdown")
	return err
}

// SendTaskCompleted notifies that a task has completed
func (n *Notifier) SendTaskCompleted(ctx context.Context, taskID, title, prURL string) error {
	text := fmt.Sprintf("✅ *Pilot completed task*\n`%s` %s", taskID, escapeMarkdown(title))
	if prURL != "" {
		text += fmt.Sprintf("\n\n[PR ready for review](%s)", prURL)
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, "Markdown")
	return err
}

// SendTaskFailed notifies that a task has failed
func (n *Notifier) SendTaskFailed(ctx context.Context, taskID, title, errorMsg string) error {
	text := fmt.Sprintf("❌ *Pilot task failed*\n`%s` %s\n\n```\n%s\n```", taskID, escapeMarkdown(title), errorMsg)
	_, err := n.client.SendMessage(ctx, n.chatID, text, "Markdown")
	return err
}

// TaskProgress notifies about task progress
func (n *Notifier) TaskProgress(ctx context.Context, taskID, status string, progress int) error {
	progressBar := generateProgressBar(progress)
	text := fmt.Sprintf("⏳ *Task Progress*\n`%s` %s\n%s %d%%", taskID, escapeMarkdown(status), progressBar, progress)
	_, err := n.client.SendMessage(ctx, n.chatID, text, "Markdown")
	return err
}

// PRReady notifies that a PR is ready for review
func (n *Notifier) PRReady(ctx context.Context, taskID, title, prURL string, filesChanged int) error {
	text := fmt.Sprintf("🔔 *PR Ready for Review*\n`%s` %s\n\n[View PR](%s) • %d files changed", taskID, escapeMarkdown(title), prURL, filesChanged)
	_, err := n.client.SendMessage(ctx, n.chatID, text, "Markdown")
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

// escapeMarkdown escapes special characters for Telegram Markdown
func escapeMarkdown(text string) string {
	// In Telegram's Markdown mode, these characters need escaping: _ * [ ] ( ) ~ ` > # + - = | { } . !
	// For basic usage, we escape the most common ones
	replacer := map[rune]string{
		'_': "\\_",
		'*': "\\*",
		'[': "\\[",
		']': "\\]",
		'`': "\\`",
	}
	result := ""
	for _, c := range text {
		if escaped, ok := replacer[c]; ok {
			result += escaped
		} else {
			result += string(c)
		}
	}
	return result
}
