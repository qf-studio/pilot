package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Manager coordinates approval workflows across multiple channels
type Manager struct {
	config        *Config
	handlers      map[string]Handler // Channel name -> handler
	pending       map[string]*pendingRequest
	ruleEvaluator *RuleEvaluator
	mu            sync.RWMutex
	log           *slog.Logger
}

// pendingRequest tracks an active approval request
type pendingRequest struct {
	Request    *Request
	ResponseCh chan *Response
	Handler    Handler
	CancelFn   context.CancelFunc
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

// RegisterHandler registers an approval handler for a channel
func (m *Manager) RegisterHandler(handler Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[handler.Name()] = handler
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

// RequestApproval sends an approval request and waits for response
// Returns the decision and any error
func (m *Manager) RequestApproval(ctx context.Context, req *Request) (*Response, error) {
	if !m.IsStageEnabled(req.Stage) {
		// Check rule-based triggers before auto-approving
		if rule := m.checkRuleTriggers(req); rule != nil {
			m.log.Info("Rule-triggered approval required",
				slog.String("rule", rule.Name),
				slog.String("task_id", req.TaskID),
				slog.String("stage", string(req.Stage)))
			// Fall through to normal approval flow
		} else {
			// Stage not enabled and no rule triggered, auto-approve
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

	// Get stage config (use defaults if rule-triggered but no stage config exists)
	stageConfig := m.getStageConfig(req.Stage)
	if stageConfig == nil {
		stageConfig = &StageConfig{
			Enabled:       false,
			Timeout:       m.config.DefaultTimeout,
			DefaultAction: m.config.DefaultAction,
		}
	}

	// Set expiration based on stage timeout
	timeout := stageConfig.Timeout
	if timeout == 0 {
		timeout = m.config.DefaultTimeout
	}
	req.ExpiresAt = time.Now().Add(timeout)

	// Set approvers from config if not specified
	if len(req.Approvers) == 0 {
		req.Approvers = stageConfig.Approvers
	}

	// Find available handler
	m.mu.RLock()
	var handler Handler
	for _, h := range m.handlers {
		handler = h // Use first available handler
		break
	}
	m.mu.RUnlock()

	if handler == nil {
		// No handlers registered - use default action
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

	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Send request through handler
	responseCh, err := handler.SendApprovalRequest(timeoutCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to send approval request: %w", err)
	}

	// Track pending request
	m.mu.Lock()
	m.pending[req.ID] = &pendingRequest{
		Request:    req,
		ResponseCh: make(chan *Response, 1),
		Handler:    handler,
		CancelFn:   cancel,
	}
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, req.ID)
		m.mu.Unlock()
	}()

	// Wait for response or timeout
	select {
	case resp := <-responseCh:
		m.log.Info("Approval response received",
			slog.String("request_id", req.ID),
			slog.String("decision", string(resp.Decision)),
			slog.String("approved_by", resp.ApprovedBy))
		return resp, nil

	case <-timeoutCtx.Done():
		// Timeout - use default action
		m.log.Warn("Approval request timed out",
			slog.String("request_id", req.ID),
			slog.String("task_id", req.TaskID),
			slog.String("default_action", string(stageConfig.DefaultAction)))

		// Cancel the pending request
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

	// Build rule context from request metadata
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

	// Only evaluate rules for the request's stage
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
		pr.CancelFn()
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
