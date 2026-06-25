package sdkshim

import (
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestMessageEventToIncomingMessage_Nil(t *testing.T) {
	if got := MessageEventToIncomingMessage(nil, PlatformSlack, nil); got != nil {
		t.Fatalf("expected nil for nil event, got %+v", got)
	}
}

func TestMessageEventToIncomingMessage_PlainMessage(t *testing.T) {
	ev := &core.MessageEvent{
		Action:    "message",
		ChannelID: "C123",
		ThreadID:  "T456",
		Text:      "hello world",
		Sender:    core.Identity{UserID: "U789", DisplayName: "alice"},
	}
	msg := MessageEventToIncomingMessage(ev, PlatformSlack, nil)
	if msg == nil {
		t.Fatal("expected non-nil message")
	}
	if msg.ContextID != "C123" {
		t.Errorf("ContextID = %q, want C123", msg.ContextID)
	}
	if msg.SenderID != "U789" {
		t.Errorf("SenderID = %q, want U789", msg.SenderID)
	}
	if msg.SenderName != "alice" {
		t.Errorf("SenderName = %q, want alice", msg.SenderName)
	}
	if msg.ThreadID != "T456" {
		t.Errorf("ThreadID = %q, want T456", msg.ThreadID)
	}
	if msg.Platform != PlatformSlack {
		t.Errorf("Platform = %q, want %q", msg.Platform, PlatformSlack)
	}
	if msg.Text != "hello world" {
		t.Errorf("Text = %q, want %q", msg.Text, "hello world")
	}
	if msg.IsCallback {
		t.Error("IsCallback should be false on plain message")
	}
}

func TestMessageEventToIncomingMessage_Callback(t *testing.T) {
	ev := &core.MessageEvent{
		Action:     "callback",
		ChannelID:  "C123",
		CallbackID: "cb-1",
		Data:       "approve:TASK123",
		Sender:     core.Identity{UserID: "U1"},
	}
	msg := MessageEventToIncomingMessage(ev, PlatformDiscord, nil)
	if !msg.IsCallback {
		t.Error("expected IsCallback=true")
	}
	if msg.CallbackID != "cb-1" {
		t.Errorf("CallbackID = %q, want cb-1", msg.CallbackID)
	}
	if msg.ActionID != "approve:TASK123" {
		t.Errorf("ActionID = %q, want approve:TASK123", msg.ActionID)
	}
}

func TestMessageEventToIncomingMessage_Command(t *testing.T) {
	// The SDK bridge delivers "/"-prefixed input as a command event with
	// Command/Args set and Text empty. The shim must reconstruct the command
	// line into Text so comms.detectIntent routes it via IntentCommand →
	// CommandHandler rather than creating a task from empty text.
	tests := []struct {
		name    string
		command string
		args    []string
		want    string
	}{
		{name: "help no args", command: "/help", args: nil, want: "/help"},
		{name: "run with arg", command: "/run", args: []string{"42"}, want: "/run 42"},
		{name: "multi arg", command: "/project", args: []string{"set", "pilot"}, want: "/project set pilot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &core.MessageEvent{
				Action:    "command",
				ChannelID: "C123",
				Command:   tt.command,
				Args:      tt.args,
				Sender:    core.Identity{UserID: "U1"},
			}
			msg := MessageEventToIncomingMessage(ev, PlatformSlack, nil)
			if msg == nil {
				t.Fatal("expected non-nil message")
			}
			if msg.IsCallback {
				t.Error("IsCallback should be false for command action")
			}
			if msg.Text != tt.want {
				t.Errorf("Text = %q, want %q", msg.Text, tt.want)
			}
		})
	}
}

func TestMessageEventToIncomingMessage_CustomConverter(t *testing.T) {
	ev := &core.MessageEvent{
		Sender: core.Identity{UserID: "raw-id"},
	}
	upper := func(s string) string {
		out := make([]byte, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'a' && c <= 'z' {
				c = c - 'a' + 'A'
			}
			out[i] = c
		}
		return string(out)
	}
	msg := MessageEventToIncomingMessage(ev, PlatformTelegram, upper)
	if msg.SenderID != "RAW-ID" {
		t.Errorf("SenderID = %q, want RAW-ID", msg.SenderID)
	}
}
