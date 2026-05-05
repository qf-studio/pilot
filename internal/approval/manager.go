package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// Manager coordinates approval workflows across multiple channels
type Manager struct {
	config        *Config
	handlers      map[string]Handler // Channel name -> handler
	pending       map[string]*pendingRequest
	ruleEvaluator *RuleEvaluator
	stateWriter   PRStateWriter // optional; nil OK
	mu            sync.RWMutex
	log           *slog.Logger
}

// pendingRequest tracks an active approval request.
// ResponseCh is set only on the RequestApproval (blocking-compat) path.
type pendingRequest struct {
	Request    *Request
	Handler    Handler
	ResponseCh chan *Response // non-nil when RequestApproval is waiting synchronously
	CancelFn   context.CancelFunc
}

// recorderSetter is implemented by handlers that store the manager's recorder
// as their default callback (used by Rehydrate and future handler-initiated flows).
type recorderSetter interface {
	setRecorder(RecordDecisionFunc)
}

// NewManager creates a new approval manager.
// If the config contains rules, a RuleEvaluator is automatically initialized.
func NewManager(config *Config) *Manager {
	if config == nil {
		config = DefaultConfig()
	}
	m := &Manager{
		config:   config,
		handlers: make(map[string]Handler),
		pending:  make(map[string]*pendingRequest),
		log:      logging.WithComponent("approval"),
	}

	// Wire rule evaluator from config rules
	if len(config.Rules) > 0 {
		m.ruleEvaluator = NewRuleEvaluator(config.Rules)
		m.log.Info("Initialized rule evaluator",
			slog.Int("rule_count", len(config.Rules)))
	}

	return m
}

// WithStateWriter attaches a PRStateWriter for persisting decisions.
// Returns m to allow builder-style chaining.
func (m *Manager) WithStateWriter(w PRStateWriter) *Manager {
	m.stateWriter = w
	return m
}

// RegisterHandler registers an approval handler for a channel.
// If the handler implements recorderSetter, the manager's RecordDecision is
// wired as its default recorder so Rehydrate can call back correctly.
func (m *Manager) RegisterHandler(handler Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[handler.Name()] = handler
	if rs, ok := handler.(recorderSetter); ok {
		rs.setRecorder(m.RecordDecision)
	}
	m.log.Debug("Registered approval handler", slog.String("channel", handler.Name()))
}

// IsEnabled returns true if approval workflows are enabled
func (m *Manager) IsEnabled() bool {
	return m.config != nil && m.config.Enabled
}

// IsStageEnabled checks if a specific stage requires approval
func (m *Manager) IsStageEnabled(stage Stage) bool {
	if !m.IsEnabled() {
		return false
	}

	switch stage {
	case StagePreExecution:
		return m.config.PreExecution != nil && m.config.PreExecution.Enabled
	case StagePreMerge:
		return m.config.PreMerge != nil && m.config.PreMerge.Enabled
	case StagePostFailure:
		return m.config.PostFailure != nil && m.config.PostFailure.Enabled
	default:
		return false
	}
}

// SetRuleEvaluator configures rule-based approval triggers
func (m *Manager) SetRuleEvaluator(re *RuleEvaluator) {
	m.ruleEvaluator = re
}

// ShouldRequireApproval checks if any rule requires approval for the given context.
// Returns the matching rule or nil if no rule triggers.
func (m *Manager) ShouldRequireApproval(ruleCtx RuleContext) *Rule {
	if m.ruleEvaluator == nil {
		return nil
	}
	return m.ruleEvaluator.Evaluate(ruleCtx)
}

// SubmitApprovalRequest sends an approval request non-blocking and returns the
// request ID. The manager calls RecordDecision when the user responds.
func (m *Manager) SubmitApprovalRequest(ctx context.Context, req *Request) (string, error) {
	if !m.IsStageEnabled(req.Stage) {
		if rule := m.checkRuleTriggers(req); rule != nil {
			m.log.Info("Rule-triggered approval required",
				slog.String("rule", rule.Name),
				slog.String("task_id", req.TaskID),
				slog.String("stage", string(req.Stage)))
		} else {
			m.log.Debug("Auto-approving (stage disabled)",
				slog.String("task_id", req.TaskID),
				slog.String("stage", string(req.Stage)))
			// For the non-blocking path, immediately record the auto-approval.
			_ = m.RecordDecision(ctx, req.ID, DecisionApproved, "system")
			return req.ID, nil
		}
	}

	stageConfig := m.getStageConfig(req.Stage)
	if stageConfig == nil {
		stageConfig = &StageConfig{
			Enabled:       false,
			Timeout:       m.config.DefaultTimeout,
			DefaultAction: m.config.DefaultAction,
		}
	}

	timeout := stageConfig.Timeout
	if timeout == 0 {
		timeout = m.config.DefaultTimeout
	}
	req.ExpiresAt = time.Now().Add(timeout)

	if len(req.Approvers) == 0 {
		req.Approvers = stageConfig.Approvers
	}

	m.mu.RLock()
	handler := m.selectHandler(req)
	m.mu.RUnlock()

	if handler == nil {
		m.log.Warn("No approval handlers registered, using default action",
			slog.String("task_id", req.TaskID),
			slog.String("stage", string(req.Stage)),
			slog.String("default_action", string(stageConfig.DefaultAction)))
		_ = m.RecordDecision(ctx, req.ID, stageConfig.DefaultAction, "system")
		return req.ID, nil
	}

	m.log.Info("Submitting approval request",
		slog.String("request_id", req.ID),
		slog.String("task_id", req.TaskID),
		slog.String("stage", string(req.Stage)),
		slog.String("channel", handler.Name()),
		slog.Duration("timeout", timeout))

	m.mu.Lock()
	m.pending[req.ID] = &pendingRequest{
		Request: req,
		Handler: handler,
	}
	m.mu.Unlock()

	if err := handler.SendApprovalRequest(ctx, req, m.RecordDecision); err != nil {
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
		return "", fmt.Errorf("failed to send approval request: %w", err)
	}

	return req.ID, nil
}

// RecordDecision records a user's approval decision.
// It is the callback target for all handler implementations.
// It writes the decision to the stateWriter (if set) and signals any
// synchronous waiter from the RequestApproval compat path.
func (m *Manager) RecordDecision(ctx context.Context, requestID string, decision Decision, by string) error {
	m.mu.Lock()
	pending, exists := m.pending[requestID]
	if exists {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()

	m.log.Info("approval decision recorded",
		slog.String("request_id", requestID),
		slog.String("decision", string(decision)),
		slog.String("by", by))

	// Signal synchronous waiter (RequestApproval blocking-compat path)
	if pending != nil && pending.ResponseCh != nil {
		response := &Response{
			RequestID:   requestID,
			Decision:    decision,
			ApprovedBy:  by,
			RespondedAt: time.Now(),
		}
		select {
		case pending.ResponseCh <- response:
		default:
		}
	}

	// Persist to state — also handles rehydrated entries with no manager-side pending.
	if m.stateWriter != nil {
		if err := m.stateWriter.SetApprovalDecision(ctx, requestID, decision, by); err != nil {
			m.log.Warn("RecordDecision: state write failed",
				slog.String("request_id", requestID), slog.Any("error", err))
			return fmt.Errorf("record decision: state write: %w", err)
		}
	}

	return nil
}

// RequestApproval sends an approval request and blocks until a response is
// received or the context/timeout expires. Kept for backward compatibility
// with callers that need synchronous approval (e.g., AutoMerger).
func (m *Manager) RequestApproval(ctx context.Context, req *Request) (*Response, error) {
	if !m.IsStageEnabled(req.Stage) {
		if rule := m.checkRuleTriggers(req); rule != nil {
			m.log.Info("Rule-triggered approval required",
				slog.String("rule", rule.Name),
				slog.String("task_id", req.TaskID),
				slog.String("stage", string(req.Stage)))
		} else {
			m.log.Debug("Auto-approving (stage disabled)",
				slog.String("task_id", req.TaskID),
				slog.String("stage", string(req.Stage)))
			return &Response{
				RequestID:   req.ID,
				Decision:    DecisionApproved,
				ApprovedBy:  "system",
				Comment:     "Auto-approved: stage not enabled",
				RespondedAt: time.Now(),
			}, nil
		}
	}

	stageConfig := m.getStageConfig(req.Stage)
	if stageConfig == nil {
		stageConfig = &StageConfig{
			Enabled:       false,
			Timeout:       m.config.DefaultTimeout,
			DefaultAction: m.config.DefaultAction,
		}
	}

	timeout := stageConfig.Timeout
	if timeout == 0 {
		timeout = m.config.DefaultTimeout
	}
	req.ExpiresAt = time.Now().Add(timeout)

	if len(req.Approvers) == 0 {
		req.Approvers = stageConfig.Approvers
	}

	m.mu.RLock()
	handler := m.selectHandler(req)
	m.mu.RUnlock()

	if handler == nil {
		m.log.Warn("No approval handlers registered, using default action",
			slog.String("task_id", req.TaskID),
			slog.String("stage", string(req.Stage)),
			slog.String("default_action", string(stageConfig.DefaultAction)))
		return &Response{
			RequestID:   req.ID,
			Decision:    stageConfig.DefaultAction,
			ApprovedBy:  "system",
			Comment:     "No approval handlers configured",
			RespondedAt: time.Now(),
		}, nil
	}

	m.log.Info("Requesting approval",
		slog.String("request_id", req.ID),
		slog.String("task_id", req.TaskID),
		slog.String("stage", string(req.Stage)),
		slog.String("channel", handler.Name()),
		slog.Duration("timeout", timeout))

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	responseCh := make(chan *Response, 1)

	m.mu.Lock()
	m.pending[req.ID] = &pendingRequest{
		Request:    req,
		Handler:    handler,
		ResponseCh: responseCh,
		CancelFn:   cancel,
	}
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
	}()

	if err := handler.SendApprovalRequest(timeoutCtx, req, m.RecordDecision); err != nil {
		return nil, fmt.Errorf("failed to send approval request: %w", err)
	}

	select {
	case resp := <-responseCh:
		m.log.Info("Approval response received",
			slog.String("request_id", req.ID),
			slog.String("decision", string(resp.Decision)),
			slog.String("approved_by", resp.ApprovedBy))
		return resp, nil

	case <-timeoutCtx.Done():
		m.log.Warn("Approval request timed out",
			slog.String("request_id", req.ID),
			slog.String("task_id", req.TaskID),
			slog.String("default_action", string(stageConfig.DefaultAction)))

		_ = handler.CancelRequest(ctx, req.ID)

		return &Response{
			RequestID:   req.ID,
			Decision:    stageConfig.DefaultAction,
			ApprovedBy:  "system",
			Comment:     "Approval timed out",
			RespondedAt: time.Now(),
		}, nil
	}
}

// selectHandler picks the handler for a request (must be called with m.mu held for reading).
func (m *Manager) selectHandler(req *Request) Handler {
	if req.PreferredChannel != "" {
		if h, ok := m.handlers[req.PreferredChannel]; ok {
			return h
		}
		m.log.Warn("preferred approval channel not registered, falling back to first-available",
			slog.String("preferred_channel", req.PreferredChannel),
			slog.String("task_id", req.TaskID))
	}
	for _, h := range m.handlers {
		return h
	}
	return nil
}

// getStageConfig returns the configuration for a specific stage
func (m *Manager) getStageConfig(stage Stage) *StageConfig {
	switch stage {
	case StagePreExecution:
		return m.config.PreExecution
	case StagePreMerge:
		return m.config.PreMerge
	case StagePostFailure:
		return m.config.PostFailure
	default:
		return nil
	}
}

// checkRuleTriggers evaluates rule-based triggers from request metadata.
// Returns the matching rule or nil.
func (m *Manager) checkRuleTriggers(req *Request) *Rule {
	if m.ruleEvaluator == nil || req.Metadata == nil {
		return nil
	}

	ruleCtx := RuleContext{
		TaskID: req.TaskID,
	}

	if v, ok := req.Metadata["consecutive_failures"]; ok {
		if n, ok := v.(int); ok {
			ruleCtx.ConsecutiveFailures = n
		}
	}

	if v, ok := req.Metadata["total_spend_cents"]; ok {
		if n, ok := v.(int); ok {
			ruleCtx.TotalSpendCents = n
		}
	}

	if v, ok := req.Metadata["complexity"]; ok {
		if s, ok := v.(string); ok {
			ruleCtx.Complexity = s
		}
	}

	if v, ok := req.Metadata["changed_files"]; ok {
		if files, ok := v.([]string); ok {
			ruleCtx.ChangedFiles = files
		}
	}

	return m.ruleEvaluator.EvaluateForStage(ruleCtx, req.Stage)
}

// CancelPending cancels all pending approval requests for a task
func (m *Manager) CancelPending(ctx context.Context, taskID string) {
	m.mu.Lock()
	toCancel := make([]*pendingRequest, 0)
	for _, pr := range m.pending {
		if pr.Request.TaskID == taskID {
			toCancel = append(toCancel, pr)
		}
	}
	m.mu.Unlock()

	for _, pr := range toCancel {
		if pr.CancelFn != nil {
			pr.CancelFn()
		}
		_ = pr.Handler.CancelRequest(ctx, pr.Request.ID)
		m.log.Debug("Cancelled pending approval",
			slog.String("request_id", pr.Request.ID),
			slog.String("task_id", taskID))
	}
}

// GetPendingRequests returns all pending approval requests
func (m *Manager) GetPendingRequests() []*Request {
	m.mu.RLock()
	defer m.mu.RUnlock()

	requests := make([]*Request, 0, len(m.pending))
	for _, pr := range m.pending {
		requests = append(requests, pr.Request)
	}
	return requests
}
