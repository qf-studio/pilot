package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SocketModeClient connects to Slack's Socket Mode API using an app-level token.
// It handles the initial HTTP handshake to obtain a WebSocket URL.
type SocketModeClient struct {
	appToken   string
	apiURL     string
	httpClient *http.Client
}

// NewSocketModeClient creates a new Socket Mode client with the given app-level token.
// The token must be an xapp-... app-level token (not a bot token).
func NewSocketModeClient(appToken string) *SocketModeClient {
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     slackAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewSocketModeClientWithBaseURL creates a Socket Mode client with a custom API base URL.
// Used for testing with httptest.NewServer.
func NewSocketModeClientWithBaseURL(appToken, baseURL string) *SocketModeClient {
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// connectionsOpenResponse is the JSON response from apps.connections.open.
type connectionsOpenResponse struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// ErrAuthFailure indicates the app-level token was rejected by Slack.
var ErrAuthFailure = fmt.Errorf("slack socket mode: authentication failed")

// ErrConnectionOpen indicates a non-auth failure when opening a connection.
var ErrConnectionOpen = fmt.Errorf("slack socket mode: failed to open connection")

// OpenConnection calls apps.connections.open with the app-level token
// and returns the WebSocket URL for event streaming.
func (s *SocketModeClient) OpenConnection(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/apps.connections.open", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.appToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrConnectionOpen, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%w: failed to read response: %w", ErrConnectionOpen, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrConnectionOpen, resp.StatusCode, string(body))
	}

	var result connectionsOpenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("%w: failed to parse response: %w", ErrConnectionOpen, err)
	}

	if !result.OK {
		// Slack returns specific error codes for auth issues
		switch result.Error {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked":
			return "", fmt.Errorf("%w: %s", ErrAuthFailure, result.Error)
		default:
			return "", fmt.Errorf("%w: %s", ErrConnectionOpen, result.Error)
		}
	}

	if result.URL == "" {
		return "", fmt.Errorf("%w: empty WebSocket URL in response", ErrConnectionOpen)
	}

	return result.URL, nil
}
