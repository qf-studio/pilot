package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// wsDialer abstracts WebSocket dialing for testing.
type wsDialer interface {
	DialContext(ctx context.Context, url string) (*websocket.Conn, error)
}

// defaultDialer uses gorilla/websocket's default dialer.
type defaultDialer struct{}

func (d defaultDialer) DialContext(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	return conn, err
}

// reconnectBackoff controls delays between reconnection attempts.
const (
	initialReconnectDelay = 1 * time.Second
	maxReconnectDelay     = 30 * time.Second
)

// SocketModeClient connects to Slack's Socket Mode API using an app-level token.
// It handles the initial HTTP handshake to obtain a WebSocket URL, and provides
// Listen() for a full event loop with automatic reconnection.
type SocketModeClient struct {
	appToken   string
	apiURL     string
	httpClient *http.Client
	dialer     wsDialer
	log        *slog.Logger
}

// NewSocketModeClient creates a new Socket Mode client with the given app-level token.
// The token must be an xapp-... app-level token (not a bot token).
func NewSocketModeClient(appToken string) *SocketModeClient {
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     slackAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		dialer:     defaultDialer{},
		log:        logging.WithComponent("slack.socketmode"),
	}
}

// NewSocketModeClientWithBaseURL creates a Socket Mode client with a custom API base URL.
// Used for testing with httptest.NewServer.
func NewSocketModeClientWithBaseURL(appToken, baseURL string) *SocketModeClient {
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		dialer:     defaultDialer{},
		log:        logging.WithComponent("slack.socketmode"),
	}
}

// connectionsOpenResponse is the JSON response from apps.connections.open.
type connectionsOpenResponse struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// ErrAuthFailure indicates the app-level token was rejected by Slack.
var ErrAuthFailure = fmt.Errorf("slack socket mode: authentication failed")

// ErrConnectionOpen indicates a non-auth failure when opening a connection.
var ErrConnectionOpen = fmt.Errorf("slack socket mode: failed to open connection")

// OpenConnection calls apps.connections.open with the app-level token
// and returns the WebSocket URL for event streaming.
func (s *SocketModeClient) OpenConnection(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/apps.connections.open", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.appToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrConnectionOpen, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%w: failed to read response: %w", ErrConnectionOpen, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrConnectionOpen, resp.StatusCode, string(body))
	}

	var result connectionsOpenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("%w: failed to parse response: %w", ErrConnectionOpen, err)
	}

	if !result.OK {
		// Slack returns specific error codes for auth issues
		switch result.Error {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked":
			return "", fmt.Errorf("%w: %s", ErrAuthFailure, result.Error)
		default:
			return "", fmt.Errorf("%w: %s", ErrConnectionOpen, result.Error)
		}
	}

	if result.URL == "" {
		return "", fmt.Errorf("%w: empty WebSocket URL in response", ErrConnectionOpen)
	}

	return result.URL, nil
}

// Listen connects to Slack's Socket Mode WebSocket and emits parsed SocketEvents.
// It automatically reconnects when the server sends a disconnect envelope or the
// connection drops unexpectedly. Listen blocks until ctx is cancelled.
//
// The returned channel is closed when ctx is cancelled and all reconnect attempts
// have stopped. Callers should range over the channel:
//
//	events, err := client.Listen(ctx)
//	for evt := range events {
//	    // handle evt
//	}
func (s *SocketModeClient) Listen(ctx context.Context) (<-chan SocketEvent, error) {
	// Validate by establishing the first connection before returning.
	wssURL, err := s.OpenConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}

	out := make(chan SocketEvent, 64)

	go s.listenLoop(ctx, wssURL, out)

	return out, nil
}

// listenLoop runs the connect → read → reconnect cycle until ctx is cancelled.
func (s *SocketModeClient) listenLoop(ctx context.Context, initialURL string, out chan<- SocketEvent) {
	defer close(out)

	wssURL := initialURL
	delay := initialReconnectDelay

	for {
		if ctx.Err() != nil {
			return
		}

		reconnect := s.runConnection(ctx, wssURL, out)
		if !reconnect || ctx.Err() != nil {
			return
		}

		// Back off before reconnecting.
		s.log.Info("reconnecting to Socket Mode",
			slog.Duration("delay", delay))

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Get a fresh WSS URL for each reconnection.
		newURL, err := s.OpenConnection(ctx)
		if err != nil {
			s.log.Error("failed to get new WSS URL for reconnect",
				slog.Any("error", err))
			// Exponential backoff on handshake failure.
			delay = min(delay*2, maxReconnectDelay)
			continue
		}

		wssURL = newURL
		delay = initialReconnectDelay // reset on successful handshake
	}
}

// runConnection dials the WSS URL, creates a SocketModeHandler, and forwards
// parsed SocketEvents to the output channel. Returns true if the caller should
// reconnect (disconnect envelope or unexpected close), false on clean shutdown.
func (s *SocketModeClient) runConnection(ctx context.Context, wssURL string, out chan<- SocketEvent) (reconnect bool) {
	conn, err := s.dialer.DialContext(ctx, wssURL)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		s.log.Error("failed to dial WebSocket",
			slog.String("url", wssURL),
			slog.Any("error", err))
		return true // retry
	}

	handler, rawEvents := NewSocketModeHandler(conn)

	// Run handler in background — it will close rawEvents when done.
	go handler.Run()

	// Forward events until handler closes the channel or ctx is cancelled.
	shouldReconnect := false
	for {
		select {
		case <-ctx.Done():
			handler.Close()
			// Drain remaining events.
			for range rawEvents {
			}
			return false

		case raw, ok := <-rawEvents:
			if !ok {
				// Handler closed — connection dropped or server closed.
				return shouldReconnect || true
			}

			// Disconnect signals reconnect.
			if raw.Type == SocketEventDisconnect {
				s.log.Info("disconnect received, will reconnect",
					slog.String("envelope_id", raw.EnvelopeID))
				shouldReconnect = true
				continue
			}

			// Parse the raw payload into a high-level SocketEvent.
			// The raw.Payload for events_api type contains the inner event payload.
			evt, err := s.parseRawEvent(raw)
			if err != nil {
				s.log.Warn("failed to parse raw event",
					slog.String("type", string(raw.Type)),
					slog.Any("error", err))
				continue
			}
			if evt == nil {
				continue // non-event envelope (hello, unknown inner type, etc.)
			}

			select {
			case out <- *evt:
			case <-ctx.Done():
				handler.Close()
				for range rawEvents {
				}
				return false
			}
		}
	}
}

// parseRawEvent converts a SocketModeEvent into a SocketEvent.
// Only events_api envelopes produce SocketEvents; other types return nil.
func (s *SocketModeClient) parseRawEvent(raw SocketModeEvent) (*SocketEvent, error) {
	if raw.Type != SocketEventMessage {
		// Interactive, slash_commands, etc. — not yet mapped to SocketEvent.
		return nil, nil
	}

	// raw.Payload is the events_api payload (already unwrapped from envelope by handler).
	return parseEventsAPIPayload(raw.Payload)
}
