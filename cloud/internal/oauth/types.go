package oauth

import (
	"time"

	"github.com/google/uuid"
)

// Provider represents an OAuth provider
type Provider string

const (
	ProviderGitHub Provider = "github"
	ProviderLinear Provider = "linear"
	ProviderJira   Provider = "jira"
)

// Integration represents a connected OAuth integration
type Integration struct {
	ID             uuid.UUID              `json:"id"`
	OrgID          uuid.UUID              `json:"org_id"`
	Provider       Provider               `json:"provider"`
	AccessToken    string                 `json:"-"` // Never expose
	RefreshToken   string                 `json:"-"` // Never expose
	TokenExpiresAt *time.Time             `json:"token_expires_at,omitempty"`
	ProviderUserID string                 `json:"provider_user_id,omitempty"`
	ProviderData   map[string]interface{} `json:"provider_data,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// IntegrationView is the public view of an integration (no tokens)
type IntegrationView struct {
	ID             uuid.UUID  `json:"id"`
	Provider       Provider   `json:"provider"`
	ProviderUserID string     `json:"provider_user_id,omitempty"`
	Connected      bool       `json:"connected"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// ToView converts an Integration to its public view
func (i *Integration) ToView() IntegrationView {
	return IntegrationView{
		ID:             i.ID,
		Provider:       i.Provider,
		ProviderUserID: i.ProviderUserID,
		Connected:      i.AccessToken != "",
		ExpiresAt:      i.TokenExpiresAt,
		CreatedAt:      i.CreatedAt,
	}
}

// OAuthState holds state during OAuth flow
type OAuthState struct {
	State       string    `json:"state"`
	OrgID       uuid.UUID `json:"org_id"`
	UserID      uuid.UUID `json:"user_id"`
	Provider    Provider  `json:"provider"`
	RedirectURL string    `json:"redirect_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// TokenResponse from OAuth providers
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// GitHubUser represents GitHub user info
type GitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

// LinearUser represents Linear user info
type LinearUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// LinearOrganization represents Linear org info
type LinearOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// JiraUser represents Jira user info
type JiraUser struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrls"`
}

// ProviderConfig holds OAuth configuration for a provider
type ProviderConfig struct {
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	RedirectURL  string
}

// DefaultProviderConfigs returns OAuth configs for all providers
func DefaultProviderConfigs(baseURL string) map[Provider]ProviderConfig {
	return map[Provider]ProviderConfig{
		ProviderGitHub: {
			AuthURL:     "https://github.com/login/oauth/authorize",
			TokenURL:    "https://github.com/login/oauth/access_token",
			Scopes:      []string{"repo", "read:user", "user:email"},
			RedirectURL: baseURL + "/oauth/callback/github",
		},
		ProviderLinear: {
			AuthURL:     "https://linear.app/oauth/authorize",
			TokenURL:    "https://api.linear.app/oauth/token",
			Scopes:      []string{"read", "write", "issues:create", "issues:update"},
			RedirectURL: baseURL + "/oauth/callback/linear",
		},
		ProviderJira: {
			AuthURL:     "https://auth.atlassian.com/authorize",
			TokenURL:    "https://auth.atlassian.com/oauth/token",
			Scopes:      []string{"read:jira-work", "write:jira-work", "read:jira-user", "offline_access"},
			RedirectURL: baseURL + "/oauth/callback/jira",
		},
	}
}
