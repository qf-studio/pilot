package slack

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestNewHandler(t *testing.T) {
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})

	if h.socketClient == nil {
		t.Error("NewHandler() should initialize socketClient")
	}
	if h.apiClient == nil {
		t.Error("NewHandler() should initialize apiClient")
	}
	if h.log == nil {
		t.Error("NewHandler() should initialize logger")
	}
}

func TestNewHandler_WithClient(t *testing.T) {
	client := NewClient("xoxb-test-token")
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		Client:   client,
	})

	if h.apiClient != client {
		t.Error("NewHandler() should reuse provided client")
	}
}

func TestHandler_IsAllowed(t *testing.T) {
	tests := []struct {
		name            string
		allowedChannels []string
		allowedUsers    []string
		channelID       string
		userID          string
		want            bool
	}{
		{
			name:            "no restrictions allows all",
			allowedChannels: nil,
			allowedUsers:    nil,
			channelID:       "C123",
			userID:          "U456",
			want:            true,
		},
		{
			name:            "allowed channel",
			allowedChannels: []string{"C123"},
			allowedUsers:    nil,
			channelID:       "C123",
			userID:          "U456",
			want:            true,
		},
		{
			name:            "disallowed channel",
			allowedChannels: []string{"C999"},
			allowedUsers:    nil,
			channelID:       "C123",
			userID:          "U456",
			want:            false,
		},
		{
			name:            "allowed user",
			allowedChannels: nil,
			allowedUsers:    []string{"U456"},
			channelID:       "C123",
			userID:          "U456",
			want:            true,
		},
		{
			name:            "disallowed user",
			allowedChannels: nil,
			allowedUsers:    []string{"U999"},
			channelID:       "C123",
			userID:          "U456",
			want:            false,
		},
		{
			name:            "allowed by channel when both configured",
			allowedChannels: []string{"C123"},
			allowedUsers:    []string{"U999"},
			channelID:       "C123",
			userID:          "U456",
			want:            true,
		},
		{
			name:            "allowed by user when both configured",
			allowedChannels: []string{"C999"},
			allowedUsers:    []string{"U456"},
			channelID:       "C123",
			userID:          "U456",
			want:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&HandlerConfig{
				AppToken:        "xapp-test-token",
				BotToken:        "xoxb-test-token",
				AllowedChannels: tt.allowedChannels,
				AllowedUsers:    tt.allowedUsers,
			})

			got := h.isAllowed(tt.channelID, tt.userID)
			if got != tt.want {
				t.Errorf("isAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMemberResolverAdapter(t *testing.T) {
	resolver := &mockMemberResolver{
		mappings: map[string]string{
			"U67890": "member-alice",
		},
	}

	adapter := &MemberResolverAdapter{Inner: resolver}

	memberID, err := adapter.ResolveIdentity("U67890")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if memberID != "member-alice" {
		t.Errorf("ResolveIdentity() = %q, want %q", memberID, "member-alice")
	}

	// Unknown user
	memberID, err = adapter.ResolveIdentity("U99999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if memberID != "" {
		t.Errorf("ResolveIdentity() for unknown = %q, want empty", memberID)
	}
}

// mockMemberResolver implements MemberResolver for testing.
type mockMemberResolver struct {
	mappings map[string]string // slackUserID -> memberID
}

func (m *mockMemberResolver) ResolveSlackIdentity(slackUserID, email string) (string, error) {
	if m.mappings == nil {
		return "", nil
	}
	return m.mappings[slackUserID], nil
}

// mockCommsHandler records HandleMessage calls for shim path tests.
type mockCommsHandler struct {
	got []*comms.IncomingMessage
}

func (m *mockCommsHandler) HandleMessage(_ context.Context, msg *comms.IncomingMessage) {
	m.got = append(m.got, msg)
}

func (m *mockCommsHandler) CleanupLoop(_ context.Context) {}

func TestHandler_HandleMessage_MessageAction(t *testing.T) {
	mock := &mockCommsHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	h.commsHandler = mock

	ev := core.MessageEvent{
		Action:    "message",
		ChannelID: "C123",
		ThreadID:  "T456",
		Text:      "deploy the app",
		Sender:    core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mock.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages, want 1", len(mock.got))
	}
	got := mock.got[0]
	if got.ContextID != "C123" {
		t.Errorf("ContextID = %q, want C123", got.ContextID)
	}
	if got.SenderID != "U789" {
		t.Errorf("SenderID = %q, want U789", got.SenderID)
	}
	if got.Text != "deploy the app" {
		t.Errorf("Text = %q, want %q", got.Text, "deploy the app")
	}
	if got.Platform != "slack" {
		t.Errorf("Platform = %q, want slack", got.Platform)
	}
	if got.IsCallback {
		t.Error("IsCallback should be false for message action")
	}
}

func TestHandler_HandleMessage_CommandAction(t *testing.T) {
	mock := &mockCommsHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	h.commsHandler = mock

	ev := core.MessageEvent{
		Action:    "command",
		ChannelID: "C123",
		Command:   "/run",
		Args:      []string{"42"},
		Sender:    core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mock.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages, want 1", len(mock.got))
	}
	got := mock.got[0]
	if got.Platform != "slack" {
		t.Errorf("Platform = %q, want slack", got.Platform)
	}
	if got.IsCallback {
		t.Error("IsCallback should be false for command action")
	}
	// The SDK bridge delivers commands with Text empty and Command/Args set.
	// HandleMessage must reconstruct the command line so comms routes it via
	// IntentCommand → CommandHandler instead of creating a task from empty text.
	if got.Text != "/run 42" {
		t.Errorf("Text = %q, want %q (command line must be reconstructed)", got.Text, "/run 42")
	}
}

// TestHandler_HandleMessage_CommandHelpReconstructed is the regression test for
// the live "@Pilot /help creates a task" bug. The SDK bridge intercepts the
// "/" prefix upstream and delivers /help as a command event with empty Text;
// HandleMessage must put "/help" into Text so comms.detectIntent returns
// IntentCommand (→ CommandHandler) rather than defaulting an empty message to a task.
func TestHandler_HandleMessage_CommandHelpReconstructed(t *testing.T) {
	mock := &mockCommsHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	h.commsHandler = mock

	ev := core.MessageEvent{
		Action:    "command",
		ChannelID: "C123",
		Command:   "/help",
		Sender:    core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mock.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages, want 1", len(mock.got))
	}
	if got := mock.got[0].Text; got != "/help" {
		t.Errorf("Text = %q, want %q (command must reach comms as /help, not empty)", got, "/help")
	}
}

func TestHandler_HandleMessage_CallbackAction(t *testing.T) {
	mock := &mockCommsHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	h.commsHandler = mock

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "execute",
		Data:       "execute",
		Sender:     core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mock.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages, want 1", len(mock.got))
	}
	got := mock.got[0]
	if !got.IsCallback {
		t.Error("IsCallback should be true for callback action")
	}
	if got.ActionID != "execute" {
		t.Errorf("ActionID = %q, want execute", got.ActionID)
	}
	if got.CallbackID != "execute" {
		t.Errorf("CallbackID = %q, want execute", got.CallbackID)
	}
}

// mockApprovalHandler records HandleInteraction calls for approval-routing tests (GH-4431).
type mockApprovalHandler struct {
	calls []approvalCall
	ret   bool
}

type approvalCall struct {
	actionID, value, userID, username, responseURL string
}

func (m *mockApprovalHandler) HandleInteraction(_ context.Context, actionID, value, userID, username, responseURL string) bool {
	m.calls = append(m.calls, approvalCall{actionID, value, userID, username, responseURL})
	return m.ret
}

// TestHandler_HandleMessage_ApprovalCallback_Approve is the regression test for
// GH-4431: a socket-mode "Merge"/"Approve" button click must be routed to the
// approval handler, not forwarded to comms (which has no concept of approval
// requests and previously replied "No pending task to confirm." for every
// approval click).
func TestHandler_HandleMessage_ApprovalCallback_Approve(t *testing.T) {
	mockComms := &mockCommsHandler{}
	mockApproval := &mockApprovalHandler{ret: true}
	h := NewHandler(&HandlerConfig{
		AppToken:        "xapp-test-token",
		BotToken:        "xoxb-test-token",
		ApprovalHandler: mockApproval,
	})
	h.commsHandler = mockComms

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "approve",
		Data:       "approve:REQ-1",
		Sender:     core.Identity{UserID: "U789", DisplayName: "alice"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mockComms.got) != 0 {
		t.Fatalf("HandleMessage() delivered %d messages to comms, want 0 (approval clicks must not reach comms)", len(mockComms.got))
	}
	if len(mockApproval.calls) != 1 {
		t.Fatalf("HandleMessage() called approval handler %d times, want 1", len(mockApproval.calls))
	}
	call := mockApproval.calls[0]
	if call.actionID != "approve" {
		t.Errorf("actionID = %q, want approve", call.actionID)
	}
	if call.value != "approve:REQ-1" {
		t.Errorf("value = %q, want approve:REQ-1", call.value)
	}
	if call.userID != "U789" {
		t.Errorf("userID = %q, want U789", call.userID)
	}
	if call.username != "alice" {
		t.Errorf("username = %q, want alice", call.username)
	}
}

// TestHandler_HandleMessage_ApprovalCallback_Reject mirrors the Approve case
// for the reject button, and covers the value-prefix fallback match (GH-4431).
func TestHandler_HandleMessage_ApprovalCallback_Reject(t *testing.T) {
	mockApproval := &mockApprovalHandler{ret: true}
	h := NewHandler(&HandlerConfig{
		AppToken:        "xapp-test-token",
		BotToken:        "xoxb-test-token",
		ApprovalHandler: mockApproval,
	})

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "reject",
		Data:       "reject:REQ-2",
		Sender:     core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mockApproval.calls) != 1 {
		t.Fatalf("HandleMessage() called approval handler %d times, want 1", len(mockApproval.calls))
	}
	if mockApproval.calls[0].value != "reject:REQ-2" {
		t.Errorf("value = %q, want reject:REQ-2", mockApproval.calls[0].value)
	}
}

// TestHandler_HandleMessage_ExecuteCallback_StillReachesComms verifies the
// execute/cancel task-confirmation buttons are unaffected by the approval
// routing added for GH-4431 — they must still reach comms.
func TestHandler_HandleMessage_ExecuteCallback_StillReachesComms(t *testing.T) {
	mockComms := &mockCommsHandler{}
	mockApproval := &mockApprovalHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken:        "xapp-test-token",
		BotToken:        "xoxb-test-token",
		ApprovalHandler: mockApproval,
	})
	h.commsHandler = mockComms

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "execute",
		Data:       "execute",
		Sender:     core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mockApproval.calls) != 0 {
		t.Errorf("execute callback must not be routed to approval handler, got %d calls", len(mockApproval.calls))
	}
	if len(mockComms.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages to comms, want 1", len(mockComms.got))
	}
	if !mockComms.got[0].IsCallback || mockComms.got[0].ActionID != "execute" {
		t.Errorf("expected execute callback to reach comms unchanged, got %+v", mockComms.got[0])
	}
}

// TestHandler_HandleMessage_ApprovalCallback_NilApprovalHandler covers a
// deployment where approval isn't configured (nil ApprovalHandler): the
// approve/reject click falls through to comms rather than panicking. comms
// will reply "Unknown action" per the GH-4431 fallthrough fix, but the
// important thing here is HandleMessage must not panic on a nil handler.
func TestHandler_HandleMessage_ApprovalCallback_NilApprovalHandler(t *testing.T) {
	mockComms := &mockCommsHandler{}
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	h.commsHandler = mockComms

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "approve",
		Data:       "approve:REQ-3",
		Sender:     core.Identity{UserID: "U789"},
	}

	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	if len(mockComms.got) != 1 {
		t.Fatalf("HandleMessage() delivered %d messages to comms, want 1 (fallback when no approval handler configured)", len(mockComms.got))
	}
}

func TestHandler_HandleMessage_NilCommsHandler(t *testing.T) {
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	})
	// commsHandler is nil — HandleMessage must not panic.
	ev := core.MessageEvent{Action: "message", Text: "hello", ChannelID: "C1"}
	if err := h.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage() with nil commsHandler error = %v", err)
	}
}

func TestRateLimiter(t *testing.T) {
	config := &comms.RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 5,
		TasksPerHour:      2,
		BurstSize:         3,
	}

	limiter := comms.NewRateLimiter(config)

	// Should allow up to burst size
	for i := 0; i < 3; i++ {
		if !limiter.AllowMessage("C123") {
			t.Errorf("AllowMessage() should allow message %d", i+1)
		}
	}

	// Should be rate limited after burst
	if limiter.AllowMessage("C123") {
		t.Error("AllowMessage() should rate limit after burst")
	}

	// Different channel should have its own bucket
	if !limiter.AllowMessage("C456") {
		t.Error("AllowMessage() should allow message for different channel")
	}

	// Task rate limiting
	for i := 0; i < 2; i++ {
		if !limiter.AllowTask("C789") {
			t.Errorf("AllowTask() should allow task %d", i+1)
		}
	}

	if limiter.AllowTask("C789") {
		t.Error("AllowTask() should rate limit after burst")
	}
}

func TestFormatter(t *testing.T) {
	t.Run("FormatGreeting with name", func(t *testing.T) {
		got := FormatGreeting("Alice")
		if got == "" {
			t.Error("FormatGreeting() should return non-empty string")
		}
		if !strings.Contains(got, "Alice") {
			t.Error("FormatGreeting() should include username")
		}
	})

	t.Run("FormatGreeting without name", func(t *testing.T) {
		got := FormatGreeting("")
		if got == "" {
			t.Error("FormatGreeting() should return non-empty string")
		}
	})

	t.Run("FormatProgressUpdate", func(t *testing.T) {
		got := FormatProgressUpdate("TASK-123", "Implementing", 50, "Working...")
		if got == "" {
			t.Error("FormatProgressUpdate() should return non-empty string")
		}
		if !strings.Contains(got, "TASK-123") {
			t.Error("FormatProgressUpdate() should include task ID")
		}
		if !strings.Contains(got, "50%") {
			t.Error("FormatProgressUpdate() should include percentage")
		}
	})

	t.Run("ChunkContent", func(t *testing.T) {
		short := "short text"
		chunks := ChunkContent(short, 100)
		if len(chunks) != 1 {
			t.Errorf("ChunkContent() for short text should return 1 chunk, got %d", len(chunks))
		}

		long := strings.Repeat("a", 200) + "\n" + strings.Repeat("b", 200)
		chunks = ChunkContent(long, 100)
		if len(chunks) <= 1 {
			t.Error("ChunkContent() for long text should return multiple chunks")
		}
	})

	t.Run("truncateText", func(t *testing.T) {
		short := "hello"
		got := truncateText(short, 10)
		if got != short {
			t.Errorf("truncateText() for short string = %q, want %q", got, short)
		}

		long := "hello world this is a long string"
		got = truncateText(long, 10)
		if len(got) > 10 {
			t.Errorf("truncateText() should truncate to max length, got len=%d", len(got))
		}
		if !strings.Contains(got, "...") {
			t.Error("truncateText() should add ellipsis")
		}
	})
}

func TestPlanningErrorMessage(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		ctxErr       error
		wantContains string
	}{
		{
			name:         "deadline exceeded surfaces timeout message",
			err:          context.DeadlineExceeded,
			ctxErr:       context.DeadlineExceeded,
			wantContains: "timed out",
		},
		{
			name:         "generic error surfaces error text",
			err:          errors.New("claude exited with code 1"),
			ctxErr:       nil,
			wantContains: "claude exited with code 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planningErrorMessage(tc.err, tc.ctxErr)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("planningErrorMessage() = %q, want string containing %q", got, tc.wantContains)
			}
		})
	}
}

func TestPlanEmptyMessage(t *testing.T) {
	tests := []struct {
		name          string
		resultError   string
		resultSuccess bool
		wantContains  string
	}{
		{
			name:          "executor error surfaced",
			resultError:   "claude exited with code 1",
			resultSuccess: false,
			wantContains:  "claude exited with code 1",
		},
		{
			name:          "error surfaced even when success is true",
			resultError:   "partial failure",
			resultSuccess: true,
			wantContains:  "partial failure",
		},
		{
			name:          "non-success without error indicates timeout",
			resultError:   "",
			resultSuccess: false,
			wantContains:  "timed out",
		},
		{
			name:          "success with no output suggests direct execution",
			resultError:   "",
			resultSuccess: true,
			wantContains:  "directly",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := planEmptyMessage(tc.resultError, tc.resultSuccess)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("planEmptyMessage(%q, %v) = %q, want string containing %q",
					tc.resultError, tc.resultSuccess, got, tc.wantContains)
			}
		})
	}
}
