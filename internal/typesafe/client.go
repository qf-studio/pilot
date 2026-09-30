package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxErrorBodyBytes caps the response-body excerpt included in an error.
const maxErrorBodyBytes = 200

// Client is a minimal System One client: stdlib HTTP, one call, no retries.
// Gates fail open to their regex floor, so retry/backoff would only add latency.
type Client struct {
	apiKey     string
	model      string
	endpoint   string
	timeout    time.Duration
	httpClient *http.Client
	log        *slog.Logger
}

// NewClient builds a client from cfg (nil-safe defaults) and an API key.
// A nil logger discards output.
func NewClient(cfg Config, key string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Client{
		apiKey:     key,
		model:      cfg.EffectiveModel(),
		endpoint:   cfg.EffectiveEndpoint(),
		timeout:    cfg.EffectiveTimeout(),
		httpClient: &http.Client{},
		log:        log,
	}
}

type askRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Ask sends state and questions to System One and returns the answers. The
// call is bounded by the configured timeout on top of ctx. Errors carry the
// HTTP status and never the API key.
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (Answers, error) {
	var out Answers

	body, err := json.Marshal(askRequest{State: state, Model: c.model, Questions: questions})
	if err != nil {
		return out, fmt.Errorf("typesafe: marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("typesafe: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pilot-typesafe/1")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("typesafe: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		c.log.Debug("typesafe: non-2xx response", "status", resp.StatusCode, "latency_ms", time.Since(start).Milliseconds())
		return out, fmt.Errorf("typesafe: API returned status %d: %s", resp.StatusCode,
			c.scrub(strings.TrimSpace(string(excerpt))))
	}

	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Answers{}, fmt.Errorf("typesafe: decode response: %w", err)
	}
	if len(out.Answers) == 0 && len(questions) > 0 {
		return Answers{}, fmt.Errorf("typesafe: response carried no answers")
	}
	c.log.Debug("typesafe: ask ok", "model", out.Model, "questions", len(questions),
		"input_tokens", out.Usage.InputTokens, "output_tokens", out.Usage.OutputTokens,
		"latency_ms", time.Since(start).Milliseconds())
	return out, nil
}

// scrub removes the API key and credential-shaped substrings from server
// text that will be embedded in an error (a server could echo the key back).
func (c *Client) scrub(s string) string {
	if c.apiKey != "" {
		s = strings.ReplaceAll(s, c.apiKey, redactedPlaceholder)
	}
	return redactSecrets(s)
}
