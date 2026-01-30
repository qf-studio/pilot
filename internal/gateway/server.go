package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// Server is the main gateway server handling WebSocket and HTTP connections.
// It provides a control plane for managing Pilot via WebSocket, receives webhooks
// from external services (Linear, GitHub, Jira, Asana), and exposes REST APIs for status
// and task management. Server is safe for concurrent use.
type Server struct {
	config   *Config
	auth     *Authenticator
	sessions *SessionManager
	router   *Router
	upgrader websocket.Upgrader
	server   *http.Server
	mu       sync.RWMutex
	running  bool
}

// Config holds gateway server configuration including network binding options.
type Config struct {
	// Host is the network interface to bind to (e.g., "127.0.0.1" or "0.0.0.0").
	Host string `yaml:"host"`
	// Port is the TCP port number to listen on.
	Port int `yaml:"port"`
}

// localhostPrefixes are the allowed origin prefixes for localhost connections.
// Origins must match exactly or be followed by a port (e.g., ":3000").
var localhostPrefixes = []string{
	"http://localhost",
	"http://127.0.0.1",
	"https://localhost",
	"https://127.0.0.1",
}

// isLocalhost checks if the origin is a valid localhost origin.
// Returns true for origins like "http://localhost", "http://localhost:3000",
// but false for "http://localhost.evil.com" (subdomain attack).
func isLocalhost(origin string) bool {
	for _, prefix := range localhostPrefixes {
		if origin == prefix {
			return true
		}
		// Check for port suffix (must start with ":")
		if strings.HasPrefix(origin, prefix+":") {
			return true
		}
	}
	return false
}

// NewServer creates a new gateway server with the given configuration.
// The server is not started until Start is called.
func NewServer(config *Config) *Server {
	return NewServerWithAuth(config, nil)
}

// NewServerWithAuth creates a new gateway server with the given configuration
// and authentication config. Protected API routes will require authentication.
func NewServerWithAuth(config *Config, authConfig *AuthConfig) *Server {
	var auth *Authenticator
	if authConfig != nil {
		auth = NewAuthenticator(authConfig)
	}

	s := &Server{
		config:   config,
		auth:     auth,
		sessions: NewSessionManager(),
		router:   NewRouter(),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				// Allow requests with no origin (same-origin, CLI tools, etc.)
				if origin == "" {
					return true
				}
				// Allow localhost origins for development
				// Check for exact match or with port (e.g., :3000)
				if isLocalhost(origin) {
					return true
				}
				// Reject all non-localhost origins for security
				// External sites cannot establish WebSocket connections
				return false
			},
		},
	}
	return s
}

// Start starts the gateway server and blocks until the context is cancelled
// or an error occurs. It sets up WebSocket, REST API, and webhook endpoints.
// Returns an error if the server fails to start or is already running.
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

	// Public endpoints (no auth required)
	mux.HandleFunc("/health", s.handleHealth)

	// Protected API endpoints (auth required when configured)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/v1/status", s.handleStatus)
	apiMux.HandleFunc("/api/v1/tasks", s.handleTasks)

	// Apply auth middleware to API routes
	if s.auth != nil {
		mux.Handle("/api/v1/", s.auth.Middleware(apiMux))
	} else {
		mux.Handle("/api/v1/", apiMux)
	}

	// Webhook endpoints for adapters (use signature validation, not bearer tokens)
	mux.HandleFunc("/webhooks/linear", s.handleLinearWebhook)
	mux.HandleFunc("/webhooks/github", s.handleGithubWebhook)
	mux.HandleFunc("/webhooks/gitlab", s.handleGitlabWebhook)
	mux.HandleFunc("/webhooks/jira", s.handleJiraWebhook)
	mux.HandleFunc("/webhooks/asana", s.handleAsanaWebhook)

	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logging.WithComponent("gateway").Info("Gateway starting", slog.String("addr", addr))

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

// Shutdown gracefully shuts down the server with a 30-second timeout.
// It waits for active connections to complete before returning.
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
		logging.WithComponent("gateway").Error("WebSocket upgrade error", slog.Any("error", err))
		return
	}

	session := s.sessions.Create(conn)
	defer s.sessions.Remove(session.ID)

	logging.WithComponent("gateway").Info("New WebSocket session", slog.String("session_id", session.ID))

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logging.WithComponent("gateway").Warn("WebSocket error", slog.Any("error", err))
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
	// Return placeholder for now - tasks would come from executor/memory integration
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"tasks": []interface{}{},
	})
}

// Router returns the server's message router for registering handlers.
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

	logging.WithComponent("gateway").Info("Received Linear webhook", slog.Any("type", payload["type"]))

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

	logging.WithComponent("gateway").Info("Received GitHub webhook", slog.String("event_type", eventType))

	// Route to GitHub adapter
	s.router.HandleWebhook("github", payload)

	w.WriteHeader(http.StatusOK)
}

// handleJiraWebhook receives webhooks from Jira
func (s *Server) handleJiraWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Jira may send signature in header (if configured)
	signature := r.Header.Get("X-Hub-Signature")

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Add metadata to payload for handler
	payload["_signature"] = signature

	webhookEvent, _ := payload["webhookEvent"].(string)
	logging.WithComponent("gateway").Info("Received Jira webhook", slog.String("event", webhookEvent))

	// Route to Jira adapter
	s.router.HandleWebhook("jira", payload)

	w.WriteHeader(http.StatusOK)
}

// handleGitlabWebhook receives webhooks from GitLab
func (s *Server) handleGitlabWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GitLab sends event type in header and uses simple token auth
	eventType := r.Header.Get("X-Gitlab-Event")
	token := r.Header.Get("X-Gitlab-Token")

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Add metadata to payload for handler
	payload["_event_type"] = eventType
	payload["_token"] = token

	logging.WithComponent("gateway").Info("Received GitLab webhook", slog.String("event_type", eventType))

	// Route to GitLab adapter
	s.router.HandleWebhook("gitlab", payload)

	w.WriteHeader(http.StatusOK)
}

// handleAsanaWebhook receives webhooks from Asana
func (s *Server) handleAsanaWebhook(w http.ResponseWriter, r *http.Request) {
	// Asana webhook handshake: respond with X-Hook-Secret header
	if hookSecret := r.Header.Get("X-Hook-Secret"); hookSecret != "" {
		logging.WithComponent("gateway").Info("Received Asana webhook handshake")
		w.Header().Set("X-Hook-Secret", hookSecret)
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Asana sends signature in X-Hook-Signature header
	signature := r.Header.Get("X-Hook-Signature")

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Add metadata to payload for handler
	payload["_signature"] = signature

	logging.WithComponent("gateway").Info("Received Asana webhook")

	// Route to Asana adapter
	s.router.HandleWebhook("asana", payload)

	w.WriteHeader(http.StatusOK)
}
