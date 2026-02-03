package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/alekspetrov/pilot/internal/transcription"
)

// Config holds Telegram adapter configuration
type Config struct {
	Enabled       bool                  `yaml:"enabled"`
	BotToken      string                `yaml:"bot_token"`
	ChatID        string                `yaml:"chat_id"`
	Polling       bool                  `yaml:"polling"`         // Enable inbound polling
	AllowedIDs    []int64               `yaml:"allowed_ids"`     // User/chat IDs allowed to send tasks
	PlainTextMode bool                  `yaml:"plain_text_mode"` // Use plain text instead of Markdown (default: true for messaging apps)
	Transcription *transcription.Config `yaml:"transcription"`   // Voice message transcription config
	RateLimit     *RateLimitConfig      `yaml:"rate_limit"`      // Rate limiting config (optional)
	LLMClassifier *LLMClassifierConfig  `yaml:"llm_classifier"`  // LLM intent classification config (optional)
}

// LLMClassifierConfig configures LLM-based intent classification
type LLMClassifierConfig struct {
	Enabled        bool          `yaml:"enabled"`         // Enable LLM classification (default: false)
	APIKey         string        `yaml:"api_key"`         // Anthropic API key (falls back to ANTHROPIC_API_KEY env)
	TimeoutSeconds int           `yaml:"timeout_seconds"` // Timeout for classification (default: 2)
	HistorySize    int           `yaml:"history_size"`    // Messages to keep per chat (default: 10)
	HistoryTTL     time.Duration `yaml:"history_ttl"`     // TTL for conversation history (default: 30m)
}

// DefaultConfig returns default Telegram configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:       false,
		PlainTextMode: true, // Plain text by default for better messaging app compatibility
	}
}

// Notifier sends notifications to Telegram
type Notifier struct {
	client        *Client
	chatID        string
	plainTextMode bool
}

// NewNotifier creates a new Telegram notifier
func NewNotifier(config *Config) *Notifier {
	return &Notifier{
		client:        NewClient(config.BotToken),
		chatID:        config.ChatID,
		plainTextMode: config.PlainTextMode,
	}
}

// getParseMode returns the parse mode based on plainTextMode setting.
// Returns empty string for plain text, "Markdown" for markdown mode.
func (n *Notifier) getParseMode() string {
	if n.plainTextMode {
		return ""
	}
	return "Markdown"
}

// SendMessage sends a plain text message
func (n *Notifier) SendMessage(ctx context.Context, text string) error {
	_, err := n.client.SendMessage(ctx, n.chatID, text, "")
	return err
}

// SendTaskStarted notifies that a task has started
func (n *Notifier) SendTaskStarted(ctx context.Context, taskID, title string) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("🚀 Pilot started task\n%s %s", taskID, title)
	} else {
		text = fmt.Sprintf("🚀 *Pilot started task*\n`%s` %s", taskID, escapeMarkdown(title))
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// SendTaskCompleted notifies that a task has completed
func (n *Notifier) SendTaskCompleted(ctx context.Context, taskID, title, prURL string) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("✅ Pilot completed task\n%s %s", taskID, title)
		if prURL != "" {
			text += fmt.Sprintf("\n\nPR ready for review: %s", prURL)
		}
	} else {
		text = fmt.Sprintf("✅ *Pilot completed task*\n`%s` %s", taskID, escapeMarkdown(title))
		if prURL != "" {
			text += fmt.Sprintf("\n\n[PR ready for review](%s)", prURL)
		}
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// SendTaskFailed notifies that a task has failed
func (n *Notifier) SendTaskFailed(ctx context.Context, taskID, title, errorMsg string) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("❌ Pilot task failed\n%s %s\n\n%s", taskID, title, errorMsg)
	} else {
		text = fmt.Sprintf("❌ *Pilot task failed*\n`%s` %s\n\n```\n%s\n```", taskID, escapeMarkdown(title), errorMsg)
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// TaskProgress notifies about task progress
func (n *Notifier) TaskProgress(ctx context.Context, taskID, status string, progress int) error {
	progressBar := generateProgressBar(progress)
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("⏳ Task Progress\n%s %s\n%s %d%%", taskID, status, progressBar, progress)
	} else {
		text = fmt.Sprintf("⏳ *Task Progress*\n`%s` %s\n%s %d%%", taskID, escapeMarkdown(status), progressBar, progress)
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// PRReady notifies that a PR is ready for review
func (n *Notifier) PRReady(ctx context.Context, taskID, title, prURL string, filesChanged int) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("🔔 PR Ready for Review\n%s %s\n\n%s • %d files changed", taskID, title, prURL, filesChanged)
	} else {
		text = fmt.Sprintf("🔔 *PR Ready for Review*\n`%s` %s\n\n[View PR](%s) • %d files changed", taskID, escapeMarkdown(title), prURL, filesChanged)
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
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

// SendBudgetWarning notifies about approaching budget limits
func (n *Notifier) SendBudgetWarning(ctx context.Context, alertType, message string) error {
	var icon, title string
	switch alertType {
	case "daily_budget_warning":
		icon = "⚠️"
		title = "Daily Budget Warning"
	case "monthly_budget_warning":
		icon = "⚠️"
		title = "Monthly Budget Warning"
	case "daily_budget_exceeded":
		icon = "🚫"
		title = "Daily Budget Exceeded"
	case "monthly_budget_exceeded":
		icon = "🚫"
		title = "Monthly Budget Exceeded"
	default:
		icon = "💰"
		title = "Budget Alert"
	}

	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("%s %s\n\n%s", icon, title, message)
	} else {
		text = fmt.Sprintf("%s *%s*\n\n%s", icon, title, escapeMarkdown(message))
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// SendBudgetPaused notifies that task execution has been paused due to budget
func (n *Notifier) SendBudgetPaused(ctx context.Context, reason string) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("🛑 Task Execution Paused\n\n%s\n\nNew tasks will not start until limits reset or budget is increased.", reason)
	} else {
		text = fmt.Sprintf("🛑 *Task Execution Paused*\n\n%s\n\nNew tasks will not start until limits reset or budget is increased.", escapeMarkdown(reason))
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// SendTaskBlocked notifies that a task was blocked due to budget limits
func (n *Notifier) SendTaskBlocked(ctx context.Context, taskID, reason string) error {
	var text string
	if n.plainTextMode {
		text = fmt.Sprintf("⛔ Task Blocked\n%s\n\n%s", taskID, reason)
	} else {
		text = fmt.Sprintf("⛔ *Task Blocked*\n`%s`\n\n%s", taskID, escapeMarkdown(reason))
	}
	_, err := n.client.SendMessage(ctx, n.chatID, text, n.getParseMode())
	return err
}

// Note: escapeMarkdown is defined in formatter.go
