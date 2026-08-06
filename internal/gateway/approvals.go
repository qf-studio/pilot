package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// SetDecisionRecorder wires the DecisionRecorder seam (typically *approval.Manager)
// used by POST /api/v1/approvals/{requestId}/decision (GH-4748 / C14 pilot write
// leg). Must be wired at both call sites Telegram/Slack use WithDecisionRecorder —
// gateway-mode and polling-mode in cmd/pilot/main.go — or the endpoint 503s in
// whichever mode is missing it. Until set, decisions cannot be recorded.
func (s *Server) SetDecisionRecorder(r approval.DecisionRecorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisionRecorder = r
}

// approvalResponse is one entry in GET /api/v1/approvals. ExecutionID, ProjectPath,
// PRNumber, and PRUrl are nullable: the approval_pending row (source of the pending
// list) has no execution linkage until SetApprovalRequestID runs, and that call is
// best-effort (see memory.Store.SetApprovalRequestID), so the join can legitimately
// miss.
type approvalResponse struct {
	RequestID   string    `json:"requestId"`
	ExecutionID *string   `json:"executionId"`
	TaskID      string    `json:"taskId"`
	ProjectPath *string   `json:"projectPath"`
	PRNumber    *int      `json:"prNumber"`
	PRUrl       *string   `json:"prUrl"`
	RequestedAt time.Time `json:"requestedAt"`
}

// decisionRequestBody is the POST /api/v1/approvals/{requestId}/decision body.
type decisionRequestBody struct {
	Decision string `json:"decision"`
	By       string `json:"by"`
}

// decisionResponseBody echoes the recorded decision.
type decisionResponseBody struct {
	RequestID string    `json:"requestId"`
	Decision  string    `json:"decision"`
	By        string    `json:"by"`
	DecidedAt time.Time `json:"decidedAt"`
}

// handleApprovals returns pending approval requests (GH-4748 / C14 pilot read leg).
// Pending = rows in approval_pending not yet expired — the same predicate
// TelegramHandler.Rehydrate / SlackHandler.Rehydrate use to decide what to
// re-notify after a restart (internal/approval/telegram.go, .../slack.go).
// Reused here rather than re-derived, per the task's explicit instruction not to
// invent a new pending predicate.
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	rows, err := store.LoadPendingApprovals()
	if err != nil {
		http.Error(w, "failed to fetch approvals", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	out := make([]approvalResponse, 0, len(rows))
	for _, row := range rows {
		if row.ExpiresAt.Before(now) {
			continue // expired, not yet pruned — same skip Rehydrate applies
		}

		resp := approvalResponse{
			RequestID:   row.ID,
			TaskID:      row.TaskID,
			RequestedAt: row.CreatedAt,
		}

		exec, execErr := store.GetExecutionByApprovalRequestID(row.ID)
		if execErr == nil && exec != nil {
			if projectPath != "" && exec.ProjectPath != projectPath {
				continue
			}
			execID := exec.ID
			resp.ExecutionID = &execID
			pp := exec.ProjectPath
			resp.ProjectPath = &pp
			if exec.PRUrl != "" {
				prURL := exec.PRUrl
				resp.PRUrl = &prURL
				if n := parsePRNumberFromURL(prURL); n > 0 {
					resp.PRNumber = &n
				}
			}
		} else if projectPath != "" {
			// Can't attribute this request to the scoped project (no execution
			// linkage yet, or the lookup failed) — exclude rather than leak a
			// possibly cross-project pending approval.
			continue
		}

		out = append(out, resp)
	}

	writeJSON(w, out)
}

// handleApprovalDecision records an approval/rejection through the DecisionRecorder
// seam (GH-4748 / C14 pilot write leg). Delegates to Manager.RecordDecision — the
// same call Telegram/Slack button callbacks make — so in-process waiter cleanup
// (cancelling the goroutine blocked in SubmitApprovalRequest, if one is still
// alive) matches a channel decision exactly; a direct store write would skip that.
// Also deletes the persisted approval_pending row, mirroring the channel handlers'
// own decision path (they own that store via WithStore(); Manager does not), so a
// later Rehydrate() doesn't re-notify a request already decided over HTTP.
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestId")
	if requestID == "" {
		http.Error(w, "missing requestId", http.StatusBadRequest)
		return
	}

	var body decisionRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	var decision approval.Decision
	switch body.Decision {
	case "approve":
		decision = approval.DecisionApproved
	case "reject":
		decision = approval.DecisionRejected
	default:
		http.Error(w, `decision must be "approve" or "reject"`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.By) == "" {
		http.Error(w, "missing by", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	recorder := s.decisionRecorder
	s.mu.RUnlock()

	if recorder == nil {
		http.Error(w, "decision recorder not configured", http.StatusServiceUnavailable)
		return
	}
	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	pending, err := findPendingApproval(store, requestID)
	if err != nil {
		http.Error(w, "failed to look up approval", http.StatusInternalServerError)
		return
	}
	if pending == nil {
		// Not currently pending — tell "never existed"/"expired" (404) apart from
		// "already decided" (409) using the execution row's recorded decision.
		exec, execErr := store.GetExecutionByApprovalRequestID(requestID)
		if execErr == nil && exec != nil && exec.ApprovalDecision != "" {
			http.Error(w, "approval already decided", http.StatusConflict)
			return
		}
		http.Error(w, "unknown approval request", http.StatusNotFound)
		return
	}

	now := time.Now()
	if err := recorder.RecordDecision(r.Context(), requestID, decision, body.By); err != nil {
		http.Error(w, "failed to record decision", http.StatusInternalServerError)
		return
	}

	if err := store.DeletePendingApproval(requestID); err != nil {
		logging.WithComponent("gateway").Warn("failed to delete persisted approval after HTTP decision",
			slog.String("request_id", requestID), slog.Any("error", err))
	}

	writeJSON(w, decisionResponseBody{
		RequestID: requestID,
		Decision:  body.Decision,
		By:        body.By,
		DecidedAt: now,
	})
}

// findPendingApproval returns the approval_pending row for requestID, or nil if not
// found or already expired — mirroring the Rehydrate() predicate (see
// handleApprovals doc). A small linear scan over LoadPendingApprovals is
// intentional: the pending set is operator-facing ("needs you") and expected to
// stay small, matching how Rehydrate itself walks the full set.
func findPendingApproval(store DashboardStore, requestID string) (*memory.PendingApproval, error) {
	rows, err := store.LoadPendingApprovals()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, row := range rows {
		if row.ID != requestID {
			continue
		}
		if row.ExpiresAt.Before(now) {
			return nil, nil
		}
		return row, nil
	}
	return nil, nil
}

// parsePRNumberFromURL extracts a PR number from a GitHub PR URL (".../pull/123").
// Returns 0 if the URL doesn't contain one. Deliberately duplicated from
// internal/executor.parsePRNumberFromURL rather than imported, to avoid pulling
// the executor package into gateway for one helper.
func parsePRNumberFromURL(url string) int {
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return 0
	}
	numStr := strings.TrimSpace(url[idx+len("/pull/"):])
	if slashIdx := strings.Index(numStr, "/"); slashIdx >= 0 {
		numStr = numStr[:slashIdx]
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}
