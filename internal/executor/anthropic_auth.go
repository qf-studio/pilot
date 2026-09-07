package executor

import (
	"fmt"
	"net/http"
	"strings"
)

// anthropicAPIStatusError carries the HTTP status code from a non-200
// Anthropic Messages API response so callers can distinguish, e.g., a 401
// (bad/rejected credential) from a transient 5xx without string-matching
// the error text.
type anthropicAPIStatusError struct {
	StatusCode int
	Body       string
}

func (e *anthropicAPIStatusError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

// anthropicOAuthBetaHeader is required by the Anthropic Messages API when
// authenticating with a Claude Code subscription OAuth token instead of a
// real API key.
const anthropicOAuthBetaHeader = "oauth-2025-04-20"

// isAnthropicOAuthToken reports whether key is a Claude Code subscription
// OAuth token (e.g. "sk-ant-oat01-...") rather than an Anthropic API key
// (e.g. "sk-ant-api03-..."). Both share the "sk-ant-" prefix, so the two
// must be told apart by the segment that follows it.
//
// GH-5344: the effort classifier and the direct Anthropic backend both used
// to treat every "sk-ant-" value as an API key and send it via x-api-key.
// OAuth tokens rejected that header with 401 "API key is invalid" on every
// call — direct mode never worked on hosts where only
// CLAUDE_CODE_OAUTH_TOKEN is set (the founder box).
func isAnthropicOAuthToken(key string) bool {
	rest := strings.TrimPrefix(key, "sk-ant-")
	if rest == key {
		// No "sk-ant-" prefix at all — not an Anthropic-issued credential
		// of either kind (e.g. a bare bearer token from a proxy).
		return false
	}
	return !strings.HasPrefix(rest, "api")
}

// setAnthropicAuthHeaders sets the appropriate authentication header(s) on
// req for apiKey, distinguishing Anthropic API keys from Claude Code OAuth
// tokens:
//   - "sk-ant-api..." (API key)         -> x-api-key
//   - "sk-ant-oat..." (OAuth token)      -> Authorization: Bearer + anthropic-beta
//   - anything else (e.g. proxy tokens) -> Authorization: Bearer
func setAnthropicAuthHeaders(req *http.Request, apiKey string) {
	switch {
	case isAnthropicOAuthToken(apiKey):
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("anthropic-beta", anthropicOAuthBetaHeader)
	case strings.HasPrefix(apiKey, "sk-ant-"):
		req.Header.Set("x-api-key", apiKey)
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
