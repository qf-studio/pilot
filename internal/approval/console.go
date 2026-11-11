package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// consoleChannelName is the Handler.Name() of ConsoleHandler and the
// `approval_source` config spelling that routes to it (no alias).
const consoleChannelName = "console"

// ConsoleHandler is the approval channel of a hosted tenant: it stores the
// request in the pending-approval store and sends nothing. The hosted console
// lists the row through GET /api/v1/approvals and decides it through
// POST /api/v1/approvals/{requestId}/decision, which calls
// Manager.RecordDecision — so the handler holds no goroutine, no network
// client and no in-memory pending map (the store is the only state, which also
// makes a restart a no-op: there is nothing to rehydrate).
type ConsoleHandler struct {
	store PendingApprovalStore
	log   *slog.Logger
}

// NewConsoleHandler creates a console approval handler. Attach the store with
// WithStore before registering it.
func NewConsoleHandler() *ConsoleHandler {
	return &ConsoleHandler{log: logging.WithComponent("approval.console")}
}

// Name returns the handler name
func (h *ConsoleHandler) Name() string {
	return consoleChannelName
}

// WithStore attaches the pending-approval store the gateway routes read.
// Returns h to allow builder-style chaining.
func (h *ConsoleHandler) WithStore(store PendingApprovalStore) *ConsoleHandler {
	h.store = store
	return h
}

// SendApprovalRequest inserts the pending-approval row and returns a channel
// that is never written to or closed: the decision does not arrive on it, it
// arrives through the gateway route and Manager.RecordDecision. Unlike the
// other handlers, a failed insert is an error — the row is the only way the
// request can reach the console, so a handler that logged and carried on would
// leave the controller waiting out the timeout on a request nobody can see.
func (h *ConsoleHandler) SendApprovalRequest(_ context.Context, req *Request) (<-chan *Response, error) {
	if h.store == nil {
		return nil, errors.New("console approval handler has no pending-approval store")
	}
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
		return nil, fmt.Errorf("store console approval request: %w", err)
	}
	h.log.Info("approval request stored for the console",
		slog.String("request_id", req.ID),
		slog.String("task_id", req.TaskID),
		slog.String("stage", string(req.Stage)))
	return make(chan *Response), nil
}

// CancelRequest deletes the request's row. Deleting an absent row is not an
// error (a decision through the gateway route has usually removed it already).
func (h *ConsoleHandler) CancelRequest(_ context.Context, requestID string) error {
	if h.store == nil {
		return nil
	}
	if err := h.store.DeletePendingApproval(requestID); err != nil {
		return fmt.Errorf("delete console approval request: %w", err)
	}
	return nil
}

// PruneExpired deletes the console rows whose expiry has passed. It never
// touches another channel's rows (see ownedChannels). Returns the number of
// rows deleted.
func (h *ConsoleHandler) PruneExpired(_ context.Context) (int, error) {
	if h.store == nil {
		return 0, nil
	}
	n, err := h.store.PrunePendingApprovals(time.Now(), ownedChannels(h.Name()))
	if err != nil {
		return 0, fmt.Errorf("prune expired: sweep store: %w", err)
	}
	if n > 0 {
		h.log.Info("pruned expired console approvals", slog.Int64("count", n))
	}
	return int(n), nil
}
