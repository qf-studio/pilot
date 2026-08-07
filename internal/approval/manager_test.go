package approval

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockPRStateWriter records SetApprovalDecision calls for test assertions.
type mockPRStateWriter struct {
	mu    sync.Mutex
	calls []struct{ requestID, decision, by string }
	err   error // returned by SetApprovalDecision, if non-nil
}

func (w *mockPRStateWriter) SetApprovalDecision(_ context.Context, requestID, decision, by string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, struct{ requestID, decision, by string }{requestID, decision, by})
	return w.err
}

func (w *mockPRStateWriter) getCalls() []struct{ requestID, decision, by string } {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]struct{ requestID, decision, by string }, len(w.calls))
	copy(result, w.calls)
	return result
}

// mockHandler is a test double for Handler
type mockHandler struct {
	name        string
	sentReqs    []*Request
	respondWith *Response
	cancelCalls []string
	mu          sync.Mutex
}

func (m *mockHandler) Name() string {
	return m.name
}

func (m *mockHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	m.mu.Lock()
	m.sentReqs = append(m.sentReqs, req)
	m.mu.Unlock()
	ch := make(chan *Response, 1)
	if m.respondWith != nil {
		go func() {
			time.Sleep(10 * time.Millisecond) // Simulate async response
			ch <- m.respondWith
		}()
	}
	return ch, nil
}

func (m *mockHandler) CancelRequest(ctx context.Context, requestID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls = append(m.cancelCalls, requestID)
	return nil
}

func TestManager_DisabledByDefault(t *testing.T) {
	m := NewManager(nil)
	if m.IsEnabled() {
		t.Error("expected manager to be disabled by default")
	}
}

func TestManager_IsStageEnabled(t *testing.T) {
	tests := []struct {
		name     string
		stage    Stage
		setup    func(*Config)
		expected bool
	}{
		{
			name:  "pre_execution disabled",
			stage: StagePreExecution,
			setup: func(c *Config) {
				c.Enabled = true
				c.PreExecution.Enabled = false
			},
			expected: false,
		},
		{
			name:  "pre_execution enabled",
			stage: StagePreExecution,
			setup: func(c *Config) {
				c.Enabled = true
				c.PreExecution.Enabled = true
			},
			expected: true,
		},
		{
			name:  "pre_merge enabled",
			stage: StagePreMerge,
			setup: func(c *Config) {
				c.Enabled = true
				c.PreMerge.Enabled = true
			},
			expected: true,
		},
		{
			name:  "post_failure enabled",
			stage: StagePostFailure,
			setup: func(c *Config) {
				c.Enabled = true
				c.PostFailure.Enabled = true
			},
			expected: true,
		},
		{
			name:  "global disabled overrides stage",
			stage: StagePreExecution,
			setup: func(c *Config) {
				c.Enabled = false
				c.PreExecution.Enabled = true
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			tt.setup(config)
			m := NewManager(config)

			if got := m.IsStageEnabled(tt.stage); got != tt.expected {
				t.Errorf("IsStageEnabled(%s) = %v, want %v", tt.stage, got, tt.expected)
			}
		})
	}
}

func TestManager_IsStageEnabled_UnknownStage(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true

	m := NewManager(config)

	// Unknown stage should return false
	if m.IsStageEnabled(Stage("unknown_stage")) {
		t.Error("expected unknown stage to return false")
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	// Verify basic structure
	if config == nil {
		t.Fatal("expected non-nil config")
	}

	if config.Enabled {
		t.Error("expected disabled by default")
	}

	if config.DefaultTimeout != 1*time.Hour {
		t.Errorf("expected default timeout 1 hour, got %v", config.DefaultTimeout)
	}

	if config.DefaultAction != DecisionRejected {
		t.Errorf("expected default action rejected, got %s", config.DefaultAction)
	}

	// Verify stage configs exist and are disabled
	stages := []struct {
		name   string
		config *StageConfig
	}{
		{"PreExecution", config.PreExecution},
		{"PreMerge", config.PreMerge},
		{"PostFailure", config.PostFailure},
	}

	for _, s := range stages {
		if s.config == nil {
			t.Errorf("expected %s config to be non-nil", s.name)
			continue
		}
		if s.config.Enabled {
			t.Errorf("expected %s to be disabled by default", s.name)
		}
		if s.config.DefaultAction != DecisionRejected {
			t.Errorf("expected %s default action to be rejected, got %s", s.name, s.config.DefaultAction)
		}
	}
}

func TestDecision_String(t *testing.T) {
	tests := []struct {
		decision Decision
		expected string
	}{
		{DecisionApproved, "approved"},
		{DecisionRejected, "rejected"},
		{DecisionTimeout, "timeout"},
	}

	for _, tt := range tests {
		if string(tt.decision) != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, string(tt.decision))
		}
	}
}

func TestStage_String(t *testing.T) {
	tests := []struct {
		stage    Stage
		expected string
	}{
		{StagePreExecution, "pre_execution"},
		{StagePreMerge, "pre_merge"},
		{StagePostFailure, "post_failure"},
	}

	for _, tt := range tests {
		if string(tt.stage) != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, string(tt.stage))
		}
	}
}

// --- Integration tests: rule evaluation wired into manager ---

func TestManager_NewManager_WiresRulesFromConfig(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.Rules = []Rule{
		{
			Name:      "fail-gate",
			Condition: Condition{Type: ConditionConsecutiveFailures, Threshold: 3},
			Stage:     StagePreExecution,
			Enabled:   true,
		},
	}

	m := NewManager(config)

	// RuleEvaluator should be set automatically
	if m.ruleEvaluator == nil {
		t.Fatal("expected rule evaluator to be initialized from config rules")
	}
}

func TestManager_NewManager_NoRules_NoEvaluator(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	// No rules

	m := NewManager(config)

	if m.ruleEvaluator != nil {
		t.Error("expected no rule evaluator when config has no rules")
	}
}

func TestManager_SubmitApprovalRequest_NonBlocking(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)

	handler := &mockHandler{name: "test"}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "async-nb-1",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	start := time.Now()
	requestID, err := m.SubmitApprovalRequest(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestID != req.ID {
		t.Errorf("expected request ID %q, got %q", req.ID, requestID)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("SubmitApprovalRequest should return immediately, took %v", elapsed)
	}
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected 1 request sent to handler, got %d", len(handler.sentReqs))
	}
}

// TestManager_SubmitApprovalRequest_PreferredChannelRouting verifies that a
// request with PreferredChannel set is routed to that handler even when
// multiple handlers are registered — never to whichever handler happens to
// win map iteration order (GH-4380).
func TestManager_SubmitApprovalRequest_PreferredChannelRouting(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)

	telegram := &mockHandler{name: "telegram"}
	slack := &mockHandler{name: "slack"}
	m.RegisterHandler(telegram)
	m.RegisterHandler(slack)

	req := &Request{
		ID:               "async-preferred-1",
		TaskID:           "TASK-01",
		Stage:            StagePreExecution,
		Title:            "Test task",
		CreatedAt:        time.Now(),
		PreferredChannel: "slack",
	}

	if _, err := m.SubmitApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(slack.sentReqs) != 1 {
		t.Errorf("expected 1 request sent to slack handler, got %d", len(slack.sentReqs))
	}
	if len(telegram.sentReqs) != 0 {
		t.Errorf("expected 0 requests sent to telegram handler, got %d", len(telegram.sentReqs))
	}
}

// TestManager_SubmitApprovalRequest_PreferredChannelMissing_FailsExplicitly
// verifies that when PreferredChannel names a handler that isn't registered,
// SubmitApprovalRequest returns a named error instead of silently falling
// back to an arbitrary registered handler. Before GH-4380, this fell through
// to whichever handler won Go's map iteration order — the root cause of an
// approval_source: slack request silently landing on Telegram with a
// Slack-shaped Approvers[0] as the chat destination.
func TestManager_SubmitApprovalRequest_PreferredChannelMissing_FailsExplicitly(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)

	telegram := &mockHandler{name: "telegram"}
	m.RegisterHandler(telegram)

	req := &Request{
		ID:               "async-preferred-missing-1",
		TaskID:           "TASK-01",
		Stage:            StagePreExecution,
		Title:            "Test task",
		CreatedAt:        time.Now(),
		PreferredChannel: "slack",
	}

	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when the preferred channel is not registered, got nil")
	}
	if !strings.Contains(err.Error(), "slack") {
		t.Errorf("expected error to name the missing channel %q, got: %v", "slack", err)
	}
	if len(telegram.sentReqs) != 0 {
		t.Errorf("expected no silent fallback to telegram handler, got %d requests", len(telegram.sentReqs))
	}
}

// TestManager_SubmitApprovalRequest_GitHubReviewAlias verifies the GH-4772
// fix: a request configured with PreferredChannel "github-review" (the
// autopilot.ApprovalSourceGitHubReview config value,
// internal/autopilot/types.go:36) resolves to the handler registered as
// "github" (GitHubHandler.Name()) instead of hitting the preferredMissing
// hard error just because the two names don't literally match.
func TestManager_SubmitApprovalRequest_GitHubReviewAlias(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)

	github := &mockHandler{name: "github"}
	m.RegisterHandler(github)

	req := &Request{
		ID:               "async-alias-1",
		TaskID:           "TASK-01",
		Stage:            StagePreExecution,
		Title:            "Test task",
		CreatedAt:        time.Now(),
		PreferredChannel: "github-review",
	}

	if _, err := m.SubmitApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(github.sentReqs) != 1 {
		t.Errorf("expected 1 request sent to the github handler via the github-review alias, got %d", len(github.sentReqs))
	}
}

// TestManager_SubmitApprovalRequest_UnresolvableChannelStillHardErrors
// verifies GH-4380 semantics are preserved after adding the github-review
// alias: a channel name that's still unresolvable after normalization must
// still fail loudly, never silently fall back to another registered handler.
func TestManager_SubmitApprovalRequest_UnresolvableChannelStillHardErrors(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)

	telegram := &mockHandler{name: "telegram"}
	m.RegisterHandler(telegram)

	req := &Request{
		ID:               "async-alias-missing-1",
		TaskID:           "TASK-01",
		Stage:            StagePreExecution,
		Title:            "Test task",
		CreatedAt:        time.Now(),
		PreferredChannel: "github-review",
	}

	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error when the aliased channel's target handler isn't registered, got nil")
	}
	if len(telegram.sentReqs) != 0 {
		t.Errorf("expected no silent fallback to telegram handler, got %d requests", len(telegram.sentReqs))
	}
}

// TestManager_SubmitApprovalRequest_NoPreferredChannel_FallsBackToFirstAvailable
// preserves the pre-existing behavior for requests that don't specify a
// PreferredChannel at all (e.g. legacy callers) — the fail-fast behavior only
// applies when PreferredChannel is explicitly set but unregistered.
func TestManager_SubmitApprovalRequest_NoPreferredChannel_FallsBackToFirstAvailable(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 5 * time.Second

	m := NewManager(config)
	handler := &mockHandler{name: "test"}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "async-no-preferred-1",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	if _, err := m.SubmitApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(handler.sentReqs) != 1 {
		t.Errorf("expected 1 request sent to handler, got %d", len(handler.sentReqs))
	}
}

func TestManager_SubmitApprovalRequest_AutoApprove_StageDisabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	// PreExecution disabled — should auto-approve without touching handler

	m := NewManager(config)
	handler := &mockHandler{name: "test"}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "async-auto-1",
		TaskID:    "TASK-02",
		Stage:     StagePreExecution,
		CreatedAt: time.Now(),
	}

	requestID, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestID != req.ID {
		t.Errorf("expected %q, got %q", req.ID, requestID)
	}
	if len(handler.sentReqs) != 0 {
		t.Errorf("expected no handler calls for auto-approve, got %d", len(handler.sentReqs))
	}
}

func TestManager_SubmitApprovalRequest_PersistsResponseViaWriter(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 2 * time.Second

	m := NewManager(config)
	writer := &mockPRStateWriter{}
	m.WithStateWriter(writer)

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "async-write-1",
			Decision:   DecisionApproved,
			ApprovedBy: "alice",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "async-write-1",
		TaskID:    "TASK-03",
		Stage:     StagePreExecution,
		CreatedAt: time.Now(),
	}

	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wait for background goroutine to record the decision
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls := writer.getCalls(); len(calls) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	calls := writer.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 writer call, got %d", len(calls))
	}
	if calls[0].requestID != "async-write-1" {
		t.Errorf("expected request ID async-write-1, got %s", calls[0].requestID)
	}
	if calls[0].decision != "approved" {
		t.Errorf("expected approved, got %s", calls[0].decision)
	}
	if calls[0].by != "alice" {
		t.Errorf("expected alice, got %s", calls[0].by)
	}
}

func TestManager_RecordDecision_PersistsViaWriter(t *testing.T) {
	m := NewManager(nil)
	writer := &mockPRStateWriter{}
	m.WithStateWriter(writer)

	err := m.RecordDecision(context.Background(), "req-42", DecisionApproved, "user123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := writer.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 writer call, got %d", len(calls))
	}
	if calls[0].requestID != "req-42" {
		t.Errorf("expected req-42, got %s", calls[0].requestID)
	}
	if calls[0].decision != "approved" {
		t.Errorf("expected approved, got %s", calls[0].decision)
	}
	if calls[0].by != "user123" {
		t.Errorf("expected user123, got %s", calls[0].by)
	}
}

// TestManager_RecordDecision_PropagatesWriterError verifies RecordDecision
// wraps (rather than swallows) an error from the state writer, so a typed
// sentinel like memory.ErrApprovalAlreadyDecided survives errors.Is() all
// the way up to the HTTP handler (GH-4757).
func TestManager_RecordDecision_PropagatesWriterError(t *testing.T) {
	m := NewManager(nil)
	sentinel := errors.New("already decided sentinel")
	writer := &mockPRStateWriter{err: sentinel}
	m.WithStateWriter(writer)

	err := m.RecordDecision(context.Background(), "req-conflict", DecisionApproved, "user123")
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
}

func TestManager_RecordDecision_NoWriter_NoError(t *testing.T) {
	m := NewManager(nil)
	// No state writer attached
	if err := m.RecordDecision(context.Background(), "req-no-writer", DecisionRejected, "bot"); err != nil {
		t.Errorf("unexpected error without writer: %v", err)
	}
}

// TestManager_RecordDecision_CleansUpPendingOnWriterError is the GH-4777
// regression test for the "waiter cleanup skipped on error" bug (PR#4767
// review, item 3): RecordDecision used to early-return on a writer error
// (e.g. memory.ErrApprovalAlreadyDecided from a lost race) before ever
// reaching the pending-map delete + CancelFn — leaving a race loser's channel
// button armed and its background timeout goroutine alive. Cleanup must run
// exactly once regardless of whether the write succeeded.
func TestManager_RecordDecision_CleansUpPendingOnWriterError(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)
	sentinel := errors.New("already decided sentinel")
	writer := &mockPRStateWriter{err: sentinel}
	m.WithStateWriter(writer)

	handler := &mockHandler{name: "test"} // never responds
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "race-loser-1",
		TaskID:    "TASK-07",
		Stage:     StagePreExecution,
		CreatedAt: time.Now(),
	}
	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // let the background goroutine start

	pending := m.GetPendingRequests()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending before RecordDecision, got %d", len(pending))
	}

	recErr := m.RecordDecision(context.Background(), req.ID, DecisionApproved, "loser")
	if !errors.Is(recErr, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", recErr)
	}

	// Cleanup must have happened despite the writer error.
	pending = m.GetPendingRequests()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after RecordDecision (cleanup must run on error too), got %d", len(pending))
	}

	// Cleanup must be idempotent — a second call for the same requestID (e.g.
	// a retried callback) must not panic or double-cancel.
	recErr2 := m.RecordDecision(context.Background(), req.ID, DecisionApproved, "loser-retry")
	if !errors.Is(recErr2, sentinel) {
		t.Fatalf("expected wrapped sentinel error on retry, got %v", recErr2)
	}
}

func TestManager_RecordDecision_CancelsPendingRequest(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)
	handler := &mockHandler{name: "test"} // never responds
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "async-cancel-1",
		TaskID:    "TASK-05",
		Stage:     StagePreExecution,
		CreatedAt: time.Now(),
	}
	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// Verify request is tracked as pending
	time.Sleep(20 * time.Millisecond)
	pending := m.GetPendingRequests()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending after submit, got %d", len(pending))
	}

	// Record decision externally — should remove from pending
	if err := m.RecordDecision(context.Background(), req.ID, DecisionApproved, "admin"); err != nil {
		t.Fatalf("record decision: %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	pending = m.GetPendingRequests()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after RecordDecision, got %d", len(pending))
	}
}

func TestManager_RecordDecision_NoDoubleWrite_WhenCancelsBackgroundGoroutine(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)
	writer := &mockPRStateWriter{}
	m.WithStateWriter(writer)

	handler := &mockHandler{name: "test"} // never responds
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "no-double-write-1",
		TaskID:    "TASK-06",
		Stage:     StagePreExecution,
		CreatedAt: time.Now(),
	}
	_, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	time.Sleep(20 * time.Millisecond) // let goroutine start

	// RecordDecision records the real decision and cancels the background goroutine.
	if err := m.RecordDecision(context.Background(), req.ID, DecisionApproved, "admin"); err != nil {
		t.Fatalf("record decision: %v", err)
	}

	// Give background goroutine time to react to context cancellation.
	time.Sleep(50 * time.Millisecond)

	// Only one write should have happened (from RecordDecision, not the goroutine).
	calls := writer.getCalls()
	if len(calls) != 1 {
		t.Errorf("expected exactly 1 writer call, got %d (possible double-write)", len(calls))
	}
	if len(calls) > 0 && calls[0].decision != "approved" {
		t.Errorf("expected approved, got %s", calls[0].decision)
	}
}

func TestManager_WithStateWriter_Chaining(t *testing.T) {
	m := NewManager(nil)
	writer := &mockPRStateWriter{}
	returned := m.WithStateWriter(writer)
	if returned != m {
		t.Error("WithStateWriter should return the same Manager for chaining")
	}
	if m.stateWriter != writer {
		t.Error("stateWriter not set after WithStateWriter")
	}
}

func TestManager_RuleTrigger_DisabledRule_Skipped(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = false
	config.Rules = []Rule{
		{
			Name:      "disabled-gate",
			Condition: Condition{Type: ConditionConsecutiveFailures, Threshold: 1},
			Stage:     StagePreExecution,
			Enabled:   false, // Disabled
		},
	}

	m := NewManager(config)

	handler := &mockHandler{name: "test"}
	m.RegisterHandler(handler)

	req := &Request{
		ID:     "disabled-rule",
		TaskID: "TASK-06",
		Stage:  StagePreExecution,
		Title:  "Task",
		Metadata: map[string]interface{}{
			"consecutive_failures": 10,
		},
		CreatedAt: time.Now(),
	}

	// Disabled rule → no trigger → auto-approve via SubmitApprovalRequest
	requestID, err := m.SubmitApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestID != req.ID {
		t.Errorf("expected auto-approve requestID=%q, got %q", req.ID, requestID)
	}

	// Disabled rule → no trigger → auto-approve
	if len(handler.sentReqs) != 0 {
		t.Errorf("expected 0 requests (rule disabled), got %d", len(handler.sentReqs))
	}
}

func TestManager_ShouldRequireApproval_WithConfigRules(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.Rules = []Rule{
		{
			Name:      "spend-gate",
			Condition: Condition{Type: ConditionSpendThreshold, Threshold: 1000},
			Stage:     StagePreExecution,
			Enabled:   true,
		},
	}

	m := NewManager(config)

	// Should match
	rule := m.ShouldRequireApproval(RuleContext{
		TaskID:          "TASK-01",
		TotalSpendCents: 2000,
	})
	if rule == nil {
		t.Fatal("expected matching rule")
	}
	if rule.Name != "spend-gate" {
		t.Errorf("expected spend-gate, got %s", rule.Name)
	}

	// Should not match
	rule = m.ShouldRequireApproval(RuleContext{
		TaskID:          "TASK-02",
		TotalSpendCents: 500,
	})
	if rule != nil {
		t.Errorf("expected no match, got rule %s", rule.Name)
	}
}

// --- NotifyMerged tests (GH-4164) ---

// mockMergeNotifyingHandler is a test double for a Handler that also
// implements MergeNotifier.
type mockMergeNotifyingHandler struct {
	mockHandler
	mu    sync.Mutex
	calls []struct{ requestID, shortSHA string }
	err   error
}

func (m *mockMergeNotifyingHandler) NotifyMerged(_ context.Context, requestID, shortSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, struct{ requestID, shortSHA string }{requestID, shortSHA})
	return m.err
}

func (m *mockMergeNotifyingHandler) getCalls() []struct{ requestID, shortSHA string } {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]struct{ requestID, shortSHA string }, len(m.calls))
	copy(out, m.calls)
	return out
}

// TestManager_NotifyMerged_ForwardsToMergeNotifierHandlers verifies that
// NotifyMerged forwards to every registered handler implementing
// MergeNotifier, and skips handlers that don't (e.g. plain mockHandler).
func TestManager_NotifyMerged_ForwardsToMergeNotifierHandlers(t *testing.T) {
	m := NewManager(nil)
	notifying := &mockMergeNotifyingHandler{mockHandler: mockHandler{name: "telegram"}}
	plain := &mockHandler{name: "github"}
	m.RegisterHandler(notifying)
	m.RegisterHandler(plain)

	m.NotifyMerged(context.Background(), "req-1", "abc1234")

	calls := notifying.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call to the MergeNotifier handler, got %d", len(calls))
	}
	if calls[0].requestID != "req-1" || calls[0].shortSHA != "abc1234" {
		t.Errorf("unexpected call args: %+v", calls[0])
	}
}

// TestManager_NotifyMerged_NoRegisteredHandlers_NoPanic ensures NotifyMerged
// is a safe no-op when no handlers are registered at all.
func TestManager_NotifyMerged_NoRegisteredHandlers_NoPanic(t *testing.T) {
	m := NewManager(nil)
	m.NotifyMerged(context.Background(), "req-1", "abc1234")
}

// TestManager_NotifyMerged_HandlerErrorIsNonFatal ensures a MergeNotifier
// handler error is logged, not propagated — a Telegram/Slack hiccup here
// must never fail the caller's merge-transition handling.
func TestManager_NotifyMerged_HandlerErrorIsNonFatal(t *testing.T) {
	m := NewManager(nil)
	notifying := &mockMergeNotifyingHandler{
		mockHandler: mockHandler{name: "telegram"},
		err:         errors.New("telegram api down"),
	}
	m.RegisterHandler(notifying)

	// Must not panic; the failure is logged internally.
	m.NotifyMerged(context.Background(), "req-1", "abc1234")

	if len(notifying.getCalls()) != 1 {
		t.Errorf("expected the handler to still be called once despite returning an error")
	}
}
