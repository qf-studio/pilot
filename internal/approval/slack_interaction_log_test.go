package approval

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"
)

// delayAttr matches a non-negative delivery_delay_ms attribute.
var delayAttr = regexp.MustCompile(`delivery_delay_ms=\d+`)

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

			if !h.HandleInteraction(context.Background(), "act", tt.value, "U1", "alice", "", "") {
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

// TestSlackHandler_HandleInteraction_LogsActionTS pins GH-5999: the received
// line carries the click's action_ts verbatim and, when it parses, the delivery
// delay in ms. An empty or garbage action_ts omits the delay and never fails
// the click.
func TestSlackHandler_HandleInteraction_LogsActionTS(t *testing.T) {
	tests := []struct {
		name      string
		actionTS  string
		wantAttr  string
		wantDelay bool
	}{
		{"valid action_ts", "1793466563.669229", "action_ts=1793466563.669229", true},
		{"empty action_ts", "", `action_ts=""`, false},
		{"garbage action_ts", "garbage", "action_ts=garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			h := NewSlackHandler(&mockSlackClient{}, "#approvals")
			h.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			if !h.HandleInteraction(context.Background(), "act", "approve:ts-req", "U1", "alice", "", tt.actionTS) {
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
			if !strings.Contains(received, tt.wantAttr) {
				t.Errorf("received line missing %s: %s", tt.wantAttr, received)
			}
			m := delayAttr.FindStringSubmatch(received)
			if tt.wantDelay {
				if m == nil {
					t.Fatalf("received line missing non-negative delivery_delay_ms: %s", received)
				}
			} else if strings.Contains(received, "delivery_delay_ms") {
				t.Errorf("received line must omit delivery_delay_ms: %s", received)
			}
		})
	}
}

func TestDeliveryDelay(t *testing.T) {
	now := time.Unix(1793466565, 169229000) // 1.5s after the click below
	tests := []struct {
		ts     string
		want   time.Duration
		wantOK bool
	}{
		{"1793466563.669229", 1500 * time.Millisecond, true},
		{"", 0, false},
		{"garbage", 0, false},
		{"NaN", 0, false},
		{"Inf", 0, false},
		{"-5", 0, false},
	}
	for _, tt := range tests {
		got, ok := deliveryDelay(now, tt.ts)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("deliveryDelay(%q) = %v, %v; want %v, %v", tt.ts, got, ok, tt.want, tt.wantOK)
		}
	}
}

// A non-approval value is not an approval interaction and must stay silent at INFO.
func TestSlackHandler_HandleInteraction_NonApprovalNotLogged(t *testing.T) {
	var buf bytes.Buffer
	h := NewSlackHandler(&mockSlackClient{}, "#approvals")
	h.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if h.HandleInteraction(context.Background(), "act", "execute:task", "U1", "alice", "", "") {
		t.Fatal("non-approval value must not be handled")
	}
	if strings.Contains(buf.String(), "received") {
		t.Errorf("unexpected log for non-approval value: %s", buf.String())
	}
}
