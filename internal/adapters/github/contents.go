package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// contentsResponse is the subset of the GitHub Contents API response
// (GET /repos/{owner}/{repo}/contents/{path}) needed to decode a file's
// content. GitHub returns Content base64-encoded, wrapped at 60 characters
// with embedded newlines, so decoding must strip whitespace first.
type contentsResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// GetFileContent fetches a single file's contents from a repo at the given
// ref (branch, tag, or SHA; empty string uses the repo's default branch) via
// the GitHub Contents API and returns it decoded as a string.
//
// This goes through doRequest, so it shares Client's normal auth, retry, and
// error handling — and, since httpClient.Transport is left unset (nil),
// requests fall back to http.DefaultTransport, which is exactly the hook
// internal/ghbudget installs its rate-limit-tracking RoundTripper over at
// daemon startup. GetFileContent therefore inherits ghbudget protection
// automatically; no wiring is needed here.
func (c *Client) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s", owner, repo, path)
	if ref != "" {
		apiPath += "?ref=" + url.QueryEscape(ref)
	}

	var resp contentsResponse
	if err := c.doRequest(ctx, http.MethodGet, apiPath, nil, &resp); err != nil {
		return "", err
	}

	if resp.Encoding != "" && resp.Encoding != "base64" {
		return "", fmt.Errorf("unsupported content encoding %q for %s/%s:%s", resp.Encoding, owner, repo, path)
	}

	// GitHub wraps the base64 payload at 60 characters with embedded
	// newlines; strip all whitespace before decoding.
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, resp.Content)

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", fmt.Errorf("decode base64 content for %s/%s:%s: %w", owner, repo, path, err)
	}

	return string(decoded), nil
}
