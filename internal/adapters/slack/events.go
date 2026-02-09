package slack

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Socket Mode event types
const (
	EventTypeMessage    = "message"
	EventTypeAppMention = "app_mention"
)

// Socket Mode envelope types
const (
	EnvelopeTypeEventsAPI  = "events_api"
	EnvelopeTypeDisconnect = "disconnect"
	EnvelopeTypeHello      = "hello"
)

// SocketEvent represents a parsed Slack event from Socket Mode.
// It is the normalized output of parseEnvelope() — downstream consumers
// work with this struct instead of raw JSON.
type SocketEvent struct {
	Type      string      // "message" or "app_mention"
	ChannelID string      // Channel where the event occurred
	UserID    string      // User who sent the message
	Text      string      // Message text (with bot mention stripped for app_mention)
	ThreadTS  string      // Parent thread timestamp (empty if not in thread)
	Timestamp string      // Message timestamp
	BotID     string      // Non-empty if message was sent by a bot
	Files     []SlackFile // File attachments (images, documents, etc.)
}

// SlackFile represents a file attachment in a Slack message.
type SlackFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	URL      string `json:"url_private"`
	Size     int    `json:"size"`
}

// socketEnvelope is the top-level Socket Mode WebSocket message.
type socketEnvelope struct {
	EnvelopeID string          `json:"envelope_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	// Disconnect-specific fields
	Reason string `json:"reason,omitempty"`
}

// eventsAPIPayload wraps the Events API callback inside a Socket Mode envelope.
type eventsAPIPayload struct {
	Token  string         `json:"token"`
	TeamID string         `json:"team_id"`
	Event  json.RawMessage `json:"event"`
	Type   string         `json:"type"` // "event_callback"
}

// innerEvent is the raw event object inside eventsAPIPayload.Event.
type innerEvent struct {
	Type      string     `json:"type"`
	Channel   string     `json:"channel"`
	User      string     `json:"user"`
	Text      string     `json:"text"`
	ThreadTS  string     `json:"thread_ts,omitempty"`
	TS        string     `json:"ts"`
	BotID     string     `json:"bot_id,omitempty"`
	Files     []SlackFile `json:"files,omitempty"`
}

// botMentionRegex matches <@UBOTID> patterns in Slack message text.
// Slack encodes user mentions as <@U...> in the raw text.
var botMentionRegex = regexp.MustCompile(`<@U[A-Z0-9]+>\s*`)

// parseEnvelope parses a raw Socket Mode WebSocket message into a SocketEvent.
// It returns the envelope ID (needed for acknowledgment), the parsed event
// (nil for non-event envelopes like hello/disconnect), and any error.
func parseEnvelope(data []byte) (envelopeID string, event *SocketEvent, err error) {
	var env socketEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	envelopeID = env.EnvelopeID

	switch env.Type {
	case EnvelopeTypeEventsAPI:
		evt, err := parseEventsAPIPayload(env.Payload)
		if err != nil {
			return envelopeID, nil, fmt.Errorf("parse events_api payload: %w", err)
		}
		return envelopeID, evt, nil

	case EnvelopeTypeDisconnect:
		// Disconnect envelopes signal the client to reconnect.
		// Return nil event — caller handles reconnect logic.
		return envelopeID, nil, nil

	case EnvelopeTypeHello:
		// Hello is sent on connection open. No event to process.
		return envelopeID, nil, nil

	default:
		// Unknown envelope type — not an error, just nothing to process.
		return envelopeID, nil, nil
	}
}

// parseEventsAPIPayload extracts a SocketEvent from an events_api payload.
func parseEventsAPIPayload(raw json.RawMessage) (*SocketEvent, error) {
	var payload eventsAPIPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal events_api payload: %w", err)
	}

	var inner innerEvent
	if err := json.Unmarshal(payload.Event, &inner); err != nil {
		return nil, fmt.Errorf("unmarshal inner event: %w", err)
	}

	// Only process message and app_mention events
	switch inner.Type {
	case EventTypeMessage, EventTypeAppMention:
		// OK — continue
	default:
		return nil, nil
	}

	evt := &SocketEvent{
		Type:      inner.Type,
		ChannelID: inner.Channel,
		UserID:    inner.User,
		Text:      inner.Text,
		ThreadTS:  inner.ThreadTS,
		Timestamp: inner.TS,
		BotID:     inner.BotID,
		Files:     inner.Files,
	}

	// Strip <@BOTID> mention prefix from app_mention text
	if evt.Type == EventTypeAppMention {
		evt.Text = stripBotMention(evt.Text)
	}

	return evt, nil
}

// stripBotMention removes all <@U...> mention patterns from text and trims whitespace.
func stripBotMention(text string) string {
	cleaned := botMentionRegex.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned)
}

// IsBotMessage returns true if the event was sent by a bot (including self).
// Use this to filter out the bot's own messages and avoid feedback loops.
func (e *SocketEvent) IsBotMessage() bool {
	return e.BotID != ""
}

// IsFromBot returns true if the event's BotID matches the given bot ID.
// This is useful for specifically filtering out self-messages when
// the bot's own ID is known.
func (e *SocketEvent) IsFromBot(botID string) bool {
	return e.BotID == botID
}
