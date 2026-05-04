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
			provider:     OAuthProviderGitLab,
			wantAuthURL:  "https://gitlab.com/oauth/authorize",
			wantTokenURL: "https://gitlab.com/oauth/token",
		},
		{
			provider:     OAuthProviderMicrosoft,
			wantAuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			wantTokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		},
		{
			provider:       OAuthProviderMicrosoft,
			customAuthURL:  "https://login.microsoftonline.com/mytenant/oauth2/v2.0/authorize",
			customTokenURL: "https://login.microsoftonline.com/mytenant/oauth2/v2.0/token",
			wantAuthURL:    "https://login.microsoftonline.com/mytenant/oauth2/v2.0/authorize",
			wantTokenURL:   "https://login.microsoftonline.com/mytenant/oauth2/v2.0/token",
		},
		{
			provider:     OAuthProviderDiscord,
			wantAuthURL:  "https://discord.com/api/oauth2/authorize",
			wantTokenURL: "https://discord.com/api/oauth2/token",
		},
		{
			provider:     OAuthProviderBitbucket,
			wantAuthURL:  "https://bitbucket.org/site/oauth2/authorize",
			wantTokenURL: "https://bitbucket.org/site/oauth2/access_token",
		},
		{
			provider:       OAuthProviderBitbucket,
			customAuthURL:  "https://bitbucket.example.com/rest/oauth2/1.0/authorize",
			customTokenURL: "https://bitbucket.example.com/rest/oauth2/1.0/token",
			wantAuthURL:    "https://bitbucket.example.com/rest/oauth2/1.0/authorize",
			wantTokenURL:   "https://bitbucket.example.com/rest/oauth2/1.0/token",
		},
		{
			provider:     OAuthProviderSlack,
			wantAuthURL:  "https://slack.com/openid/connect/authorize",
			wantTokenURL: "https://slack.com/api/openid.connect.token",
		},
		{
			provider:     OAuthProviderLinkedIn,
			wantAuthURL:  "https://www.linkedin.com/oauth/v2/authorization",
			wantTokenURL: "https://www.linkedin.com/oauth/v2/accessToken",
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

func TestOAuthGitLabSelfHosted(t *testing.T) {
	// Self-hosted GitLab overrides default gitlab.com endpoints via AuthURL/TokenURL.
	customAuth := "https://gitlab.mycompany.com/oauth/authorize"
	customToken := "https://gitlab.mycompany.com/oauth/token"
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderGitLab,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURL:  "http://localhost/callback",
		AuthURL:      customAuth,
		TokenURL:     customToken,
	})
	if got := h.resolvedAuthURL(); got != customAuth {
		t.Errorf("resolvedAuthURL() = %q, want %q (self-hosted override)", got, customAuth)
	}
	if got := h.resolvedTokenURL(); got != customToken {
		t.Errorf("resolvedTokenURL() = %q, want %q (self-hosted override)", got, customToken)
	}
}

func TestOAuthBitbucketServerSelfHosted(t *testing.T) {
	// Bitbucket Server/Data Center deployments override bitbucket.org endpoints via AuthURL/TokenURL.
	customAuth := "https://bitbucket.mycompany.com/rest/oauth2/1.0/authorize"
	customToken := "https://bitbucket.mycompany.com/rest/oauth2/1.0/token"
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderBitbucket,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURL:  "http://localhost/callback",
		AuthURL:      customAuth,
		TokenURL:     customToken,
	})
	if got := h.resolvedAuthURL(); got != customAuth {
		t.Errorf("resolvedAuthURL() = %q, want %q (self-hosted override)", got, customAuth)
	}
	if got := h.resolvedTokenURL(); got != customToken {
		t.Errorf("resolvedTokenURL() = %q, want %q (self-hosted override)", got, customToken)
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

	t.Run("gitlab defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderGitLab})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for gitlab provider")
		}
		// Verify read_user is included as it's the primary GitLab scope
		found := false
		for _, s := range scopes {
			if s == "read_user" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected read_user in gitlab default scopes, got %v", scopes)
		}
	})

	t.Run("microsoft defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderMicrosoft})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for microsoft provider")
		}
		found := false
		for _, s := range scopes {
			if s == "User.Read" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected User.Read in microsoft default scopes, got %v", scopes)
		}
	})

	t.Run("discord defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderDiscord})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for discord provider")
		}
		found := false
		for _, s := range scopes {
			if s == "identify" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected identify in discord default scopes, got %v", scopes)
		}
	})

	t.Run("bitbucket defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderBitbucket})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for bitbucket provider")
		}
		found := false
		for _, s := range scopes {
			if s == "account" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected account in bitbucket default scopes, got %v", scopes)
		}
	})

	t.Run("slack defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderSlack})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for slack provider")
		}
		found := false
		for _, s := range scopes {
			if s == "openid" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected openid in slack default scopes, got %v", scopes)
		}
	})

	t.Run("linkedin defaults when no scopes configured", func(t *testing.T) {
		h := NewOAuthHandler(&OAuthConfig{Provider: OAuthProviderLinkedIn})
		scopes := h.resolvedScopes()
		if len(scopes) == 0 {
			t.Error("expected default scopes for linkedin provider")
		}
		found := false
		for _, s := range scopes {
			if s == "openid" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected openid in linkedin default scopes, got %v", scopes)
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

func TestHandleLogin_GitLab(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderGitLab,
		ClientID:     "test-gitlab-client",
		ClientSecret: "test-gitlab-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(gitlab) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "gitlab.com/oauth/authorize") {
		t.Errorf("Location %q does not point to GitLab authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-gitlab-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
}

func TestHandleLogin_Microsoft(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderMicrosoft,
		ClientID:     "test-ms-client",
		ClientSecret: "test-ms-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(microsoft) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "login.microsoftonline.com/common/oauth2/v2.0/authorize") {
		t.Errorf("Location %q does not point to Microsoft authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-ms-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
}

func TestHandleLogin_MicrosoftTenantOverride(t *testing.T) {
	tenantAuth := "https://login.microsoftonline.com/mytenant/oauth2/v2.0/authorize"
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderMicrosoft,
		ClientID:     "test-ms-client",
		ClientSecret: "test-ms-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
		AuthURL:      tenantAuth,
		TokenURL:     "https://login.microsoftonline.com/mytenant/oauth2/v2.0/token",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, tenantAuth) {
		t.Errorf("Location %q does not start with tenant-specific URL %q", loc, tenantAuth)
	}
}

func TestHandleLogin_Discord(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderDiscord,
		ClientID:     "test-discord-client",
		ClientSecret: "test-discord-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(discord) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "discord.com/api/oauth2/authorize") {
		t.Errorf("Location %q does not point to Discord authorize endpoint", loc)
	}
	if !strings.Contains(loc, "prompt=consent") {
		t.Errorf("Location %q missing prompt=consent for Discord", loc)
	}
	if !strings.Contains(loc, "client_id=test-discord-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
}

func TestHandleLogin_Bitbucket(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderBitbucket,
		ClientID:     "test-bitbucket-client",
		ClientSecret: "test-bitbucket-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(bitbucket) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "bitbucket.org/site/oauth2/authorize") {
		t.Errorf("Location %q does not point to Bitbucket authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-bitbucket-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("Location %q missing state parameter", loc)
	}
}

func TestHandleLogin_Slack(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderSlack,
		ClientID:     "test-slack-client",
		ClientSecret: "test-slack-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(slack) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "slack.com/openid/connect/authorize") {
		t.Errorf("Location %q does not point to Slack authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-slack-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("Location %q missing state parameter", loc)
	}
}

func TestHandleLogin_LinkedIn(t *testing.T) {
	h := NewOAuthHandler(&OAuthConfig{
		Provider:     OAuthProviderLinkedIn,
		ClientID:     "test-linkedin-client",
		ClientSecret: "test-linkedin-secret",
		RedirectURL:  "http://localhost:9090/auth/callback",
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()

	h.HandleLogin(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("HandleLogin(linkedin) status = %d, want %d", w.Code, http.StatusFound)
	}

	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "linkedin.com/oauth/v2/authorization") {
		t.Errorf("Location %q does not point to LinkedIn authorize endpoint", loc)
	}
	if !strings.Contains(loc, "client_id=test-linkedin-client") {
		t.Errorf("Location %q missing client_id", loc)
	}
	if !strings.Contains(loc, "state=") {
		t.Errorf("Location %q missing state parameter", loc)
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

func TestExchangeCode(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			body:       `{"access_token":"tok","expires_in":3600,"scope":"read:user"}`,
		},
		{
			name:       "non-200 status closes body without error check panic",
			statusCode: http.StatusUnauthorized,
			body:       `{"error":"bad_verification_code"}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			h := NewOAuthHandler(&OAuthConfig{
				Provider:     OAuthProviderGeneric,
				ClientID:     "cid",
				ClientSecret: "csec",
				RedirectURL:  "http://localhost/callback",
				TokenURL:     srv.URL,
			})
			h.HTTPClient = srv.Client()

			tok, err := h.exchangeCode(t.Context(), "auth-code")
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tok == nil || tok.Value == "" {
					t.Error("expected non-empty token")
				}
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

func TestRevokeSession(t *testing.T) {
	h := NewOAuthHandler(oauthTestConfig())

	h.mu.Lock()
	h.sessions["live-session"] = &Token{
		Value:     "access-token",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	h.mu.Unlock()

	// Revoke the session
	h.RevokeSession("live-session")

	// Token must be gone
	_, err := h.ValidateSessionToken("live-session")
	if err == nil {
		t.Error("expected error after RevokeSession, got nil")
	}

	// Revoking a non-existent token must not panic
	h.RevokeSession("does-not-exist")
}

func TestHandleLogout(t *testing.T) {
	t.Run("valid session is revoked and returns 204", func(t *testing.T) {
		h := NewOAuthHandler(oauthTestConfig())

		h.mu.Lock()
		h.sessions["session-to-revoke"] = &Token{
			Value:     "access-token",
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}
		h.mu.Unlock()

		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		req.Header.Set("Authorization", "Bearer session-to-revoke")
		w := httptest.NewRecorder()

		h.HandleLogout(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("HandleLogout() status = %d, want %d", w.Code, http.StatusNoContent)
		}

		// Session must be invalidated
		_, err := h.ValidateSessionToken("session-to-revoke")
		if err == nil {
			t.Error("session should be invalid after logout")
		}
	})

	t.Run("missing token still returns 204", func(t *testing.T) {
		h := NewOAuthHandler(oauthTestConfig())
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		w := httptest.NewRecorder()

		h.HandleLogout(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("HandleLogout() without token: status = %d, want %d", w.Code, http.StatusNoContent)
		}
	})

	t.Run("unknown token still returns 204", func(t *testing.T) {
		h := NewOAuthHandler(oauthTestConfig())
		req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
		req.Header.Set("Authorization", "Bearer unknown-token")
		w := httptest.NewRecorder()

		h.HandleLogout(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("HandleLogout() with unknown token: status = %d, want %d", w.Code, http.StatusNoContent)
		}
	})
}

func TestRegisterOAuthRoutes_Logout(t *testing.T) {
	cfg := &AuthConfig{
		Type:  AuthTypeOAuth,
		OAuth: oauthTestConfig(),
	}
	auth := NewAuthenticator(cfg)

	// Plant a session to verify logout removes it
	auth.oauthHandler.mu.Lock()
	auth.oauthHandler.sessions["revoke-me"] = &Token{
		Value:     "tok",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	auth.oauthHandler.mu.Unlock()

	mux := http.NewServeMux()
	auth.RegisterOAuthRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer revoke-me")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("/auth/logout status = %d, want %d", w.Code, http.StatusNoContent)
	}

	_, err := auth.oauthHandler.ValidateSessionToken("revoke-me")
	if err == nil {
		t.Error("session should be invalid after /auth/logout")
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
