package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuthProviderName identifies a supported OAuth2 provider.
type OAuthProviderName string

const (
	OAuthProviderGitHub OAuthProviderName = "github"
	OAuthProviderGoogle OAuthProviderName = "google"
)

// OAuthProviderConfig holds OAuth2 settings for a single provider.
type OAuthProviderConfig struct {
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	RedirectURL  string   `yaml:"redirect_url"`
	Scopes       []string `yaml:"scopes"`
}

// OAuthConfig holds OAuth2 configuration for the gateway.
// Providers is a map from provider name (e.g. "github") to its settings.
type OAuthConfig struct {
	Providers map[OAuthProviderName]*OAuthProviderConfig `yaml:"providers"`
}

// oauthEndpoints holds the authorization and token endpoint URLs for each provider.
var oauthEndpoints = map[OAuthProviderName]struct{ Auth, Token string }{
	OAuthProviderGitHub: {
		Auth:  "https://github.com/login/oauth/authorize",
		Token: "https://github.com/login/oauth/access_token",
	},
	OAuthProviderGoogle: {
		Auth:  "https://accounts.google.com/o/oauth2/v2/auth",
		Token: "https://oauth2.googleapis.com/token",
	},
}

// oauthStateStore manages short-lived CSRF state tokens for OAuth flows.
type oauthStateStore struct {
	mu     sync.Mutex
	states map[string]time.Time
}

func newOAuthStateStore() *oauthStateStore {
	return &oauthStateStore{
		states: make(map[string]time.Time),
	}
}

// generate creates a new state token valid for 10 minutes.
func (s *oauthStateStore) generate() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.URLEncoding.EncodeToString(b)

	s.mu.Lock()
	s.states[token] = time.Now().Add(10 * time.Minute)
	s.mu.Unlock()

	return token, nil
}

// verify returns true if the token is valid and not expired, consuming it.
func (s *oauthStateStore) verify(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	exp, ok := s.states[token]
	if !ok {
		return false
	}
	delete(s.states, token)
	return time.Now().Before(exp)
}

// OAuthTokenResponse holds the access token returned after a successful exchange.
type OAuthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
}

// OAuthManager handles the OAuth2 authorization code flow for the gateway.
// It supports multiple providers via a single /auth/oauth/{provider} path prefix.
type OAuthManager struct {
	config     *OAuthConfig
	states     *oauthStateStore
	httpClient *http.Client
}

// NewOAuthManager creates an OAuthManager from config. Returns nil if config is nil.
func NewOAuthManager(config *OAuthConfig) *OAuthManager {
	if config == nil {
		return nil
	}
	return &OAuthManager{
		config:     config,
		states:     newOAuthStateStore(),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// StartHandler handles GET /auth/oauth/{provider}.
// It redirects the browser to the provider's authorization page.
func (m *OAuthManager) StartHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	providerName := OAuthProviderName(strings.TrimPrefix(r.URL.Path, "/auth/oauth/"))
	cfg, ok := m.config.Providers[providerName]
	if !ok || cfg == nil {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	endpoints, ok := oauthEndpoints[providerName]
	if !ok {
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}

	state, err := m.states.generate()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	params := url.Values{
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURL},
		"scope":         {strings.Join(cfg.Scopes, " ")},
		"state":         {state},
		"response_type": {"code"},
	}

	http.Redirect(w, r, endpoints.Auth+"?"+params.Encode(), http.StatusFound)
}

// CallbackHandler handles GET /auth/oauth/{provider}/callback.
// It validates the state, exchanges the code for an access token, and returns it as JSON.
func (m *OAuthManager) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract provider: /auth/oauth/{provider}/callback → provider
	path := strings.TrimPrefix(r.URL.Path, "/auth/oauth/")
	path = strings.TrimSuffix(path, "/callback")
	providerName := OAuthProviderName(path)

	cfg, ok := m.config.Providers[providerName]
	if !ok || cfg == nil {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	endpoints, ok := oauthEndpoints[providerName]
	if !ok {
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}

	state := r.URL.Query().Get("state")
	if !m.states.verify(state) {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			http.Error(w, "provider error: "+errMsg, http.StatusBadRequest)
			return
		}
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	token, err := m.exchangeCode(r.Context(), endpoints.Token, cfg, code)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(token)
}

// exchangeCode performs the OAuth2 authorization code → access token exchange.
func (m *OAuthManager) exchangeCode(ctx context.Context, tokenURL string, cfg *OAuthProviderConfig, code string) (*OAuthTokenResponse, error) {
	body := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}

	var result OAuthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}

	return &result, nil
}
