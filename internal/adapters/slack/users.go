package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	userCacheTTL = 30 * time.Minute
)

// UserInfo represents a Slack user's profile information
type UserInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	IsBot       bool   `json:"is_bot"`
}

// userCacheEntry holds a cached user info with expiration
type userCacheEntry struct {
	user      *UserInfo
	expiresAt time.Time
}

// userCache provides thread-safe caching for user info
type userCache struct {
	entries sync.Map
}

var globalUserCache = &userCache{}

// get retrieves a user from cache if not expired
func (c *userCache) get(userID string) (*UserInfo, bool) {
	val, ok := c.entries.Load(userID)
	if !ok {
		return nil, false
	}
	entry := val.(*userCacheEntry)
	if time.Now().After(entry.expiresAt) {
		c.entries.Delete(userID)
		return nil, false
	}
	return entry.user, true
}

// set stores a user in cache with TTL
func (c *userCache) set(userID string, user *UserInfo) {
	c.entries.Store(userID, &userCacheEntry{
		user:      user,
		expiresAt: time.Now().Add(userCacheTTL),
	})
}

// usersInfoResponse represents the response from users.info API
type usersInfoResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	User  struct {
		ID      string `json:"id"`
		IsBot   bool   `json:"is_bot"`
		Profile struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
			Email       string `json:"email"`
		} `json:"profile"`
	} `json:"user"`
}

// GetUserInfo retrieves user profile information from Slack with caching
func (c *Client) GetUserInfo(ctx context.Context, userID string) (*UserInfo, error) {
	// Check cache first
	if cached, ok := globalUserCache.get(userID); ok {
		return cached, nil
	}

	// Make API request
	url := slackAPIURL + "/users.info?user=" + userID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result usersInfoResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("slack API error: %s", result.Error)
	}

	// Build UserInfo from response
	displayName := result.User.Profile.DisplayName
	if displayName == "" {
		displayName = result.User.Profile.RealName
	}

	userInfo := &UserInfo{
		ID:          result.User.ID,
		DisplayName: displayName,
		Email:       result.User.Profile.Email,
		IsBot:       result.User.IsBot,
	}

	// Cache the result
	globalUserCache.set(userID, userInfo)

	return userInfo, nil
}
