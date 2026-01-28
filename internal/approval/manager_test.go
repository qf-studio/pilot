package approval

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

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

func (m *mockHandler) getCancelCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.cancelCalls))
	copy(result, m.cancelCalls)
	return result
}

func TestManager_DisabledByDefault(t *testing.T) {
	m := NewManager(nil)
	if m.IsEnabled() {
		t.Error("expected manager to be disabled by default")
	}
}

func TestManager_StageDisabled_AutoApproves(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	// All stages disabled by default

	m := NewManager(config)

	req := &Request{
		ID:     "test-1",
		TaskID: "TASK-01",
		Stage:  StagePreExecution,
		Title:  "Test task",
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected auto-approve, got %s", resp.Decision)
	}

	if resp.ApprovedBy != "system" {
		t.Errorf("expected system approval, got %s", resp.ApprovedBy)
	}
}

func TestManager_StageEnabled_SendsRequest(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 100 * time.Millisecond

	m := NewManager(config)

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "test-1",
			Decision:   DecisionApproved,
			ApprovedBy: "user123",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-1",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(handler.sentReqs) != 1 {
		t.Errorf("expected 1 request sent, got %d", len(handler.sentReqs))
	}

	if resp.Decision != DecisionApproved {
		t.Errorf("expected approved, got %s", resp.Decision)
	}

	if resp.ApprovedBy != "user123" {
		t.Errorf("expected user123, got %s", resp.ApprovedBy)
	}
}

func TestManager_Timeout_UsesDefaultAction(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 50 * time.Millisecond
	config.PreExecution.DefaultAction = DecisionRejected

	m := NewManager(config)

	// Handler that never responds
	handler := &mockHandler{
		name:        "test",
		respondWith: nil, // No response
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-timeout",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionRejected {
		t.Errorf("expected rejected (default action), got %s", resp.Decision)
	}

	// Should have cancelled the request
	if len(handler.cancelCalls) != 1 {
		t.Errorf("expected cancel to be called, got %d calls", len(handler.cancelCalls))
	}
}

func TestManager_NoHandlers_UsesDefaultAction(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreMerge.Enabled = true
	config.PreMerge.DefaultAction = DecisionRejected

	m := NewManager(config)
	// No handlers registered

	req := &Request{
		ID:        "test-no-handler",
		TaskID:    "TASK-01",
		Stage:     StagePreMerge,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Decision != DecisionRejected {
		t.Errorf("expected rejected (default action), got %s", resp.Decision)
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

func TestManager_CancelPending(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)

	handler := &mockHandler{
		name:        "test",
		respondWith: nil, // Never responds
	}
	m.RegisterHandler(handler)

	// Start a request in the background
	req := &Request{
		ID:        "test-cancel",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_, _ = m.RequestApproval(ctx, req)
	}()

	// Wait for request to be pending
	time.Sleep(50 * time.Millisecond)

	// Cancel all pending for this task
	m.CancelPending(context.Background(), "TASK-01")

	// Verify cancel was called at least once (may be called by both CancelPending and timeout)
	time.Sleep(50 * time.Millisecond)
	cancelCalls := handler.getCancelCalls()
	if len(cancelCalls) < 1 {
		t.Errorf("expected at least 1 cancel call, got %d", len(cancelCalls))
	}

	// Verify the request ID was cancelled
	foundRequestID := false
	for _, id := range cancelCalls {
		if id == "test-cancel" {
			foundRequestID = true
			break
		}
	}
	if !foundRequestID {
		t.Errorf("expected request 'test-cancel' to be cancelled")
	}
}

func TestManager_GetPendingRequests(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)

	handler := &mockHandler{
		name:        "test",
		respondWith: nil,
	}
	m.RegisterHandler(handler)

	// Initially no pending requests
	pending := m.GetPendingRequests()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending requests, got %d", len(pending))
	}

	// Start multiple requests in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 3; i++ {
		req := &Request{
			ID:        fmt.Sprintf("test-%d", i),
			TaskID:    fmt.Sprintf("TASK-%02d", i),
			Stage:     StagePreExecution,
			Title:     fmt.Sprintf("Test task %d", i),
			CreatedAt: time.Now(),
		}
		go func(r *Request) {
			_, _ = m.RequestApproval(ctx, r)
		}(req)
	}

	// Wait for requests to be pending
	time.Sleep(100 * time.Millisecond)

	pending = m.GetPendingRequests()
	if len(pending) != 3 {
		t.Errorf("expected 3 pending requests, got %d", len(pending))
	}
}

func TestManager_CancelPending_NoMatchingTask(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second

	m := NewManager(config)

	handler := &mockHandler{
		name:        "test",
		respondWith: nil,
	}
	m.RegisterHandler(handler)

	// Start a request
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := &Request{
		ID:        "test-1",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	go func() {
		_, _ = m.RequestApproval(ctx, req)
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel for a different task - should not affect our request
	m.CancelPending(context.Background(), "TASK-99")

	// Original request should still be pending
	pending := m.GetPendingRequests()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending request, got %d", len(pending))
	}
}

func TestManager_RequestApproval_SetsApproversFromConfig(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 100 * time.Millisecond
	config.PreExecution.Approvers = []string{"user1", "user2"}

	m := NewManager(config)

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "test-approvers",
			Decision:   DecisionApproved,
			ApprovedBy: "user1",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-approvers",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
		// No approvers set - should come from config
	}

	_, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify approvers were set from config
	if len(handler.sentReqs) != 1 {
		t.Fatalf("expected 1 request sent, got %d", len(handler.sentReqs))
	}

	sentReq := handler.sentReqs[0]
	if len(sentReq.Approvers) != 2 {
		t.Errorf("expected 2 approvers, got %d", len(sentReq.Approvers))
	}
}

func TestManager_RequestApproval_PreservesRequestApprovers(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 100 * time.Millisecond
	config.PreExecution.Approvers = []string{"config-user"}

	m := NewManager(config)

	handler := &mockHandler{
		name: "test",
		respondWith: &Response{
			RequestID:  "test-preserve",
			Decision:   DecisionApproved,
			ApprovedBy: "req-user",
		},
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-preserve",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		Approvers: []string{"req-user"}, // Request has its own approvers
		CreatedAt: time.Now(),
	}

	_, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify approvers from request were preserved (not overwritten by config)
	sentReq := handler.sentReqs[0]
	if len(sentReq.Approvers) != 1 || sentReq.Approvers[0] != "req-user" {
		t.Errorf("expected request approvers to be preserved, got %v", sentReq.Approvers)
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

func TestManager_RequestApproval_AllStages(t *testing.T) {
	tests := []struct {
		name       string
		stage      Stage
		enableFunc func(*Config)
	}{
		{
			name:  "pre_execution stage",
			stage: StagePreExecution,
			enableFunc: func(c *Config) {
				c.PreExecution.Enabled = true
				c.PreExecution.Timeout = 100 * time.Millisecond
			},
		},
		{
			name:  "pre_merge stage",
			stage: StagePreMerge,
			enableFunc: func(c *Config) {
				c.PreMerge.Enabled = true
				c.PreMerge.Timeout = 100 * time.Millisecond
			},
		},
		{
			name:  "post_failure stage",
			stage: StagePostFailure,
			enableFunc: func(c *Config) {
				c.PostFailure.Enabled = true
				c.PostFailure.Timeout = 100 * time.Millisecond
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Enabled = true
			tt.enableFunc(config)

			m := NewManager(config)

			handler := &mockHandler{
				name: "test",
				respondWith: &Response{
					RequestID:  "test-stage",
					Decision:   DecisionApproved,
					ApprovedBy: "user",
				},
			}
			m.RegisterHandler(handler)

			req := &Request{
				ID:        "test-stage",
				TaskID:    "TASK-01",
				Stage:     tt.stage,
				Title:     "Test task",
				CreatedAt: time.Now(),
			}

			resp, err := m.RequestApproval(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resp.Decision != DecisionApproved {
				t.Errorf("expected approved, got %s", resp.Decision)
			}
		})
	}
}

func TestManager_RequestApproval_UsesDefaultTimeout(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 0 // Zero timeout - should use default
	config.DefaultTimeout = 50 * time.Millisecond
	config.PreExecution.DefaultAction = DecisionRejected

	m := NewManager(config)

	handler := &mockHandler{
		name:        "test",
		respondWith: nil, // No response - will timeout
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-default-timeout",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	start := time.Now()
	resp, err := m.RequestApproval(context.Background(), req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have timed out around 50ms
	if elapsed < 40*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("expected timeout around 50ms, got %v", elapsed)
	}

	if resp.Decision != DecisionRejected {
		t.Errorf("expected rejected on timeout, got %s", resp.Decision)
	}
}

func TestManager_ContextCancellation(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 10 * time.Second
	config.PreExecution.DefaultAction = DecisionRejected

	m := NewManager(config)

	handler := &mockHandler{
		name:        "test",
		respondWith: nil, // No response
	}
	m.RegisterHandler(handler)

	req := &Request{
		ID:        "test-ctx-cancel",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	resp, err := m.RequestApproval(ctx, req)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have timed out around 50ms from context
	if elapsed < 40*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Errorf("expected timeout around 50ms from context, got %v", elapsed)
	}

	if resp.Decision != DecisionRejected {
		t.Errorf("expected rejected on timeout, got %s", resp.Decision)
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

func TestManager_MultipleHandlers(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = true
	config.PreExecution.Enabled = true
	config.PreExecution.Timeout = 100 * time.Millisecond

	m := NewManager(config)

	handler1 := &mockHandler{
		name: "handler1",
		respondWith: &Response{
			RequestID:  "test-multi",
			Decision:   DecisionApproved,
			ApprovedBy: "handler1-user",
		},
	}
	handler2 := &mockHandler{
		name:        "handler2",
		respondWith: nil,
	}

	// Register multiple handlers
	m.RegisterHandler(handler1)
	m.RegisterHandler(handler2)

	req := &Request{
		ID:        "test-multi",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		CreatedAt: time.Now(),
	}

	resp, err := m.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should use one of the handlers (implementation uses first available)
	if resp.Decision != DecisionApproved {
		t.Errorf("expected approved, got %s", resp.Decision)
	}

	// Only one handler should have received the request
	totalSent := len(handler1.sentReqs) + len(handler2.sentReqs)
	if totalSent != 1 {
		t.Errorf("expected exactly 1 request sent to handlers, got %d", totalSent)
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
