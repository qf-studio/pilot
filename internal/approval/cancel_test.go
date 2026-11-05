package approval

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestManager_Cancel_IgnoresLaterDecision covers GH-6131: a request cancelled
// because its PR closed must not accept a late decision.
func TestManager_Cancel_IgnoresLaterDecision(t *testing.T) {
	m := NewManager(DefaultConfig())

	m.Cancel(context.Background(), "") // empty id is a no-op
	if m.IsCancelled("") {
		t.Fatal("empty id must not be recorded as cancelled")
	}

	m.Cancel(context.Background(), "pr-7-1") // unknown id still records the cancel
	if !m.IsCancelled("pr-7-1") {
		t.Fatal("Cancel did not mark the request cancelled")
	}
	err := m.RecordDecision(context.Background(), "pr-7-1", DecisionApproved, "alice")
	if !errors.Is(err, ErrRequestCancelled) {
		t.Fatalf("RecordDecision err = %v, want ErrRequestCancelled", err)
	}
}

// TestSlackHandler_CancelledRequest_ClickAnsweredPRClosed covers GH-6131: a
// click on a cancelled card gets the "this PR is closed" answer and records no
// decision.
func TestSlackHandler_CancelledRequest_ClickAnsweredPRClosed(t *testing.T) {
	client := &mockSlackClient{}
	recorder := &mockDecisionRecorder{}
	handler := NewSlackHandler(client, "#approvals").WithDecisionRecorder(recorder)

	req := &Request{
		ID:        "pr-9-1",
		TaskID:    "GH-9",
		Stage:     StagePreMerge,
		Title:     "PR #9",
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	if err := handler.CancelRequest(context.Background(), req.ID); err != nil {
		t.Fatalf("CancelRequest: %v", err)
	}

	if !handler.HandleInteraction(context.Background(), "approve", "approve:pr-9-1", "U1", "alice", "", "") {
		t.Fatal("click on a cancelled request must still be handled")
	}
	if len(client.ephemeralToUserCalls) != 1 || client.ephemeralToUserCalls[0].Text != cancelledRequestText {
		t.Fatalf("ephemeral answers = %+v, want one %q", client.ephemeralToUserCalls, cancelledRequestText)
	}
	if len(recorder.calls) != 0 {
		t.Errorf("cancelled click recorded %d decision(s), want 0", len(recorder.calls))
	}
}
