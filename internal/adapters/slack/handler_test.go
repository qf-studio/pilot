package slack

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/comms"
)

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

func TestHandler_TrackSender(t *testing.T) {
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	}, nil)

	// Track a sender
	h.TrackSender("C12345", "U67890")

	// Verify tracking
	got := h.GetLastSender("C12345")
	if got != "U67890" {
		t.Errorf("GetLastSender() = %q, want %q", got, "U67890")
	}

	// Empty channel/user should not track
	h.TrackSender("", "U11111")
	h.TrackSender("C22222", "")

	if h.GetLastSender("") != "" {
		t.Error("empty channel should not be tracked")
	}
	if h.GetLastSender("C22222") != "" {
		t.Error("empty user should not be tracked")
	}
}

func TestHandler_TrackSender_OverwritesPrevious(t *testing.T) {
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
	}, nil)

	h.TrackSender("C12345", "U11111")
	h.TrackSender("C12345", "U22222")

	got := h.GetLastSender("C12345")
	if got != "U22222" {
		t.Errorf("GetLastSender() = %q, want %q (should overwrite)", got, "U22222")
	}
}

func TestHandler_ResolveMemberID_NoResolver(t *testing.T) {
	h := NewHandler(&HandlerConfig{
		AppToken: "xapp-test-token",
		BotToken: "xoxb-test-token",
		// No MemberResolver
	}, nil)

	h.TrackSender("C12345", "U67890")

	// Without resolver, should return empty string
	got := h.resolveMemberID("C12345")
	if got != "" {
		t.Errorf("resolveMemberID() without resolver = %q, want empty", got)
	}
}

func TestHandler_ResolveMemberID_WithResolver(t *testing.T) {
	resolver := &mockMemberResolver{
		mappings: map[string]string{
			"U67890": "member-alice",
			"U11111": "member-bob",
		},
	}

	h := NewHandler(&HandlerConfig{
		AppToken:       "xapp-test-token",
		BotToken:       "xoxb-test-token",
		MemberResolver: resolver,
	}, nil)

	// Track and resolve
	h.TrackSender("C12345", "U67890")
	got := h.resolveMemberID("C12345")
	if got != "member-alice" {
		t.Errorf("resolveMemberID() = %q, want %q", got, "member-alice")
	}

	// Different channel, different user
	h.TrackSender("C99999", "U11111")
	got = h.resolveMemberID("C99999")
	if got != "member-bob" {
		t.Errorf("resolveMemberID() = %q, want %q", got, "member-bob")
	}
}

func TestHandler_ResolveMemberID_UnknownUser(t *testing.T) {
	resolver := &mockMemberResolver{
		mappings: map[string]string{
			"U67890": "member-alice",
		},
	}

	h := NewHandler(&HandlerConfig{
		AppToken:       "xapp-test-token",
		BotToken:       "xoxb-test-token",
		MemberResolver: resolver,
	}, nil)

	h.TrackSender("C12345", "U99999") // Unknown user
	got := h.resolveMemberID("C12345")
	if got != "" {
		t.Errorf("resolveMemberID() for unknown user = %q, want empty", got)
	}
}

func TestHandler_ResolveMemberID_NoSenderTracked(t *testing.T) {
	resolver := &mockMemberResolver{
		mappings: map[string]string{
			"U67890": "member-alice",
		},
	}

	h := NewHandler(&HandlerConfig{
		AppToken:       "xapp-test-token",
		BotToken:       "xoxb-test-token",
		MemberResolver: resolver,
	}, nil)

	// No sender tracked for this channel
	got := h.resolveMemberID("C12345")
	if got != "" {
		t.Errorf("resolveMemberID() with no sender = %q, want empty", got)
	}
}

func TestNewHandler(t *testing.T) {
	resolver := &mockMemberResolver{}

	h := NewHandler(&HandlerConfig{
		AppToken:       "xapp-test-token",
		BotToken:       "xoxb-test-token",
		MemberResolver: resolver,
	}, nil)

	if h.socketClient == nil {
		t.Error("NewHandler() should initialize socketClient")
	}
	if h.apiClient == nil {
		t.Error("NewHandler() should initialize apiClient")
	}
	if h.memberResolver != resolver {
		t.Error("NewHandler() should set memberResolver from config")
	}
	if h.lastSender == nil {
		t.Error("NewHandler() should initialize lastSender map")
	}
	if h.log == nil {
		t.Error("NewHandler() should initialize logger")
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
			}, nil)

			got := h.isAllowed(tt.channelID, tt.userID)
			if got != tt.want {
				t.Errorf("isAllowed() = %v, want %v", got, tt.want)
			}
		})
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
