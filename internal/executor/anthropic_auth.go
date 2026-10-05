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
// OAuth token rather than an Anthropic API key. All Anthropic credentials
// share the "sk-ant-" prefix, so they are told apart by the segment after it.
//
// OAuth is classified positively — exactly one shape:
//   - "sk-ant-oat..." (e.g. "sk-ant-oat01-...") — Claude Code OAuth token
//
// Everything else under "sk-ant-" is an API key sent via x-api-key:
//   - "sk-ant-api..." (e.g. "sk-ant-api03-...") — classic Console API key
//   - "sk-ant-usr..." (e.g. "sk-ant-usr-...")   — identity-backed personal key
//   - service-account keys (any other "sk-ant-<shape>")
//
// GH-5344: OAuth tokens sent via x-api-key get 401 "API key is invalid", so
// direct mode must send them as Bearer + the oauth beta header.
// GH-5612: the original rule was "not sk-ant-api", which misclassified the
// newer identity-backed API key shapes as OAuth and made them 401 on the
// Bearer path. Decision: only the known OAuth prefix is OAuth.
func isAnthropicOAuthToken(key string) bool {
	rest := strings.TrimPrefix(key, "sk-ant-")
	if rest == key {
		// No "sk-ant-" prefix at all — not an Anthropic-issued credential
		// of either kind (e.g. a bare bearer token from a proxy).
		return false
	}
	return strings.HasPrefix(rest, "oat")
}

// setAnthropicAuthHeaders sets the appropriate authentication header(s) on
// req for apiKey, distinguishing Anthropic API keys from Claude Code OAuth
// tokens:
//   - "sk-ant-oat..." (OAuth token)      -> Authorization: Bearer + anthropic-beta
//   - any other "sk-ant-..." (API keys:
//     api, usr, service account)         -> x-api-key
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
