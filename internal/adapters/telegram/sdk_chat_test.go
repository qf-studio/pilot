package telegram

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestMessageEventToIncoming_Message(t *testing.T) {
	ev := core.MessageEvent{
		Action:    "message",
		ChannelID: "12345",
		ThreadID:  "67",
		Text:      "deploy the api",
		Sender:    core.Identity{UserID: "999", DisplayName: "alice"},
	}

	msg := messageEventToIncoming(ev)

	if msg.Platform != "telegram" {
		t.Errorf("Platform = %q, want telegram", msg.Platform)
	}
	if msg.ContextID != "12345" {
		t.Errorf("ContextID = %q, want 12345", msg.ContextID)
	}
	if msg.SenderID != "999" {
		t.Errorf("SenderID = %q, want 999 (pass-through)", msg.SenderID)
	}
	if msg.SenderName != "alice" {
		t.Errorf("SenderName = %q, want alice", msg.SenderName)
	}
	if msg.Text != "deploy the api" {
		t.Errorf("Text = %q, want %q", msg.Text, "deploy the api")
	}
	if msg.ThreadID != "67" {
		t.Errorf("ThreadID = %q, want 67", msg.ThreadID)
	}
	if msg.IsCallback {
		t.Error("IsCallback = true, want false for a plain message")
	}
	if msg.RawEvent == nil {
		t.Error("RawEvent = nil, want the SDK event")
	}
}

func TestMessageEventToIncoming_Callback(t *testing.T) {
	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  "12345",
		CallbackID: "cb-abc",
		Data:       "approve:TASK123",
		Sender:     core.Identity{UserID: "999", DisplayName: "alice"},
	}

	msg := messageEventToIncoming(ev)

	if !msg.IsCallback {
		t.Error("IsCallback = false, want true for a callback event")
	}
	if msg.CallbackID != "cb-abc" {
		t.Errorf("CallbackID = %q, want cb-abc", msg.CallbackID)
	}
	if msg.ActionID != "approve:TASK123" {
		t.Errorf("ActionID = %q, want approve:TASK123 (from ev.Data)", msg.ActionID)
	}
}

// HandleMessage must satisfy core.MessageHandler and be safe when the comms
// handler has not been wired yet (e.g. bridge created before SetCommsHandler).
func TestHandleMessage_NilCommsHandlerIsSafe(t *testing.T) {
	var _ core.MessageHandler = (*Handler)(nil)

	h := &Handler{} // commsHandler nil
	if err := h.HandleMessage(context.Background(), core.MessageEvent{Action: "message", Text: "hi"}); err != nil {
		t.Errorf("HandleMessage with nil commsHandler returned err = %v, want nil", err)
	}
}
