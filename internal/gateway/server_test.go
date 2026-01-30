package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewServer(t *testing.T) {
	config := &Config{
		Host: "127.0.0.1",
		Port: 9090,
	}

	server := NewServer(config)

	if server == nil {
		t.Fatal("NewServer returned nil")
	}
	if server.config != config {
		t.Error("Server config not set correctly")
	}
	if server.sessions == nil {
		t.Error("Sessions manager not initialized")
	}
	if server.router == nil {
		t.Error("Router not initialized")
	}
}

func TestHealthEndpoint(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got '%s'", response["status"])
	}
}

func TestStatusEndpoint(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()

	server.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response["version"] != "0.1.0" {
		t.Errorf("Expected version '0.1.0', got '%v'", response["version"])
	}
}

func TestLinearWebhook(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	// Test invalid method
	req := httptest.NewRequest(http.MethodGet, "/webhooks/linear", nil)
	w := httptest.NewRecorder()
	server.handleLinearWebhook(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405 for GET, got %d", w.Code)
	}

	// Test valid POST
	payload := `{"action": "create", "type": "Issue", "data": {}}`
	req = httptest.NewRequest(http.MethodPost, "/webhooks/linear", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	server.handleLinearWebhook(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for POST, got %d", w.Code)
	}

	// Test invalid JSON
	req = httptest.NewRequest(http.MethodPost, "/webhooks/linear", strings.NewReader("invalid"))
	w = httptest.NewRecorder()
	server.handleLinearWebhook(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestServerStartStop(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 19090} // Use different port for test
	server := NewServer(config)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Server should shutdown gracefully
	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("Server returned: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Server did not shut down in time")
	}
}

func TestRouter(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	router := server.Router()
	if router == nil {
		t.Error("Router() returned nil")
	}
}

func TestCheckOrigin(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name     string
		origin   string
		expected bool
	}{
		{
			name:     "empty origin (same-origin request)",
			origin:   "",
			expected: true,
		},
		{
			name:     "localhost HTTP",
			origin:   "http://localhost",
			expected: true,
		},
		{
			name:     "localhost with port HTTP",
			origin:   "http://localhost:3000",
			expected: true,
		},
		{
			name:     "localhost HTTPS",
			origin:   "https://localhost",
			expected: true,
		},
		{
			name:     "localhost with port HTTPS",
			origin:   "https://localhost:8080",
			expected: true,
		},
		{
			name:     "127.0.0.1 HTTP",
			origin:   "http://127.0.0.1",
			expected: true,
		},
		{
			name:     "127.0.0.1 with port HTTP",
			origin:   "http://127.0.0.1:9000",
			expected: true,
		},
		{
			name:     "127.0.0.1 HTTPS",
			origin:   "https://127.0.0.1",
			expected: true,
		},
		{
			name:     "127.0.0.1 with port HTTPS",
			origin:   "https://127.0.0.1:443",
			expected: true,
		},
		{
			name:     "external origin rejected",
			origin:   "https://example.com",
			expected: false,
		},
		{
			name:     "malicious site rejected",
			origin:   "https://evil-site.com",
			expected: false,
		},
		{
			name:     "HTTP external origin rejected",
			origin:   "http://attacker.com",
			expected: false,
		},
		{
			name:     "localhost subdomain rejected",
			origin:   "https://localhost.evil.com",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			result := server.upgrader.CheckOrigin(req)
			if result != tt.expected {
				t.Errorf("CheckOrigin(%q) = %v, want %v", tt.origin, result, tt.expected)
			}
		})
	}
}

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		name     string
		origin   string
		expected bool
	}{
		// Valid localhost origins
		{"http localhost", "http://localhost", true},
		{"http localhost with port", "http://localhost:3000", true},
		{"http localhost with standard port", "http://localhost:80", true},
		{"https localhost", "https://localhost", true},
		{"https localhost with port", "https://localhost:443", true},
		{"http 127.0.0.1", "http://127.0.0.1", true},
		{"http 127.0.0.1 with port", "http://127.0.0.1:8080", true},
		{"https 127.0.0.1", "https://127.0.0.1", true},
		{"https 127.0.0.1 with port", "https://127.0.0.1:9000", true},

		// Invalid/malicious origins
		{"localhost subdomain attack", "https://localhost.evil.com", false},
		{"localhost path attack", "http://localhostevil.com", false},
		{"127.0.0.1 subdomain attack", "https://127.0.0.1.evil.com", false},
		{"external https", "https://example.com", false},
		{"external http", "http://attacker.com", false},
		{"empty string", "", false},
		{"just localhost word", "localhost", false},
		{"localhost with path no protocol", "localhost:3000", false},
		{"file protocol", "file://localhost", false},
		{"ftp protocol", "ftp://localhost", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isLocalhost(tt.origin)
			if result != tt.expected {
				t.Errorf("isLocalhost(%q) = %v, want %v", tt.origin, result, tt.expected)
			}
		})
	}
}

func TestHandleHealthTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		expectedStatus int
		checkBody      bool
	}{
		{
			name:           "GET request",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			checkBody:      true,
		},
		{
			name:           "POST request",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
			checkBody:      true,
		},
		{
			name:           "HEAD request",
			method:         http.MethodHead,
			expectedStatus: http.StatusOK,
			checkBody:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/health", nil)
			w := httptest.NewRecorder()

			server.handleHealth(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if w.Header().Get("Content-Type") != "application/json" {
				t.Error("Expected Content-Type application/json")
			}

			if tt.checkBody {
				var response map[string]string
				if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}
				if response["status"] != "healthy" {
					t.Errorf("Expected status 'healthy', got '%s'", response["status"])
				}
			}
		})
	}
}

func TestHandleStatusTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		expectedStatus int
	}{
		{
			name:           "GET request",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "POST request",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/status", nil)
			w := httptest.NewRecorder()

			server.handleStatus(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if w.Header().Get("Content-Type") != "application/json" {
				t.Error("Expected Content-Type application/json")
			}

			var response map[string]interface{}
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response["version"] != "0.1.0" {
				t.Errorf("Expected version '0.1.0', got '%v'", response["version"])
			}

			if _, ok := response["running"]; !ok {
				t.Error("Response should include 'running' field")
			}

			if _, ok := response["sessions"]; !ok {
				t.Error("Response should include 'sessions' field")
			}
		})
	}
}

func TestHandleTasksTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		expectedStatus int
	}{
		{
			name:           "GET request returns empty tasks",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "POST request",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/tasks", nil)
			w := httptest.NewRecorder()

			server.handleTasks(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if w.Header().Get("Content-Type") != "application/json" {
				t.Error("Expected Content-Type application/json")
			}

			var response map[string]interface{}
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			tasks, ok := response["tasks"]
			if !ok {
				t.Error("Response should include 'tasks' field")
			}

			taskArray, ok := tasks.([]interface{})
			if !ok {
				t.Error("Tasks should be an array")
			}

			if len(taskArray) != 0 {
				t.Errorf("Expected empty tasks array, got %d items", len(taskArray))
			}
		})
	}
}

func TestGithubWebhookTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		payload        string
		eventType      string
		signature      string
		expectedStatus int
	}{
		{
			name:           "GET method not allowed",
			method:         http.MethodGet,
			payload:        "",
			eventType:      "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "PUT method not allowed",
			method:         http.MethodPut,
			payload:        "",
			eventType:      "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE method not allowed",
			method:         http.MethodDelete,
			payload:        "",
			eventType:      "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "valid POST with issue event",
			method:         http.MethodPost,
			payload:        `{"action": "opened", "issue": {"number": 1}}`,
			eventType:      "issues",
			signature:      "sha256=abc123",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with push event",
			method:         http.MethodPost,
			payload:        `{"ref": "refs/heads/main", "commits": []}`,
			eventType:      "push",
			signature:      "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with pull_request event",
			method:         http.MethodPost,
			payload:        `{"action": "opened", "pull_request": {"number": 42}}`,
			eventType:      "pull_request",
			signature:      "sha256=def456",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid JSON",
			method:         http.MethodPost,
			payload:        "not valid json",
			eventType:      "issues",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty payload",
			method:         http.MethodPost,
			payload:        "",
			eventType:      "ping",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/webhooks/github", strings.NewReader(tt.payload))
			if tt.eventType != "" {
				req.Header.Set("X-GitHub-Event", tt.eventType)
			}
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature-256", tt.signature)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleGithubWebhook(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestJiraWebhookTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		payload        string
		signature      string
		expectedStatus int
	}{
		{
			name:           "GET method not allowed",
			method:         http.MethodGet,
			payload:        "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "PUT method not allowed",
			method:         http.MethodPut,
			payload:        "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE method not allowed",
			method:         http.MethodDelete,
			payload:        "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "valid POST with issue_created event",
			method:         http.MethodPost,
			payload:        `{"webhookEvent": "jira:issue_created", "issue": {"key": "PROJ-123"}}`,
			signature:      "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with issue_updated event",
			method:         http.MethodPost,
			payload:        `{"webhookEvent": "jira:issue_updated", "issue": {"key": "PROJ-456"}}`,
			signature:      "sha1=signature123",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with comment_created event",
			method:         http.MethodPost,
			payload:        `{"webhookEvent": "comment_created", "comment": {"body": "test"}}`,
			signature:      "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid JSON",
			method:         http.MethodPost,
			payload:        "{invalid json",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty payload",
			method:         http.MethodPost,
			payload:        "",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/webhooks/jira", strings.NewReader(tt.payload))
			if tt.signature != "" {
				req.Header.Set("X-Hub-Signature", tt.signature)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleJiraWebhook(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestLinearWebhookTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		payload        string
		expectedStatus int
	}{
		{
			name:           "GET method not allowed",
			method:         http.MethodGet,
			payload:        "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "PUT method not allowed",
			method:         http.MethodPut,
			payload:        "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE method not allowed",
			method:         http.MethodDelete,
			payload:        "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "PATCH method not allowed",
			method:         http.MethodPatch,
			payload:        "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "valid POST with issue create",
			method:         http.MethodPost,
			payload:        `{"action": "create", "type": "Issue", "data": {"id": "123"}}`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with issue update",
			method:         http.MethodPost,
			payload:        `{"action": "update", "type": "Issue", "data": {"id": "123"}}`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with comment create",
			method:         http.MethodPost,
			payload:        `{"action": "create", "type": "Comment", "data": {"body": "test"}}`,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid JSON",
			method:         http.MethodPost,
			payload:        "not json at all",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty payload",
			method:         http.MethodPost,
			payload:        "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "malformed JSON",
			method:         http.MethodPost,
			payload:        `{"action": "create", "type":}`,
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/webhooks/linear", strings.NewReader(tt.payload))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleLinearWebhook(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}
		})
	}
}

func TestServerStartAlreadyRunning(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 19091}
	server := NewServer(config)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start server in background
	go func() {
		_ = server.Start(ctx)
	}()

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	// Try to start again - should fail
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()

	err := server.Start(ctx2)
	if err == nil {
		t.Error("Expected error when starting already running server")
	}
	if err != nil && !strings.Contains(err.Error(), "already running") {
		t.Errorf("Expected 'already running' error, got: %v", err)
	}
}

func TestServerShutdownNotRunning(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	// Shutdown without starting - should be no-op
	err := server.Shutdown()
	if err != nil {
		t.Errorf("Shutdown on non-running server should not error: %v", err)
	}
}

func TestNewServerWithAuth(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	authConfig := &AuthConfig{
		Type:  AuthTypeAPIToken,
		Token: "test-token",
	}

	server := NewServerWithAuth(config, authConfig)

	if server == nil {
		t.Fatal("NewServerWithAuth returned nil")
	}
	if server.auth == nil {
		t.Error("Server auth not initialized")
	}
	if server.config != config {
		t.Error("Server config not set correctly")
	}
}

func TestNewServerWithAuth_NilAuth(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}

	server := NewServerWithAuth(config, nil)

	if server == nil {
		t.Fatal("NewServerWithAuth returned nil")
	}
	if server.auth != nil {
		t.Error("Server auth should be nil when authConfig is nil")
	}
}

func TestAPIEndpointsRequireAuth(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 19092}
	authConfig := &AuthConfig{
		Type:  AuthTypeAPIToken,
		Token: "secret-api-token",
	}
	server := NewServerWithAuth(config, authConfig)

	// Start server
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Test endpoints
	tests := []struct {
		name           string
		endpoint       string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "status without auth returns 401",
			endpoint:       "/api/v1/status",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "status with valid auth returns 200",
			endpoint:       "/api/v1/status",
			authHeader:     "Bearer secret-api-token",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "tasks without auth returns 401",
			endpoint:       "/api/v1/tasks",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "tasks with valid auth returns 200",
			endpoint:       "/api/v1/tasks",
			authHeader:     "Bearer secret-api-token",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "health is public (no auth required)",
			endpoint:       "/health",
			authHeader:     "",
			expectedStatus: http.StatusOK,
		},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19092"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, baseURL+tt.endpoint, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("Status = %d, want %d", resp.StatusCode, tt.expectedStatus)
			}
		})
	}
}

func TestWebhooksDoNotRequireBearerAuth(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 19093}
	authConfig := &AuthConfig{
		Type:  AuthTypeAPIToken,
		Token: "secret-api-token",
	}
	server := NewServerWithAuth(config, authConfig)

	// Start server
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Webhooks should NOT require bearer auth (they use signature validation instead)
	webhooks := []string{
		"/webhooks/linear",
		"/webhooks/github",
		"/webhooks/jira",
		"/webhooks/asana",
	}

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19093"

	for _, endpoint := range webhooks {
		t.Run(endpoint, func(t *testing.T) {
			// Send valid JSON payload without bearer token
			payload := `{"action": "test", "type": "test"}`
			req, err := http.NewRequest(http.MethodPost, baseURL+endpoint, strings.NewReader(payload))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			// Should return 200 OK, not 401 Unauthorized
			if resp.StatusCode == http.StatusUnauthorized {
				t.Errorf("Webhook %s should not require bearer auth", endpoint)
			}
		})
	}
}

func TestAsanaWebhookTableDriven(t *testing.T) {
	config := &Config{Host: "127.0.0.1", Port: 9090}
	server := NewServer(config)

	tests := []struct {
		name           string
		method         string
		payload        string
		hookSecret     string
		signature      string
		expectedStatus int
		expectedHeader string
	}{
		{
			name:           "handshake returns X-Hook-Secret",
			method:         http.MethodPost,
			payload:        "",
			hookSecret:     "asana-webhook-secret-123",
			signature:      "",
			expectedStatus: http.StatusOK,
			expectedHeader: "asana-webhook-secret-123",
		},
		{
			name:           "GET method not allowed",
			method:         http.MethodGet,
			payload:        "",
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "PUT method not allowed",
			method:         http.MethodPut,
			payload:        "",
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "DELETE method not allowed",
			method:         http.MethodDelete,
			payload:        "",
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusMethodNotAllowed,
		},
		{
			name:           "valid POST with task event",
			method:         http.MethodPost,
			payload:        `{"events": [{"action": "added", "resource": {"gid": "123", "resource_type": "task"}}]}`,
			hookSecret:     "",
			signature:      "sha256=abc123",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "valid POST with task changed event",
			method:         http.MethodPost,
			payload:        `{"events": [{"action": "changed", "resource": {"gid": "456", "resource_type": "task"}}]}`,
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusOK,
		},
		{
			name:           "invalid JSON",
			method:         http.MethodPost,
			payload:        "not valid json",
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "empty payload",
			method:         http.MethodPost,
			payload:        "",
			hookSecret:     "",
			signature:      "",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/webhooks/asana", strings.NewReader(tt.payload))
			if tt.hookSecret != "" {
				req.Header.Set("X-Hook-Secret", tt.hookSecret)
			}
			if tt.signature != "" {
				req.Header.Set("X-Hook-Signature", tt.signature)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			server.handleAsanaWebhook(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			if tt.expectedHeader != "" {
				if got := w.Header().Get("X-Hook-Secret"); got != tt.expectedHeader {
					t.Errorf("Expected X-Hook-Secret header %q, got %q", tt.expectedHeader, got)
				}
			}
		})
	}
}
