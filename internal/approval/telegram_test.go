package approval

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newCapturingLogger returns a logger that writes text-formatted records into
// the returned buffer, so tests can assert on both level and message content.
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h), &buf
}

// mockTelegramClient implements TelegramClient for testing
type mockTelegramClient struct {
	mu             sync.Mutex
	sentMessages   []mockSentMessage
	editedMessages []mockEditedMessage
	answeredCbs    []mockAnsweredCallback
	sendError      error
	editError      error
	answerError    error
	nextMessageID  int64
}

type mockSentMessage struct {
	ChatID   string
	Text     string
	Keyboard [][]InlineKeyboardButton
	ThreadID int64
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

func (m *mockTelegramClient) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton, messageThreadID int64) (*MessageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sendError != nil {
		return nil, m.sendError
	}

	m.sentMessages = append(m.sentMessages, mockSentMessage{
		ChatID:   chatID,
		Text:     text,
		Keyboard: keyboard,
		ThreadID: messageThreadID,
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
	handler := NewTelegramHandler(client, "12345", 0)

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
			handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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

func TestTelegramHandler_HandleCallback_ExpiredButStillPending(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{
		ID:        "req-expired-race",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(-time.Minute), // already past its deadline
	}

	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A button tap races the periodic PruneExpired sweep: the request is
	// still in the pending map, but its ExpiresAt has already elapsed.
	handled := handler.HandleCallback(context.Background(), "cb123", "approve:req-expired-race", "user123", "testuser")
	if !handled {
		t.Error("expected callback to be handled")
	}

	// Must NOT fake an approval — the deadline already passed.
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 {
		t.Fatalf("expected 1 answered callback, got %d", len(cbs))
	}
	if !containsString(cbs[0].Text, "expired") {
		t.Errorf("expected expired message, got: %s", cbs[0].Text)
	}

	// Message should be edited to show expiry, not approval.
	edited := client.getEditedMessages()
	if len(edited) != 1 {
		t.Fatalf("expected 1 edited message, got %d", len(edited))
	}
	if !containsString(edited[0].Text, "EXPIRED") {
		t.Errorf("expected EXPIRED in message, got: %s", edited[0].Text)
	}

	// Response channel should resolve to timeout, not approved.
	select {
	case resp := <-respCh:
		if resp == nil {
			t.Fatal("expected response, got nil")
		}
		if resp.Decision != DecisionTimeout {
			t.Errorf("expected timeout, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestTelegramHandler_HandleCallback_EditError(t *testing.T) {
	client := &mockTelegramClient{
		editError: errors.New("edit failed"),
	}
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

	tests := []struct {
		stage     Stage
		wantIcon  string
		wantLabel string
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
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{
		ID:     "test",
		TaskID: "TASK-01",
		Title:  "Test Task",
	}

	tests := []struct {
		decision   Decision
		wantIcon   string
		wantStatus string
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

// TestTelegramHandler_FormatResponseMessage_ReleasePlan is the GH-4164
// table-driven test covering the three release-trigger ack-card variants
// (on_merge, on_schedule, disabled/absent) plus the rejected and no-plan
// control paths, which must fall back to the generic APPROVED/REJECTED card.
func TestTelegramHandler_FormatResponseMessage_ReleasePlan(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	tests := []struct {
		name         string
		req          *Request
		decision     Decision
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:         "on_merge trigger",
			req:          &Request{TaskID: "GH-1", Title: "Merge approval for PR #1", ReleasePlan: "Will release immediately after merge."},
			decision:     DecisionApproved,
			wantContains: []string{"✅ Approved by tester", "merging", "Will release immediately after merge."},
		},
		{
			name:         "on_schedule trigger renders next train time",
			req:          &Request{TaskID: "GH-2", Title: "Merge approval for PR #2", ReleasePlan: "Rides the next release train: 2026-07-11 16:00 CET."},
			decision:     DecisionApproved,
			wantContains: []string{"✅ Approved by tester", "Rides the next release train: 2026-07-11 16:00 CET."},
		},
		{
			name:         "releaser disabled or absent",
			req:          &Request{TaskID: "GH-3", Title: "Merge approval for PR #3", ReleasePlan: "No releaser configured for this repo (merge only)."},
			decision:     DecisionApproved,
			wantContains: []string{"✅ Approved by tester", "No releaser configured for this repo (merge only)."},
		},
		{
			name:         "rejected — release plan text never shown even if set",
			req:          &Request{TaskID: "GH-4", Title: "Merge approval for PR #4", ReleasePlan: "Will release immediately after merge."},
			decision:     DecisionRejected,
			wantContains: []string{"❌", "REJECTED"},
			wantAbsent:   []string{"Will release immediately after merge."},
		},
		{
			name:         "approved with no release plan — legacy control path",
			req:          &Request{TaskID: "GH-5", Title: "Pre-execution approval"},
			decision:     DecisionApproved,
			wantContains: []string{"✅", "APPROVED", "Decision: tester"},
			wantAbsent:   []string{"merging"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := handler.formatResponseMessage(tt.req, tt.decision, "tester")
			for _, want := range tt.wantContains {
				if !containsString(text, want) {
					t.Errorf("expected message to contain %q, got: %s", want, text)
				}
			}
			for _, absent := range tt.wantAbsent {
				if containsString(text, absent) {
					t.Errorf("expected message to NOT contain %q, got: %s", absent, text)
				}
			}
		})
	}
}

func TestTelegramHandler_CreateApprovalKeyboard(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

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
	handler := NewTelegramHandler(client, "chat123", 0)

	// Manually add a pending request with 0 message ID
	respCh := make(chan *Response, 1)
	handler.mu.Lock()
	handler.pending["req-zero-cb"] = &telegramPending{
		Request: &Request{
			ID:        "req-zero-cb",
			TaskID:    "TASK-01",
			Title:     "Test",
			ExpiresAt: time.Now().Add(1 * time.Hour),
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

// TestTelegramHandler_ApproverRouting verifies that the destination chat_id is
// resolved from req.Approvers[0] when set, and falls back to the constructor
// chat_id when the slice is empty.
func TestTelegramHandler_ApproverRouting(t *testing.T) {
	t.Run("uses approver chat_id when set", func(t *testing.T) {
		client := &mockTelegramClient{}
		handler := NewTelegramHandler(client, "constructor-chat", 0)

		req := &Request{
			ID:        "req-approver",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{"99999"},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := handler.SendApprovalRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		msgs := client.getSentMessages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message sent, got %d", len(msgs))
		}
		if msgs[0].ChatID != "99999" {
			t.Errorf("expected chat_id '99999', got '%s'", msgs[0].ChatID)
		}
	})

	t.Run("falls back to constructor chat_id when approvers empty", func(t *testing.T) {
		client := &mockTelegramClient{}
		handler := NewTelegramHandler(client, "constructor-chat", 0)

		req := &Request{
			ID:        "req-no-approver",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := handler.SendApprovalRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		msgs := client.getSentMessages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message sent, got %d", len(msgs))
		}
		if msgs[0].ChatID != "constructor-chat" {
			t.Errorf("expected chat_id 'constructor-chat', got '%s'", msgs[0].ChatID)
		}
	})

	t.Run("uses @channel approver as-is", func(t *testing.T) {
		client := &mockTelegramClient{}
		handler := NewTelegramHandler(client, "constructor-chat", 0)

		req := &Request{
			ID:        "req-approver-channel",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{"@mychannel"},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := handler.SendApprovalRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		msgs := client.getSentMessages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message sent, got %d", len(msgs))
		}
		if msgs[0].ChatID != "@mychannel" {
			t.Errorf("expected chat_id '@mychannel', got '%s'", msgs[0].ChatID)
		}
	})

	// GH-4380: a request routed here (e.g. by the manager's PreferredChannel
	// fallback, or a config where Approvers was populated with an identity
	// meant for a different channel) must not blindly dial an
	// Approvers[0] that isn't a plausible Telegram destination — that
	// produced a "chat not found" 400 on every retry against the live
	// incident's PRs #4373/#4374.
	t.Run("falls back to constructor chat_id when approver is not a valid telegram destination", func(t *testing.T) {
		client := &mockTelegramClient{}
		handler := NewTelegramHandler(client, "constructor-chat", 0)

		req := &Request{
			ID:        "req-invalid-approver",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{"#pointer"}, // Slack channel name, not a Telegram chat id
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := handler.SendApprovalRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		msgs := client.getSentMessages()
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message sent, got %d", len(msgs))
		}
		if msgs[0].ChatID != "constructor-chat" {
			t.Errorf("expected fallback to constructor chat_id 'constructor-chat', got '%s'", msgs[0].ChatID)
		}
	})

	t.Run("edit after callback uses same chat_id as original send", func(t *testing.T) {
		client := &mockTelegramClient{}
		handler := NewTelegramHandler(client, "constructor-chat", 0)

		req := &Request{
			ID:        "req-edit-routing",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{"99999"},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}

		_, err := handler.SendApprovalRequest(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// userID must match the configured Approvers entry (GH-5155) for the
		// callback to be authorized — this test exercises routing, not
		// authorization, so it uses the approver identity itself.
		handler.HandleCallback(context.Background(), "cb1", "approve:req-edit-routing", "99999", "tester")

		edited := client.getEditedMessages()
		if len(edited) != 1 {
			t.Fatalf("expected 1 edited message, got %d", len(edited))
		}
		if edited[0].ChatID != "99999" {
			t.Errorf("expected edit chat_id '99999', got '%s'", edited[0].ChatID)
		}
	})
}

func TestIsValidTelegramChatID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty string", "", false},
		{"positive numeric chat id", "283716179", true},
		{"negative numeric group chat id", "-1001234567890", true},
		{"public channel handle", "@mychannel", true},
		{"bare @", "@", false},
		{"slack channel name", "#pointer", false},
		{"slack user id", "U012ABCDEF", false},
		{"non-numeric garbage", "not-a-chat-id", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidTelegramChatID(tt.in); got != tt.want {
				t.Errorf("isValidTelegramChatID(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestTelegramHandler_InvalidApprover_WarnsOnce verifies that a persistently
// invalid Approvers[0] value logs a warning only once (deduped by value)
// rather than once per SendApprovalRequest call, so a misconfigured
// approvers list doesn't spam the log every controller tick (GH-4380).
func TestTelegramHandler_InvalidApprover_WarnsOnce(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "constructor-chat", 0)
	logger, buf := newCapturingLogger()
	handler.log = logger

	for i := 0; i < 3; i++ {
		req := &Request{
			ID:        "req-warn-once",
			TaskID:    "TASK-01",
			Stage:     StagePreMerge,
			Title:     "Test",
			Approvers: []string{"#pointer"},
			ExpiresAt: time.Now().Add(1 * time.Hour),
		}
		if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	warnCount := strings.Count(buf.String(), "not a valid Telegram destination")
	if warnCount != 1 {
		t.Errorf("expected exactly 1 warning log for the repeated invalid approver, got %d\nlog:\n%s", warnCount, buf.String())
	}
}

// --- store tests ---

// mockPendingStore is an in-memory PendingApprovalStore for unit tests.
type mockPendingStore struct {
	mu   sync.Mutex
	rows map[string]*memory.PendingApproval
	// per-call error injection
	insertErr error
	deleteErr error
	loadErr   error
	pruneErr  error
}

func newMockPendingStore() *mockPendingStore {
	return &mockPendingStore{rows: make(map[string]*memory.PendingApproval)}
}

func (s *mockPendingStore) InsertPendingApproval(a *memory.PendingApproval) error {
	if s.insertErr != nil {
		return s.insertErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	s.rows[a.ID] = &cp
	return nil
}

func (s *mockPendingStore) DeletePendingApproval(id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	return nil
}

func (s *mockPendingStore) LoadPendingApprovals() ([]*memory.PendingApproval, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*memory.PendingApproval, 0, len(s.rows))
	for _, r := range s.rows {
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

func (s *mockPendingStore) PrunePendingApprovals(cutoff time.Time, channels []string) (int64, error) {
	if s.pruneErr != nil {
		return 0, s.pruneErr
	}
	owned := make(map[string]bool, len(channels))
	for _, c := range channels {
		owned[c] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for id, r := range s.rows {
		if r.ExpiresAt.Before(cutoff) && owned[r.PreferredChannel] {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func (s *mockPendingStore) PrunePendingApprovalsOutside(cutoff time.Time, knownChannels []string) (int64, error) {
	if s.pruneErr != nil {
		return 0, s.pruneErr
	}
	known := make(map[string]bool, len(knownChannels))
	for _, c := range knownChannels {
		known[c] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for id, r := range s.rows {
		if r.ExpiresAt.Before(cutoff) && !known[r.PreferredChannel] {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func (s *mockPendingStore) get(id string) *memory.PendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[id]
}

func (s *mockPendingStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

func TestTelegramHandler_WithStore_PersistOnSend(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	req := &Request{
		ID: "persist-1", TaskID: "T-1", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.len() != 1 {
		t.Fatalf("expected 1 persisted row, got %d", store.len())
	}
	row := store.get("persist-1")
	if row == nil {
		t.Fatal("expected row to be stored")
	}
	if row.TaskID != "T-1" {
		t.Errorf("expected TaskID T-1, got %s", row.TaskID)
	}
}

// TestTelegramHandler_WithStore_PersistsProject is the GH-4773 round-trip
// regression test: SendApprovalRequest must copy req.Project onto the
// persisted memory.PendingApproval row.
func TestTelegramHandler_WithStore_PersistsProject(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	req := &Request{
		ID: "persist-project-1", TaskID: "T-1", Stage: StagePreMerge,
		Title: "Test", Project: "/home/user/projects/pilot", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	row := store.get("persist-project-1")
	if row == nil {
		t.Fatal("expected row to be stored")
	}
	if row.Project != "/home/user/projects/pilot" {
		t.Errorf("row.Project = %q, want /home/user/projects/pilot", row.Project)
	}
}

func TestTelegramHandler_WithStore_DeleteOnCallback(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	req := &Request{
		ID: "del-cb-1", TaskID: "T-2", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.len() != 1 {
		t.Fatal("expected 1 row after send")
	}

	handler.HandleCallback(context.Background(), "cb1", "approve:del-cb-1", "u", "user")

	if store.len() != 0 {
		t.Errorf("expected 0 rows after callback, got %d", store.len())
	}
}

func TestTelegramHandler_WithStore_DeleteOnCancel(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	req := &Request{
		ID: "del-cancel-1", TaskID: "T-3", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := handler.CancelRequest(context.Background(), "del-cancel-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.len() != 0 {
		t.Errorf("expected 0 rows after cancel, got %d", store.len())
	}
}

func TestTelegramHandler_Rehydrate_RestoреsNonExpired(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()

	// Pre-populate store with one non-expired and one expired row.
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "live", TaskID: "T-live", Stage: "pre_merge",
		Title: "Live", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "dead", TaskID: "T-dead", Stage: "pre_merge",
		Title: "Dead", CreatedAt: time.Now(), ExpiresAt: past,
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.RLock()
	_, livePending := handler.pending["live"]
	_, deadPending := handler.pending["dead"]
	handler.mu.RUnlock()

	if !livePending {
		t.Error("expected non-expired approval to be rehydrated")
	}
	if deadPending {
		t.Error("expected expired approval to NOT be rehydrated")
	}
	// Expired row should be pruned from store.
	if store.get("dead") != nil {
		t.Error("expected expired row to be deleted from store")
	}
}

func TestTelegramHandler_Rehydrate_NoStore(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)
	// No store attached — Rehydrate should be a no-op.
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("expected no error without store, got: %v", err)
	}
}

func TestTelegramHandler_Rehydrate_CallbackWorksAfterRehydrate(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-cb", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	// Simulate a button tap arriving after restart — should NOT answer "expired".
	handled := handler.HandleCallback(context.Background(), "cb-r", "approve:rehy-cb", "u", "tester")
	if !handled {
		t.Error("expected callback to be handled")
	}

	cbs := client.getAnsweredCallbacks()
	if len(cbs) == 0 {
		t.Fatal("expected callback answer")
	}
	if containsString(cbs[0].Text, "expired") {
		t.Errorf("expected non-expired answer, got: %s", cbs[0].Text)
	}
}

// mockDecisionRecorder is a test double for DecisionRecorder.
type mockDecisionRecorder struct {
	mu    sync.Mutex
	calls []struct {
		requestID string
		decision  Decision
		by        string
	}
	err error
}

func (r *mockDecisionRecorder) RecordDecision(_ context.Context, requestID string, decision Decision, by string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		requestID string
		decision  Decision
		by        string
	}{requestID, decision, by})
	return r.err
}

func (r *mockDecisionRecorder) getCalls() []struct {
	requestID string
	decision  Decision
	by        string
} {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]struct {
		requestID string
		decision  Decision
		by        string
	}, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestTelegramHandler_HandleCallback_RecordsDecisionViaRecorder(t *testing.T) {
	client := &mockTelegramClient{}
	recorder := &mockDecisionRecorder{}
	handler := NewTelegramHandler(client, "chat123", 0).WithDecisionRecorder(recorder)

	req := &Request{ID: "req-1", TaskID: "T-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.HandleCallback(context.Background(), "cb-1", "approve:req-1", "u1", "tester")

	calls := recorder.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 RecordDecision call, got %d", len(calls))
	}
	if calls[0].requestID != "req-1" || calls[0].decision != DecisionApproved || calls[0].by != "tester" {
		t.Errorf("unexpected recorded decision: %+v", calls[0])
	}
}

// TestTelegramHandler_Rehydrate_CallbackRecordsDecisionDirectly is the GH-3825
// regression test: after a restart, Rehydrate reconstructs the pending entry
// with a fresh ResponseCh that no goroutine is reading (the original waiter
// died with the old process). Without a DecisionRecorder, the decision made
// by a button tap would only be sent into that unread channel and lost. With
// the recorder wired, HandleCallback must persist the decision directly.
func TestTelegramHandler_Rehydrate_CallbackRecordsDecisionDirectly(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	recorder := &mockDecisionRecorder{}
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-rec", TaskID: "T-R2", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store).WithDecisionRecorder(recorder)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb-r2", "reject:rehy-rec", "u2", "reviewer")
	if !handled {
		t.Fatal("expected callback to be handled")
	}

	calls := recorder.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected decision to be recorded directly after rehydrate, got %d calls", len(calls))
	}
	if calls[0].requestID != "rehy-rec" || calls[0].decision != DecisionRejected || calls[0].by != "reviewer" {
		t.Errorf("unexpected recorded decision: %+v", calls[0])
	}
}

func TestTelegramHandler_HandleCallback_RecorderErrorIsNonFatal(t *testing.T) {
	client := &mockTelegramClient{}
	recorder := &mockDecisionRecorder{err: errors.New("db down")}
	handler := NewTelegramHandler(client, "chat123", 0).WithDecisionRecorder(recorder)

	req := &Request{ID: "req-2", TaskID: "T-2", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb-2", "approve:req-2", "u1", "tester")
	if !handled {
		t.Error("expected callback to still be handled when recorder fails")
	}
}

// TestTelegramHandler_HandleCallback_RaceLoss_ShowsAlreadyDecided is the
// GH-4777 regression test (PR#4767 review, item 4): a tap that loses the
// decision race — RecordDecision returns memory.ErrApprovalAlreadyDecided
// because another decider (a concurrent HTTP POST, or the same request
// answered via Slack) already recorded the outcome first — must answer
// "already decided" and edit the message accordingly, never a success card
// claiming this tap's decision won.
func TestTelegramHandler_HandleCallback_RaceLoss_ShowsAlreadyDecided(t *testing.T) {
	client := &mockTelegramClient{}
	recorder := &mockDecisionRecorder{err: memory.ErrApprovalAlreadyDecided}
	handler := NewTelegramHandler(client, "chat123", 0).WithDecisionRecorder(recorder)

	req := &Request{ID: "req-race-loss", TaskID: "T-Race", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb-race", "approve:req-race-loss", "u1", "loser")
	if !handled {
		t.Fatal("expected callback to still be handled on a lost race")
	}

	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 {
		t.Fatalf("expected 1 answered callback, got %d", len(cbs))
	}
	if cbs[0].Text != "Already decided" {
		t.Errorf("answer text = %q, want %q", cbs[0].Text, "Already decided")
	}

	edited := client.getEditedMessages()
	if len(edited) != 1 {
		t.Fatalf("expected 1 edited message, got %d", len(edited))
	}
	if !containsString(edited[0].Text, "ALREADY DECIDED") {
		t.Errorf("edited message = %q, want it to mention ALREADY DECIDED", edited[0].Text)
	}
	if containsString(edited[0].Text, "APPROVED") {
		t.Errorf("edited message = %q, must not claim the race-losing decision was applied", edited[0].Text)
	}

	// A race loser must never register a merge-follow-up target, even though
	// its own decision was "approved" — only the winner's decision reaches a
	// merge.
	handler.mu.RLock()
	_, resolved := handler.resolved["req-race-loss"]
	handler.mu.RUnlock()
	if resolved {
		t.Error("race-losing decision must not be tracked in resolved (no merge follow-up for a loser)")
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

// --- Callback logging + rehydrate re-notification tests (GH-4159) ---

// TestTelegramHandler_HandleCallback_UnknownRequest_LogsInfo is the GH-4159
// regression test: a tap for a request not in h.pending (e.g. landing during
// a restart window before Rehydrate runs) must produce a visible INFO log
// with request_id and user — previously this path logged nothing at all.
func TestTelegramHandler_HandleCallback_UnknownRequest_LogsInfo(t *testing.T) {
	client := &mockTelegramClient{}
	logger, buf := newCapturingLogger()
	handler := NewTelegramHandler(client, "chat123", 0)
	handler.log = logger

	handled := handler.HandleCallback(context.Background(), "cb-unknown", "approve:ghost-request", "u1", "tester")
	if !handled {
		t.Error("expected callback to be handled")
	}

	out := buf.String()
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("expected INFO level log for unknown-request tap, got: %s", out)
	}
	if !strings.Contains(out, "request_id=ghost-request") {
		t.Errorf("expected request_id in log, got: %s", out)
	}
	if !strings.Contains(out, "user=tester") {
		t.Errorf("expected user in log, got: %s", out)
	}
}

// TestTelegramHandler_HandleCallback_AnswerCallbackError_LogsWarn ensures an
// AnswerCallback failure is surfaced at WARN rather than silently swallowed
// (GH-4159) — this covers the success-path call site.
func TestTelegramHandler_HandleCallback_AnswerCallbackError_LogsWarn(t *testing.T) {
	client := &mockTelegramClient{answerError: errors.New("telegram api down")}
	logger, buf := newCapturingLogger()
	handler := NewTelegramHandler(client, "chat123", 0)
	handler.log = logger

	req := &Request{ID: "req-answer-fail", TaskID: "T-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb-1", "approve:req-answer-fail", "u1", "tester")
	if !handled {
		t.Error("expected callback to be handled even when AnswerCallback fails")
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level log for AnswerCallback failure, got: %s", out)
	}
	if !strings.Contains(out, "failed to answer approval callback") {
		t.Errorf("expected AnswerCallback failure message, got: %s", out)
	}
}

// TestTelegramHandler_HandleCallback_UnknownRequest_AnswerCallbackError_LogsWarn
// covers the unknown-request call site's AnswerCallback error path.
func TestTelegramHandler_HandleCallback_UnknownRequest_AnswerCallbackError_LogsWarn(t *testing.T) {
	client := &mockTelegramClient{answerError: errors.New("telegram api down")}
	logger, buf := newCapturingLogger()
	handler := NewTelegramHandler(client, "chat123", 0)
	handler.log = logger

	handled := handler.HandleCallback(context.Background(), "cb-1", "approve:ghost", "u1", "tester")
	if !handled {
		t.Error("expected callback to be handled even when AnswerCallback fails")
	}

	out := buf.String()
	if !strings.Contains(out, "failed to answer unknown-request approval callback") {
		t.Errorf("expected unknown-request AnswerCallback failure message, got: %s", out)
	}
}

// TestTelegramHandler_HandleCallback_ExpiredButPending_AnswerCallbackError_LogsWarn
// covers the expired-but-still-pending call site's AnswerCallback error path.
func TestTelegramHandler_HandleCallback_ExpiredButPending_AnswerCallbackError_LogsWarn(t *testing.T) {
	client := &mockTelegramClient{answerError: errors.New("telegram api down")}
	logger, buf := newCapturingLogger()
	handler := NewTelegramHandler(client, "chat123", 0)
	handler.log = logger

	req := &Request{ID: "req-exp-answer-fail", TaskID: "T-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(-time.Minute)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb-1", "approve:req-exp-answer-fail", "u1", "tester")
	if !handled {
		t.Error("expected callback to be handled even when AnswerCallback fails")
	}

	out := buf.String()
	if !strings.Contains(out, "failed to answer expired approval callback") {
		t.Errorf("expected expired-request AnswerCallback failure message, got: %s", out)
	}
}

// TestTelegramHandler_HandleCallback_ApproversGate covers the GH-5155
// identity check: when Request.Approvers is non-empty, only a userID that
// exactly matches one of its entries may decide the request. A rejected tap
// must not mutate any state — the request stays pending so the real
// approver can still act.
func TestTelegramHandler_HandleCallback_ApproversGate(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{
		ID:        "req-approvers-gate",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		Approvers: []string{"12345", "67890"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A user not in Approvers must be rejected without consuming the request.
	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-approvers-gate", "intruder", "intruder")
	if !handled {
		t.Error("expected callback to be handled (even when unauthorized)")
	}

	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 || cbs[0].Text != "You are not authorized to decide this request" {
		t.Fatalf("expected unauthorized answer text, got: %+v", cbs)
	}
	select {
	case resp := <-respCh:
		t.Fatalf("expected no response for unauthorized tap, got: %+v", resp)
	default:
	}

	handler.mu.RLock()
	_, stillPending := handler.pending["req-approvers-gate"]
	handler.mu.RUnlock()
	if !stillPending {
		t.Fatal("expected request to remain pending after an unauthorized tap")
	}

	// The listed approver can still decide it afterwards.
	handled = handler.HandleCallback(context.Background(), "cb2", "approve:req-approvers-gate", "67890", "approver")
	if !handled {
		t.Error("expected callback to be handled")
	}
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

// TestTelegramHandler_HandleCallback_AllowlistFallback covers the GH-5155
// fallback path: when Request.Approvers is empty, authorization falls back
// to the handler-scoped allowlist set via WithAllowedUsers.
func TestTelegramHandler_HandleCallback_AllowlistFallback(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0).WithAllowedUsers([]string{"allowed-1"})

	req := &Request{
		ID:        "req-allowlist-fallback",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Not in the handler allowlist -> rejected.
	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-allowlist-fallback", "not-allowed", "someone")
	if !handled {
		t.Error("expected callback to be handled (even when unauthorized)")
	}
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 || cbs[0].Text != "You are not authorized to decide this request" {
		t.Fatalf("expected unauthorized answer text, got: %+v", cbs)
	}

	// In the handler allowlist -> authorized.
	handled = handler.HandleCallback(context.Background(), "cb2", "approve:req-allowlist-fallback", "allowed-1", "someone-else")
	if !handled {
		t.Error("expected callback to be handled")
	}
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

// TestTelegramHandler_HandleCallback_UnrestrictedWhenNoApproversOrAllowlist
// covers the GH-5155 default: empty Approvers and no handler allowlist means
// any user may decide the request (preserves prior behavior).
func TestTelegramHandler_HandleCallback_UnrestrictedWhenNoApproversOrAllowlist(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{
		ID:        "req-unrestricted",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-unrestricted", "anyone", "anyone")
	if !handled {
		t.Error("expected callback to be handled")
	}
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}

// TestTelegramHandler_Rehydrate_ResendsFreshPromptPerPendingRequest is the
// GH-4159 regression test: after a restart, Rehydrate must send a fresh,
// actionable prompt for each restored request (same request_id/buttons) so
// the user has a live message to tap, since the pre-restart message's
// callback query may have expired during downtime.
func TestTelegramHandler_Rehydrate_ResendsFreshPromptPerPendingRequest(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-resend", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	sent := client.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 resent prompt, got %d", len(sent))
	}
	if !containsString(sent[0].Text, "rehydrated after restart") {
		t.Errorf("expected rehydrate notice in resent prompt, got: %s", sent[0].Text)
	}
	if len(sent[0].Keyboard) != 1 || len(sent[0].Keyboard[0]) != 2 {
		t.Fatalf("expected approve/reject keyboard on resent prompt, got %v", sent[0].Keyboard)
	}
	if sent[0].Keyboard[0][0].CallbackData != "approve:rehy-resend" {
		t.Errorf("expected same request_id in callback data, got %s", sent[0].Keyboard[0][0].CallbackData)
	}

	// The resent message's ID must be tracked so future edits target it.
	handler.mu.RLock()
	p := handler.pending["rehy-resend"]
	handler.mu.RUnlock()
	if p == nil || p.MessageID == 0 {
		t.Fatal("expected rehydrated entry to carry the resent message's MessageID")
	}
}

// TestTelegramHandler_Rehydrate_NoSpamOnRepeatedCalls ensures a request
// already tracked in h.pending is not re-notified on a second Rehydrate call
// — otherwise every startup retry (or duplicate Rehydrate invocation) would
// spam the user with a fresh prompt for the same pending request.
func TestTelegramHandler_Rehydrate_NoSpamOnRepeatedCalls(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-once", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("first rehydrate error: %v", err)
	}
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("second rehydrate error: %v", err)
	}
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("third rehydrate error: %v", err)
	}

	sent := client.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected exactly 1 resent prompt across repeated Rehydrate calls, got %d", len(sent))
	}
}

// TestTelegramHandler_Rehydrate_ResendFailureIsNonFatal ensures a failure to
// resend the fresh prompt does not fail Rehydrate itself — the request stays
// tracked in h.pending so a tap against the (possibly still-live) original
// message can still be processed.
func TestTelegramHandler_Rehydrate_ResendFailureIsNonFatal(t *testing.T) {
	client := &mockTelegramClient{sendError: errors.New("network error")}
	store := newMockPendingStore()
	logger, buf := newCapturingLogger()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-fail", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	handler.log = logger

	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("expected Rehydrate to succeed even if the resend fails, got: %v", err)
	}

	handler.mu.RLock()
	_, exists := handler.pending["rehy-fail"]
	handler.mu.RUnlock()
	if !exists {
		t.Error("expected request to remain tracked in h.pending despite resend failure")
	}

	out := buf.String()
	if !strings.Contains(out, "failed to resend rehydrated approval prompt") {
		t.Errorf("expected resend-failure warning, got: %s", out)
	}
}

// --- PruneExpired tests (GH-3825) ---

func TestTelegramHandler_PruneExpired_EditsMessageAndRemoves(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	req := &Request{
		ID: "exp-1", TaskID: "T-1", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(-time.Minute),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	handler.mu.RLock()
	_, stillPending := handler.pending["exp-1"]
	handler.mu.RUnlock()
	if stillPending {
		t.Error("expected expired request to be removed from pending map")
	}

	edited := client.getEditedMessages()
	if len(edited) != 1 {
		t.Fatalf("expected 1 edited message, got %d", len(edited))
	}
	if !containsString(edited[0].Text, "expired") && !containsString(edited[0].Text, "EXPIRED") {
		t.Errorf("expected edited message to mention expiry, got: %s", edited[0].Text)
	}

	if store.get("exp-1") != nil {
		t.Error("expected persisted row to be deleted")
	}
}

func TestTelegramHandler_PruneExpired_LeavesNonExpired(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	expired := &Request{ID: "exp-2", TaskID: "T-2", Stage: StagePreMerge, Title: "Old", ExpiresAt: time.Now().Add(-time.Minute)}
	live := &Request{ID: "live-2", TaskID: "T-3", Stage: StagePreMerge, Title: "Fresh", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), expired); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := handler.SendApprovalRequest(context.Background(), live); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	handler.mu.RLock()
	_, expiredPending := handler.pending["exp-2"]
	_, livePending := handler.pending["live-2"]
	handler.mu.RUnlock()
	if expiredPending {
		t.Error("expected expired request to be pruned")
	}
	if !livePending {
		t.Error("expected non-expired request to remain pending")
	}
	if store.get("live-2") == nil {
		t.Error("expected non-expired row to remain persisted")
	}
}

// TestTelegramHandler_PruneExpired_RehydratedGetsFreshMessageID covers the
// GH-4159 rehydrate re-notification: Rehydrate resends a fresh prompt for
// each newly-restored request, so unlike the pre-fix behavior the entry now
// carries a live MessageID (from the resend) and PruneExpired can edit it.
func TestTelegramHandler_PruneExpired_RehydratedGetsFreshMessageID(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	// Insert with a short-lived future expiry so Rehydrate accepts it, then
	// let it lapse before pruning.
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-prune", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(20 * time.Millisecond),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	// Rehydrate should have resent a fresh prompt carrying a live MessageID.
	sent := client.getSentMessages()
	if len(sent) != 1 {
		t.Fatalf("expected 1 resent rehydrate prompt, got %d", len(sent))
	}

	time.Sleep(30 * time.Millisecond)

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	// The resent message now has a known MessageID, so PruneExpired should
	// edit it to show expiry.
	if len(client.getEditedMessages()) != 1 {
		t.Errorf("expected 1 message edit for the resent rehydrate prompt, got %d", len(client.getEditedMessages()))
	}
	handler.mu.RLock()
	_, stillPending := handler.pending["rehy-prune"]
	handler.mu.RUnlock()
	if stillPending {
		t.Error("expected rehydrated expired request to be removed from pending map")
	}
	if store.get("rehy-prune") != nil {
		t.Error("expected persisted row to be deleted")
	}
}

// TestTelegramHandler_PruneExpired_RehydratedResendFailure_NoMessageID covers
// the case where Rehydrate's resend fails (e.g. network error) — the entry
// keeps MessageID 0, so PruneExpired must not attempt an edit against it.
func TestTelegramHandler_PruneExpired_RehydratedResendFailure_NoMessageID(t *testing.T) {
	client := &mockTelegramClient{sendError: errors.New("network error")}
	store := newMockPendingStore()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-prune-fail", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(20 * time.Millisecond),
	})

	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	time.Sleep(30 * time.Millisecond)

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	if len(client.getEditedMessages()) != 0 {
		t.Errorf("expected no message edit when resend failed and no MessageID is known, got %d", len(client.getEditedMessages()))
	}
	if store.get("rehy-prune-fail") != nil {
		t.Error("expected persisted row to be deleted")
	}
}

func TestTelegramHandler_PruneExpired_NoStore(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{ID: "exp-3", TaskID: "T-4", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(-time.Minute)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error without a store: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}
	if len(client.getEditedMessages()) != 1 {
		t.Errorf("expected message to still be edited without a store, got %d edits", len(client.getEditedMessages()))
	}
}

func TestTelegramHandler_PruneExpired_SweepsOrphanedStoreRows(t *testing.T) {
	client := &mockTelegramClient{}
	store := newMockPendingStore()
	handler := NewTelegramHandler(client, "chat123", 0).WithStore(store)

	// A row with no in-memory pending counterpart — e.g. left behind by a
	// process that crashed before Rehydrate ran.
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "orphan-1", TaskID: "T-5", Stage: "pre_merge",
		Title: "Orphan", CreatedAt: time.Now().Add(-2 * time.Hour), ExpiresAt: time.Now().Add(-time.Hour),
	})

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 in-memory prunes (orphan was never in pending), got %d", n)
	}
	if store.get("orphan-1") != nil {
		t.Error("expected orphaned expired row to be swept from the store")
	}
}

// --- EditMessage fallback + NotifyMerged tests (GH-4164) ---

// TestTelegramHandler_HandleCallback_EditFailure_FallsBackToNewMessage is the
// GH-4164 regression test: when EditMessage fails on the ack card, the
// handler must send a brand-new message with the same content so a recorded
// decision is never left with zero user feedback.
func TestTelegramHandler_HandleCallback_EditFailure_FallsBackToNewMessage(t *testing.T) {
	client := &mockTelegramClient{editError: errors.New("edit failed")}
	logger, buf := newCapturingLogger()
	handler := NewTelegramHandler(client, "chat123", 0)
	handler.log = logger

	req := &Request{ID: "req-fallback", TaskID: "T-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-fallback", "u1", "tester")
	if !handled {
		t.Fatal("expected callback to be handled")
	}

	// No successful edit — client returns an error for every EditMessage call.
	if len(client.getEditedMessages()) != 0 {
		t.Errorf("expected no successful edits, got %d", len(client.getEditedMessages()))
	}

	sent := client.getSentMessages()
	if len(sent) != 2 { // 1 = original approval prompt, 2 = fallback response card
		t.Fatalf("expected 2 sent messages (prompt + fallback), got %d", len(sent))
	}
	fallback := sent[len(sent)-1]
	if fallback.ChatID != "chat123" {
		t.Errorf("expected fallback chat_id 'chat123', got '%s'", fallback.ChatID)
	}
	if !containsString(fallback.Text, "APPROVED") {
		t.Errorf("expected fallback message to carry the response card text, got: %s", fallback.Text)
	}

	out := buf.String()
	if !strings.Contains(out, "falling back to a new message") {
		t.Errorf("expected fallback log line, got: %s", out)
	}
}

// TestTelegramHandler_NotifyMerged_SendsFollowUpInSameChat verifies that once
// a request has been approved via Telegram, NotifyMerged posts a short
// merge-completion follow-up to the same chat the approval card was sent to.
func TestTelegramHandler_NotifyMerged_SendsFollowUpInSameChat(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{
		ID: "req-merged", TaskID: "GH-1", Stage: StagePreMerge, Title: "Test",
		Approvers: []string{"99999"}, ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// userID must match the configured Approvers entry (GH-5155) for the
	// callback to be authorized.
	handler.HandleCallback(context.Background(), "cb1", "approve:req-merged", "99999", "tester")

	sentBefore := len(client.getSentMessages())

	if err := handler.NotifyMerged(context.Background(), "req-merged", "abc1234"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sent := client.getSentMessages()
	if len(sent) != sentBefore+1 {
		t.Fatalf("expected 1 additional message, got %d total (was %d)", len(sent), sentBefore)
	}
	followUp := sent[len(sent)-1]
	if followUp.ChatID != "99999" {
		t.Errorf("expected follow-up chat_id '99999' (same as approval), got '%s'", followUp.ChatID)
	}
	if !containsString(followUp.Text, "Merged") || !containsString(followUp.Text, "abc1234") {
		t.Errorf("expected follow-up to mention the merge and short SHA, got: %s", followUp.Text)
	}
}

// TestTelegramHandler_NotifyMerged_UnknownRequestIsNoOp ensures a requestID
// with no resolved (approved) decision — e.g. never approved via Telegram,
// or already notified once — is a silent no-op.
func TestTelegramHandler_NotifyMerged_UnknownRequestIsNoOp(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	if err := handler.NotifyMerged(context.Background(), "never-approved", "abc1234"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.getSentMessages()) != 0 {
		t.Errorf("expected no messages sent for an unresolved request, got %d", len(client.getSentMessages()))
	}
}

// TestTelegramHandler_NotifyMerged_FiresOnceThenNoOp ensures a second
// NotifyMerged call for the same requestID (e.g. a duplicate merge-transition
// tick) does not send a second follow-up.
func TestTelegramHandler_NotifyMerged_FiresOnceThenNoOp(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{ID: "req-once", TaskID: "GH-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	handler.HandleCallback(context.Background(), "cb1", "approve:req-once", "u1", "tester")

	if err := handler.NotifyMerged(context.Background(), "req-once", "abc1234"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	afterFirst := len(client.getSentMessages())

	if err := handler.NotifyMerged(context.Background(), "req-once", "def5678"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.getSentMessages()) != afterFirst {
		t.Errorf("expected no additional message on second NotifyMerged call, got %d (was %d)", len(client.getSentMessages()), afterFirst)
	}
}

// TestTelegramHandler_PruneExpired_SweepsStaleResolvedDecisions ensures an
// approved decision that never reaches NotifyMerged within resolvedRetention
// is dropped from the resolved map so it cannot grow unbounded.
func TestTelegramHandler_PruneExpired_SweepsStaleResolvedDecisions(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0)

	req := &Request{ID: "req-stale", TaskID: "GH-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	handler.HandleCallback(context.Background(), "cb1", "approve:req-stale", "u1", "tester")

	handler.mu.Lock()
	handler.resolved["req-stale"].DecidedAt = time.Now().Add(-resolvedRetention - time.Minute)
	handler.mu.Unlock()

	if _, err := handler.PruneExpired(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.RLock()
	_, exists := handler.resolved["req-stale"]
	handler.mu.RUnlock()
	if exists {
		t.Error("expected stale resolved decision to be swept")
	}
}

func TestTelegramHandlerMessageThreadID(t *testing.T) {
	tests := []struct {
		name            string
		messageThreadID int64
		approvers       []string
		wantChatID      string
		wantThreadID    int64
	}{
		{
			name:            "topic applied to the configured chat",
			messageThreadID: 12,
			approvers:       []string{},
			wantChatID:      "configured-chat",
			wantThreadID:    12,
		},
		{
			name:            "topic dropped for an approver override",
			messageThreadID: 12,
			approvers:       []string{"99999"},
			wantChatID:      "99999",
			wantThreadID:    0,
		},
		{
			name:            "invalid approver falls back to chat and keeps the topic",
			messageThreadID: 12,
			approvers:       []string{"#slack-channel"},
			wantChatID:      "configured-chat",
			wantThreadID:    12,
		},
		{
			name:            "no topic configured",
			messageThreadID: 0,
			approvers:       []string{},
			wantChatID:      "configured-chat",
			wantThreadID:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockTelegramClient{}
			handler := NewTelegramHandler(client, "configured-chat", tt.messageThreadID)

			req := &Request{
				ID:        "req-thread",
				TaskID:    "TASK-01",
				Stage:     StagePreMerge,
				Title:     "Test",
				Approvers: tt.approvers,
				ExpiresAt: time.Now().Add(1 * time.Hour),
			}

			if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			msgs := client.getSentMessages()
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message sent, got %d", len(msgs))
			}
			if msgs[0].ChatID != tt.wantChatID {
				t.Errorf("chat_id = %q, want %q", msgs[0].ChatID, tt.wantChatID)
			}
			if msgs[0].ThreadID != tt.wantThreadID {
				t.Errorf("message_thread_id = %d, want %d", msgs[0].ThreadID, tt.wantThreadID)
			}
		})
	}
}

// TestTelegramHandler_WithAllowedIDs_AliasesWithAllowedUsers covers GH-5157:
// WithAllowedIDs is an alternate-named builder for the same allowlist as
// WithAllowedUsers, so it must gate HandleCallback identically.
func TestTelegramHandler_WithAllowedIDs_AliasesWithAllowedUsers(t *testing.T) {
	client := &mockTelegramClient{}
	handler := NewTelegramHandler(client, "chat123", 0).WithAllowedIDs([]string{"allowed-1"})

	req := &Request{
		ID:        "req-with-allowed-ids",
		TaskID:    "TASK-01",
		Stage:     StagePreExecution,
		Title:     "Test task",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	respCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleCallback(context.Background(), "cb1", "approve:req-with-allowed-ids", "not-allowed", "someone")
	if !handled {
		t.Error("expected callback to be handled (even when unauthorized)")
	}
	cbs := client.getAnsweredCallbacks()
	if len(cbs) != 1 || cbs[0].Text != "You are not authorized to decide this request" {
		t.Fatalf("expected unauthorized answer text, got: %+v", cbs)
	}

	handled = handler.HandleCallback(context.Background(), "cb2", "approve:req-with-allowed-ids", "allowed-1", "someone-else")
	if !handled {
		t.Error("expected callback to be handled")
	}
	select {
	case resp := <-respCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %s", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for response")
	}
}
