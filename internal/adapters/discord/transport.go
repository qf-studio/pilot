package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// GatewayClient connects to Discord Gateway and handles event streaming.
type GatewayClient struct {
	botToken      string
	intents       int
	conn          *websocket.Conn
	sessionID     string
	botUserID     string
	seq           *int
	heartbeatTick *time.Ticker
	stopCh        chan struct{}
	mu            sync.Mutex
	closeOnce     sync.Once
	log           *slog.Logger
}

// NewGatewayClient creates a new Discord Gateway client.
func NewGatewayClient(botToken string, intents int) *GatewayClient {
	return &GatewayClient{
		botToken: botToken,
		intents:  intents,
		stopCh:   make(chan struct{}),
		log:      logging.WithComponent("discord.gateway"),
	}
}

// Connect establishes a WebSocket connection to Discord Gateway.
func (g *GatewayClient) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Get gateway URL
	client := NewClient(g.botToken)
	gatewayURL, err := client.GetGatewayURL(ctx)
	if err != nil {
		return fmt.Errorf("get gateway url: %w", err)
	}

	// Connect to WebSocket
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, gatewayURL+"?v=10&encoding=json", nil)
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}

	g.conn = conn
	g.log.Info("Connected to Discord Gateway")

	// Wait for HELLO and send IDENTIFY
	if err := g.handleHello(ctx); err != nil {
		_ = g.conn.Close()
		g.conn = nil
		return fmt.Errorf("handle hello: %w", err)
	}

	return nil
}

// handleHello receives HELLO opcode and starts heartbeat loop.
func (g *GatewayClient) handleHello(ctx context.Context) error {
	// Set read deadline for HELLO
	deadline := time.Now().Add(10 * time.Second)
	_ = g.conn.SetReadDeadline(deadline)
	defer func() { _ = g.conn.SetReadDeadline(time.Time{}) }()

	var event GatewayEvent
	if err := g.conn.ReadJSON(&event); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}

	if event.Op != OpcodeHello {
		return fmt.Errorf("expected hello opcode %d, got %d", OpcodeHello, event.Op)
	}

	var hello Hello
	data, _ := json.Marshal(event.D)
	if err := json.Unmarshal(data, &hello); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}

	// Send IDENTIFY
	identifyData := IdentifyData{
		Token:   g.botToken,
		Intents: g.intents,
		Properties: map[string]string{
			"os":      "linux",
			"browser": "pilot",
			"device":  "pilot",
		},
	}

	identify := Identify{
		Op: OpcodeIdentify,
		D:  identifyData,
	}

	if err := g.conn.WriteJSON(identify); err != nil {
		return fmt.Errorf("send identify: %w", err)
	}

	g.log.Info("Sent IDENTIFY", slog.Int("heartbeat_interval", hello.HeartbeatInterval))

	// Start heartbeat loop
	g.heartbeatTick = time.NewTicker(time.Duration(hello.HeartbeatInterval) * time.Millisecond)
	go g.heartbeatLoop()

	return nil
}

// heartbeatLoop sends periodic heartbeat messages.
func (g *GatewayClient) heartbeatLoop() {
	defer g.heartbeatTick.Stop()

	for {
		select {
		case <-g.stopCh:
			return
		case <-g.heartbeatTick.C:
			g.mu.Lock()
			if g.conn == nil {
				g.mu.Unlock()
				return
			}

			hb := Heartbeat{
				Op: OpcodeHeartbeat,
				D:  g.seq,
			}

			_ = g.conn.WriteJSON(hb)
			g.mu.Unlock()
		}
	}
}

// isResumableCloseCode returns true for Discord close codes 4000-4009 that allow session resume.
func isResumableCloseCode(code int) bool {
	return code >= CloseCodeUnknownError && code <= CloseCodeSessionTimeout
}

// isFatalCloseCode returns true for close codes that require a full re-identify (no resume).
func isFatalCloseCode(code int) bool {
	return code == CloseCodeAuthenticationFailed || code == CloseCodeInvalidToken
}

// extractCloseCode extracts the WebSocket close code from a *websocket.CloseError.
// Returns 0 if the error is not a close error.
func extractCloseCode(err error) int {
	if closeErr, ok := err.(*websocket.CloseError); ok {
		return closeErr.Code
	}
	return 0
}

// Listen returns a channel of incoming events. Blocks until ctx is cancelled.
func (g *GatewayClient) Listen(ctx context.Context) (<-chan GatewayEvent, error) {
	if g.conn == nil {
		return nil, fmt.Errorf("not connected")
	}

	out := make(chan GatewayEvent, 64)

	go func() {
		defer close(out)

		for {
			select {
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			default:
			}

			var event GatewayEvent
			if err := g.conn.ReadJSON(&event); err != nil {
				// gorilla/websocket surfaces close frames as errors from ReadJSON,
				// not as events. Extract the close code from the error.
				closeCode := extractCloseCode(err)
				if closeCode != 0 {
					if isFatalCloseCode(closeCode) {
						g.log.Error("Fatal close code, cannot reconnect",
							slog.Int("code", closeCode), slog.Any("error", err))
						return
					}
					if isResumableCloseCode(closeCode) {
						g.log.Warn("Resumable close code, reconnecting",
							slog.Int("code", closeCode), slog.Any("error", err))
					}
				} else {
					g.log.Warn("Read event error", slog.Any("error", err))
				}
				return
			}

			// Track sequence number for RESUME
			if event.S != nil {
				g.mu.Lock()
				g.seq = event.S
				g.mu.Unlock()
			}

			// Track session ID on READY
			if event.T != nil && *event.T == "READY" {
				var readyData struct {
					SessionID string `json:"session_id"`
					User      struct {
						ID string `json:"id"`
					} `json:"user"`
				}
				data, _ := json.Marshal(event.D)
				if err := json.Unmarshal(data, &readyData); err == nil {
					g.mu.Lock()
					g.sessionID = readyData.SessionID
					if readyData.User.ID != "" {
						g.botUserID = readyData.User.ID
					}
					g.mu.Unlock()
					g.log.Info("Received READY",
						slog.String("session_id", readyData.SessionID),
						slog.String("bot_user_id", readyData.User.ID))
				}
			}

			select {
			case out <- event:
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			}
		}
	}()

	return out, nil
}

// StartListening connects to the gateway and listens for events with automatic
// reconnection. On resumable close codes (4000-4009) it attempts RESUME with
// exponential backoff. On non-resumable codes it performs a full reconnect.
// Fatal codes (4004, 4014) abort immediately.
func (g *GatewayClient) StartListening(ctx context.Context) (<-chan GatewayEvent, error) {
	out := make(chan GatewayEvent, 64)

	// Initial connect
	if err := g.Connect(ctx); err != nil {
		return nil, fmt.Errorf("initial connect: %w", err)
	}

	go func() {
		defer close(out)

		const (
			minBackoff = 1 * time.Second
			maxBackoff = 60 * time.Second
		)
		backoff := minBackoff

		for {
			events, err := g.Listen(ctx)
			if err != nil {
				g.log.Error("Listen failed", slog.Any("error", err))
				return
			}

			// Forward events to caller
			var lastErr error
		eventLoop:
			for {
				select {
				case <-ctx.Done():
					return
				case <-g.stopCh:
					return
				case evt, ok := <-events:
					if !ok {
						break eventLoop
					}
					// Reset backoff on successful event
					backoff = minBackoff

					select {
					case out <- evt:
					case <-ctx.Done():
						return
					case <-g.stopCh:
						return
					}
				}
			}

			// Channel closed — check if we should reconnect
			select {
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			default:
			}

			// Determine reconnection strategy from the last read error
			g.mu.Lock()
			canResume := g.sessionID != "" && g.seq != nil
			// Close the old connection
			if g.conn != nil {
				_ = g.conn.Close()
				g.conn = nil
			}
			if g.heartbeatTick != nil {
				g.heartbeatTick.Stop()
			}
			g.mu.Unlock()

			g.log.Info("Reconnecting to Discord Gateway",
				slog.Duration("backoff", backoff),
				slog.Bool("resume", canResume),
				slog.Any("last_error", lastErr))

			select {
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			case <-time.After(backoff):
			}

			// Exponential backoff
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}

			// Attempt reconnect
			if err := g.Connect(ctx); err != nil {
				g.log.Error("Reconnect failed", slog.Any("error", err))
				continue
			}

			// If we have session state, attempt RESUME
			if canResume {
				if err := g.Resume(ctx); err != nil {
					g.log.Warn("Resume failed, will re-identify on next connect",
						slog.Any("error", err))
					g.mu.Lock()
					g.sessionID = ""
					g.seq = nil
					g.mu.Unlock()
				}
			}
		}
	}()

	return out, nil
}

// Resume attempts to resume the session.
func (g *GatewayClient) Resume(ctx context.Context) error {
	g.mu.Lock()
	if g.conn == nil || g.sessionID == "" || g.seq == nil {
		g.mu.Unlock()
		return fmt.Errorf("cannot resume: missing session")
	}

	resume := Resume{
		Op: OpcodeResume,
		D: ResumeData{
			Token:     g.botToken,
			SessionID: g.sessionID,
			Seq:       *g.seq,
		},
	}
	g.mu.Unlock()

	if err := g.conn.WriteJSON(resume); err != nil {
		return fmt.Errorf("send resume: %w", err)
	}

	g.log.Info("Sent RESUME", slog.String("session_id", g.sessionID))
	return nil
}

// BotUserID returns the bot's user ID extracted from the READY event.
func (g *GatewayClient) BotUserID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.botUserID
}

// Close closes the WebSocket connection. Safe to call multiple times.
func (g *GatewayClient) Close() error {
	var closeErr error
	g.closeOnce.Do(func() {
		g.mu.Lock()
		defer g.mu.Unlock()

		close(g.stopCh)
		if g.heartbeatTick != nil {
			g.heartbeatTick.Stop()
		}

		if g.conn != nil {
			closeErr = g.conn.Close()
		}
	})
	return closeErr
}
