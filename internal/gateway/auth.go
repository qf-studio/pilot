package gateway

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"
)

// AuthType defines the authentication method
type AuthType string

const (
	AuthTypeClaudeCode AuthType = "claude-code"
	AuthTypeAPIToken   AuthType = "api-token"
)

// AuthConfig holds authentication configuration
type AuthConfig struct {
	Type  AuthType `yaml:"type"`
	Token string   `yaml:"token,omitempty"`
}

// Authenticator handles authentication
type Authenticator struct {
	config *AuthConfig
}

// NewAuthenticator creates a new authenticator
func NewAuthenticator(config *AuthConfig) *Authenticator {
	return &Authenticator{config: config}
}

// Authenticate validates a request
func (a *Authenticator) Authenticate(r *http.Request) error {
	switch a.config.Type {
	case AuthTypeClaudeCode:
		return a.authenticateClaudeCode(r)
	case AuthTypeAPIToken:
		return a.authenticateAPIToken(r)
	default:
		return errors.New("unknown auth type")
	}
}

// authenticateClaudeCode validates Claude Code authentication
func (a *Authenticator) authenticateClaudeCode(r *http.Request) error {
	// Claude Code uses local socket authentication
	// For now, accept all local connections
	if isLocalRequest(r) {
		return nil
	}
	return errors.New("claude-code auth requires local connection")
}

// authenticateAPIToken validates API token authentication
func (a *Authenticator) authenticateAPIToken(r *http.Request) error {
	token := extractBearerToken(r)
	if token == "" {
		return errors.New("missing authorization token")
	}

	if !secureCompare(token, a.config.Token) {
		return errors.New("invalid token")
	}

	return nil
}

// isLocalRequest checks if the request is from localhost
func isLocalRequest(r *http.Request) bool {
	host := r.RemoteAddr
	return strings.HasPrefix(host, "127.0.0.1") ||
		strings.HasPrefix(host, "localhost") ||
		strings.HasPrefix(host, "[::1]")
}

// extractBearerToken extracts the bearer token from Authorization header
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}

	const prefix = "Bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}

	return auth[len(prefix):]
}

// secureCompare performs constant-time string comparison
func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Middleware returns an HTTP middleware that authenticates requests.
// If authentication fails, it returns 401 Unauthorized.
// If no auth config is provided (nil), all requests are allowed.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || a.config == nil {
			next.ServeHTTP(w, r)
			return
		}

		if err := a.Authenticate(r); err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Token represents an authentication token
type Token struct {
	Value     string
	ExpiresAt time.Time
	Scopes    []string
}

// IsExpired checks if the token is expired
func (t *Token) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// HasScope checks if the token has a specific scope
func (t *Token) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}
