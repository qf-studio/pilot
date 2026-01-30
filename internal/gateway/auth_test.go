package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewAuthenticator(t *testing.T) {
	config := &AuthConfig{
		Type:  AuthTypeAPIToken,
		Token: testutil.FakeBearerToken,
	}

	auth := NewAuthenticator(config)

	if auth == nil {
		t.Fatal("NewAuthenticator returned nil")
	}
	if auth.config != config {
		t.Error("Config not set correctly")
	}
}

func TestAuthenticateAPIToken(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		configToken string
		authHeader  string
		expectError bool
	}{
		{
			name:        "valid token",
			token:       "secret-token-123",
			configToken: "secret-token-123",
			authHeader:  "Bearer secret-token-123",
			expectError: false,
		},
		{
			name:        "invalid token",
			token:       "wrong-token",
			configToken: "secret-token-123",
			authHeader:  "Bearer wrong-token",
			expectError: true,
		},
		{
			name:        "missing authorization header",
			token:       "",
			configToken: "secret-token-123",
			authHeader:  "",
			expectError: true,
		},
		{
			name:        "missing Bearer prefix",
			token:       "secret-token-123",
			configToken: "secret-token-123",
			authHeader:  "secret-token-123",
			expectError: true,
		},
		{
			name:        "lowercase bearer",
			token:       "secret-token-123",
			configToken: "secret-token-123",
			authHeader:  "bearer secret-token-123",
			expectError: false, // Should work with case-insensitive comparison
		},
		{
			name:        "empty token after Bearer",
			token:       "",
			configToken: "secret-token-123",
			authHeader:  "Bearer ",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AuthConfig{
				Type:  AuthTypeAPIToken,
				Token: tt.configToken,
			}
			auth := NewAuthenticator(config)

			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			err := auth.Authenticate(req)
			if (err != nil) != tt.expectError {
				t.Errorf("Authenticate() error = %v, expectError = %v", err, tt.expectError)
			}
		})
	}
}

func TestAuthenticateClaudeCode(t *testing.T) {
	tests := []struct {
		name        string
		remoteAddr  string
		expectError bool
	}{
		{
			name:        "localhost 127.0.0.1",
			remoteAddr:  "127.0.0.1:12345",
			expectError: false,
		},
		{
			name:        "localhost with port only",
			remoteAddr:  "localhost:8080",
			expectError: false,
		},
		{
			name:        "IPv6 localhost",
			remoteAddr:  "[::1]:8080",
			expectError: false,
		},
		{
			name:        "external IP",
			remoteAddr:  "192.168.1.100:8080",
			expectError: true,
		},
		{
			name:        "public IP",
			remoteAddr:  "8.8.8.8:443",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AuthConfig{
				Type: AuthTypeClaudeCode,
			}
			auth := NewAuthenticator(config)

			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			req.RemoteAddr = tt.remoteAddr

			err := auth.Authenticate(req)
			if (err != nil) != tt.expectError {
				t.Errorf("Authenticate() error = %v, expectError = %v", err, tt.expectError)
			}
		})
	}
}

func TestAuthenticateUnknownType(t *testing.T) {
	config := &AuthConfig{
		Type: "unknown-type",
	}
	auth := NewAuthenticator(config)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	err := auth.Authenticate(req)

	if err == nil {
		t.Error("Expected error for unknown auth type")
	}
}

func TestIsLocalRequest(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		expected   bool
	}{
		{
			name:       "127.0.0.1 with port",
			remoteAddr: "127.0.0.1:8080",
			expected:   true,
		},
		{
			name:       "127.0.0.1 without port",
			remoteAddr: "127.0.0.1",
			expected:   true,
		},
		{
			name:       "localhost with port",
			remoteAddr: "localhost:3000",
			expected:   true,
		},
		{
			name:       "IPv6 localhost",
			remoteAddr: "[::1]:9090",
			expected:   true,
		},
		{
			name:       "private IP",
			remoteAddr: "192.168.1.1:8080",
			expected:   false,
		},
		{
			name:       "public IP",
			remoteAddr: "203.0.113.50:443",
			expected:   false,
		},
		{
			name:       "empty address",
			remoteAddr: "",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = tt.remoteAddr

			result := isLocalRequest(req)
			if result != tt.expected {
				t.Errorf("isLocalRequest(%q) = %v, want %v", tt.remoteAddr, result, tt.expected)
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "valid Bearer token",
			header:   "Bearer my-secret-token",
			expected: "my-secret-token",
		},
		{
			name:     "lowercase bearer",
			header:   "bearer my-token",
			expected: "my-token",
		},
		{
			name:     "UPPERCASE BEARER",
			header:   "BEARER my-token",
			expected: "my-token",
		},
		{
			name:     "mixed case Bearer",
			header:   "BeArEr my-token",
			expected: "my-token",
		},
		{
			name:     "empty header",
			header:   "",
			expected: "",
		},
		{
			name:     "no Bearer prefix",
			header:   "my-token",
			expected: "",
		},
		{
			name:     "Basic auth",
			header:   "Basic dXNlcjpwYXNz",
			expected: "",
		},
		{
			name:     "Bearer only",
			header:   "Bearer",
			expected: "",
		},
		{
			name:     "Bearer with space only",
			header:   "Bearer ",
			expected: "",
		},
		{
			name:     "token with spaces",
			header:   "Bearer token with spaces",
			expected: "token with spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			result := extractBearerToken(req)
			if result != tt.expected {
				t.Errorf("extractBearerToken() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestSecureCompare(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		expected bool
	}{
		{
			name:     "equal strings",
			a:        "secret-token",
			b:        "secret-token",
			expected: true,
		},
		{
			name:     "different strings",
			a:        "secret-token",
			b:        "different-token",
			expected: false,
		},
		{
			name:     "empty strings",
			a:        "",
			b:        "",
			expected: true,
		},
		{
			name:     "one empty string",
			a:        "token",
			b:        "",
			expected: false,
		},
		{
			name:     "different lengths",
			a:        "short",
			b:        "longer-string",
			expected: false,
		},
		{
			name:     "case sensitive",
			a:        "Token",
			b:        "token",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := secureCompare(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("secureCompare(%q, %q) = %v, want %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestTokenIsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		expected  bool
	}{
		{
			name:      "not expired",
			expiresAt: time.Now().Add(1 * time.Hour),
			expected:  false,
		},
		{
			name:      "expired",
			expiresAt: time.Now().Add(-1 * time.Hour),
			expected:  true,
		},
		{
			name:      "just expired",
			expiresAt: time.Now().Add(-1 * time.Second),
			expected:  true,
		},
		{
			name:      "zero time (expired)",
			expiresAt: time.Time{},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &Token{
				Value:     testutil.FakeBearerToken,
				ExpiresAt: tt.expiresAt,
			}

			result := token.IsExpired()
			if result != tt.expected {
				t.Errorf("IsExpired() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestTokenHasScope(t *testing.T) {
	tests := []struct {
		name     string
		scopes   []string
		scope    string
		expected bool
	}{
		{
			name:     "has exact scope",
			scopes:   []string{"read", "write", "admin"},
			scope:    "write",
			expected: true,
		},
		{
			name:     "does not have scope",
			scopes:   []string{"read", "write"},
			scope:    "admin",
			expected: false,
		},
		{
			name:     "wildcard scope",
			scopes:   []string{"*"},
			scope:    "anything",
			expected: true,
		},
		{
			name:     "empty scopes",
			scopes:   []string{},
			scope:    "read",
			expected: false,
		},
		{
			name:     "nil scopes",
			scopes:   nil,
			scope:    "read",
			expected: false,
		},
		{
			name:     "single scope match",
			scopes:   []string{"read"},
			scope:    "read",
			expected: true,
		},
		{
			name:     "scope case sensitive",
			scopes:   []string{"Read"},
			scope:    "read",
			expected: false,
		},
		{
			name:     "wildcard with other scopes",
			scopes:   []string{"read", "*", "write"},
			scope:    "admin",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &Token{
				Value:     testutil.FakeBearerToken,
				ExpiresAt: time.Now().Add(1 * time.Hour),
				Scopes:    tt.scopes,
			}

			result := token.HasScope(tt.scope)
			if result != tt.expected {
				t.Errorf("HasScope(%q) = %v, want %v", tt.scope, result, tt.expected)
			}
		})
	}
}

func TestAuthTypes(t *testing.T) {
	tests := []struct {
		name     string
		authType AuthType
		expected string
	}{
		{
			name:     "claude-code type",
			authType: AuthTypeClaudeCode,
			expected: "claude-code",
		},
		{
			name:     "api-token type",
			authType: AuthTypeAPIToken,
			expected: "api-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.authType) != tt.expected {
				t.Errorf("AuthType = %s, want %s", tt.authType, tt.expected)
			}
		})
	}
}

func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		authConfig     *AuthConfig
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "nil authenticator allows request",
			authConfig:     nil,
			authHeader:     "",
			expectedStatus: http.StatusOK,
		},
		{
			name: "valid token allows request",
			authConfig: &AuthConfig{
				Type:  AuthTypeAPIToken,
				Token: testutil.FakeBearerToken,
			},
			authHeader:     "Bearer " + testutil.FakeBearerToken,
			expectedStatus: http.StatusOK,
		},
		{
			name: "invalid token returns 401",
			authConfig: &AuthConfig{
				Type:  AuthTypeAPIToken,
				Token: testutil.FakeBearerToken,
			},
			authHeader:     "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "missing token returns 401",
			authConfig: &AuthConfig{
				Type:  AuthTypeAPIToken,
				Token: testutil.FakeBearerToken,
			},
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "claude-code auth allows localhost",
			authConfig: &AuthConfig{
				Type: AuthTypeClaudeCode,
			},
			authHeader:     "",
			expectedStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth *Authenticator
			if tt.authConfig != nil {
				auth = NewAuthenticator(tt.authConfig)
			}

			// Create test handler
			testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("OK"))
			})

			// Wrap with middleware
			handler := auth.Middleware(testHandler)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			// Set localhost for claude-code auth test
			if tt.authConfig != nil && tt.authConfig.Type == AuthTypeClaudeCode {
				req.RemoteAddr = "127.0.0.1:12345"
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Middleware() status = %d, want %d", w.Code, tt.expectedStatus)
			}
		})
	}
}

func TestAuthMiddleware_NilAuth(t *testing.T) {
	// Test that nil authenticator passes through requests
	var auth *Authenticator

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := auth.Middleware(testHandler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for nil auth, got %d", w.Code)
	}
}
