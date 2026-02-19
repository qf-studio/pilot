package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/gorilla/websocket"
)

const (
	// wsPingInterval is the interval between ping frames sent to the client.
	wsPingInterval = 30 * time.Second
	// wsPongTimeout is how long to wait for a pong response before closing.
	wsPongTimeout = 10 * time.Second
	// wsWriteTimeout is the deadline for writing a message to the client.
	wsWriteTimeout = 5 * time.Second
	// wsInitialLogCount is the number of historical log entries sent on connect.
	wsInitialLogCount = 50
)

// LogStreamStore provides log subscription capabilities for WebSocket streaming.
type LogStreamStore interface {
	GetRecentLogs(limit int) ([]*memory.LogEntry, error)
	SubscribeLogs() chan *memory.LogEntry
	UnsubscribeLogs(ch chan *memory.LogEntry)
}

// SetLogStreamStore configures the store used by the dashboard WebSocket endpoint.
func (s *Server) SetLogStreamStore(store LogStreamStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logStreamStore = store
}

// handleDashboardWebSocket upgrades the connection to WebSocket and streams
// log entries in real-time. On connect it sends the last 50 entries as an
// initial payload, then pushes new entries as they arrive.
func (s *Server) handleDashboardWebSocket(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	store := s.logStreamStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "log stream store not configured", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logging.WithComponent("gateway").Error("dashboard WS upgrade error", slog.Any("error", err))
		return
	}

	log := logging.WithComponent("gateway")
	log.Info("dashboard WebSocket connected", slog.String("remote", r.RemoteAddr))

	// Subscribe to new log entries before fetching history to avoid gaps.
	sub := store.SubscribeLogs()
	defer store.UnsubscribeLogs(sub)

	// Send initial batch of recent log entries.
	if err := sendInitialLogs(conn, store); err != nil {
		log.Warn("dashboard WS initial send failed", slog.Any("error", err))
		_ = conn.Close()
		return
	}

	// Set up pong handler for keepalive.
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))
	})
	_ = conn.SetReadDeadline(time.Now().Add(wsPingInterval + wsPongTimeout))

	// Read pump: drain client messages (none expected) and detect disconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
					log.Warn("dashboard WS read error", slog.Any("error", err))
				}
				return
			}
		}
	}()

	// Write pump: stream new entries and send pings.
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case entry, ok := <-sub:
			if !ok {
				return
			}
			resp := logEntryResponse{
				Ts:        entry.Timestamp.Format("15:04:05"),
				Level:     entry.Level,
				Message:   entry.Message,
				Component: entry.Component,
			}
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteJSON(resp); err != nil {
				log.Debug("dashboard WS write error", slog.Any("error", err))
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// sendInitialLogs sends the last N log entries to the newly connected client.
func sendInitialLogs(conn *websocket.Conn, store LogStreamStore) error {
	entries, err := store.GetRecentLogs(wsInitialLogCount)
	if err != nil {
		return err
	}

	// GetRecentLogs returns DESC order; reverse for chronological display.
	result := make([]logEntryResponse, len(entries))
	for i, e := range entries {
		result[len(entries)-1-i] = logEntryResponse{
			Ts:        e.Timestamp.Format("15:04:05"),
			Level:     e.Level,
			Message:   e.Message,
			Component: e.Component,
		}
	}

	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	msg, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, msg)
}
