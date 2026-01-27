package approval

import (
	"context"
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
	m.sentReqs = append(m.sentReqs, req)
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
