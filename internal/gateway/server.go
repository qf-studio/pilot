package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// ReadinessChecker is an interface for components that can report their readiness.
// Implement this interface to register health checks with the gateway server.
type ReadinessChecker interface {
	// Name returns a unique identifier for this checker.
	Name() string
	// Ready returns true if the component is ready to accept traffic.
	Ready() bool
}

// livenessState tracks metrics for liveness checks.
type livenessState struct {
	panicCount      atomic.Int64
	lastPanicTime   atomic.Int64 // Unix timestamp
	lastHeartbeat   atomic.Int64 // Unix timestamp
	maxGoroutines   int
	panicWindowSecs int64
}

// AutopilotPRState holds the state of a single PR tracked by autopilot.
// Used by AutopilotProvider to decouple gateway from autopilot package.
type AutopilotPRState struct {
	PRNumber   int
	PRURL      string
	Stage      string
	CIStatus   string
	Error      string
	BranchName string
}

// AutopilotProvider exposes autopilot state to the gateway API.
type AutopilotProvider interface {
	GetEnvironment() string
	GetActivePRs() []*AutopilotPRState
	GetFailureCount() int
	IsAutoReleaseEnabled() bool
}

// Server is the main gateway server handling WebSocket and HTTP connections.
// It provides a control plane for managing Pilot via WebSocket, receives webhooks
// from external services (Linear, GitHub, Jira, Asana), and exposes REST APIs for status
// and task management. Server is safe for concurrent use.
type Server struct {
	config              *Config
	auth                *Authenticator
	sessions            *SessionManager
	router              *Router
	upgrader            websocket.Upgrader
	server              *http.Server
	mu                  sync.RWMutex
	running             bool
	customHandlers      map[string]http.Handler
	githubWebhookSecret string // Secret for GitHub webhook signature validation
	readinessCheckers   []ReadinessChecker
	liveness            *livenessState
	prometheusExporter  *PrometheusExporter
	autopilotProvider   AutopilotProvider
}

// Config holds gateway server configuration including network binding options.
type Config struct {
	// Host is the network interface to bind to (e.g., "127.0.0.1" or "0.0.0.0").
	Host string `yaml:"host"`
	// Port is the TCP port number to listen on.
	Port int `yaml:"port"`
	// GithubWebhookSecret is the secret for GitHub webhook signature validation.
	// If set, incoming GitHub webhooks must have valid HMAC-SHA256 signatures.
	GithubWebhookSecret string `yaml:"-"` // Set programmatically from adapters config
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
		config:              config,
		auth:                auth,
		sessions:            NewSessionManager(),
		router:              NewRouter(),
		customHandlers:      make(map[string]http.Handler),
		githubWebhookSecret: config.GithubWebhookSecret,
		readinessCheckers:   make([]ReadinessChecker, 0),
		liveness: &livenessState{
			maxGoroutines:   1000,
			panicWindowSecs: 300, // 5 minutes
		},
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
	// Initialize heartbeat
	s.liveness.lastHeartbeat.Store(time.Now().Unix())
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
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/live", s.handleLive)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// Protected API endpoints (auth required when configured)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/api/v1/status", s.handleStatus)
	apiMux.HandleFunc("/api/v1/tasks", s.handleTasks)
	apiMux.HandleFunc("/api/v1/autopilot", s.handleAutopilot)

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

	// Register custom handlers
	s.mu.RLock()
	for path, handler := range s.customHandlers {
		mux.Handle(path, handler)
	}
	s.mu.RUnlock()

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

// RegisterHandler registers a custom HTTP handler for a path.
// Must be called before Start(). The handler will be registered when the server starts.
func (s *Server) RegisterHandler(path string, handler http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customHandlers[path] = handler
}

// SetMetricsSource sets the metrics source for the Prometheus /metrics endpoint.
// Must be called before Start().
func (s *Server) SetMetricsSource(source MetricsSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prometheusExporter = NewPrometheusExporter(source)
}

// SetAutopilotProvider sets the autopilot provider for the /api/v1/autopilot endpoint.
// Must be called before Start().
func (s *Server) SetAutopilotProvider(p AutopilotProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.autopilotProvider = p
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

// handleReady returns readiness status for Kubernetes readiness probes.
// Returns 200 when all registered readiness checks pass, 503 otherwise.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Update heartbeat to show main loop is responsive
	s.liveness.lastHeartbeat.Store(time.Now().Unix())

	checks := make(map[string]bool)
	allReady := true

	s.mu.RLock()
	for _, checker := range s.readinessCheckers {
		ready := checker.Ready()
		checks[checker.Name()] = ready
		if !ready {
			allReady = false
		}
	}
	s.mu.RUnlock()

	response := map[string]interface{}{
		"ready":  allReady,
		"checks": checks,
	}

	if !allReady {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(response)
}

// handleLive returns liveness status for Kubernetes liveness probes.
// Returns 200 when the process is alive and not deadlocked, 503 otherwise.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	checks := make(map[string]interface{})
	alive := true

	// Check 1: Goroutine count
	goroutineCount := runtime.NumGoroutine()
	goroutineOK := goroutineCount < s.liveness.maxGoroutines
	checks["goroutines"] = map[string]interface{}{
		"count": goroutineCount,
		"max":   s.liveness.maxGoroutines,
		"ok":    goroutineOK,
	}
	if !goroutineOK {
		alive = false
	}

	// Check 2: Recent panics
	now := time.Now().Unix()
	panicCount := s.liveness.panicCount.Load()
	lastPanic := s.liveness.lastPanicTime.Load()
	recentPanics := lastPanic > 0 && (now-lastPanic) < s.liveness.panicWindowSecs
	panicOK := !recentPanics
	checks["panics"] = map[string]interface{}{
		"count":          panicCount,
		"recent":         recentPanics,
		"window_seconds": s.liveness.panicWindowSecs,
		"ok":             panicOK,
	}
	if !panicOK {
		alive = false
	}

	// Check 3: Main loop responsiveness (heartbeat freshness)
	lastHeartbeat := s.liveness.lastHeartbeat.Load()
	heartbeatAge := now - lastHeartbeat
	heartbeatOK := heartbeatAge < 60 // Less than 60 seconds old
	checks["heartbeat"] = map[string]interface{}{
		"last_seconds_ago": heartbeatAge,
		"ok":               heartbeatOK,
	}
	if !heartbeatOK {
		alive = false
	}

	response := map[string]interface{}{
		"alive":  alive,
		"checks": checks,
	}

	if !alive {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(response)
}

// RegisterReadinessChecker adds a readiness checker to the server.
// Readiness checks are evaluated when /ready endpoint is called.
func (s *Server) RegisterReadinessChecker(checker ReadinessChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readinessCheckers = append(s.readinessCheckers, checker)
}

// RecordPanic records a panic event for liveness tracking.
// Call this from panic recovery handlers to track system health.
func (s *Server) RecordPanic() {
	s.liveness.panicCount.Add(1)
	s.liveness.lastPanicTime.Store(time.Now().Unix())
}

// Heartbeat updates the heartbeat timestamp to show the main loop is responsive.
// Call this periodically from long-running loops.
func (s *Server) Heartbeat() {
	s.liveness.lastHeartbeat.Store(time.Now().Unix())
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

// handleAutopilot returns current autopilot state including active PRs.
func (s *Server) handleAutopilot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	s.mu.RLock()
	provider := s.autopilotProvider
	s.mu.RUnlock()

	if provider == nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"enabled":      false,
			"environment":  "",
			"autoRelease":  false,
			"activePRs":    []interface{}{},
			"failureCount": 0,
		})
		return
	}

	prs := provider.GetActivePRs()
	activePRs := make([]map[string]interface{}, 0, len(prs))
	for _, pr := range prs {
		activePRs = append(activePRs, map[string]interface{}{
			"number":     pr.PRNumber,
			"url":        pr.PRURL,
			"stage":      pr.Stage,
			"ciStatus":   pr.CIStatus,
			"error":      pr.Error,
			"branchName": pr.BranchName,
		})
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":      true,
		"environment":  provider.GetEnvironment(),
		"autoRelease":  provider.IsAutoReleaseEnabled(),
		"activePRs":    activePRs,
		"failureCount": provider.GetFailureCount(),
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

	// Read raw body first (required for HMAC signature validation)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate webhook signature if secret is configured
	if s.githubWebhookSecret != "" {
		if !github.VerifyWebhookSignature(body, signature, s.githubWebhookSecret) {
			logging.WithComponent("gateway").Warn("GitHub webhook signature verification failed",
				slog.String("event_type", eventType))
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
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

// handleMetrics returns metrics in Prometheus text format
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	exporter := s.prometheusExporter
	s.mu.RUnlock()

	if exporter == nil {
		http.Error(w, "Metrics not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := exporter.WritePrometheus(w); err != nil {
		http.Error(w, "Failed to write metrics", http.StatusInternalServerError)
		return
	}
}
