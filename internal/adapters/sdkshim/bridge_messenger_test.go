package sdkshim

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// recordingBridge captures every OutboundMessage handed to Send so tests can
// assert the exact wire shape the SDK bridge receives.
type recordingBridge struct {
	sent []core.OutboundMessage
}

func (b *recordingBridge) Start(ctx context.Context) error { return nil }
func (b *recordingBridge) Send(_ context.Context, m core.OutboundMessage) (core.MessageRef, error) {
	b.sent = append(b.sent, m)
	return core.MessageRef{ChannelID: m.ChannelID, MessageID: "msg-1", ThreadID: m.ThreadID}, nil
}
func (b *recordingBridge) Edit(ctx context.Context, ref core.MessageRef, text string) error {
	return nil
}
func (b *recordingBridge) Ack(ctx context.Context, callbackID string) error { return nil }

// TestBridgeMessengerThreadIDPropagation guards the accept-and-discard gap
// PR#5121 closes: every comms.Messenger method that takes a threadID must set
// it on the core.OutboundMessage it hands to the bridge. SendText was the one
// implementation that silently dropped it (compiles clean, so only a wire-
// level assertion catches a regression).
func TestBridgeMessengerThreadIDPropagation(t *testing.T) {
	const threadID = "42"

	tests := []struct {
		name string
		send func(m *bridgeMessenger) error
	}{
		{
			name: "SendText",
			send: func(m *bridgeMessenger) error {
				return m.SendText(context.Background(), "C1", threadID, "hello")
			},
		},
		{
			name: "SendConfirmation",
			send: func(m *bridgeMessenger) error {
				_, err := m.SendConfirmation(context.Background(), "C1", threadID, "TG-1", "desc", "proj")
				return err
			},
		},
		{
			name: "SendResult",
			send: func(m *bridgeMessenger) error {
				return m.SendResult(context.Background(), "C1", threadID, "TG-1", true, "out", "")
			},
		},
		{
			name: "SendChunked",
			send: func(m *bridgeMessenger) error {
				return m.SendChunked(context.Background(), "C1", threadID, "content", "prefix")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge := &recordingBridge{}
			m := &bridgeMessenger{bridge: bridge}

			if err := tt.send(m); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			if len(bridge.sent) == 0 {
				t.Fatalf("%s: no outbound message reached the bridge", tt.name)
			}
			for i, sent := range bridge.sent {
				if sent.ThreadID != threadID {
					t.Errorf("%s: outbound message %d ThreadID = %q, want %q", tt.name, i, sent.ThreadID, threadID)
				}
			}
		})
	}
}

// TestBridgeMessengerSendTextEmptyThreadID asserts the zero-value fallback:
// an empty threadID must reach the bridge as an empty ThreadID (adapters
// treat empty as "no thread" — never a fabricated value).
func TestBridgeMessengerSendTextEmptyThreadID(t *testing.T) {
	bridge := &recordingBridge{}
	m := &bridgeMessenger{bridge: bridge}

	if err := m.SendText(context.Background(), "C1", "", "hello"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if len(bridge.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(bridge.sent))
	}
	if got := bridge.sent[0].ThreadID; got != "" {
		t.Errorf("ThreadID = %q, want empty", got)
	}
}
