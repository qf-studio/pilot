package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// oauthTestConfig returns a minimal OAuthConfig suitable for unit tests.
func oauthTestConfig() *OAuthConfig {
	return &OAuthConfig{
		Provider:     OAuthProviderGitHub,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	}
}

func TestNewOAuthHandler(t *testing.T) {
	cfg := oauthTestConfig()
	h := NewOAuthHandler(cfg)
	if h == nil {
		t.Fatal("NewOAuthHandler returned nil")
	}
	if h.config != cfg {
		t.Error("config not stored")
	}
	if h.pending == nil || h.sessions == nil {
		t.Error("maps not initialized")
	}
}

func TestOAuthProviderEndpoints(t *testing.T) {
	tests := []struct {
		provider       OAuthProvider
		wantAuthURL    string
		wantTokenURL   string
		customAuthURL  string
		customTokenURL string
	}{
		{
			provider:     OAuthProviderGitHub,
			wantAuthURL:  "https://github.com/login/oauth/authorize",
			wantTokenURL: "https://github.com/login/oauth/access_token",
		},
		{
			provider:     OAuthProviderGoogle,
			wantAuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
			wantTokenURL: "https://oauth2.googleapis.com/token",
		},
		{
			provider:       OAuthProviderGeneric,
			customAuthURL:  "https://auth.example.com/oauth/authorize",
			customTokenURL: "https://auth.example.com/oauth/token",
			wantAuthURL:    "https://auth.example.com/oauth/authorize",
			wantTokenURL:   "https://auth.example.com/oauth/token",
		},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			h := NewOAuthHandler(&OAuthConfig{
				Provider:     tt.provider,
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
				RedirectURL:  "http://localhost/callback",
				AuthURL:      tt.customAuthURL,
				TokenURL:     tt.customTokenURL,
			})
			if got := h.resolvedAuthURL(); got != tt.wantAuthURL {
				t.Errorf("resolvedAuthURL() = %q, want %q", got, tt.wantAuthURL)
			}
			if got := h.resolvedTokenURL(); got != tt.wantTokenURL {
				t.Errorf("resolvedTokenURL() = %q, want %q", got, tt.wantTokenURL)
			}
		})
	}
}

func TestOAuthResolvedScopes(t *testing.T) {
	t.Run("custom scopes override defaults", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{
			Provider: OAuthProviderGitHub,
			Scopes:   []string{"repo", "read:org"},
		})
		scopes := h.resolvedScopes()
		if len(scopes) != 2 || scopes[0] != "repo" {
			t.Errorf("resolvedScopes() = %v, want [repo read:org]", scopes)
		}
	})

	t.Run("github defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderGitHub})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for github provider")
		}
	})

	t.Run("google defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderGoogle})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for google provider")
		}
	})

	t.Run("generic provider with no scopes returns nil", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderGeneric})
		if scopes := h.resolvedScopes(); scopes != nil {
			t.Errorf("expected nil scopes for generic provider, got %v", scopes)
		}
	})
}

func TestHandleLogin(t *testing.T) {
	h := NewOAuthHandler(oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin() status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("HandleLogin() returned no Location header")
	}
	if !strings.Contains(loc, "github.com/login/oauth/authorize") {
		t.Errorf("Location %q does not point to GitHub authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-client-id") {
		t.Errorf("Location %q missing client_id", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("Location %q missing state parameter", loc)
	}

	// Verify state was stored
	h.mu.Lock()
	pendingCount := len(h.pending)
	h.mu.Unlock()
	if pendingCount != 1 {
		t.Errorf("expected 1 pending state, got %d", pendingCount)
	}
}

func TestHandleCallback_Errors(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{
			name:       "missing state",
			query:      "code=abc",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing code",
			query:      "state=xyz",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "provider error param",
			query:      "error=access_denied",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewOAuthHandler(oauthTestConfig())
			req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+tt.query, nil)
			w := httptest.NewRecorder()

			h.HandleCallback(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("HandleCallback() status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestHandleCallback_InvalidState(t *testing.T) {
	h := NewOAuthHandler(oauthTestConfig())

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=invalid-state", nil)
	w := httptest.NewRecorder()

	h.HandleCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleCallback() with invalid state: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleCallback_ExpiredState(t *testing.T) {
	h := NewOAuthHandler(oauthTestConfig())

	// Insert an already-expired state
	h.mu.Lock()
	h.pending["expired-state"] = &oauthState{
		provider:  OAuthProviderGitHub,
		expiresAt: time.Now().Add(-1 * time.Minute),
	}
	h.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=abc&state=expired-state", nil)
	w := httptest.NewRecorder()

	h.HandleCallback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleCallback() with expired state: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleCallback_Success(t *testing.T) {
	// Spin up a mock token endpoint
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":3600,"scope":"read:user user:email"}`))
	}))
	defer tokenServer.Close()

	cfg := &OAuthConfig{
		Provider:     OAuthProviderGeneric,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURL:  "http://localhost/auth/callback",
		AuthURL:      "http://localhost/oauth/authorize",
		TokenURL:     tokenServer.URL,
	}
	h := NewOAuthHandler(cfg)
	h.HTTPClient = tokenServer.Client()

	// Insert a valid pending state
	h.mu.Lock()
	h.pending["valid-state"] = &oauthState{
		provider:  OAuthProviderGeneric,
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	h.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=auth-code&state=valid-state", nil)
	w := httptest.NewRecorder()

	h.HandleCallback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleCallback() status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["token"] == "" || resp["token"] == nil {
		t.Error("response missing session token")
	}

	// Verify state was consumed
	h.mu.Lock()
	remaining := len(h.pending)
	h.mu.Unlock()
	if remaining != 0 {
		t.Errorf("pending states not cleared: %d remaining", remaining)
	}
}

func TestValidateSessionToken(t *testing.T) {
	h := NewOAuthHandler(oauthTestConfig())

	t.Run("invalid token", func(t *testing.T) {
		_, err := h.ValidateSessionToken("does-not-exist")
		if err == nil {
			t.Error("expected error for unknown token")
		}
	})

	t.Run("expired token", func(t *testing.T) {
		h.mu.Lock()
		h.sessions["expired"] = &Token{
			Value:     "expired-access-token",
			ExpiresAt: time.Now().Add(-1 * time.Minute),
		}
		h.mu.Unlock()

		_, err := h.ValidateSessionToken("expired")
		if err == nil {
			t.Error("expected error for expired token")
		}
		// Verify expired token was cleaned up
		h.mu.Lock()
		_, stillPresent := h.sessions["expired"]
		h.mu.Unlock()
		if stillPresent {
			t.Error("expired token should be removed from store")
		}
	})

	t.Run("valid token", func(t *testing.T) {
		h.mu.Lock()
		h.sessions["valid"] = &Token{
			Value:     "live-access-token",
			ExpiresAt: time.Now().Add(1 * time.Hour),
			Scopes:    []string{"read:user"},
		}
		h.mu.Unlock()

		tok, err := h.ValidateSessionToken("valid")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tok.Value != "live-access-token" {
			t.Errorf("token.Value = %q, want %q", tok.Value, "live-access-token")
		}
	})
}

func TestParseTokenResponse(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		scopes    []string
		wantErr   bool
		wantToken string
		wantScope string
	}{
		{
			name:      "valid response with all fields",
			body:      `{"access_token":"tok123","expires_in":7200,"scope":"repo read:user"}`,
			scopes:    []string{"default"},
			wantToken: "tok123",
			wantScope: "repo",
		},
		{
			name:      "valid response using fallback scopes",
			body:      `{"access_token":"tok456","expires_in":3600}`,
			scopes:    []string{"fallback-scope"},
			wantToken: "tok456",
			wantScope: "fallback-scope",
		},
		{
			name:      "valid response with default expiry",
			body:      `{"access_token":"tok789"}`,
			scopes:    nil,
			wantToken: "tok789",
		},
		{
			name:    "error response",
			body:    `{"error":"invalid_client","error_description":"bad credentials"}`,
			wantErr: true,
		},
		{
			name:    "empty access token",
			body:    `{"expires_in":3600}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			body:    `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := parseTokenResponse([]byte(tt.body), tt.scopes)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseTokenResponse() error = %v, wantErr = %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if tok.Value != tt.wantToken {
				t.Errorf("token.Value = %q, want %q", tok.Value, tt.wantToken)
			}
			if tt.wantScope != "" && (len(tok.Scopes) == 0 || tok.Scopes[0] != tt.wantScope) {
				t.Errorf("token.Scopes[0] = %q, want %q", tok.Scopes[0], tt.wantScope)
			}
		})
	}
}

func TestGenerateRandomHex(t *testing.T) {
	token1, err := generateRandomHex(16)
	if err != nil {
		t.Fatalf("generateRandomHex() error: %v", err)
	}
	if len(token1) != 32 { // 16 bytes → 32 hex chars
		t.Errorf("len(%q) = %d, want 32", token1, len(token1))
	}

	token2, err := generateRandomHex(16)
	if err != nil {
		t.Fatalf("generateRandomHex() error: %v", err)
	}
	if token1 == token2 {
		t.Error("generateRandomHex() produced duplicate tokens")
	}
}

func TestAuthTypeOAuth(t *testing.T) {
	if string(AuthTypeOAuth) != "oauth" {
		t.Errorf("AuthTypeOAuth = %q, want %q", AuthTypeOAuth, "oauth")
	}
}

func TestNewAuthenticatorOAuth(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: oauthTestConfig(),
	}
	auth := NewAuthenticator(cfg)

	if auth.oauthHandler == nil {
		t.Error("oauthHandler not created for oauth auth type")
	}
	if auth.OAuthHandler() == nil {
		t.Error("OAuthHandler() returned nil")
	}
}

func TestNewAuthenticatorOAuth_NilOAuthConfig(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: nil,
	}
	auth := NewAuthenticator(cfg)

	// oauthHandler should be nil when OAuth config is absent
	if auth.oauthHandler != nil {
		t.Error("oauthHandler should be nil when OAuth config is nil")
	}
}

func TestAuthenticateOAuth_NoHandler(t *testing.T) {
	auth := &Authenticator{
		config: &AuthConfig{Type: AuthTypeOAuth},
		// oauthHandler intentionally nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer some-token")

	err := auth.Authenticate(req)
	if err == nil {
		t.Error("expected error when oauth handler is not configured")
	}
}

func TestAuthenticateOAuth_MissingToken(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: oauthTestConfig(),
	}
	auth := NewAuthenticator(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	if err := auth.Authenticate(req); err == nil {
		t.Error("expected error for missing token")
	}
}

func TestAuthenticateOAuth_ValidSession(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: oauthTestConfig(),
	}
	auth := NewAuthenticator(cfg)

	// Plant a valid session
	auth.oauthHandler.mu.Lock()
	auth.oauthHandler.sessions["my-session"] = &Token{
		Value:     "provider-token",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	auth.oauthHandler.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Authorization", "Bearer my-session")

	if err := auth.Authenticate(req); err != nil {
		t.Errorf("Authenticate() with valid OAuth session: %v", err)
	}
}

func TestRegisterOAuthRoutes(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: oauthTestConfig(),
	}
	auth := NewAuthenticator(cfg)

	mux := http.NewServeMux()
	auth.RegisterOAuthRoutes(mux)

	// /auth/login should redirect
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("/auth/login status = %d, want %d", w.Code, http.StatusFound)
	}

	// /auth/callback with no params should return 400
	req2 := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("/auth/callback (no params) status = %d, want %d", w2.Code, http.StatusBadRequest)
	}
}

func TestRegisterOAuthRoutes_NilAuth(t *testing.T) {
	// Should not panic with nil authenticator
	var auth *Authenticator
	mux := http.NewServeMux()
	auth.RegisterOAuthRoutes(mux) // no-op
}

func TestRegisterOAuthRoutes_NonOAuthAuth(t *testing.T) {
	// Non-OAuth authenticator should not register OAuth routes
	cfg := &AuthConfig{
		Type:  AuthTypeAPIToken,
		Token: "some-token",
	}
	auth := NewAuthenticator(cfg)

	mux := http.NewServeMux()
	auth.RegisterOAuthRoutes(mux) // no-op, no oauth handler

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	// Default mux returns 404 for unregistered paths
	if w.Code != http.StatusNotFound {
		t.Errorf("/auth/login without OAuth: status = %d, want %d", w.Code, http.StatusNotFound)
	}
}
