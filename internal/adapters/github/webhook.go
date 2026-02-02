package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookEventType represents the type of webhook event
type WebhookEventType string

const (
	EventIssuesOpened       WebhookEventType = "issues.opened"
	EventIssuesLabeled      WebhookEventType = "issues.labeled"
	EventIssuesClosed       WebhookEventType = "issues.closed"
	EventIssueComment       WebhookEventType = "issue_comment.created"
	EventPRReviewSubmitted  WebhookEventType = "pull_request_review.submitted"
	EventPRReviewDismissed  WebhookEventType = "pull_request_review.dismissed"
)

// WebhookPayload represents a GitHub webhook payload
type WebhookPayload struct {
	Action     string      `json:"action"`
	Issue      *Issue      `json:"issue,omitempty"`
	Repository *Repository `json:"repository,omitempty"`
	Label      *Label      `json:"label,omitempty"` // The label that was added (for labeled events)
	Sender     *User       `json:"sender,omitempty"`
}

// PRReviewCallback is called when a PR review event is received
type PRReviewCallback func(ctx context.Context, prNumber int, action, state, reviewer string, repo *Repository) error

// WebhookHandler handles GitHub webhooks
type WebhookHandler struct {
	client        *Client
	webhookSecret string
	pilotLabel    string
	onIssue       func(context.Context, *Issue, *Repository) error
	onPRReview    PRReviewCallback
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(client *Client, webhookSecret, pilotLabel string) *WebhookHandler {
	return &WebhookHandler{
		client:        client,
		webhookSecret: webhookSecret,
		pilotLabel:    pilotLabel,
	}
}

// OnIssue sets the callback for when a pilot-labeled issue is received
func (h *WebhookHandler) OnIssue(callback func(context.Context, *Issue, *Repository) error) {
	h.onIssue = callback
}

// OnPRReview sets the callback for when a PR review event is received
func (h *WebhookHandler) OnPRReview(callback PRReviewCallback) {
	h.onPRReview = callback
}

// VerifySignature verifies the GitHub webhook signature
func (h *WebhookHandler) VerifySignature(payload []byte, signature string) bool {
	if h.webhookSecret == "" {
		// No secret configured, skip verification (development mode)
		return true
	}

	return VerifyWebhookSignature(payload, signature, h.webhookSecret)
}

// VerifyWebhookSignature verifies a GitHub webhook signature against a secret.
// This is a standalone function for use by the gateway without a WebhookHandler instance.
// Returns true if signature is valid, false otherwise.
func VerifyWebhookSignature(payload []byte, signature, secret string) bool {
	if secret == "" {
		// No secret configured, skip verification (development mode)
		return true
	}

	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}

	expectedSig := signature[7:] // Remove "sha256=" prefix

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	actualSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(expectedSig), []byte(actualSig))
}

// Handle processes a webhook payload
func (h *WebhookHandler) Handle(ctx context.Context, eventType string, payload map[string]interface{}) error {
	action, _ := payload["action"].(string)

	logging.WithComponent("github").Debug("GitHub webhook", slog.String("event", eventType), slog.String("action", action))

	switch eventType {
	case "issues":
		// Process issue create/labeled events
		switch action {
		case "opened":
			return h.handleIssueOpened(ctx, payload)
		case "labeled":
			return h.handleIssueLabeled(ctx, payload)
		}
	case "pull_request_review":
		// Process PR review events
		return h.handlePRReview(ctx, payload, action)
	}

	return nil
}

// handleIssueOpened processes newly created issues
func (h *WebhookHandler) handleIssueOpened(ctx context.Context, payload map[string]interface{}) error {
	issue, repo, err := h.extractIssueAndRepo(payload)
	if err != nil {
		return err
	}

	// Check if issue has pilot label
	if !h.hasPilotLabel(issue) {
		logging.WithComponent("github").Debug("Issue does not have pilot label, skipping", slog.Int("number", issue.Number))
		return nil
	}

	return h.processIssue(ctx, issue, repo)
}

// handleIssueLabeled processes issues when a label is added
func (h *WebhookHandler) handleIssueLabeled(ctx context.Context, payload map[string]interface{}) error {
	// Check if the added label is the pilot label
	labelData, ok := payload["label"].(map[string]interface{})
	if !ok {
		return nil
	}

	labelName, _ := labelData["name"].(string)
	if labelName != h.pilotLabel {
		logging.WithComponent("github").Debug("Label is not pilot label, skipping", slog.String("label", labelName))
		return nil
	}

	issue, repo, err := h.extractIssueAndRepo(payload)
	if err != nil {
		return err
	}

	return h.processIssue(ctx, issue, repo)
}

// processIssue processes an issue that should be handled by Pilot
func (h *WebhookHandler) processIssue(ctx context.Context, issue *Issue, repo *Repository) error {
	logging.WithComponent("github").Info("Processing pilot issue",
		slog.String("repo", repo.FullName),
		slog.Int("number", issue.Number),
		slog.String("title", issue.Title))

	// Fetch full issue details via API (webhook payload may be incomplete)
	fullIssue, err := h.client.GetIssue(ctx, repo.Owner.Login, repo.Name, issue.Number)
	if err != nil {
		return fmt.Errorf("failed to fetch issue details: %w", err)
	}

	// Call the callback
	if h.onIssue != nil {
		return h.onIssue(ctx, fullIssue, repo)
	}

	return nil
}

// extractIssueAndRepo extracts issue and repository from payload
func (h *WebhookHandler) extractIssueAndRepo(payload map[string]interface{}) (*Issue, *Repository, error) {
	issueData, ok := payload["issue"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("missing issue in payload")
	}

	repoData, ok := payload["repository"].(map[string]interface{})
	if !ok {
		return nil, nil, fmt.Errorf("missing repository in payload")
	}

	// Parse issue
	issue := &Issue{
		Number:  int(issueData["number"].(float64)),
		Title:   issueData["title"].(string),
		State:   issueData["state"].(string),
		HTMLURL: issueData["html_url"].(string),
	}
	if body, ok := issueData["body"].(string); ok {
		issue.Body = body
	}

	// Parse labels
	if labelsData, ok := issueData["labels"].([]interface{}); ok {
		for _, l := range labelsData {
			if labelMap, ok := l.(map[string]interface{}); ok {
				label := Label{Name: labelMap["name"].(string)}
				if id, ok := labelMap["id"].(float64); ok {
					label.ID = int64(id)
				}
				issue.Labels = append(issue.Labels, label)
			}
		}
	}

	// Parse repository
	ownerData, _ := repoData["owner"].(map[string]interface{})
	repo := &Repository{
		Name:     repoData["name"].(string),
		FullName: repoData["full_name"].(string),
		HTMLURL:  repoData["html_url"].(string),
		Owner: User{
			Login: ownerData["login"].(string),
		},
	}
	if cloneURL, ok := repoData["clone_url"].(string); ok {
		repo.CloneURL = cloneURL
	}
	if sshURL, ok := repoData["ssh_url"].(string); ok {
		repo.SSHURL = sshURL
	}

	return issue, repo, nil
}

// hasPilotLabel checks if the issue has the pilot label
func (h *WebhookHandler) hasPilotLabel(issue *Issue) bool {
	for _, label := range issue.Labels {
		if label.Name == h.pilotLabel {
			return true
		}
	}
	return false
}

// handlePRReview processes pull request review events
func (h *WebhookHandler) handlePRReview(ctx context.Context, payload map[string]interface{}, action string) error {
	if h.onPRReview == nil {
		return nil
	}

	// Extract PR number
	prData, ok := payload["pull_request"].(map[string]interface{})
	if !ok {
		return nil
	}
	prNumber := int(prData["number"].(float64))

	// Extract review state
	reviewData, ok := payload["review"].(map[string]interface{})
	if !ok {
		return nil
	}
	state, _ := reviewData["state"].(string)

	// Extract reviewer
	var reviewer string
	if userData, ok := reviewData["user"].(map[string]interface{}); ok {
		reviewer, _ = userData["login"].(string)
	}

	// Extract repository
	repoData, ok := payload["repository"].(map[string]interface{})
	if !ok {
		return nil
	}
	ownerData, _ := repoData["owner"].(map[string]interface{})
	repo := &Repository{
		Name:     repoData["name"].(string),
		FullName: repoData["full_name"].(string),
		Owner: User{
			Login: ownerData["login"].(string),
		},
	}

	logging.WithComponent("github").Info("PR review event",
		slog.Int("pr_number", prNumber),
		slog.String("action", action),
		slog.String("state", state),
		slog.String("reviewer", reviewer))

	return h.onPRReview(ctx, prNumber, action, state, reviewer, repo)
}
