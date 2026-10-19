package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// GitHubReviewClient defines the interface for GitHub PR review operations
type GitHubReviewClient interface {
	HasApprovalReview(ctx context.Context, owner, repo string, number int) (bool, string, error)
}

// GitHubHandler handles approval requests via GitHub PR reviews
type GitHubHandler struct {
	client       GitHubReviewClient
	owner        string
	repo         string
	resolveRepo  func(project string) (owner, repo string, ok bool)
	pollInterval time.Duration
	pending      map[string]*githubPending // requestID -> pending state
	mu           sync.RWMutex
	log          *slog.Logger
}

// githubPending tracks a pending GitHub approval request
type githubPending struct {
	Request    *Request
	Owner      string // repo the PR lives in (resolved per request)
	Repo       string
	PRNumber   int
	ResponseCh chan *Response
	CancelFn   context.CancelFunc
}

// GitHubHandlerConfig holds configuration for the GitHub approval handler
type GitHubHandlerConfig struct {
	Owner        string
	Repo         string
	PollInterval time.Duration // How often to check for reviews (default: 30s)

	// ResolveRepo maps Request.Project (the canonicalized project path) to the
	// GitHub owner/repo the project's PRs live in (GH-5716). Optional: when nil,
	// every request with a non-empty Project fails closed. A request with an
	// empty Project always uses the handler's default Owner/Repo.
	ResolveRepo func(project string) (owner, repo string, ok bool)
}

// NewGitHubHandler creates a new GitHub PR review approval handler
func NewGitHubHandler(client GitHubReviewClient, cfg *GitHubHandlerConfig) *GitHubHandler {
	pollInterval := cfg.PollInterval
	if pollInterval == 0 {
		pollInterval = 30 * time.Second
	}

	return &GitHubHandler{
		client:       client,
		owner:        cfg.Owner,
		repo:         cfg.Repo,
		resolveRepo:  cfg.ResolveRepo,
		pollInterval: pollInterval,
		pending:      make(map[string]*githubPending),
		log:          logging.WithComponent("approval.github"),
	}
}

// Name returns the handler name
func (h *GitHubHandler) Name() string {
	return "github"
}

// SendApprovalRequest starts polling for PR approval reviews
func (h *GitHubHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	responseCh := make(chan *Response, 1)

	// Extract PR number from metadata
	prNumber, ok := req.Metadata["pr_number"].(int)
	if !ok {
		// Try float64 (JSON unmarshaling)
		if prFloat, ok := req.Metadata["pr_number"].(float64); ok {
			prNumber = int(prFloat)
		} else {
			return nil, fmt.Errorf("pr_number missing from approval request metadata")
		}
	}

	// Decision: resolve the repo per request and fail closed. Falling back to the
	// default repo for an unresolvable project would check the wrong repo's PR
	// with the same number and could resolve the gate as approved (GH-5716).
	owner, repo := h.owner, h.repo
	if req.Project != "" {
		var ok bool
		if h.resolveRepo != nil {
			owner, repo, ok = h.resolveRepo(req.Project)
		}
		if !ok || owner == "" || repo == "" {
			return nil, fmt.Errorf("cannot resolve GitHub repo for approval project %q: refusing to check reviews against the default repo", req.Project)
		}
	}

	// Create cancellable context for polling
	pollCtx, cancelFn := context.WithCancel(ctx)

	// Track pending request
	h.mu.Lock()
	h.pending[req.ID] = &githubPending{
		Request:    req,
		Owner:      owner,
		Repo:       repo,
		PRNumber:   prNumber,
		ResponseCh: responseCh,
		CancelFn:   cancelFn,
	}
	h.mu.Unlock()

	h.log.Info("Waiting for GitHub PR approval",
		slog.String("request_id", req.ID),
		slog.String("repo", owner+"/"+repo),
		slog.Int("pr_number", prNumber),
		slog.Duration("poll_interval", h.pollInterval))

	// Start polling goroutine
	go h.pollForApproval(pollCtx, req.ID)

	return responseCh, nil
}

// pollForApproval polls GitHub for approval reviews
func (h *GitHubHandler) pollForApproval(ctx context.Context, requestID string) {
	ticker := time.NewTicker(h.pollInterval)
	defer ticker.Stop()

	// Check immediately first
	if h.checkApproval(ctx, requestID) {
		return
	}

	for {
		select {
		case <-ctx.Done():
			h.log.Debug("Polling cancelled",
				slog.String("request_id", requestID))
			return
		case <-ticker.C:
			if h.checkApproval(ctx, requestID) {
				return
			}
		}
	}
}

// checkApproval checks if the PR has been approved and sends response if so
func (h *GitHubHandler) checkApproval(ctx context.Context, requestID string) bool {
	h.mu.RLock()
	pending, exists := h.pending[requestID]
	h.mu.RUnlock()

	if !exists {
		return true // Request was cancelled/removed
	}

	approved, approver, err := h.client.HasApprovalReview(ctx, pending.Owner, pending.Repo, pending.PRNumber)
	if err != nil {
		h.log.Warn("Failed to check PR reviews",
			slog.String("request_id", requestID),
			slog.String("repo", pending.Owner+"/"+pending.Repo),
			slog.Int("pr_number", pending.PRNumber),
			slog.Any("error", err))
		return false
	}

	if approved {
		h.log.Info("PR approved",
			slog.String("request_id", requestID),
			slog.Int("pr_number", pending.PRNumber),
			slog.String("approver", approver))

		// Remove from pending
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()

		// Send response
		response := &Response{
			RequestID:   requestID,
			Decision:    DecisionApproved,
			ApprovedBy:  approver,
			RespondedAt: time.Now(),
		}

		select {
		case pending.ResponseCh <- response:
		default:
		}
		close(pending.ResponseCh)

		return true
	}

	return false
}

// CancelRequest cancels a pending approval request
func (h *GitHubHandler) CancelRequest(ctx context.Context, requestID string) error {
	h.mu.Lock()
	pending, exists := h.pending[requestID]
	if exists {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()

	if !exists {
		return nil
	}

	// Cancel polling
	pending.CancelFn()

	// Close response channel
	close(pending.ResponseCh)

	h.log.Debug("Cancelled GitHub approval request",
		slog.String("request_id", requestID))

	return nil
}

// HandleReviewEvent handles a GitHub pull_request_review webhook event
// This provides instant response instead of waiting for poll
func (h *GitHubHandler) HandleReviewEvent(ctx context.Context, owner, repo string, prNumber int, action, state, reviewer string) bool {
	// Only process submitted reviews that are approvals
	if action != "submitted" || state != "approved" {
		return false
	}

	// Find pending request for this PR (owner/repo + number: PR numbers collide across repos)
	h.mu.Lock()
	var foundID string
	var foundPending *githubPending
	for id, pending := range h.pending {
		if pending.PRNumber == prNumber && pending.Owner == owner && pending.Repo == repo {
			foundID = id
			foundPending = pending
			delete(h.pending, id)
			break
		}
	}
	h.mu.Unlock()

	if foundPending == nil {
		return false
	}

	h.log.Info("PR approved via webhook",
		slog.String("request_id", foundID),
		slog.String("repo", owner+"/"+repo),
		slog.Int("pr_number", prNumber),
		slog.String("reviewer", reviewer))

	// Cancel polling
	foundPending.CancelFn()

	// Send response
	response := &Response{
		RequestID:   foundID,
		Decision:    DecisionApproved,
		ApprovedBy:  reviewer,
		RespondedAt: time.Now(),
	}

	select {
	case foundPending.ResponseCh <- response:
	default:
	}
	close(foundPending.ResponseCh)

	return true
}

// HandleChangesRequestedEvent handles when changes are requested on a PR
// This rejects the approval request
func (h *GitHubHandler) HandleChangesRequestedEvent(ctx context.Context, owner, repo string, prNumber int, reviewer string) bool {
	// Find pending request for this PR (owner/repo + number: PR numbers collide across repos)
	h.mu.Lock()
	var foundID string
	var foundPending *githubPending
	for id, pending := range h.pending {
		if pending.PRNumber == prNumber && pending.Owner == owner && pending.Repo == repo {
			foundID = id
			foundPending = pending
			delete(h.pending, id)
			break
		}
	}
	h.mu.Unlock()

	if foundPending == nil {
		return false
	}

	h.log.Info("PR changes requested via webhook",
		slog.String("request_id", foundID),
		slog.String("repo", owner+"/"+repo),
		slog.Int("pr_number", prNumber),
		slog.String("reviewer", reviewer))

	// Cancel polling
	foundPending.CancelFn()

	// Send rejection response
	response := &Response{
		RequestID:   foundID,
		Decision:    DecisionRejected,
		ApprovedBy:  reviewer,
		Comment:     "Changes requested",
		RespondedAt: time.Now(),
	}

	select {
	case foundPending.ResponseCh <- response:
	default:
	}
	close(foundPending.ResponseCh)

	return true
}
