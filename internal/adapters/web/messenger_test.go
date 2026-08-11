package web

import (
	"context"
	"testing"
	"time"
)

func TestWebMessenger_SendText_SeqOrdering(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	for i, text := range []string{"one", "two", "three"} {
		if err := m.SendText(ctx, "web:c1", text); err != nil {
			t.Fatalf("SendText[%d] error: %v", i, err)
		}
	}

	events, latest := m.Events("web:c1", 0)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if latest != 3 {
		t.Fatalf("latest seq = %d, want 3", latest)
	}
	for i, ev := range events {
		wantSeq := int64(i + 1)
		if ev.Seq != wantSeq {
			t.Errorf("events[%d].Seq = %d, want %d", i, ev.Seq, wantSeq)
		}
		if ev.Type != EventText {
			t.Errorf("events[%d].Type = %q, want %q", i, ev.Type, EventText)
		}
	}

	// after=1 should drop the first event only.
	tail, _ := m.Events("web:c1", 1)
	if len(tail) != 2 || tail[0].Text != "two" {
		t.Fatalf("Events(after=1) = %+v, want [two, three]", tail)
	}
}

func TestWebMessenger_SendConfirmation_UsesTaskIDAsMessageRef(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	ref, err := m.SendConfirmation(ctx, "web:c1", "", "TASK-1", "Do the thing", "myproject")
	if err != nil {
		t.Fatalf("SendConfirmation error: %v", err)
	}
	if ref != "TASK-1" {
		t.Fatalf("messageRef = %q, want %q", ref, "TASK-1")
	}

	events, _ := m.Events("web:c1", 0)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != EventConfirmation {
		t.Errorf("Type = %q, want %q", ev.Type, EventConfirmation)
	}
	if ev.MessageRef != "TASK-1" || ev.TaskID != "TASK-1" {
		t.Errorf("MessageRef/TaskID = %q/%q, want TASK-1/TASK-1", ev.MessageRef, ev.TaskID)
	}
	if ev.Text != "Do the thing (myproject)" {
		t.Errorf("Text = %q", ev.Text)
	}
}

func TestWebMessenger_SendProgress_SharesMessageRef(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	// First progress call with an empty ref (as comms.Handler.executeTaskCore
	// does for the initial "Starting" progress event) defaults to taskID.
	ref1, err := m.SendProgress(ctx, "web:c1", "", "TASK-1", "Starting", 0, "Initializing...")
	if err != nil {
		t.Fatalf("SendProgress error: %v", err)
	}
	if ref1 != "TASK-1" {
		t.Fatalf("ref1 = %q, want TASK-1", ref1)
	}

	ref2, err := m.SendProgress(ctx, "web:c1", ref1, "TASK-1", "Working", 60, "Halfway there")
	if err != nil {
		t.Fatalf("SendProgress error: %v", err)
	}
	if ref2 != ref1 {
		t.Fatalf("ref2 = %q, want %q (reused)", ref2, ref1)
	}

	events, _ := m.Events("web:c1", 0)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	for _, ev := range events {
		if ev.MessageRef != "TASK-1" {
			t.Errorf("event MessageRef = %q, want TASK-1", ev.MessageRef)
		}
		if ev.Type != EventProgress {
			t.Errorf("event Type = %q, want %q", ev.Type, EventProgress)
		}
	}
	if events[1].Progress == nil || *events[1].Progress != 0.6 {
		t.Errorf("events[1].Progress = %v, want 0.6", events[1].Progress)
	}
}

func TestWebMessenger_SendResult_CarriesPRUrlAndSuccess(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	if err := m.SendResult(ctx, "web:c1", "", "TASK-1", true, "all done", "https://github.com/x/y/pull/1"); err != nil {
		t.Fatalf("SendResult error: %v", err)
	}

	events, _ := m.Events("web:c1", 0)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Type != EventResult {
		t.Errorf("Type = %q, want %q", ev.Type, EventResult)
	}
	if ev.PRUrl != "https://github.com/x/y/pull/1" {
		t.Errorf("PRUrl = %q", ev.PRUrl)
	}
	if ev.Success == nil || !*ev.Success {
		t.Errorf("Success = %v, want true", ev.Success)
	}
	if ev.MessageRef != "TASK-1" {
		t.Errorf("MessageRef = %q, want TASK-1", ev.MessageRef)
	}
}

func TestWebMessenger_BufferCap_DropsOldest(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	total := maxEventsPerConversation + 10
	for i := 0; i < total; i++ {
		if err := m.SendText(ctx, "web:c1", "msg"); err != nil {
			t.Fatalf("SendText error: %v", err)
		}
	}

	events, latest := m.Events("web:c1", 0)
	if len(events) != maxEventsPerConversation {
		t.Fatalf("got %d events, want cap %d", len(events), maxEventsPerConversation)
	}
	if latest != int64(total) {
		t.Fatalf("latest = %d, want %d (seq keeps counting past the cap)", latest, total)
	}
	// The oldest surviving event should be the 11th sent (1-indexed seq 11),
	// since the first 10 were dropped to stay at the cap.
	wantFirstSeq := int64(total - maxEventsPerConversation + 1)
	if events[0].Seq != wantFirstSeq {
		t.Errorf("events[0].Seq = %d, want %d", events[0].Seq, wantFirstSeq)
	}
}

func TestWebMessenger_ConversationExpiry(t *testing.T) {
	m := NewMessenger()
	fakeNow := time.Now()
	m.now = func() time.Time { return fakeNow }
	ctx := context.Background()

	if err := m.SendText(ctx, "web:c1", "hello"); err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	// Still within the window: events survive.
	fakeNow = fakeNow.Add(conversationExpiry - time.Minute)
	events, _ := m.Events("web:c1", 0)
	if len(events) != 1 {
		t.Fatalf("got %d events before expiry, want 1", len(events))
	}

	// Past the window: the conversation is pruned and looks brand new.
	fakeNow = fakeNow.Add(2 * time.Minute)
	events, latest := m.Events("web:c1", 0)
	if len(events) != 0 {
		t.Fatalf("got %d events after expiry, want 0", len(events))
	}
	if latest != 0 {
		t.Fatalf("latest after expiry = %d, want 0 (seq resets)", latest)
	}
}

func TestWebMessenger_MaxMessageLength(t *testing.T) {
	m := NewMessenger()
	if got := m.MaxMessageLength(); got != MaxMessageLength {
		t.Fatalf("MaxMessageLength() = %d, want %d", got, MaxMessageLength)
	}
}

func TestWebMessenger_AcknowledgeCallback_NoOp(t *testing.T) {
	m := NewMessenger()
	if err := m.AcknowledgeCallback(context.Background(), "cb1"); err != nil {
		t.Fatalf("AcknowledgeCallback error: %v", err)
	}
}

func TestWebMessenger_LatestSeq_UnknownConversation(t *testing.T) {
	m := NewMessenger()
	if got := m.LatestSeq("web:nope"); got != 0 {
		t.Fatalf("LatestSeq(unknown) = %d, want 0", got)
	}
}

func TestWebMessenger_SendChunked_PrependsPrefix(t *testing.T) {
	m := NewMessenger()
	ctx := context.Background()

	if err := m.SendChunked(ctx, "web:c1", "", "body text", "PREFIX: "); err != nil {
		t.Fatalf("SendChunked error: %v", err)
	}
	events, _ := m.Events("web:c1", 0)
	if len(events) != 1 || events[0].Text != "PREFIX: body text" {
		t.Fatalf("events = %+v", events)
	}
}
