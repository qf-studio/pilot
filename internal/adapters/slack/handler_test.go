package slack

import (
	"testing"
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
	})

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
	})

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
		// No MemberResolver
	})

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
		MemberResolver: resolver,
	})

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
		MemberResolver: resolver,
	})

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
		MemberResolver: resolver,
	})

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
		MemberResolver: resolver,
	})

	if h.client == nil {
		t.Error("NewHandler() should initialize client")
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
