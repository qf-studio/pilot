package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// mockTelegramClient implements TelegramClient for testing
type mockTelegramClient struct {
	mu              sync.Mutex
	sentMessages    []mockSentMessage
	editedMessages  []mockEditedMessage
	answeredCbs     []mockAnsweredCallback
	sendError       error
	editError       error
	answerError     error
	nextMessageID   int64
}

type mockSentMessage struct {
	ChatID   string
	Text     string
	Keyboard [][]InlineKeyboardButton
}

type mockEditedMessage struct {
	ChatID    string
	MessageID int64
	Text      string
}

type mockAnsweredCallback struct {
	CallbackID string
	Text       string
}

func (m *mockTelegramClient) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton) (*MessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sendError != nil {
		return nil, m.sendError
	}

	m.sentMessages = append(m.sentMessages, mockSentMessage{
		ChatID:   chatID,
		Text:     text,
		Keyboard: keyboard,
	})

	m.nextMessageID++
	return &MessageResponse{
		Result: &MessageResult{
			MessageID: m.nextMessageID,
		},
	}, nil
}

func (m *mockTelegramClient) EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.editError != nil {
		return m.editError
	}

	m.editedMessages = append(m.editedMessages, mockEditedMessage{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
	})
	return nil
}

func (m *mockTelegramClient) AnswerCallback(ctx context.Context, callbackID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.answerError != nil {
		return m.answerError
	}

	m.answeredCbs = append(m.answeredCbs, mockAnsweredCallback{
		CallbackID: callbackID,
		Text:       text,
	})
	return nil
}

func (m *mockTelegramClient) getSentMessages() []mockSentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockSentMessage, len(m.sentMessages))
	copy(result, m.sentMessages)
	return result
}

func (m *mockTelegramClient) getEditedMessages() []mockEditedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockEditedMessage, len(m.editedMessages))
	copy(result, m.editedMessages)
	return result
}

func (m *mockTelegramClient) getAnsweredCallbacks() []mockAnsweredCallback {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockAnsweredCallback, len(m.answeredCbs))
	copy(result, m.answeredCbs)
	return result
}

func TestTelegramHandler_Name(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "12345")

	if handler.Name() != "telegram" {
		t.Errorf("expected name 'telegram', got '%s'", handler.Name())
	}
}

func TestTelegramHandler_SendApprovalRequest(t *testing.T) {
	tests := []struct {
		name          string
		stage         Stage
		wantKeyboard  string
		wantStageText string
	}{
		{
			name:          "pre_execution stage",
			stage:         StagePreExecution,
			wantKeyboard:  "Execute",
			wantStageText: "Pre-Execution Approval",
		},
		{
			name:          "pre_merge stage",
			stage:         StagePreMerge,
			wantKeyboard:  "Merge",
			wantStageText: "Pre-Merge Approval",
		},
		{
			name:          "post_failure stage",
			stage:         StagePostFailure,
			wantKeyboard:  "Retry",
			wantStageText: "Post-Failure Decision",
		},
		{
			name:          "unknown stage",
			stage:         Stage("unknown"),
			wantKeyboard:  "Approve",
			wantStageText: "Approval Required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockTelegramClient{}
			handler := NewTelegramHandler(client, "chat123")

			req := &Request{
				ID:        "req-1",
				TaskID:    "TASK-01",
				Stage:     tt.stage,
				Title:     "Test task title",
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}

			respCh, err := handler.SendApprovalRequest(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if respCh == nil {
				t.Fatal("expected non-nil response channel")
			}

			// Verify message was sent
			msgs := client.getSentMessages()
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message sent, got %d", len(msgs))
			}

			msg := msgs[0]
			if msg.ChatID != "chat123" {
				t.Errorf("expected chat ID 'chat123', got '%s'", msg.ChatID)
			}

			// Check message contains expected text
			if !containsString(msg.Text, tt.wantStageText) {
				t.Errorf("expected message to contain '%s', got '%s'", tt.wantStageText, msg.Text)
			}

			// Check keyboard
			if len(msg.Keyboard) != 1 || len(msg.Keyboard[0]) != 2 {
				t.Errorf("expected 1 row with 2 buttons, got %v", msg.Keyboard)
			}

			if !containsString(msg.Keyboard[0][0].Text, tt.wantKeyboard) {
				t.Errorf("expected approve button to contain '%s', got '%s'", tt.wantKeyboard, msg.Keyboard[0][0].Text)
			}
		})
	}
}

func TestTelegramHandler_SendApprovalRequest_WithMetadata(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:          "req-meta",
		TaskID:      "TASK-01",
		Stage:       StagePreMerge,
		Title:       "Test PR merge",
		Description: "This is a detailed description",
		Metadata: map[string]interface{}{
			"pr_url": "https://github.com/org/repo/pull/123",
			"error":  "Some error message",
		},
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	_, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := client.getSentMessages()
	msg := msgs[0]

	// Check metadata is included
	if !containsString(msg.Text, "https://github.com/org/repo/pull/123") {
		t.Error("expected PR URL in message")
	}
	if !containsString(msg.Text, "Some error message") {
		t.Error("expected error in message")
	}
	if !containsString(msg.Text, "This is a detailed description") {
		t.Error("expected description in message")
	}
}

func TestTelegramHandler_SendApprovalRequest_Error(t *testing.T) {
	client := &mockTelegramClient{
		sendError: errors.New("network error"),
	}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-err",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	_, err := handler.SendApprovalRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !containsString(err.Error(), "failed to send Telegram message") {
		t.Errorf("expected error about Telegram message, got: %v", err)
	}
}

func TestTelegramHandler_CancelRequest(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-cancel",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task to cancel",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	// Send request first
	_, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error sending request: %v", err)
	}

	// Now cancel it
	err = handler.CancelRequest(context.Background(), "req-cancel")
	if err != nil {
		t.Fatalf("unexpected error cancelling: %v", err)
	}

	// Verify message was edited
	edited := client.getEditedMessages()
	if len(edited) != 1 {
		t.Fatalf("expected 1 edited message, got %d", len(edited))
	}

	if !containsString(edited[0].Text, "CANCELLED") {
		t.Errorf("expected cancelled message, got: %s", edited[0].Text)
	}
}

func TestTelegramHandler_CancelRequest_NotFound(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	// Cancel a request that doesn't exist
	err := handler.CancelRequest(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("expected no error for nonexistent request, got: %v", err)
	}

	// No messages should be edited
	edited := client.getEditedMessages()
	if len(edited) != 0 {
		t.Errorf("expected no edited messages, got %d", len(edited))
	}
}

func TestTelegramHandler_CancelRequest_EditError(t *testing.T) {
	client := &mockTelegramClient{
		editError: errors.New("edit failed"),
	}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-edit-err",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	_, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error sending request: %v", err)
	}

	// Cancel should not fail even if edit fails (just logs warning)
	err = handler.CancelRequest(context.Background(), "req-edit-err")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestTelegramHandler_HandleCallback_Approve(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-cb-approve",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Handle approve callback
	handled := handler.HandleCallback(context.Background(), "cb123", "approve:req-cb-approve", "user123", "testuser")
	if !handled {
		t.Error("expected callback to be handled")
	}

	// Wait for response
	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response, got nil")
		}
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
		if resp.ApprovedBy != "testuser" {
			t.Errorf("expected testuser, got %s", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	// Verify callback was answered
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 {
		t.Fatalf("expected 1 answered callback, got %d", len(cbs))
	}
	if cbs[0].Text != "Approved!" {
		t.Errorf("expected 'Approved!', got '%s'", cbs[0].Text)
	}

	// Verify message was edited
	edited := client.getEditedMessages()
	if len(edited) != 1 {
		t.Fatalf("expected 1 edited message, got %d", len(edited))
	}
	if !containsString(edited[0].Text, "APPROVED") {
		t.Errorf("expected APPROVED in message, got: %s", edited[0].Text)
	}
}

func TestTelegramHandler_HandleCallback_Reject(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-cb-reject",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Handle reject callback
	handled := handler.HandleCallback(context.Background(), "cb123", "reject:req-cb-reject", "user123", "testuser")
	if !handled {
		t.Error("expected callback to be handled")
	}

	// Wait for response
	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response, got nil")
		}
		if resp.Decision != DecisionRejected {
			t.Errorf("expected rejected, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	// Verify callback answer text
	cbs := client.getAnsweredCallbacks()
	if cbs[0].Text != "Rejected" {
		t.Errorf("expected 'Rejected', got '%s'", cbs[0].Text)
	}
}

func TestTelegramHandler_HandleCallback_InvalidFormat(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	tests := []struct {
		name string
		data string
	}{
		{"empty data", ""},
		{"random data", "random:data"},
		{"partial approve", "approve"},
		{"partial reject", "reject"},
		{"invalid prefix", "unknown:req-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handled := handler.HandleCallback(context.Background(), "cb123", tt.data, "user", "username")
			if handled {
				t.Errorf("expected callback '%s' to not be handled", tt.data)
			}
		})
	}
}

func TestTelegramHandler_HandleCallback_ExpiredRequest(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	// Handle callback for a request that doesn't exist
	handled := handler.HandleCallback(context.Background(), "cb123", "approve:nonexistent", "user", "username")
	if !handled {
		t.Error("expected callback to be handled (even for expired requests)")
	}

	// Verify "expired" callback answer was sent
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 {
		t.Fatalf("expected 1 answered callback, got %d", len(cbs))
	}
	if !containsString(cbs[0].Text, "expired") {
		t.Errorf("expected expired message, got: %s", cbs[0].Text)
	}
}

func TestTelegramHandler_HandleCallback_EditError(t *testing.T) {
	client := &mockTelegramClient{
		editError: errors.New("edit failed"),
	}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-edit-fail",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Handle callback - should succeed even if edit fails
	handled := handler.HandleCallback(context.Background(), "cb123", "approve:req-edit-fail", "user", "testuser")
	if !handled {
		t.Error("expected callback to be handled")
	}

	// Should still receive response
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestTruncateForTelegram(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short text unchanged",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length unchanged",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "long text truncated",
			input:    "hello world",
			maxLen:   8,
			expected: "hello...",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncateForTelegram(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{
			name:     "negative duration",
			duration: -1 * time.Minute,
			expected: "expired",
		},
		{
			name:     "seconds",
			duration: 30 * time.Second,
			expected: "30 seconds",
		},
		{
			name:     "one minute",
			duration: 1 * time.Minute,
			expected: "1 minutes",
		},
		{
			name:     "multiple minutes",
			duration: 45 * time.Minute,
			expected: "45 minutes",
		},
		{
			name:     "one hour",
			duration: 1 * time.Hour,
			expected: "1 hour",
		},
		{
			name:     "multiple hours",
			duration: 5 * time.Hour,
			expected: "5 hours",
		},
		{
			name:     "zero duration",
			duration: 0,
			expected: "0 seconds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDuration(tt.duration)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestTelegramHandler_FormatApprovalMessage_Stages(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	tests := []struct {
		stage       Stage
		wantIcon    string
		wantLabel   string
	}{
		{StagePreExecution, "🚀", "Pre-Execution Approval"},
		{StagePreMerge, "🔀", "Pre-Merge Approval"},
		{StagePostFailure, "❌", "Post-Failure Decision"},
		{Stage("unknown"), "⚠️", "Approval Required"},
	}

	for _, tt := range tests {
		t.Run(string(tt.stage), func(t *testing.T) {
			req := &Request{
				ID:        "test",
				TaskID:    "TASK-01",
				Stage:     tt.stage,
				Title:     "Test",
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}

			text := handler.formatApprovalMessage(req)

			if !containsString(text, tt.wantIcon) {
				t.Errorf("expected icon '%s' in message", tt.wantIcon)
			}
			if !containsString(text, tt.wantLabel) {
				t.Errorf("expected label '%s' in message", tt.wantLabel)
			}
		})
	}
}

func TestTelegramHandler_FormatResponseMessage(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:     "test",
		TaskID: "TASK-01",
		Title:  "Test Task",
	}

	tests := []struct {
		decision    Decision
		wantIcon    string
		wantStatus  string
	}{
		{DecisionApproved, "✅", "APPROVED"},
		{DecisionRejected, "❌", "REJECTED"},
		{DecisionTimeout, "⏱", "TIMEOUT"},
	}

	for _, tt := range tests {
		t.Run(string(tt.decision), func(t *testing.T) {
			text := handler.formatResponseMessage(req, tt.decision, "testuser")

			if !containsString(text, tt.wantIcon) {
				t.Errorf("expected icon '%s' in message", tt.wantIcon)
			}
			if !containsString(text, tt.wantStatus) {
				t.Errorf("expected status '%s' in message", tt.wantStatus)
			}
			if !containsString(text, "testuser") {
				t.Error("expected username in message")
			}
		})
	}
}

func TestTelegramHandler_CreateApprovalKeyboard(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	tests := []struct {
		stage       Stage
		wantApprove string
		wantReject  string
	}{
		{StagePreExecution, "Execute", "Cancel"},
		{StagePreMerge, "Merge", "Reject"},
		{StagePostFailure, "Retry", "Abort"},
		{Stage("unknown"), "Approve", "Reject"},
	}

	for _, tt := range tests {
		t.Run(string(tt.stage), func(t *testing.T) {
			req := &Request{
				ID:    "test-kb",
				Stage: tt.stage,
			}

			keyboard := handler.createApprovalKeyboard(req)

			if len(keyboard) != 1 || len(keyboard[0]) != 2 {
				t.Fatalf("expected 1x2 keyboard, got %v", keyboard)
			}

			if !containsString(keyboard[0][0].Text, tt.wantApprove) {
				t.Errorf("expected approve button to contain '%s', got '%s'", tt.wantApprove, keyboard[0][0].Text)
			}
			if !containsString(keyboard[0][1].Text, tt.wantReject) {
				t.Errorf("expected reject button to contain '%s', got '%s'", tt.wantReject, keyboard[0][1].Text)
			}

			// Verify callback data format
			if keyboard[0][0].CallbackData != "approve:test-kb" {
				t.Errorf("expected callback 'approve:test-kb', got '%s'", keyboard[0][0].CallbackData)
			}
			if keyboard[0][1].CallbackData != "reject:test-kb" {
				t.Errorf("expected callback 'reject:test-kb', got '%s'", keyboard[0][1].CallbackData)
			}
		})
	}
}

func TestTelegramHandler_NilMessageResponse(t *testing.T) {
	// Create a client that returns nil result
	client := &mockTelegramClient{}
	// Modify to return nil result
	handler := NewTelegramHandler(client, "chat123")

	req := &Request{
		ID:        "req-nil",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}

	// Should handle gracefully even with message ID 0
	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if respCh == nil {
		t.Fatal("expected non-nil response channel")
	}
}

func TestTelegramHandler_CancelWithZeroMessageID(t *testing.T) {
	// Create handler that tracks a request with message ID 0
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	// Manually add a pending request with 0 message ID
	handler.mu.Lock()
	handler.pending["req-zero"] = &telegramPending{
		Request: &Request{
			ID:     "req-zero",
			TaskID: "TASK-01",
			Title:  "Test",
		},
		MessageID:  0, // Zero message ID
		ResponseCh: make(chan *Response, 1),
	}
	handler.mu.Unlock()

	// Cancel should not attempt to edit message
	err := handler.CancelRequest(context.Background(), "req-zero")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No messages should be edited since message ID is 0
	edited := client.getEditedMessages()
	if len(edited) != 0 {
		t.Errorf("expected no edited messages for zero message ID, got %d", len(edited))
	}
}

func TestTelegramHandler_HandleCallbackWithZeroMessageID(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123")

	// Manually add a pending request with 0 message ID
	respCh := make(chan *Response, 1)
	handler.mu.Lock()
	handler.pending["req-zero-cb"] = &telegramPending{
		Request: &Request{
			ID:     "req-zero-cb",
			TaskID: "TASK-01",
			Title:  "Test",
		},
		MessageID:  0,
		ResponseCh: respCh,
	}
	handler.mu.Unlock()

	// Handle callback - should not try to edit message
	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-zero-cb", "user", "testuser")
	if !handled {
		t.Error("expected callback to be handled")
	}

	// Should still receive response
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}

	// No messages should be edited
	edited := client.getEditedMessages()
	if len(edited) != 0 {
		t.Errorf("expected no edited messages, got %d", len(edited))
	}
}

// containsString is a helper to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
