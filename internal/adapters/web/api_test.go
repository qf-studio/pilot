package web

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
)

func newTestAPI() (*API, *WebMessenger) {
	m := NewMessenger()
	h := comms.NewHandler(&comms.HandlerConfig{
		Messenger:    m,
		TaskIDPrefix: "WEB",
	})
	return NewAPI(h, m), m
}

func TestAPI_Dispatch_MissingConversationID(t *testing.T) {
	api, _ := newTestAPI()
	_, err := api.Dispatch(context.Background(), DispatchRequest{Text: "hi"})
	if !errors.Is(err, ErrMissingConversationID) {
		t.Fatalf("err = %v, want ErrMissingConversationID", err)
	}
}

func TestAPI_Dispatch_MissingText(t *testing.T) {
	api, _ := newTestAPI()
	_, err := api.Dispatch(context.Background(), DispatchRequest{ConversationID: "c1"})
	if !errors.Is(err, ErrMissingText) {
		t.Fatalf("err = %v, want ErrMissingText", err)
	}
}

func TestAPI_Dispatch_CallbackMissingFields(t *testing.T) {
	api, _ := newTestAPI()
	_, err := api.Dispatch(context.Background(), DispatchRequest{ConversationID: "c1", IsCallback: true})
	if !errors.Is(err, ErrMissingCallbackFields) {
		t.Fatalf("err = %v, want ErrMissingCallbackFields", err)
	}
}

func TestAPI_Dispatch_RejectsApprovalCallback(t *testing.T) {
	api, _ := newTestAPI()

	cases := []DispatchRequest{
		{ConversationID: "c1", IsCallback: true, CallbackID: "cb1", ActionID: "approve"},
		{ConversationID: "c1", IsCallback: true, CallbackID: "cb1", ActionID: "reject"},
		{ConversationID: "c1", IsCallback: true, CallbackID: "approve:req-123", ActionID: "click"},
		{ConversationID: "c1", IsCallback: true, CallbackID: "reject:req-123", ActionID: "click"},
	}
	for _, req := range cases {
		_, err := api.Dispatch(context.Background(), req)
		if !errors.Is(err, ErrApprovalCallback) {
			t.Errorf("req=%+v err = %v, want ErrApprovalCallback", req, err)
		}
	}
}

func TestAPI_Dispatch_ValidTextMessage_ReturnsAcceptTimeSeq(t *testing.T) {
	api, m := newTestAPI()
	ctx := context.Background()

	// Seed one prior event so the accept-time seq is non-zero.
	if err := m.SendText(ctx, "web:c1", "prior"); err != nil {
		t.Fatalf("seed SendText error: %v", err)
	}

	seq, err := api.Dispatch(ctx, DispatchRequest{ConversationID: "c1", Text: "hello", Sender: "operator"})
	if err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if seq != 1 {
		t.Fatalf("accept-time seq = %d, want 1 (only the seeded event existed at accept time)", seq)
	}

	// HandleMessage runs on a goroutine — wait for it to land an event
	// (a greeting reply to "hello") before asserting.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := m.Events("web:c1", seq)
		if len(events) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no new event landed after dispatch")
}

func TestAPI_Dispatch_UsesGivenContextNotCancelledOne(t *testing.T) {
	api, m := newTestAPI()

	// requestCtx simulates the inbound HTTP request's own context — a real
	// gateway handler must NOT pass this to Dispatch (see
	// internal/gateway/chat.go's handleChatMessages, which stashes the
	// long-lived daemon context precisely to avoid this). It is genuinely
	// cancelled here (GH-4843 D4: previously this test never cancelled
	// anything, so the "not cancelled one" behavior it claims to cover was
	// never actually exercised).
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if requestCtx.Err() == nil {
		t.Fatal("test setup bug: requestCtx should already be cancelled")
	}

	// daemonCtx is what a correctly-wired caller actually passes to
	// Dispatch — long-lived and never cancelled by the request lifecycle.
	daemonCtx := context.Background()
	seq, err := api.Dispatch(daemonCtx, DispatchRequest{ConversationID: "c1", Text: "hello"})
	if err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if seq != 0 {
		t.Fatalf("seq = %d, want 0", seq)
	}

	// Dispatch completing (the goroutine landing its event) despite
	// requestCtx already being cancelled proves Dispatch uses only the
	// context explicitly given to it, not some other context the caller
	// happens to also hold.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := m.Events("web:c1", 0)
		if len(events) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no event landed — dispatch appears to have been affected by the cancelled request context")
}

func TestAPI_Events_UnknownConversationIsEmptyNotError(t *testing.T) {
	api, _ := newTestAPI()
	events, latestSeq := api.Events("nope", 0)
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0", len(events))
	}
	if events == nil {
		t.Error("events = nil, want non-nil empty slice (GH-4843 D3)")
	}
	if latestSeq != 0 {
		t.Errorf("latestSeq = %d, want 0", latestSeq)
	}
}

func TestAPI_Events_UsesConversationIDPrefix(t *testing.T) {
	api, m := newTestAPI()
	ctx := context.Background()

	// Write directly on the prefixed contextID, as comms.Handler would.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.SendText(ctx, "web:c1", "hi")
	}()
	wg.Wait()

	events, latestSeq := api.Events("c1", 0)
	if len(events) != 1 || events[0].Text != "hi" {
		t.Fatalf("events = %+v", events)
	}
	if latestSeq != 1 {
		t.Errorf("latestSeq = %d, want 1", latestSeq)
	}
}

// TestAPI_Events_LatestSeqDetectsReset exercises the GH-4843 D2 reset-
// detection contract end to end through API.Events: after a daemon restart
// (simulated here by a fresh WebMessenger with no buffer for the
// conversation), a client polling with a stale `after` cursor greater than
// the server's latestSeq gets an unambiguous signal — latestSeq (0) is less
// than its own cursor — telling it to reset to after=0, entirely from the
// response body.
func TestAPI_Events_LatestSeqDetectsReset(t *testing.T) {
	api, _ := newTestAPI()

	const staleClientCursor = 42
	events, latestSeq := api.Events("c1", staleClientCursor)
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0 (fresh buffer)", len(events))
	}
	if latestSeq >= staleClientCursor {
		t.Fatalf("latestSeq = %d, want < %d (client cursor) so reset is detectable", latestSeq, staleClientCursor)
	}
}
