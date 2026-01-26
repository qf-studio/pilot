package briefs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
)

// DeliveryService orchestrates brief delivery to configured channels
type DeliveryService struct {
	config       *BriefConfig
	slackClient  *slack.Client
	emailSender  EmailSender
	logger       *slog.Logger
	slackFmt     *SlackFormatter
	emailFmt     *EmailFormatter
	plainFmt     *PlainTextFormatter
}

// EmailSender interface for sending emails
type EmailSender interface {
	Send(ctx context.Context, to []string, subject, htmlBody string) error
}

// DeliveryOption configures the delivery service
type DeliveryOption func(*DeliveryService)

// WithSlackClient sets the Slack client
func WithSlackClient(client *slack.Client) DeliveryOption {
	return func(d *DeliveryService) {
		d.slackClient = client
	}
}

// WithEmailSender sets the email sender
func WithEmailSender(sender EmailSender) DeliveryOption {
	return func(d *DeliveryService) {
		d.emailSender = sender
	}
}

// WithLogger sets the logger
func WithLogger(logger *slog.Logger) DeliveryOption {
	return func(d *DeliveryService) {
		d.logger = logger
	}
}

// NewDeliveryService creates a new delivery service
func NewDeliveryService(config *BriefConfig, opts ...DeliveryOption) *DeliveryService {
	d := &DeliveryService{
		config:   config,
		logger:   slog.Default(),
		slackFmt: NewSlackFormatter(),
		emailFmt: NewEmailFormatter(),
		plainFmt: NewPlainTextFormatter(),
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// DeliverAll sends the brief to all configured channels
func (d *DeliveryService) DeliverAll(ctx context.Context, brief *Brief) []DeliveryResult {
	results := make([]DeliveryResult, 0, len(d.config.Channels))

	for _, channel := range d.config.Channels {
		var result DeliveryResult
		result.Channel = fmt.Sprintf("%s:%s", channel.Type, channel.Channel)
		result.SentAt = time.Now()

		switch channel.Type {
		case "slack":
			result = d.deliverSlack(ctx, brief, channel)
		case "email":
			result = d.deliverEmail(ctx, brief, channel)
		default:
			result.Success = false
			result.Error = fmt.Errorf("unsupported channel type: %s", channel.Type)
		}

		results = append(results, result)
	}

	return results
}

// deliverSlack sends brief to a Slack channel
func (d *DeliveryService) deliverSlack(ctx context.Context, brief *Brief, channel ChannelConfig) DeliveryResult {
	result := DeliveryResult{
		Channel: fmt.Sprintf("slack:%s", channel.Channel),
		SentAt:  time.Now(),
	}

	if d.slackClient == nil {
		result.Success = false
		result.Error = fmt.Errorf("slack client not configured")
		return result
	}

	// Format as Slack blocks
	blocks := d.slackFmt.SlackBlocks(brief)

	// Convert blocks to slack.Block format
	slackBlocks := make([]slack.Block, 0, len(blocks))
	for _, b := range blocks {
		slackBlock := slack.Block{
			Type: b["type"].(string),
		}

		if text, ok := b["text"].(map[string]interface{}); ok {
			slackBlock.Text = &slack.TextObject{
				Type: text["type"].(string),
				Text: text["text"].(string),
			}
		}

		if elements, ok := b["elements"].([]map[string]interface{}); ok {
			slackBlock.Elements = make([]slack.TextObject, 0, len(elements))
			for _, elem := range elements {
				slackBlock.Elements = append(slackBlock.Elements, slack.TextObject{
					Type: elem["type"].(string),
					Text: elem["text"].(string),
				})
			}
		}

		slackBlocks = append(slackBlocks, slackBlock)
	}

	msg := &slack.Message{
		Channel: channel.Channel,
		Blocks:  slackBlocks,
	}

	resp, err := d.slackClient.PostMessage(ctx, msg)
	if err != nil {
		result.Success = false
		result.Error = err
		d.logger.Error("failed to deliver brief to Slack",
			"channel", channel.Channel,
			"error", err,
		)
		return result
	}

	result.Success = true
	result.MessageID = resp.TS
	d.logger.Info("brief delivered to Slack",
		"channel", channel.Channel,
		"message_ts", resp.TS,
	)

	return result
}

// deliverEmail sends brief via email
func (d *DeliveryService) deliverEmail(ctx context.Context, brief *Brief, channel ChannelConfig) DeliveryResult {
	result := DeliveryResult{
		Channel: "email",
		SentAt:  time.Now(),
	}

	if d.emailSender == nil {
		result.Success = false
		result.Error = fmt.Errorf("email sender not configured")
		return result
	}

	if len(channel.Recipients) == 0 {
		result.Success = false
		result.Error = fmt.Errorf("no email recipients configured")
		return result
	}

	// Format as HTML
	htmlBody, err := d.emailFmt.Format(brief)
	if err != nil {
		result.Success = false
		result.Error = fmt.Errorf("failed to format email: %w", err)
		return result
	}

	subject := d.emailFmt.Subject(brief)

	err = d.emailSender.Send(ctx, channel.Recipients, subject, htmlBody)
	if err != nil {
		result.Success = false
		result.Error = err
		d.logger.Error("failed to deliver brief via email",
			"recipients", channel.Recipients,
			"error", err,
		)
		return result
	}

	result.Success = true
	d.logger.Info("brief delivered via email",
		"recipients", channel.Recipients,
	)

	return result
}

// Deliver sends brief to a specific channel by name
func (d *DeliveryService) Deliver(ctx context.Context, brief *Brief, channelName string) (DeliveryResult, error) {
	for _, channel := range d.config.Channels {
		identifier := fmt.Sprintf("%s:%s", channel.Type, channel.Channel)
		if identifier == channelName || channel.Channel == channelName {
			switch channel.Type {
			case "slack":
				return d.deliverSlack(ctx, brief, channel), nil
			case "email":
				return d.deliverEmail(ctx, brief, channel), nil
			default:
				return DeliveryResult{}, fmt.Errorf("unsupported channel type: %s", channel.Type)
			}
		}
	}

	return DeliveryResult{}, fmt.Errorf("channel not found: %s", channelName)
}
