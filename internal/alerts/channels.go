package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
)

// SlackChannel sends alerts to Slack
type SlackChannel struct {
	name    string
	client  *slack.Client
	channel string
}

// NewSlackChannel creates a new Slack alert channel
func NewSlackChannel(name string, client *slack.Client, channel string) *SlackChannel {
	return &SlackChannel{
		name:    name,
		client:  client,
		channel: channel,
	}
}

func (c *SlackChannel) Name() string { return c.name }
func (c *SlackChannel) Type() string { return "slack" }

func (c *SlackChannel) Send(ctx context.Context, alert *Alert) error {
	blocks := c.formatSlackBlocks(alert)

	msg := &slack.Message{
		Channel: c.channel,
		Blocks:  blocks,
		Attachments: []slack.Attachment{
			{
				Color: c.severityColor(alert.Severity),
			},
		},
	}

	_, err := c.client.PostMessage(ctx, msg)
	return err
}

func (c *SlackChannel) formatSlackBlocks(alert *Alert) []slack.Block {
	emoji := c.severityEmoji(alert.Severity)
	severityLabel := strings.ToUpper(string(alert.Severity))

	blocks := []slack.Block{
		{
			Type: "header",
			Text: &slack.TextObject{
				Type: "plain_text",
				Text: fmt.Sprintf("%s %s Alert", emoji, severityLabel),
			},
		},
		{
			Type: "section",
			Text: &slack.TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("*%s*\n%s", alert.Title, alert.Message),
			},
		},
	}

	// Add context block with metadata
	contextElements := []slack.TextObject{
		{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Type:* `%s`", alert.Type),
		},
	}

	if alert.Source != "" {
		contextElements = append(contextElements, slack.TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Source:* `%s`", alert.Source),
		})
	}

	if alert.ProjectPath != "" {
		contextElements = append(contextElements, slack.TextObject{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Project:* `%s`", alert.ProjectPath),
		})
	}

	blocks = append(blocks, slack.Block{
		Type:     "context",
		Elements: contextElements,
	})

	return blocks
}

func (c *SlackChannel) severityEmoji(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "🚨"
	case SeverityWarning:
		return "⚠️"
	default:
		return "ℹ️"
	}
}

func (c *SlackChannel) severityColor(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "danger"
	case SeverityWarning:
		return "warning"
	default:
		return "#0066cc"
	}
}

// TelegramChannel sends alerts to Telegram
type TelegramChannel struct {
	name   string
	client *telegram.Client
	chatID int64
}

// NewTelegramChannel creates a new Telegram alert channel
func NewTelegramChannel(name string, client *telegram.Client, chatID int64) *TelegramChannel {
	return &TelegramChannel{
		name:   name,
		client: client,
		chatID: chatID,
	}
}

func (c *TelegramChannel) Name() string { return c.name }
func (c *TelegramChannel) Type() string { return "telegram" }

func (c *TelegramChannel) Send(ctx context.Context, alert *Alert) error {
	text := c.formatMessage(alert)
	chatID := fmt.Sprintf("%d", c.chatID)
	_, err := c.client.SendMessage(ctx, chatID, text, "Markdown")
	return err
}

func (c *TelegramChannel) formatMessage(alert *Alert) string {
	emoji := c.severityEmoji(alert.Severity)
	severityLabel := strings.ToUpper(string(alert.Severity))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *%s ALERT*\n\n", emoji, severityLabel))
	sb.WriteString(fmt.Sprintf("*%s*\n", escapeMarkdown(alert.Title)))
	sb.WriteString(fmt.Sprintf("%s\n\n", escapeMarkdown(alert.Message)))

	sb.WriteString(fmt.Sprintf("📋 *Type:* `%s`\n", alert.Type))

	if alert.Source != "" {
		sb.WriteString(fmt.Sprintf("🔗 *Source:* `%s`\n", alert.Source))
	}

	if alert.ProjectPath != "" {
		sb.WriteString(fmt.Sprintf("📁 *Project:* `%s`\n", alert.ProjectPath))
	}

	sb.WriteString(fmt.Sprintf("\n🕐 %s", alert.CreatedAt.Format(time.RFC822)))

	return sb.String()
}

func (c *TelegramChannel) severityEmoji(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "🚨"
	case SeverityWarning:
		return "⚠️"
	default:
		return "ℹ️"
	}
}

// escapeMarkdown escapes special characters for Telegram MarkdownV2
func escapeMarkdown(text string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

// WebhookChannel sends alerts to a webhook endpoint
type WebhookChannel struct {
	name    string
	url     string
	method  string
	headers map[string]string
	secret  string
	client  *http.Client
}

// NewWebhookChannel creates a new webhook alert channel
func NewWebhookChannel(name string, config *WebhookChannelConfig) *WebhookChannel {
	method := config.Method
	if method == "" {
		method = http.MethodPost
	}

	return &WebhookChannel{
		name:    name,
		url:     config.URL,
		method:  method,
		headers: config.Headers,
		secret:  config.Secret,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *WebhookChannel) Name() string { return c.name }
func (c *WebhookChannel) Type() string { return "webhook" }

func (c *WebhookChannel) Send(ctx context.Context, alert *Alert) error {
	payload, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("failed to marshal alert: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, c.method, c.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Add custom headers
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	// Add HMAC signature if secret is configured
	if c.secret != "" {
		signature := c.sign(payload)
		req.Header.Set("X-Signature-256", "sha256="+signature)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

func (c *WebhookChannel) sign(payload []byte) string {
	h := hmac.New(sha256.New, []byte(c.secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// EmailChannel sends alerts via email
type EmailChannel struct {
	name      string
	sender    EmailSender
	to        []string
	subject   string // Optional template
}

// EmailSender interface for sending emails
type EmailSender interface {
	Send(ctx context.Context, to []string, subject, htmlBody string) error
}

// NewEmailChannel creates a new email alert channel
func NewEmailChannel(name string, sender EmailSender, config *EmailChannelConfig) *EmailChannel {
	return &EmailChannel{
		name:    name,
		sender:  sender,
		to:      config.To,
		subject: config.Subject,
	}
}

func (c *EmailChannel) Name() string { return c.name }
func (c *EmailChannel) Type() string { return "email" }

func (c *EmailChannel) Send(ctx context.Context, alert *Alert) error {
	subject := c.formatSubject(alert)
	body := c.formatBody(alert)

	return c.sender.Send(ctx, c.to, subject, body)
}

func (c *EmailChannel) formatSubject(alert *Alert) string {
	if c.subject != "" {
		// Simple template replacement
		s := c.subject
		s = strings.ReplaceAll(s, "{{severity}}", string(alert.Severity))
		s = strings.ReplaceAll(s, "{{type}}", string(alert.Type))
		s = strings.ReplaceAll(s, "{{title}}", alert.Title)
		return s
	}

	emoji := "🔔"
	switch alert.Severity {
	case SeverityCritical:
		emoji = "🚨"
	case SeverityWarning:
		emoji = "⚠️"
	}

	return fmt.Sprintf("%s [%s] Pilot Alert: %s", emoji, strings.ToUpper(string(alert.Severity)), alert.Title)
}

func (c *EmailChannel) formatBody(alert *Alert) string {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #333; }
.container { max-width: 600px; margin: 0 auto; padding: 20px; }
.alert-box { border-radius: 8px; padding: 20px; margin-bottom: 20px; }
.critical { background: #fee2e2; border-left: 4px solid #dc2626; }
.warning { background: #fef3c7; border-left: 4px solid #f59e0b; }
.info { background: #dbeafe; border-left: 4px solid #3b82f6; }
.title { font-size: 18px; font-weight: 600; margin: 0 0 10px 0; }
.message { margin: 0 0 15px 0; }
.metadata { font-size: 14px; color: #666; }
.metadata dt { font-weight: 600; display: inline; }
.metadata dd { display: inline; margin: 0 15px 0 5px; }
.footer { margin-top: 20px; padding-top: 20px; border-top: 1px solid #eee; font-size: 12px; color: #999; }
code { background: #f3f4f6; padding: 2px 6px; border-radius: 4px; font-family: monospace; }
</style>
</head>
<body>
<div class="container">
`)

	cssClass := "info"
	switch alert.Severity {
	case SeverityCritical:
		cssClass = "critical"
	case SeverityWarning:
		cssClass = "warning"
	}

	sb.WriteString(fmt.Sprintf(`<div class="alert-box %s">`, cssClass))
	sb.WriteString(fmt.Sprintf(`<h2 class="title">%s</h2>`, alert.Title))
	sb.WriteString(fmt.Sprintf(`<p class="message">%s</p>`, alert.Message))

	sb.WriteString(`<dl class="metadata">`)
	sb.WriteString(fmt.Sprintf(`<dt>Type:</dt><dd><code>%s</code></dd>`, alert.Type))

	if alert.Source != "" {
		sb.WriteString(fmt.Sprintf(`<dt>Source:</dt><dd><code>%s</code></dd>`, alert.Source))
	}

	if alert.ProjectPath != "" {
		sb.WriteString(fmt.Sprintf(`<dt>Project:</dt><dd><code>%s</code></dd>`, alert.ProjectPath))
	}

	sb.WriteString(fmt.Sprintf(`<dt>Severity:</dt><dd>%s</dd>`, strings.ToUpper(string(alert.Severity))))
	sb.WriteString(`</dl>`)
	sb.WriteString(`</div>`)

	sb.WriteString(fmt.Sprintf(`<div class="footer">Alert ID: %s<br>Generated at %s</div>`,
		alert.ID, alert.CreatedAt.Format(time.RFC1123)))

	sb.WriteString(`</div></body></html>`)

	return sb.String()
}

// PagerDutyChannel sends alerts to PagerDuty
type PagerDutyChannel struct {
	name       string
	routingKey string
	serviceID  string
	client     *http.Client
}

const pagerDutyEventsAPI = "https://events.pagerduty.com/v2/enqueue"

// NewPagerDutyChannel creates a new PagerDuty alert channel
func NewPagerDutyChannel(name string, config *PagerDutyChannelConfig) *PagerDutyChannel {
	return &PagerDutyChannel{
		name:       name,
		routingKey: config.RoutingKey,
		serviceID:  config.ServiceID,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *PagerDutyChannel) Name() string { return c.name }
func (c *PagerDutyChannel) Type() string { return "pagerduty" }

func (c *PagerDutyChannel) Send(ctx context.Context, alert *Alert) error {
	severity := "warning"
	switch alert.Severity {
	case SeverityCritical:
		severity = "critical"
	case SeverityWarning:
		severity = "warning"
	case SeverityInfo:
		severity = "info"
	}

	event := map[string]interface{}{
		"routing_key":  c.routingKey,
		"event_action": "trigger",
		"dedup_key":    fmt.Sprintf("pilot-%s-%s", alert.Type, alert.Source),
		"payload": map[string]interface{}{
			"summary":        fmt.Sprintf("%s: %s", alert.Title, alert.Message),
			"source":         alert.Source,
			"severity":       severity,
			"timestamp":      alert.CreatedAt.Format(time.RFC3339),
			"component":      "pilot",
			"group":          alert.ProjectPath,
			"class":          string(alert.Type),
			"custom_details": alert.Metadata,
		},
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal PagerDuty event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pagerDutyEventsAPI, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create PagerDuty request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("PagerDuty request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PagerDuty returned status %d", resp.StatusCode)
	}

	return nil
}
