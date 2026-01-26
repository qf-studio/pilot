package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Service handles OAuth flows for all providers
type Service struct {
	store    *Store
	configs  map[Provider]ProviderConfig
	baseURL  string
}

// NewService creates a new OAuth service
func NewService(store *Store, baseURL string, credentials map[Provider]struct{ ClientID, ClientSecret string }) *Service {
	configs := DefaultProviderConfigs(baseURL)

	// Apply credentials
	for provider, cred := range credentials {
		if cfg, ok := configs[provider]; ok {
			cfg.ClientID = cred.ClientID
			cfg.ClientSecret = cred.ClientSecret
			configs[provider] = cfg
		}
	}

	return &Service{
		store:   store,
		configs: configs,
		baseURL: baseURL,
	}
}

// GetAuthorizationURL generates an authorization URL for a provider
func (s *Service) GetAuthorizationURL(ctx context.Context, orgID, userID uuid.UUID, provider Provider, redirectURL string) (string, error) {
	config, ok := s.configs[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider: %s", provider)
	}

	// Generate state token
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	state := base64.URLEncoding.EncodeToString(stateBytes)

	// Save state
	oauthState := &OAuthState{
		State:       state,
		OrgID:       orgID,
		UserID:      userID,
		Provider:    provider,
		RedirectURL: redirectURL,
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}

	if err := s.store.SaveOAuthState(ctx, oauthState); err != nil {
		return "", fmt.Errorf("failed to save state: %w", err)
	}

	// Build authorization URL
	params := url.Values{
		"client_id":     {config.ClientID},
		"redirect_uri":  {config.RedirectURL},
		"scope":         {strings.Join(config.Scopes, " ")},
		"state":         {state},
		"response_type": {"code"},
	}

	// Provider-specific parameters
	switch provider {
	case ProviderJira:
		params.Set("audience", "api.atlassian.com")
		params.Set("prompt", "consent")
	case ProviderLinear:
		params.Set("response_type", "code")
		params.Set("prompt", "consent")
	}

	return config.AuthURL + "?" + params.Encode(), nil
}

// HandleCallback processes the OAuth callback
func (s *Service) HandleCallback(ctx context.Context, provider Provider, code, state string) (*Integration, string, error) {
	// Validate state
	oauthState, err := s.store.GetOAuthState(ctx, state)
	if err != nil {
		return nil, "", fmt.Errorf("invalid state: %w", err)
	}

	if oauthState.Provider != provider {
		return nil, "", fmt.Errorf("provider mismatch")
	}

	config, ok := s.configs[provider]
	if !ok {
		return nil, "", fmt.Errorf("unknown provider: %s", provider)
	}

	// Exchange code for tokens
	tokens, err := s.exchangeCode(ctx, provider, config, code)
	if err != nil {
		return nil, "", fmt.Errorf("failed to exchange code: %w", err)
	}

	// Get user info from provider
	providerUserID, providerData, err := s.getProviderUserInfo(ctx, provider, tokens.AccessToken)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get user info: %w", err)
	}

	// Calculate token expiration
	var expiresAt *time.Time
	if tokens.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		expiresAt = &exp
	}

	// Create or update integration
	now := time.Now()
	integration := &Integration{
		ID:             uuid.New(),
		OrgID:          oauthState.OrgID,
		Provider:       provider,
		AccessToken:    tokens.AccessToken,
		RefreshToken:   tokens.RefreshToken,
		TokenExpiresAt: expiresAt,
		ProviderUserID: providerUserID,
		ProviderData:   providerData,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.store.SaveIntegration(ctx, integration); err != nil {
		return nil, "", fmt.Errorf("failed to save integration: %w", err)
	}

	return integration, oauthState.RedirectURL, nil
}

// exchangeCode exchanges an authorization code for tokens
func (s *Service) exchangeCode(ctx context.Context, provider Provider, config ProviderConfig, code string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {config.ClientID},
		"client_secret": {config.ClientSecret},
		"code":          {code},
		"redirect_uri":  {config.RedirectURL},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, err
	}

	return &tokens, nil
}

// getProviderUserInfo fetches user info from the provider
func (s *Service) getProviderUserInfo(ctx context.Context, provider Provider, accessToken string) (string, map[string]interface{}, error) {
	switch provider {
	case ProviderGitHub:
		return s.getGitHubUser(ctx, accessToken)
	case ProviderLinear:
		return s.getLinearUser(ctx, accessToken)
	case ProviderJira:
		return s.getJiraUser(ctx, accessToken)
	default:
		return "", nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

func (s *Service) getGitHubUser(ctx context.Context, accessToken string) (string, map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", nil, err
	}

	data := map[string]interface{}{
		"login":      user.Login,
		"name":       user.Name,
		"email":      user.Email,
		"avatar_url": user.AvatarURL,
	}

	return fmt.Sprintf("%d", user.ID), data, nil
}

func (s *Service) getLinearUser(ctx context.Context, accessToken string) (string, map[string]interface{}, error) {
	query := `query { viewer { id name email } organization { id name } }`
	body := map[string]string{"query": query}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.linear.app/graphql", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Authorization", accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Viewer       LinearUser         `json:"viewer"`
			Organization LinearOrganization `json:"organization"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, err
	}

	data := map[string]interface{}{
		"name":      result.Data.Viewer.Name,
		"email":     result.Data.Viewer.Email,
		"org_id":    result.Data.Organization.ID,
		"org_name":  result.Data.Organization.Name,
	}

	return result.Data.Viewer.ID, data, nil
}

func (s *Service) getJiraUser(ctx context.Context, accessToken string) (string, map[string]interface{}, error) {
	// First get accessible resources
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.atlassian.com/oauth/token/accessible-resources", nil)
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	var resources []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return "", nil, err
	}

	if len(resources) == 0 {
		return "", nil, fmt.Errorf("no accessible Jira sites")
	}

	// Get user info from first site
	cloudID := resources[0].ID
	userURL := fmt.Sprintf("https://api.atlassian.com/ex/jira/%s/rest/api/3/myself", cloudID)

	req, err = http.NewRequestWithContext(ctx, "GET", userURL, nil)
	if err != nil {
		return "", nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	var user JiraUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", nil, err
	}

	data := map[string]interface{}{
		"display_name": user.DisplayName,
		"email":        user.Email,
		"cloud_id":     cloudID,
		"site_name":    resources[0].Name,
		"site_url":     resources[0].URL,
	}

	return user.AccountID, data, nil
}

// RefreshToken refreshes an expired access token
func (s *Service) RefreshToken(ctx context.Context, integration *Integration) error {
	if integration.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	config, ok := s.configs[integration.Provider]
	if !ok {
		return fmt.Errorf("unknown provider: %s", integration.Provider)
	}

	data := url.Values{
		"client_id":     {config.ClientID},
		"client_secret": {config.ClientSecret},
		"refresh_token": {integration.RefreshToken},
		"grant_type":    {"refresh_token"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token refresh failed: %s", string(body))
	}

	var tokens TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokens); err != nil {
		return err
	}

	// Calculate new expiration
	var expiresAt *time.Time
	if tokens.ExpiresIn > 0 {
		exp := time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
		expiresAt = &exp
	}

	// Update refresh token if provided
	refreshToken := integration.RefreshToken
	if tokens.RefreshToken != "" {
		refreshToken = tokens.RefreshToken
	}

	return s.store.UpdateTokens(ctx, integration.ID, tokens.AccessToken, refreshToken, expiresAt)
}

// GetValidToken returns a valid access token, refreshing if needed
func (s *Service) GetValidToken(ctx context.Context, orgID uuid.UUID, provider Provider) (string, error) {
	integration, err := s.store.GetIntegration(ctx, orgID, provider)
	if err != nil {
		return "", err
	}

	// Check if token is expired or about to expire (within 5 minutes)
	if integration.TokenExpiresAt != nil && time.Now().Add(5*time.Minute).After(*integration.TokenExpiresAt) {
		if err := s.RefreshToken(ctx, integration); err != nil {
			return "", fmt.Errorf("failed to refresh token: %w", err)
		}

		// Reload integration with new token
		integration, err = s.store.GetIntegration(ctx, orgID, provider)
		if err != nil {
			return "", err
		}
	}

	return integration.AccessToken, nil
}

// DisconnectIntegration removes an OAuth integration
func (s *Service) DisconnectIntegration(ctx context.Context, orgID uuid.UUID, provider Provider) error {
	return s.store.DeleteIntegration(ctx, orgID, provider)
}

// ListIntegrations returns all integrations for an organization
func (s *Service) ListIntegrations(ctx context.Context, orgID uuid.UUID) ([]IntegrationView, error) {
	integrations, err := s.store.ListIntegrations(ctx, orgID)
	if err != nil {
		return nil, err
	}

	views := make([]IntegrationView, len(integrations))
	for i, integration := range integrations {
		views[i] = integration.ToView()
	}

	return views, nil
}
