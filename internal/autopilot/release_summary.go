package autopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

const (
	// releaseSummaryModel is the Haiku model used for fast, cheap summary generation.
	releaseSummaryModel = "claude-haiku-4-5-20251001"

	// releaseSummaryTimeout is the max time for the LLM API call.
	releaseSummaryTimeout = 15 * time.Second

	// releasePollInterval is how often we check for the GoReleaser-created release.
	releasePollInterval = 30 * time.Second

	// releasePollTimeout is the max time to wait for GoReleaser to publish.
	releasePollTimeout = 5 * time.Minute

	// anthropicAPIURL is the Anthropic Messages API endpoint.
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
)

// ReleaseSummaryGenerator enriches GitHub releases with LLM-generated summaries.
// After GoReleaser publishes a release, it prepends a human-friendly "What's New"
// section to the mechanical changelog.
type ReleaseSummaryGenerator struct {
	ghClient   *github.Client
	apiKey     string
	httpClient *http.Client
	log        *slog.Logger
	model      string
	apiURL     string
}

// NewReleaseSummaryGenerator creates a generator. Returns nil if apiKey is empty
// (graceful degradation — releases ship without summaries).
func NewReleaseSummaryGenerator(ghClient *github.Client, apiKey string, log *slog.Logger) *ReleaseSummaryGenerator {
	if apiKey == "" {
		return nil
	}
	return &ReleaseSummaryGenerator{
		ghClient: ghClient,
		apiKey:   apiKey,
		httpClient: &http.Client{
			Timeout: releaseSummaryTimeout,
		},
		log:    log,
		model:  releaseSummaryModel,
		apiURL: anthropicAPIURL,
	}
}

// SetModel overrides the model.
func (g *ReleaseSummaryGenerator) SetModel(model string) { g.model = model }

// SetAPIURL overrides the API URL.
func (g *ReleaseSummaryGenerator) SetAPIURL(url string) { g.apiURL = url }

// EnrichRelease polls for the GoReleaser-created release, generates an LLM summary
// from the commit messages, and prepends it to the release body.
// Returns nil on any failure — release enrichment is best-effort.
func (g *ReleaseSummaryGenerator) EnrichRelease(ctx context.Context, owner, repo, tag string, commits []*github.Commit) error {
	// Poll for GoReleaser to publish the release
	release, err := g.waitForRelease(ctx, owner, repo, tag)
	if err != nil {
		return fmt.Errorf("waiting for release: %w", err)
	}

	// Generate summary from commit messages
	summary, err := g.generateSummary(ctx, tag, commits)
	if err != nil {
		return fmt.Errorf("generating summary: %w", err)
	}

	// Prepend summary to existing GoReleaser changelog body
	enrichedBody := summary + "\n\n" + release.Body

	_, err = g.ghClient.UpdateRelease(ctx, owner, repo, release.ID, &github.ReleaseInput{
		Body: enrichedBody,
	})
	if err != nil {
		return fmt.Errorf("updating release: %w", err)
	}

	g.log.Info("release enriched with summary", "tag", tag)
	return nil
}

// waitForRelease polls GetReleaseByTag until the release appears or timeout.
func (g *ReleaseSummaryGenerator) waitForRelease(ctx context.Context, owner, repo, tag string) (*github.Release, error) {
	return g.waitForReleaseWithInterval(ctx, owner, repo, tag, releasePollInterval)
}

// waitForReleaseWithInterval polls with configurable interval (testable).
func (g *ReleaseSummaryGenerator) waitForReleaseWithInterval(ctx context.Context, owner, repo, tag string, interval time.Duration) (*github.Release, error) {
	deadline := time.After(releasePollTimeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Check immediately before first tick
	release, err := g.ghClient.GetReleaseByTag(ctx, owner, repo, tag)
	if err == nil && release != nil {
		return release, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for release %s (polled %v)", tag, releasePollTimeout)
		case <-ticker.C:
			release, err := g.ghClient.GetReleaseByTag(ctx, owner, repo, tag)
			if err != nil {
				g.log.Warn("failed to fetch release", "tag", tag, "error", err)
				continue
			}
			if release != nil {
				return release, nil
			}
			g.log.Debug("release not yet published, waiting", "tag", tag)
		}
	}
}

// generateSummary calls Haiku to produce a human-friendly summary from commit messages.
func (g *ReleaseSummaryGenerator) generateSummary(ctx context.Context, tag string, commits []*github.Commit) (string, error) {
	if len(commits) == 0 {
		return fmt.Sprintf("## What's New in %s\n\nMaintenance release.", tag), nil
	}

	// Build commit list for the prompt
	var commitLines []string
	for _, c := range commits {
		msg := c.Commit.Message
		// Use only first line of commit message
		if idx := strings.Index(msg, "\n"); idx >= 0 {
			msg = msg[:idx]
		}
		commitLines = append(commitLines, "- "+msg)
	}
	commitText := strings.Join(commitLines, "\n")

	systemPrompt := `You are a release notes writer. Given a list of commit messages from a software release, write a concise "What's New" summary.

Rules:
- Start with "## What's New in <version>"
- Write 3-5 bullet points summarizing the most important changes
- Group related commits into single bullet points
- Use plain language (no commit prefixes like feat/fix)
- If there are breaking changes, add a "⚠️ Breaking Changes" subsection
- Keep total output under 200 words
- Do NOT include the raw commit messages — this is a summary for end users`

	reqBody := summaryRequest{
		Model:     g.model,
		MaxTokens: 512,
		System:    systemPrompt,
		Messages: []summaryMessage{
			{
				Role:    "user",
				Content: fmt.Sprintf("Version: %s\n\nCommits:\n%s", tag, commitText),
			},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp summaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from API")
	}

	return strings.TrimSpace(apiResp.Content[0].Text), nil
}

// summaryRequest is the Anthropic Messages API request body.
type summaryRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	System    string           `json:"system"`
	Messages  []summaryMessage `json:"messages"`
}

// summaryMessage is a single message in the API request.
type summaryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// summaryResponse is the Anthropic Messages API response body.
type summaryResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}
