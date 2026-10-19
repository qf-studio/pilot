package approval

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestSlackHandler_HandleInteraction_LogsReceivedBeforeLookup pins GH-5758:
// every approval click that reaches the handler is logged at INFO with its
// request ID and action, including one for a request this handler never saw
// (the failure mode when a click lands on a different handler instance).
func TestSlackHandler_HandleInteraction_LogsReceivedBeforeLookup(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantAction string
		wantReqID  string
	}{
		{"approve unknown request", "approve:never-seen", "approved", "never-seen"},
		{"reject unknown request", "reject:never-seen-2", "rejected", "never-seen-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := NewSlackHandler(&mockSlackClient{}, "#approvals")
			h.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			if !h.HandleInteraction(context.Background(), "act", tt.value, "U1", "alice", "") {
				t.Fatal("HandleInteraction returned false for an approval action")
			}

			var received string
			for _, line := range strings.Split(buf.String(), "\n") {
				if strings.Contains(line, "Slack approval interaction received") {
					received = line
				}
			}
			if received == "" {
				t.Fatalf("no 'received' log line; got:\n%s", buf.String())
			}
			if !strings.Contains(received, "level=INFO") ||
				!strings.Contains(received, "request_id="+tt.wantReqID) ||
				!strings.Contains(received, "action="+tt.wantAction) {
				t.Errorf("received line missing INFO/request_id/action: %s", received)
			}
		})
	}
}

// A non-approval value is not an approval interaction and must stay silent at INFO.
func TestSlackHandler_HandleInteraction_NonApprovalNotLogged(t *testing.T) {
	var buf bytes.Buffer
	h := NewSlackHandler(&mockSlackClient{}, "#approvals")
	h.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if h.HandleInteraction(context.Background(), "act", "execute:task", "U1", "alice", "") {
		t.Fatal("non-approval value must not be handled")
	}
	if strings.Contains(buf.String(), "received") {
		t.Errorf("unexpected log for non-approval value: %s", buf.String())
	}
}
