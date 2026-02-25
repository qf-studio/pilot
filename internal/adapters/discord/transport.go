package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// GatewayClient connects to Discord Gateway and handles event streaming.
type GatewayClient struct {
	botToken      string
	intents       int
	conn          *websocket.Conn
	sessionID     string
	seq           *int
	heartbeatTick *time.Ticker
	stopCh        chan struct{}
	mu            sync.Mutex
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
				g.log.Warn("Read event error", slog.Any("error", err))
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
				}
				data, _ := json.Marshal(event.D)
				if err := json.Unmarshal(data, &readyData); err == nil {
					g.mu.Lock()
					g.sessionID = readyData.SessionID
					g.mu.Unlock()
					g.log.Info("Received READY", slog.String("session_id", readyData.SessionID))
				}
			}

			// Handle close codes for reconnection logic
			if event.Op < 0 { // Close frame
				closeCode := event.Op // Simplified, actual close code is in frame
				if closeCode >= CloseCodeUnknownError && closeCode <= CloseCodeSessionTimeout {
					g.log.Info("Reconnectable close code", slog.Int("code", closeCode))
					// Caller should handle reconnection
				} else if closeCode == CloseCodeInvalidToken {
					g.log.Error("Non-resumable close code", slog.Int("code", closeCode))
					return
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

// Close closes the WebSocket connection.
func (g *GatewayClient) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	close(g.stopCh)
	if g.heartbeatTick != nil {
		g.heartbeatTick.Stop()
	}

	if g.conn != nil {
		return g.conn.Close()
	}

	return nil
}
