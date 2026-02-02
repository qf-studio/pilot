package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// InteractionHandler handles Slack interactive component callbacks
type InteractionHandler struct {
	signingSecret string
	log           *slog.Logger
	onAction      func(action *InteractionAction) bool
}

// InteractionPayload represents a Slack interaction webhook payload
type InteractionPayload struct {
	Type        string                 `json:"type"`
	Token       string                 `json:"token"`
	ActionTS    string                 `json:"action_ts"`
	Team        *InteractionTeam       `json:"team"`
	User        *InteractionUser       `json:"user"`
	Channel     *InteractionChannel    `json:"channel"`
	Message     *InteractionMessage    `json:"message"`
	ResponseURL string                 `json:"response_url"`
	TriggerID   string                 `json:"trigger_id"`
	Actions     []InteractionActionDef `json:"actions"`
}

// InteractionTeam represents the team in an interaction
type InteractionTeam struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

// InteractionUser represents the user who triggered the interaction
type InteractionUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	TeamID   string `json:"team_id"`
}

// InteractionChannel represents the channel where the interaction occurred
type InteractionChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InteractionMessage represents the original message
type InteractionMessage struct {
	Type     string `json:"type"`
	TS       string `json:"ts"`
	Text     string `json:"text"`
	BotID    string `json:"bot_id,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
	Username string `json:"username,omitempty"`
}

// InteractionActionDef represents a single action in the interaction
type InteractionActionDef struct {
	Type     string `json:"type"`
	ActionID string `json:"action_id"`
	BlockID  string `json:"block_id"`
	Value    string `json:"value"`
	ActionTS string `json:"action_ts"`
}

// InteractionAction is a simplified action passed to handlers
type InteractionAction struct {
	ActionID    string
	Value       string
	UserID      string
	Username    string
	ChannelID   string
	MessageTS   string
	ResponseURL string
}

// NewInteractionHandler creates a new Slack interaction handler
func NewInteractionHandler(signingSecret string) *InteractionHandler {
	return &InteractionHandler{
		signingSecret: signingSecret,
		log:           logging.WithComponent("slack.webhook"),
	}
}

// OnAction registers a callback for button actions
// The callback should return true if the action was handled
func (h *InteractionHandler) OnAction(callback func(action *InteractionAction) bool) {
	h.onAction = callback
}

// ServeHTTP handles incoming Slack interaction webhooks
func (h *InteractionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error("Failed to read request body", slog.Any("error", err))
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	// Verify signature if signing secret is configured
	if h.signingSecret != "" {
		timestamp := r.Header.Get("X-Slack-Request-Timestamp")
		signature := r.Header.Get("X-Slack-Signature")

		if !h.verifySignature(timestamp, string(body), signature) {
			h.log.Warn("Invalid Slack signature")
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}
	}

	// Parse URL-encoded payload
	values, err := url.ParseQuery(string(body))
	if err != nil {
		h.log.Error("Failed to parse form data", slog.Any("error", err))
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	payloadStr := values.Get("payload")
	if payloadStr == "" {
		h.log.Error("Missing payload in request")
		http.Error(w, "Missing payload", http.StatusBadRequest)
		return
	}

	// Parse JSON payload
	var payload InteractionPayload
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		h.log.Error("Failed to parse payload", slog.Any("error", err))
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// Handle block_actions type (button clicks)
	if payload.Type != "block_actions" {
		h.log.Debug("Ignoring non-block_actions interaction", slog.String("type", payload.Type))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Process actions
	for _, actionDef := range payload.Actions {
		action := &InteractionAction{
			ActionID:    actionDef.ActionID,
			Value:       actionDef.Value,
			ResponseURL: payload.ResponseURL,
		}

		if payload.User != nil {
			action.UserID = payload.User.ID
			action.Username = payload.User.Username
			if action.Username == "" {
				action.Username = payload.User.Name
			}
		}

		if payload.Channel != nil {
			action.ChannelID = payload.Channel.ID
		}

		if payload.Message != nil {
			action.MessageTS = payload.Message.TS
		}

		h.log.Debug("Processing interaction",
			slog.String("action_id", action.ActionID),
			slog.String("value", action.Value),
			slog.String("user", action.Username))

		if h.onAction != nil {
			h.onAction(action)
		}
	}

	// Respond with 200 OK
	w.WriteHeader(http.StatusOK)
}

// verifySignature verifies the Slack request signature
func (h *InteractionHandler) verifySignature(timestamp, body, signature string) bool {
	// Check timestamp to prevent replay attacks (allow 5 minute window)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}

	now := time.Now().Unix()
	if abs(now-ts) > 60*5 {
		return false
	}

	// Compute expected signature
	baseString := fmt.Sprintf("v0:%s:%s", timestamp, body)
	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte(baseString))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expected), []byte(signature))
}

// abs returns the absolute value of an int64
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// ApprovalConfig holds Slack approval-specific configuration
type ApprovalConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Channel       string `yaml:"channel"`
	SigningSecret string `yaml:"signing_secret"`
}

// DefaultApprovalConfig returns default Slack approval configuration
func DefaultApprovalConfig() *ApprovalConfig {
	return &ApprovalConfig{
		Enabled: false,
		Channel: "#pilot-approvals",
	}
}

// SlackClientAdapter adapts the Slack Client to the approval.SlackClient interface
type SlackClientAdapter struct {
	client *Client
}

// NewSlackClientAdapter creates a new adapter
func NewSlackClientAdapter(client *Client) *SlackClientAdapter {
	return &SlackClientAdapter{client: client}
}

// PostInteractiveMessage implements approval.SlackClient
func (a *SlackClientAdapter) PostInteractiveMessage(ctx context.Context, msg *SlackApprovalMessage) (*SlackApprovalResponse, error) {
	resp, err := a.client.PostInteractiveMessage(ctx, &InteractiveMessage{
		Channel: msg.Channel,
		Text:    msg.Text,
		Blocks:  msg.Blocks,
	})
	if err != nil {
		return nil, err
	}
	return &SlackApprovalResponse{
		OK:      resp.OK,
		TS:      resp.TS,
		Channel: resp.Channel,
		Error:   resp.Error,
	}, nil
}

// UpdateInteractiveMessage implements approval.SlackClient
func (a *SlackClientAdapter) UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error {
	return a.client.UpdateInteractiveMessage(ctx, channel, ts, blocks, text)
}

// SlackApprovalMessage mirrors the approval package's message type
type SlackApprovalMessage struct {
	Channel string        `json:"channel"`
	Text    string        `json:"text,omitempty"`
	Blocks  []interface{} `json:"blocks,omitempty"`
}

// SlackApprovalResponse mirrors the approval package's response type
type SlackApprovalResponse struct {
	OK      bool   `json:"ok"`
	TS      string `json:"ts"`
	Channel string `json:"channel"`
	Error   string `json:"error,omitempty"`
}
