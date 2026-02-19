package autopilot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// Deployer executes post-merge deployment actions based on PostMergeConfig.
type Deployer struct {
	ghClient   *github.Client
	httpClient *http.Client
	owner      string
	repo       string
	config     *PostMergeConfig
	log        *slog.Logger
}

// NewDeployer creates a deployer for the given post-merge configuration.
func NewDeployer(ghClient *github.Client, owner, repo string, config *PostMergeConfig) *Deployer {
	return &Deployer{
		ghClient: ghClient,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		owner:  owner,
		repo:   repo,
		config: config,
		log:    slog.Default().With("component", "deployer"),
	}
}

// deployWebhookPayload is the JSON body sent for webhook deploy actions.
type deployWebhookPayload struct {
	Action    string `json:"action"`
	Repo      string `json:"repo"`
	PRNumber  int    `json:"pr_number"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"branch"`
	Timestamp string `json:"timestamp"`
}

// Deploy executes the configured post-merge action for the given PR state.
func (d *Deployer) Deploy(ctx context.Context, prState *PRState) error {
	switch d.config.Action {
	case "none", "":
		d.log.Debug("deploy action is none, skipping", "pr", prState.PRNumber)
		return nil

	case "tag":
		// Delegated to the releaser pipeline — deployer is a no-op for tags.
		d.log.Info("deploy action is tag, delegated to releaser", "pr", prState.PRNumber)
		return nil

	case "webhook":
		return d.deployWebhook(ctx, prState)

	case "branch-push":
		return d.deployBranchPush(ctx, prState)

	default:
		return fmt.Errorf("unknown deploy action: %q", d.config.Action)
	}
}

// deployWebhook sends an HTTP POST to the configured webhook URL with HMAC-SHA256 signing.
func (d *Deployer) deployWebhook(ctx context.Context, prState *PRState) error {
	if d.config.WebhookURL == "" {
		return fmt.Errorf("webhook_url is required for webhook deploy action")
	}

	payload := deployWebhookPayload{
		Action:    "deploy",
		Repo:      fmt.Sprintf("%s/%s", d.owner, d.repo),
		PRNumber:  prState.PRNumber,
		HeadSHA:   prState.HeadSHA,
		Branch:    prState.TargetBranch,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.config.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Add custom headers
	for k, v := range d.config.WebhookHeaders {
		req.Header.Set(k, v)
	}

	// HMAC-SHA256 signing
	if d.config.WebhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(d.config.WebhookSecret))
		mac.Write(body)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	}

	d.log.Info("sending deploy webhook",
		"pr", prState.PRNumber,
		"url", d.config.WebhookURL,
	)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	d.log.Info("deploy webhook sent successfully",
		"pr", prState.PRNumber,
		"status", resp.StatusCode,
	)
	return nil
}

// deployBranchPush updates (or creates) a deploy branch ref to point at the merged SHA.
func (d *Deployer) deployBranchPush(ctx context.Context, prState *PRState) error {
	if d.config.DeployBranch == "" {
		return fmt.Errorf("deploy_branch is required for branch-push deploy action")
	}

	d.log.Info("pushing to deploy branch",
		"pr", prState.PRNumber,
		"branch", d.config.DeployBranch,
		"sha", ShortSHA(prState.HeadSHA),
	)

	if err := d.ghClient.UpdateRef(ctx, d.owner, d.repo, d.config.DeployBranch, prState.HeadSHA); err != nil {
		return fmt.Errorf("failed to update deploy branch %q: %w", d.config.DeployBranch, err)
	}

	d.log.Info("deploy branch updated",
		"pr", prState.PRNumber,
		"branch", d.config.DeployBranch,
	)
	return nil
}
