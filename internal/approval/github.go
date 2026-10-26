package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// approvalWriteTimeout bounds the decision write made from the poll goroutine
// on a context detached from the (cancelled) poll context.
const approvalWriteTimeout = 10 * time.Second

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
	store        PendingApprovalStore // optional; enables restart persistence (GH-5757)
	recorder     DecisionRecorder     // optional; persists decisions directly (restart-safe, GH-5757)
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

// WithStore attaches a persistence store so pending approvals survive restarts
// (GH-5757, mirrors TelegramHandler/SlackHandler.WithStore). Returns h to allow
// builder-style chaining.
func (h *GitHubHandler) WithStore(store PendingApprovalStore) *GitHubHandler {
	h.store = store
	return h
}

// WithDecisionRecorder attaches a DecisionRecorder so a review decision is
// persisted directly to the PRState/executions store rather than relying on a
// goroutine reading the response channel. After a restart, Rehydrate restores
// the poll but no waiter, so without this an approval detected by the restarted
// poll would never reach the pipeline (GH-5757, mirrors GH-3825/GH-4411).
// Returns h to allow builder-style chaining.
func (h *GitHubHandler) WithDecisionRecorder(recorder DecisionRecorder) *GitHubHandler {
	h.recorder = recorder
	return h
}

// prNumberFromMetadata extracts the PR number from request metadata. It accepts
// int (in-process) and float64 (after a JSON round trip, e.g. a persisted row).
func prNumberFromMetadata(md map[string]interface{}) (int, bool) {
	switch v := md["pr_number"].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

// resolveTarget maps a request's Project to the GitHub owner/repo its PR lives
// in. Decision: resolve per request and fail closed. Falling back to the default
// repo for an unresolvable project would check the wrong repo's PR with the same
// number and could resolve the gate as approved (GH-5716). A request with an
// empty Project always uses the handler's default Owner/Repo. SendApprovalRequest
// and Rehydrate both call it so a restart never resolves a project differently.
func (h *GitHubHandler) resolveTarget(project string) (owner, repo string, err error) {
	if project == "" {
		return h.owner, h.repo, nil
	}
	var ok bool
	if h.resolveRepo != nil {
		owner, repo, ok = h.resolveRepo(project)
	}
	if !ok || owner == "" || repo == "" {
		return "", "", fmt.Errorf("cannot resolve GitHub repo for approval project %q: refusing to check reviews against the default repo", project)
	}
	return owner, repo, nil
}

// deletePersisted removes the request's persisted row, if a store is attached.
func (h *GitHubHandler) deletePersisted(requestID, why string) {
	if h.store == nil {
		return
	}
	if err := h.store.DeletePendingApproval(requestID); err != nil {
		h.log.Warn("failed to delete persisted approval on "+why,
			slog.String("request_id", requestID), slog.Any("error", err))
	}
}

// resolvePending finalizes a pending request that was already removed from
// h.pending: deletes its persisted row, records the decision directly (so it
// reaches the pipeline even when no waiter survived a restart), and delivers
// the response. Callers cancel polling themselves.
func (h *GitHubHandler) resolvePending(ctx context.Context, p *githubPending, resp *Response) {
	h.deletePersisted(resp.RequestID, "decision")

	if h.recorder != nil {
		if err := h.recorder.RecordDecision(ctx, resp.RequestID, resp.Decision, resp.ApprovedBy); err != nil {
			if errors.Is(err, memory.ErrApprovalAlreadyDecided) {
				h.log.Info("approval decision race lost — another decider already recorded",
					slog.String("request_id", resp.RequestID), slog.String("by", resp.ApprovedBy))
			} else {
				h.log.Warn("failed to record approval decision",
					slog.String("request_id", resp.RequestID), slog.Any("error", err))
			}
		}
	}

	select {
	case p.ResponseCh <- resp:
	default:
	}
	close(p.ResponseCh)
}

// Rehydrate loads persisted pending approvals this handler owns and restarts a
// review poll for each, so an approval given on GitHub after a daemon restart is
// still detected (GH-5757). Expired rows are deleted. A row whose project no
// longer resolves to a repo (or that lacks a PR number) is skipped with a WARN
// and no GitHub call is made for it — it never falls back to the default repo
// (GH-5716). Skipped rows stay in the store for the expiry sweep. No-op when no
// store is attached.
func (h *GitHubHandler) Rehydrate(ctx context.Context) error {
	if h.store == nil {
		return nil
	}
	rows, err := h.store.LoadPendingApprovals()
	if err != nil {
		return fmt.Errorf("rehydrate: load pending approvals: %w", err)
	}
	now := time.Now()
	rehydrated := 0
	for _, row := range rows {
		// GH-4772: only rows this handler owns ("github-review" aliases to "github").
		if !ownsChannel(h.Name(), row.PreferredChannel) {
			continue
		}
		if row.ExpiresAt.Before(now) {
			_ = h.store.DeletePendingApproval(row.ID)
			continue
		}
		prNumber, ok := prNumberFromMetadata(row.Metadata)
		if !ok {
			h.log.Warn("skipping rehydrate of GitHub approval: pr_number missing from persisted metadata",
				slog.String("request_id", row.ID))
			continue
		}
		owner, repo, err := h.resolveTarget(row.Project)
		if err != nil {
			h.log.Warn("skipping rehydrate of GitHub approval: project does not resolve to a repo",
				slog.String("request_id", row.ID),
				slog.String("project", row.Project),
				slog.Any("error", err))
			continue
		}
		req := &Request{
			ID:               row.ID,
			TaskID:           row.TaskID,
			Stage:            Stage(row.Stage),
			Title:            row.Title,
			Description:      row.Description,
			Metadata:         row.Metadata,
			Approvers:        row.Approvers,
			PreferredChannel: row.PreferredChannel,
			Project:          row.Project,
			CreatedAt:        row.CreatedAt,
			ExpiresAt:        row.ExpiresAt,
		}
		pollCtx, cancelFn := context.WithCancel(ctx)
		h.mu.Lock()
		if _, exists := h.pending[req.ID]; exists {
			h.mu.Unlock()
			cancelFn()
			continue
		}
		h.pending[req.ID] = &githubPending{
			Request:    req,
			Owner:      owner,
			Repo:       repo,
			PRNumber:   prNumber,
			ResponseCh: make(chan *Response, 1),
			CancelFn:   cancelFn,
		}
		h.mu.Unlock()
		rehydrated++
		go h.pollForApproval(pollCtx, req.ID)
	}
	if rehydrated > 0 {
		h.log.Info("rehydrated pending approvals", slog.Int("count", rehydrated))
	}
	return nil
}

// PruneExpired stops polling for requests whose ExpiresAt has passed, removes
// them from the pending map and deletes their persisted row. It also sweeps the
// store for owned rows with no in-memory counterpart. A rehydrated request has
// no waiter enforcing its timeout, so without this sweep it would poll forever
// (GH-5757, mirrors Telegram/Slack PruneExpired). Returns the number of
// in-memory requests pruned.
func (h *GitHubHandler) PruneExpired(ctx context.Context) (int, error) {
	now := time.Now()

	h.mu.Lock()
	var expired []*githubPending
	for id, p := range h.pending {
		if p.Request.ExpiresAt.Before(now) {
			expired = append(expired, p)
			delete(h.pending, id)
		}
	}
	h.mu.Unlock()

	for _, p := range expired {
		p.CancelFn()
		h.deletePersisted(p.Request.ID, "expiry")
		select {
		case p.ResponseCh <- &Response{RequestID: p.Request.ID, Decision: DecisionTimeout, RespondedAt: now}:
		default:
		}
		close(p.ResponseCh)
	}

	if h.store != nil {
		if _, err := h.store.PrunePendingApprovals(now, ownedChannels(h.Name())); err != nil {
			return len(expired), fmt.Errorf("prune expired: sweep store: %w", err)
		}
	}

	if len(expired) > 0 {
		h.log.Info("pruned expired pending approvals", slog.Int("count", len(expired)))
	}
	return len(expired), nil
}

// SendApprovalRequest starts polling for PR approval reviews
func (h *GitHubHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	responseCh := make(chan *Response, 1)

	// Extract PR number from metadata
	prNumber, ok := prNumberFromMetadata(req.Metadata)
	if !ok {
		return nil, fmt.Errorf("pr_number missing from approval request metadata")
	}

	owner, repo, err := h.resolveTarget(req.Project)
	if err != nil {
		return nil, err
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

	// Best-effort persistence so the request survives a restart (GH-5757).
	if h.store != nil {
		row := &memory.PendingApproval{
			ID:               req.ID,
			TaskID:           req.TaskID,
			Stage:            string(req.Stage),
			Title:            req.Title,
			Description:      req.Description,
			Metadata:         req.Metadata,
			Approvers:        req.Approvers,
			PreferredChannel: req.PreferredChannel,
			Project:          req.Project,
			CreatedAt:        req.CreatedAt,
			ExpiresAt:        req.ExpiresAt,
		}
		if err := h.store.InsertPendingApproval(row); err != nil {
			h.log.Warn("failed to persist pending approval", slog.String("request_id", req.ID), slog.Any("error", err))
		}
	}

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

		// Remove from pending; a webhook event or cancel may have won the race
		// and already resolved (and closed) this request.
		h.mu.Lock()
		_, stillPending := h.pending[requestID]
		delete(h.pending, requestID)
		h.mu.Unlock()
		if !stillPending {
			return true
		}
		pending.CancelFn()

		// Decision: ctx is the poll ctx and CancelFn above cancels it, so the write
		// must run on a detached, bounded ctx — otherwise SetApprovalDecision fails
		// with "context canceled" and every polled approval is dropped (GH-5803).
		writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), approvalWriteTimeout)
		defer cancelWrite()
		h.resolvePending(writeCtx, pending, &Response{
			RequestID:   requestID,
			Decision:    DecisionApproved,
			ApprovedBy:  approver,
			RespondedAt: time.Now(),
		})

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

	h.deletePersisted(requestID, "cancel")

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

	h.resolvePending(ctx, foundPending, &Response{
		RequestID:   foundID,
		Decision:    DecisionApproved,
		ApprovedBy:  reviewer,
		RespondedAt: time.Now(),
	})

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

	h.resolvePending(ctx, foundPending, &Response{
		RequestID:   foundID,
		Decision:    DecisionRejected,
		ApprovedBy:  reviewer,
		Comment:     "Changes requested",
		RespondedAt: time.Now(),
	})

	return true
}
