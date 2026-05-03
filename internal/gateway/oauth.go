package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuthProvider identifies a supported OAuth2 provider.
type OAuthProvider string

const (
	OAuthProviderGitHub  OAuthProvider = "github"
	OAuthProviderGoogle  OAuthProvider = "google"
	OAuthProviderGeneric OAuthProvider = "generic"
)

// OAuthConfig holds configuration for an OAuth2 provider.
type OAuthConfig struct {
	Provider     OAuthProvider `yaml:"provider"`
	ClientID     string        `yaml:"client_id"`
	ClientSecret string        `yaml:"client_secret"`
	// RedirectURL is the callback URL registered with the OAuth provider.
	RedirectURL string   `yaml:"redirect_url"`
	Scopes      []string `yaml:"scopes"`
	// AuthURL and TokenURL are required when Provider is "generic".
	AuthURL  string `yaml:"auth_url,omitempty"`
	TokenURL string `yaml:"token_url,omitempty"`
}

// providerEndpoints maps built-in providers to their well-known OAuth2 endpoints.
var providerEndpoints = map[OAuthProvider]struct{ AuthURL, TokenURL string }{
	OAuthProviderGitHub: {
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
	},
	OAuthProviderGoogle: {
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
	},
}

// providerDefaultScopes maps built-in providers to their default OAuth2 scopes.
var providerDefaultScopes = map[OAuthProvider][]string{
	OAuthProviderGitHub: {"read:user", "user:email"},
	OAuthProviderGoogle: {"openid", "email", "profile"},
}

// oauthState holds temporary state for an in-flight OAuth2 authorization.
type oauthState struct {
	provider  OAuthProvider
	expiresAt time.Time
}

// OAuthHandler manages the OAuth2 authorization code flow.
// It stores pending authorization states and completed session tokens in memory.
// Safe for concurrent use.
type OAuthHandler struct {
	config   *OAuthConfig
	mu       sync.Mutex
	pending  map[string]*oauthState // CSRF state → pending auth
	sessions map[string]*Token      // session token → resolved Token
	// HTTPClient is used for token exchange. Defaults to http.DefaultClient.
	// Override in tests to avoid real network calls.
	HTTPClient *http.Client
}

// NewOAuthHandler creates an OAuthHandler for the given config.
func NewOAuthHandler(config *OAuthConfig) *OAuthHandler {
	return &OAuthHandler{
		config:   config,
		pending:  make(map[string]*oauthState),
		sessions: make(map[string]*Token),
	}
}

// HandleLogin initiates the OAuth2 authorization code flow by redirecting
// the browser to the provider's authorization endpoint.
func (h *OAuthHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomHex(16)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.pending[state] = &oauthState{
		provider:  h.config.Provider,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	h.mu.Unlock()

	params := url.Values{
		"client_id":     {h.config.ClientID},
		"redirect_uri":  {h.config.RedirectURL},
		"scope":         {strings.Join(h.resolvedScopes(), " ")},
		"state":         {state},
		"response_type": {"code"},
	}
	if h.config.Provider == OAuthProviderGoogle {
		params.Set("access_type", "offline")
	}

	http.Redirect(w, r, h.resolvedAuthURL()+"?"+params.Encode(), http.StatusFound)
}

// HandleCallback handles the redirect from the OAuth2 provider after user authorization.
// It validates the CSRF state, exchanges the authorization code for a token,
// and returns the session token as JSON.
func (h *OAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if providerErr := q.Get("error"); providerErr != "" {
		http.Error(w, "OAuth provider error: "+providerErr, http.StatusBadRequest)
		return
	}

	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		http.Error(w, "Missing code or state parameter", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	pending, ok := h.pending[state]
	if ok {
		delete(h.pending, state)
	}
	h.mu.Unlock()

	if !ok || time.Now().After(pending.expiresAt) {
		http.Error(w, "Invalid or expired state", http.StatusBadRequest)
		return
	}

	token, err := h.exchangeCode(r.Context(), code)
	if err != nil {
		http.Error(w, "Token exchange failed", http.StatusInternalServerError)
		return
	}

	sessionToken, err := generateRandomHex(16)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.sessions[sessionToken] = token
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"token":      sessionToken,
		"expires_at": token.ExpiresAt,
		"scopes":     token.Scopes,
	})
}

// RevokeSession invalidates a previously-issued session token.
// If the token does not exist, RevokeSession is a no-op.
func (h *OAuthHandler) RevokeSession(sessionToken string) {
	h.mu.Lock()
	delete(h.sessions, sessionToken)
	h.mu.Unlock()
}

// HandleLogout revokes the session token supplied in the Authorization header
// and returns 204 No Content. If the token is missing or unknown the handler
// still returns 204 to avoid leaking session existence information.
func (h *OAuthHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token != "" {
		h.RevokeSession(token)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ValidateSessionToken checks if the session token is valid and unexpired.
// Expired tokens are removed from the store on first access.
func (h *OAuthHandler) ValidateSessionToken(sessionToken string) (*Token, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	t, ok := h.sessions[sessionToken]
	if !ok {
		return nil, errors.New("invalid session token")
	}
	if t.IsExpired() {
		delete(h.sessions, sessionToken)
		return nil, errors.New("session token expired")
	}
	return t, nil
}

// exchangeCode sends the authorization code to the provider's token endpoint
// and returns the resulting Token.
func (h *OAuthHandler) exchangeCode(ctx context.Context, code string) (*Token, error) {
	params := url.Values{
		"client_id":     {h.config.ClientID},
		"client_secret": {h.config.ClientSecret},
		"code":          {code},
		"redirect_uri":  {h.config.RedirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.resolvedTokenURL(),
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := h.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	return parseTokenResponse(body, h.resolvedScopes())
}

// resolvedAuthURL returns the provider's authorization endpoint URL.
func (h *OAuthHandler) resolvedAuthURL() string {
	if ep, ok := providerEndpoints[h.config.Provider]; ok && h.config.Provider != OAuthProviderGeneric {
		return ep.AuthURL
	}
	return h.config.AuthURL
}

// resolvedTokenURL returns the provider's token endpoint URL.
func (h *OAuthHandler) resolvedTokenURL() string {
	if ep, ok := providerEndpoints[h.config.Provider]; ok && h.config.Provider != OAuthProviderGeneric {
		return ep.TokenURL
	}
	return h.config.TokenURL
}

// resolvedScopes returns the configured scopes, falling back to the provider's defaults.
func (h *OAuthHandler) resolvedScopes() []string {
	if len(h.config.Scopes) > 0 {
		return h.config.Scopes
	}
	if defaults, ok := providerDefaultScopes[h.config.Provider]; ok {
		return defaults
	}
	return nil
}

// parseTokenResponse parses a JSON token response from an OAuth2 provider.
// The scopes parameter is used as a fallback when the response omits the scope field.
func parseTokenResponse(body []byte, scopes []string) (*Token, error) {
	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("oauth error %q: %s", resp.Error, resp.ErrorDesc)
	}
	if resp.AccessToken == "" {
		return nil, errors.New("no access_token in token response")
	}

	expiresAt := time.Now().Add(time.Hour) // default when provider omits expires_in
	if resp.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}

	tokenScopes := scopes
	if resp.Scope != "" {
		tokenScopes = strings.Fields(resp.Scope)
	}

	return &Token{
		Value:     resp.AccessToken,
		ExpiresAt: expiresAt,
		Scopes:    tokenScopes,
	}, nil
}

// generateRandomHex generates a cryptographically random hex string of n bytes.
func generateRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
