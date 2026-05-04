package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testOAuthConfig() *OAuthConfig {
	return &OAuthConfig{
		Providers: map[OAuthProviderName]*OAuthProviderConfig{
			OAuthProviderGitHub: {
				ClientID:     "test-client-id",
				ClientSecret: "test-client-secret",
				RedirectURL:  "http://localhost:8080/auth/oauth/github/callback",
				Scopes:       []string{"read:user", "repo"},
			},
		},
	}
}

func TestNewOAuthManager(t *testing.T) {
	t.Run("nil config returns nil", func(t *testing.T) {
		m := NewOAuthManager(nil)
		if m != nil {
			t.Error("expected nil for nil config")
		}
	})

	t.Run("valid config returns manager", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		if m == nil {
			t.Fatal("expected non-nil manager")
		}
	})
}

func TestOAuthStateStore(t *testing.T) {
	store := newOAuthStateStore()

	t.Run("generate and verify round-trip", func(t *testing.T) {
		token, err := store.generate()
		if err != nil {
			t.Fatalf("generate error: %v", err)
		}
		if token == "" {
			t.Error("expected non-empty token")
		}
		if !store.verify(token) {
			t.Error("expected verify to return true for fresh token")
		}
	})

	t.Run("token is consumed after verification", func(t *testing.T) {
		token, err := store.generate()
		if err != nil {
			t.Fatalf("generate error: %v", err)
		}
		store.verify(token)
		if store.verify(token) {
			t.Error("expected second verify to return false (token consumed)")
		}
	})

	t.Run("unknown token rejected", func(t *testing.T) {
		if store.verify("not-a-real-token") {
			t.Error("expected verify to return false for unknown token")
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		store.mu.Lock()
		store.states["expired-token"] = time.Now().Add(-1 * time.Minute)
		store.mu.Unlock()

		if store.verify("expired-token") {
			t.Error("expected verify to return false for expired token")
		}
	})
}

func TestOAuthStartHandler(t *testing.T) {
	m := NewOAuthManager(testOAuthConfig())

	t.Run("redirects to provider auth URL", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/github", nil)
		w := httptest.NewRecorder()

		m.StartHandler(w, req)

		if w.Code != http.StatusFound {
			t.Errorf("expected 302 Found, got %d", w.Code)
		}
		loc := w.Header().Get("Location")
		if loc == "" {
			t.Fatal("expected Location header")
		}
		if loc[:len("https://github.com/login/oauth/authorize")] != "https://github.com/login/oauth/authorize" {
			t.Errorf("unexpected redirect location: %s", loc)
		}
	})

	t.Run("unknown provider returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/unknown", nil)
		w := httptest.NewRecorder()

		m.StartHandler(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("non-GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/oauth/github", nil)
		w := httptest.NewRecorder()

		m.StartHandler(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})
}

func TestOAuthCallbackHandler(t *testing.T) {
	t.Run("invalid state returns 400", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/github/callback?state=bad&code=abc", nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("missing code returns 400", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		state, _ := m.states.generate()
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/github/callback?state="+state, nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("provider error returned as 400", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		state, _ := m.states.generate()
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/github/callback?state="+state+"&error=access_denied", nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("unknown provider returns 400", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/unknown/callback?state=x&code=y", nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("non-GET returns 405", func(t *testing.T) {
		m := NewOAuthManager(testOAuthConfig())
		req := httptest.NewRequest(http.MethodPost, "/auth/oauth/github/callback", nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", w.Code)
		}
	})

	t.Run("successful token exchange returns JSON", func(t *testing.T) {
		// Spin up a fake token endpoint
		fakeToken := &OAuthTokenResponse{
			AccessToken: "test-oauth-access-token",
			TokenType:   "bearer",
			Scope:       "read:user",
		}
		tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(fakeToken)
		}))
		defer tokenServer.Close()

		// Override the github token endpoint to point at the fake server
		original := oauthEndpoints[OAuthProviderGitHub]
		oauthEndpoints[OAuthProviderGitHub] = struct{ Auth, Token string }{
			Auth:  original.Auth,
			Token: tokenServer.URL,
		}
		defer func() { oauthEndpoints[OAuthProviderGitHub] = original }()

		m := NewOAuthManager(testOAuthConfig())
		state, _ := m.states.generate()
		req := httptest.NewRequest(http.MethodGet, "/auth/oauth/github/callback?state="+state+"&code=test-code", nil)
		w := httptest.NewRecorder()

		m.CallbackHandler(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var result OAuthTokenResponse
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result.AccessToken != fakeToken.AccessToken {
			t.Errorf("expected access_token %q, got %q", fakeToken.AccessToken, result.AccessToken)
		}
	})
}

func TestOAuthProviderNames(t *testing.T) {
	if string(OAuthProviderGitHub) != "github" {
		t.Errorf("unexpected OAuthProviderGitHub value: %s", OAuthProviderGitHub)
	}
	if string(OAuthProviderGoogle) != "google" {
		t.Errorf("unexpected OAuthProviderGoogle value: %s", OAuthProviderGoogle)
	}
}
