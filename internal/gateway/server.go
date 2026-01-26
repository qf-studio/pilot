package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Server is the main gateway server handling WebSocket and HTTP connections
type Server struct {
	config   *Config
	sessions *SessionManager
	router   *Router
	upgrader websocket.Upgrader
	server   *http.Server
	mu       sync.RWMutex
	running  bool
}

// Config holds gateway configuration
type Config struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// NewServer creates a new gateway server
func NewServer(config *Config) *Server {
	s := &Server{
		config:   config,
		sessions: NewSessionManager(),
		router:   NewRouter(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // TODO: Implement origin checking
			},
		},
	}
	return s
}

// Start starts the gateway server
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("server already running")
	}
	s.running = true
	s.mu.Unlock()

	mux := http.NewServeMux()

	// WebSocket endpoint for control plane
	mux.HandleFunc("/ws", s.handleWebSocket)

	// HTTP endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/tasks", s.handleTasks)

	// Webhook endpoints for adapters
	mux.HandleFunc("/webhooks/linear", s.handleLinearWebhook)
	mux.HandleFunc("/webhooks/github", s.handleGithubWebhook)

	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Gateway starting on %s", addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.running = false
	return s.server.Shutdown(ctx)
}

// handleWebSocket handles WebSocket connections for the control plane
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	session := s.sessions.Create(conn)
	defer s.sessions.Remove(session.ID)

	log.Printf("New WebSocket session: %s", session.ID)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		s.router.HandleMessage(session, message)
	}
}

// handleHealth returns server health status
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
	})
}

// handleStatus returns current Pilot status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"version":  "0.1.0",
		"running":  s.running,
		"sessions": s.sessions.Count(),
	})
}

// handleTasks returns current tasks
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// TODO: Return actual tasks
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": []interface{}{},
	})
}

// Router returns the server's router
func (s *Server) Router() *Router {
	return s.router
}

// handleLinearWebhook receives webhooks from Linear
func (s *Server) handleLinearWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	log.Printf("Received Linear webhook: %v", payload["type"])

	// Route to Linear adapter
	s.router.HandleWebhook("linear", payload)

	w.WriteHeader(http.StatusOK)
}

// handleGithubWebhook receives webhooks from GitHub
func (s *Server) handleGithubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GitHub sends event type in header
	eventType := r.Header.Get("X-GitHub-Event")
	signature := r.Header.Get("X-Hub-Signature-256")

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Add metadata to payload for handler
	payload["_event_type"] = eventType
	payload["_signature"] = signature

	log.Printf("Received GitHub webhook: %s", eventType)

	// Route to GitHub adapter
	s.router.HandleWebhook("github", payload)

	w.WriteHeader(http.StatusOK)
}
