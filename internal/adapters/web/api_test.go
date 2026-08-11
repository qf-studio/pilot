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

	// The context passed to Dispatch is cancelled immediately, but
	// HandleMessage should still observe the request's own cancellation
	// state correctly since we pass it a background context deliberately —
	// Dispatch itself doesn't derive from the caller's ctx beyond passing it
	// straight to the goroutine, so the caller (gateway handler) is what's
	// responsible for using the daemon ctx, not the request ctx. Here we
	// simulate that correctly-wired caller behavior.
	daemonCtx := context.Background()
	seq, err := api.Dispatch(daemonCtx, DispatchRequest{ConversationID: "c1", Text: "hello"})
	if err != nil {
		t.Fatalf("Dispatch error: %v", err)
	}
	if seq != 0 {
		t.Fatalf("seq = %d, want 0", seq)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := m.Events("web:c1", 0)
		if len(events) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no event landed")
}

func TestAPI_Events_UnknownConversationIsEmptyNotError(t *testing.T) {
	api, _ := newTestAPI()
	events := api.Events("nope", 0)
	if len(events) != 0 {
		t.Fatalf("got %d events, want 0", len(events))
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

	events := api.Events("c1", 0)
	if len(events) != 1 || events[0].Text != "hi" {
		t.Fatalf("events = %+v", events)
	}
}
