package sdkshim

import (
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestMessageRef_RoundTrip(t *testing.T) {
	in := core.MessageRef{ChannelID: "C1", MessageID: "M2", ThreadID: "T3"}
	s := MessageRefToString(in)
	out := MessageRefFromString(s)
	if out != in {
		t.Errorf("round-trip mismatch: in=%+v out=%+v (s=%q)", in, out, s)
	}
}

func TestMessageRefFromString_Empty(t *testing.T) {
	if got := MessageRefFromString(""); got != (core.MessageRef{}) {
		t.Errorf("expected zero MessageRef, got %+v", got)
	}
}

func TestMessageRefFromString_LegacyBareChannel(t *testing.T) {
	// A legacy ref that's just a bare channel ID (no separators) should
	// degrade gracefully so older messenger.Messenger callers don't panic.
	got := MessageRefFromString("just-a-channel")
	if got.ChannelID != "just-a-channel" {
		t.Errorf("ChannelID = %q, want just-a-channel", got.ChannelID)
	}
	if got.MessageID != "" || got.ThreadID != "" {
		t.Errorf("expected empty MessageID/ThreadID, got %+v", got)
	}
}

func TestComposeOutboundMessage_NoButtons(t *testing.T) {
	out := ComposeOutboundMessage("C1", "T1", "hi", nil, nil, nil)
	if out.ChannelID != "C1" || out.ThreadID != "T1" || out.Text != "hi" {
		t.Errorf("unexpected outbound: %+v", out)
	}
	if out.Buttons != nil {
		t.Errorf("Buttons should be nil, got %+v", out.Buttons)
	}
}

func TestComposeOutboundMessage_Buttons(t *testing.T) {
	out := ComposeOutboundMessage(
		"C1", "", "hi",
		[]string{"Approve", "Reject"},
		[]string{"approve", "reject"},
		[]string{"approve:T1", "reject:T1"},
	)
	if len(out.Buttons) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(out.Buttons))
	}
	if out.Buttons[0].Label != "Approve" || out.Buttons[0].ActionID != "approve" || out.Buttons[0].Data != "approve:T1" {
		t.Errorf("button[0] mismatch: %+v", out.Buttons[0])
	}
}

func TestComposeOutboundMessage_MismatchedSlices(t *testing.T) {
	// Labels longer than actionIDs → take the shorter prefix.
	out := ComposeOutboundMessage(
		"C1", "", "hi",
		[]string{"A", "B", "C"},
		[]string{"a"},
		[]string{"a:1", "b:2"},
	)
	if len(out.Buttons) != 1 {
		t.Errorf("expected 1 button (shortest slice wins), got %d", len(out.Buttons))
	}
}
