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
	stateWriter   PRStateWriter // optional; persists decisions for async dispatch
	mu            sync.RWMutex
	log           *slog.Logger
}

// MergeNotifier is implemented by handlers that can post a merge-completion
// follow-up in the same channel/thread as an earlier approval decision.
// Currently only TelegramHandler implements it — Manager.NotifyMerged skips
// registered handlers that don't (GH-4164).
type MergeNotifier interface {
	NotifyMerged(ctx context.Context, requestID, shortSHA string) error
}

// destinationDescriber is implemented by handlers that can report the
// resolved human-readable destination for a request (channel name/ID or DM
// target) — for logging only, not routing. Currently only SlackHandler
// implements it; handlers that don't are logged with just their Name()
// (unchanged behavior). GH-4808: without this, the "async approval request
// submitted" log line recorded only the handler name ("channel=slack"), so
// diagnosing "where did the ask go" required Slack-side message search
// instead of reading daemon.log.
type destinationDescriber interface {
	ResolvedDestination(req *Request) string
}

// resolvedDestination returns handler's resolved destination for req when it
// implements destinationDescriber, otherwise "" (the caller's log line still
// carries handler.Name()).
func resolvedDestination(handler Handler, req *Request) string {
	dd, ok := handler.(destinationDescriber)
	if !ok {
		return ""
	}
	return dd.ResolvedDestination(req)
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

// WithStateWriter attaches a PRStateWriter so that RecordDecision and
// SubmitApprovalRequest can persist decisions to the execution store.
// Returns m to allow builder-style chaining.
func (m *Manager) WithStateWriter(w PRStateWriter) *Manager {
	m.stateWriter = w
	return m
}

// SubmitApprovalRequest dispatches an approval request without blocking.
// It sends the request through the appropriate handler and returns the
// request ID immediately. The decision is later recorded via RecordDecision
// (called externally, e.g. from a Telegram callback) or by the background
// goroutine when the handler's response channel fires.
//
// Decision retrieval: RecordDecision persists the outcome to the executions
// table via PRStateWriter. Callers should poll PRState.ApprovalDecision on
// subsequent ticks rather than awaiting a channel (see controller.handleAwaitApproval).
func (m *Manager) SubmitApprovalRequest(ctx context.Context, req *Request) (string, error) {
	stageEnabled := m.IsStageEnabled(req.Stage)
	ruleFired := false
	if !stageEnabled {
		if rule := m.checkRuleTriggers(req); rule != nil {
			ruleFired = true
			m.log.Info("rule-triggered async approval",
				slog.String("rule", rule.Name),
				slog.String("task_id", req.TaskID))
		}
	}
	if !stageEnabled && !ruleFired {
		m.log.Debug("async submit: auto-approved (stage disabled)",
			slog.String("task_id", req.TaskID),
			slog.String("stage", string(req.Stage)))
		return req.ID, nil
	}

	stageConfig := m.getStageConfig(req.Stage)
	if stageConfig == nil {
		stageConfig = &StageConfig{
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
	var handler Handler
	preferredMissing := false
	if req.PreferredChannel != "" {
		// GH-4772: normalize aliases (e.g. the autopilot config value
		// "github-review" -> the GitHubHandler's registered Name()
		// "github") before lookup, so an alias never hits the hard-error
		// path below just because it doesn't literally match a handler's
		// Name(). GH-4380 semantics are unchanged: a channel that's still
		// unresolvable after normalization is a hard error, never a silent
		// fallback.
		if h, ok := m.handlers[normalizeChannelName(req.PreferredChannel)]; ok {
			handler = h
		} else {
			preferredMissing = true
		}
	} else {
		for _, h := range m.handlers {
			handler = h
			break
		}
	}
	m.mu.RUnlock()

	// GH-4380: a request with an explicit PreferredChannel must go through
	// that channel or fail loudly — never a silent, nondeterministic fallback
	// to whatever handler wins Go's map iteration order. The old "WARN + pick
	// any registered handler" behavior is how a request configured for Slack
	// (approval_source: slack) silently landed on Telegram with a Slack-shaped
	// destination in req.Approvers, producing a per-tick "chat not found" 400
	// instead of a single actionable error.
	if preferredMissing {
		return "", fmt.Errorf("approval channel %q is not registered (check adapters.%s.enabled + credentials, and adapters.%s.approval.enabled)",
			req.PreferredChannel, req.PreferredChannel, req.PreferredChannel)
	}

	if handler == nil {
		m.log.Warn("no approval handlers registered for async dispatch",
			slog.String("task_id", req.TaskID))
		return req.ID, nil
	}

	dispatchCtx, cancel := context.WithTimeout(context.Background(), timeout)

	responseCh, err := handler.SendApprovalRequest(dispatchCtx, req)
	if err != nil {
		cancel()
		return "", fmt.Errorf("failed to send approval request: %w", err)
	}

	m.mu.Lock()
	m.pending[req.ID] = &pendingRequest{
		Request:    req,
		ResponseCh: make(chan *Response, 1),
		Handler:    handler,
		CancelFn:   cancel,
	}
	m.mu.Unlock()

	defaultAction := stageConfig.DefaultAction
	logging.SafeGo("approval-manager", func() {
		defer cancel()
		select {
		case resp, ok := <-responseCh:
			if ok && resp != nil {
				m.mu.Lock()
				_, wasPending := m.pending[req.ID]
				if wasPending {
					delete(m.pending, req.ID)
				}
				m.mu.Unlock()
				// Skip write if RecordDecision already handled this request.
				if wasPending && m.stateWriter != nil {
					if werr := m.stateWriter.SetApprovalDecision(context.Background(), req.ID, string(resp.Decision), resp.ApprovedBy); werr != nil {
						m.log.Warn("async: failed to persist decision",
							slog.String("request_id", req.ID), slog.Any("error", werr))
					}
				}
				if wasPending {
					m.log.Info("async approval response recorded",
						slog.String("request_id", req.ID),
						slog.String("decision", string(resp.Decision)),
						slog.String("by", resp.ApprovedBy))
				}
			}
		case <-dispatchCtx.Done():
			_ = handler.CancelRequest(context.Background(), req.ID)
			m.mu.Lock()
			_, wasPending := m.pending[req.ID]
			if wasPending {
				delete(m.pending, req.ID)
			}
			m.mu.Unlock()
			// Skip write if RecordDecision already resolved this request.
			if wasPending {
				if m.stateWriter != nil {
					if werr := m.stateWriter.SetApprovalDecision(context.Background(), req.ID, string(defaultAction), "system"); werr != nil {
						m.log.Warn("async: failed to persist timeout decision",
							slog.String("request_id", req.ID), slog.Any("error", werr))
					}
				}
				m.log.Warn("async approval timed out",
					slog.String("request_id", req.ID),
					slog.String("default_action", string(defaultAction)))
			}
		}
	})

	m.log.Info("async approval request submitted",
		slog.String("request_id", req.ID),
		slog.String("task_id", req.TaskID),
		slog.String("channel", handler.Name()),
		slog.String("destination", resolvedDestination(handler, req)),
		slog.Duration("timeout", timeout))

	return req.ID, nil
}

// PreMergeTimeout returns the effective timeout for the pre-merge approval stage.
func (m *Manager) PreMergeTimeout() time.Duration {
	if m.config == nil {
		return 24 * time.Hour
	}
	if m.config.PreMerge != nil && m.config.PreMerge.Timeout > 0 {
		return m.config.PreMerge.Timeout
	}
	if m.config.DefaultTimeout > 0 {
		return m.config.DefaultTimeout
	}
	return 24 * time.Hour
}

// PreMergeDefaultAction returns the default decision on timeout for the pre-merge stage.
func (m *Manager) PreMergeDefaultAction() Decision {
	if m.config == nil {
		return DecisionRejected
	}
	if m.config.PreMerge != nil && m.config.PreMerge.DefaultAction != "" {
		return m.config.PreMerge.DefaultAction
	}
	return m.config.DefaultAction
}

// NotifyMerged forwards a merge-completion follow-up for requestID to every
// registered handler that implements MergeNotifier. Best-effort: a handler
// failure is logged, not returned, so a Telegram/Slack hiccup can never fail
// the caller's merge-transition handling (GH-4164).
func (m *Manager) NotifyMerged(ctx context.Context, requestID, shortSHA string) {
	m.mu.RLock()
	handlers := make([]Handler, 0, len(m.handlers))
	for _, h := range m.handlers {
		handlers = append(handlers, h)
	}
	m.mu.RUnlock()

	for _, h := range handlers {
		notifier, ok := h.(MergeNotifier)
		if !ok {
			continue
		}
		if err := notifier.NotifyMerged(ctx, requestID, shortSHA); err != nil {
			m.log.Warn("merge follow-up notification failed",
				slog.String("request_id", requestID),
				slog.String("channel", h.Name()),
				slog.Any("error", err))
		}
	}
}

// RecordDecision records the outcome of a pending approval request.
// It persists the decision via the state writer (if configured) and cancels
// any background goroutine waiting on this request. Safe to call from
// handler callbacks (e.g. Telegram button taps).
//
// GH-4777: waiter cleanup (the pending-map delete + CancelFn below) must run
// even when the writer rejects the decision (e.g. memory.ErrApprovalAlreadyDecided
// from a lost race) — the writer error is still returned to the caller, but a
// race LOSER must not leave its channel button armed or its background
// goroutine alive just because the write itself failed. Previously this
// early-returned on writer error, before cleanup ever ran (PR#4767 review).
func (m *Manager) RecordDecision(ctx context.Context, requestID string, decision Decision, by string) error {
	var writerErr error
	if m.stateWriter != nil {
		if err := m.stateWriter.SetApprovalDecision(ctx, requestID, string(decision), by); err != nil {
			writerErr = fmt.Errorf("record decision: %w", err)
		}
	}

	m.mu.Lock()
	pr, exists := m.pending[requestID]
	if exists {
		delete(m.pending, requestID)
	}
	m.mu.Unlock()

	if exists {
		// Cancel the background goroutine waiting on the handler response.
		pr.CancelFn()
		if writerErr != nil {
			m.log.Info("approval decision race lost — waiter cleaned up without applying decision",
				slog.String("request_id", requestID),
				slog.String("decision", string(decision)),
				slog.String("by", by),
				slog.Any("error", writerErr))
		} else {
			m.log.Info("approval decision recorded",
				slog.String("request_id", requestID),
				slog.String("decision", string(decision)),
				slog.String("by", by))
		}
	}

	return writerErr
}
