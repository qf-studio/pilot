package approval

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// mockSlackClient implements SlackClient for testing
type mockSlackClient struct {
	lastMessage *SlackInteractiveMessage
	response    *SlackPostMessageResponse
	updateError error

	// updateCalls records every UpdateInteractiveMessage invocation so tests
	// can assert on the text/blocks a race-loss vs. a real decision produces
	// (GH-4777).
	updateCalls []mockSlackUpdateCall
}

type mockSlackUpdateCall struct {
	Channel string
	TS      string
	Blocks  []interface{}
	Text    string
}

func (m *mockSlackClient) PostInteractiveMessage(ctx context.Context, msg *SlackInteractiveMessage) (*SlackPostMessageResponse, error) {
	m.lastMessage = msg
	if m.response == nil {
		return &SlackPostMessageResponse{
			OK:      true,
			TS:      "1234567890.123456",
			Channel: msg.Channel,
		}, nil
	}
	return m.response, nil
}

func (m *mockSlackClient) UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error {
	m.updateCalls = append(m.updateCalls, mockSlackUpdateCall{Channel: channel, TS: ts, Blocks: blocks, Text: text})
	return m.updateError
}

func TestSlackHandler_Name(t *testing.T) {
	handler := NewSlackHandler(nil, "#test")
	if handler.Name() != "slack" {
		t.Errorf("expected name 'slack', got %q", handler.Name())
	}
}

func TestSlackHandler_SendApprovalRequest(t *testing.T) {
	tests := []struct {
		name           string
		stage          Stage
		wantApproveBtn string
		wantRejectBtn  string
	}{
		{"pre_execution_stage", StagePreExecution, "✅ Execute", "❌ Cancel"},
		{"pre_merge_stage", StagePreMerge, "✅ Merge", "❌ Reject"},
		{"post_failure_stage", StagePostFailure, "🔄 Retry", "⏹ Abort"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockSlackClient{}
			handler := NewSlackHandler(client, "#approvals")

			req := &Request{
				ID:          "test-123",
				TaskID:      "TASK-01",
				Stage:       tt.stage,
				Title:       "Test PR",
				Description: "Test description",
				ExpiresAt:   time.Now().Add(time.Hour),
			}

			responseCh, err := handler.SendApprovalRequest(context.Background(), req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if responseCh == nil {
				t.Fatal("expected response channel")
			}

			if client.lastMessage == nil {
				t.Fatal("expected message to be sent")
			}
			if client.lastMessage.Channel != "#approvals" {
				t.Errorf("expected channel '#approvals', got %q", client.lastMessage.Channel)
			}
			if len(client.lastMessage.Blocks) < 2 {
				t.Fatalf("expected at least 2 blocks, got %d", len(client.lastMessage.Blocks))
			}

			// Verify actions block has correct buttons
			actionsBlock, ok := client.lastMessage.Blocks[1].(SlackActionsBlock)
			if !ok {
				t.Fatalf("expected SlackActionsBlock, got %T", client.lastMessage.Blocks[1])
			}
			if len(actionsBlock.Elements) != 2 {
				t.Fatalf("expected 2 buttons, got %d", len(actionsBlock.Elements))
			}
			if actionsBlock.Elements[0].Text.Text != tt.wantApproveBtn {
				t.Errorf("approve button: expected %q, got %q", tt.wantApproveBtn, actionsBlock.Elements[0].Text.Text)
			}
			if actionsBlock.Elements[1].Text.Text != tt.wantRejectBtn {
				t.Errorf("reject button: expected %q, got %q", tt.wantRejectBtn, actionsBlock.Elements[1].Text.Text)
			}
		})
	}
}

// TestSlackHandler_SendApprovalRequest_RehydrateDestinationParity is the
// GH-4772 regression test for bug (b): SendApprovalRequest must post to the
// same destination that Rehydrate would resolve to for the same request
// after a restart. Before the fix, SendApprovalRequest always used the
// handler's configured default channel while Rehydrate honored
// req.Approvers[0], so a per-request approver override took effect only on
// rehydrate, silently retargeting the message after a daemon restart.
func TestSlackHandler_SendApprovalRequest_RehydrateDestinationParity(t *testing.T) {
	sendClient := &mockSlackClient{}
	store := newMockPendingStore()
	sendHandler := NewSlackHandler(sendClient, "#default-approvals").WithStore(store)

	req := &Request{
		ID:               "parity-1",
		TaskID:           "TASK-01",
		Stage:            StagePreMerge,
		Title:            "Test PR",
		Approvers:        []string{"#override-channel"},
		PreferredChannel: "slack",
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
	}

	if _, err := sendHandler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("SendApprovalRequest: unexpected error: %v", err)
	}
	if sendClient.lastMessage == nil {
		t.Fatal("expected message to be sent")
	}
	sentChannel := sendClient.lastMessage.Channel

	// Simulate a restart: a fresh handler (same configured default channel)
	// rehydrating the row that was just persisted by SendApprovalRequest.
	rehydrateClient := &mockSlackClient{}
	rehydrateHandler := NewSlackHandler(rehydrateClient, "#default-approvals").WithStore(store)
	if err := rehydrateHandler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: unexpected error: %v", err)
	}

	rehydrateHandler.mu.RLock()
	pending, ok := rehydrateHandler.pending["parity-1"]
	rehydrateHandler.mu.RUnlock()
	if !ok {
		t.Fatal("expected request to be rehydrated")
	}

	if sentChannel != pending.Channel {
		t.Errorf("destination parity broken: SendApprovalRequest used %q, Rehydrate resolved %q", sentChannel, pending.Channel)
	}
	if pending.Channel != "#override-channel" {
		t.Errorf("expected both to honor the approver override, got %q", pending.Channel)
	}
}

func TestSlackHandler_HandleInteraction_Approve(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	req := &Request{
		ID:        "test-456",
		TaskID:    "TASK-02",
		Stage:     StagePreMerge,
		Title:     "Merge PR",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate button press
	handled := handler.HandleInteraction(
		context.Background(),
		"approve",
		"approve:test-456",
		"U123",
		"testuser",
		"https://hooks.slack.com/response",
	)

	if !handled {
		t.Error("expected interaction to be handled")
	}

	// Check response was sent
	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("expected approved, got %v", resp.Decision)
		}
		if resp.ApprovedBy != "testuser" {
			t.Errorf("expected approver 'testuser', got %q", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for response")
	}
}

func TestSlackHandler_HandleInteraction_Reject(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	req := &Request{
		ID:        "test-789",
		TaskID:    "TASK-03",
		Stage:     StagePreMerge,
		Title:     "Reject PR",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate reject button press
	handled := handler.HandleInteraction(
		context.Background(),
		"reject",
		"reject:test-789",
		"U456",
		"rejectuser",
		"https://hooks.slack.com/response",
	)

	if !handled {
		t.Error("expected interaction to be handled")
	}

	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionRejected {
			t.Errorf("expected rejected, got %v", resp.Decision)
		}
		if resp.ApprovedBy != "rejectuser" {
			t.Errorf("expected approver 'rejectuser', got %q", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for response")
	}
}

func TestSlackHandler_HandleInteraction_NotFound(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	// Try to handle interaction for non-existent request
	handled := handler.HandleInteraction(
		context.Background(),
		"approve",
		"approve:nonexistent",
		"U123",
		"testuser",
		"",
	)

	// Should still return true (handled, just expired)
	if !handled {
		t.Error("expected interaction to be handled even for missing request")
	}
}

func TestSlackHandler_HandleInteraction_InvalidValue(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	// Try to handle interaction with invalid value format
	handled := handler.HandleInteraction(
		context.Background(),
		"some_action",
		"invalid_format",
		"U123",
		"testuser",
		"",
	)

	if handled {
		t.Error("expected invalid value to not be handled")
	}
}

func TestSlackHandler_CancelRequest(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	req := &Request{
		ID:        "test-cancel",
		TaskID:    "TASK-04",
		Stage:     StagePreMerge,
		Title:     "Cancel Test",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cancel the request
	err = handler.CancelRequest(context.Background(), "test-cancel")
	if err != nil {
		t.Fatalf("unexpected error cancelling: %v", err)
	}

	// Response channel should be closed
	select {
	case _, ok := <-responseCh:
		if ok {
			t.Error("expected channel to be closed")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for channel close")
	}
}

func TestSlackHandler_CancelRequest_NotFound(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")

	// Cancel non-existent request should not error
	err := handler.CancelRequest(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- GH-4411: restart-survival tests (persistence, Rehydrate, DecisionRecorder) ---
// These mirror the Telegram handler's GH-3825 test suite in telegram_test.go,
// reusing its mockPendingStore/mockDecisionRecorder test doubles (same package).

func TestSlackHandler_WithStore_PersistOnSend(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	handler := NewSlackHandler(client, "#approvals").WithStore(store)

	req := &Request{
		ID: "persist-1", TaskID: "T-1", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.len() != 1 {
		t.Fatalf("expected 1 persisted row, got %d", store.len())
	}
	row := store.get("persist-1")
	if row == nil {
		t.Fatal("expected row to be stored")
	}
	if row.TaskID != "T-1" {
		t.Errorf("expected TaskID T-1, got %s", row.TaskID)
	}
}

// TestSlackHandler_WithStore_PersistsProject is the GH-4773 round-trip
// regression test: SendApprovalRequest must copy req.Project onto the
// persisted memory.PendingApproval row.
func TestSlackHandler_WithStore_PersistsProject(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	handler := NewSlackHandler(client, "#approvals").WithStore(store)

	req := &Request{
		ID: "persist-project-1", TaskID: "T-1", Stage: StagePreMerge,
		Title: "Test", Project: "/home/user/projects/pilot", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	row := store.get("persist-project-1")
	if row == nil {
		t.Fatal("expected row to be stored")
	}
	if row.Project != "/home/user/projects/pilot" {
		t.Errorf("row.Project = %q, want /home/user/projects/pilot", row.Project)
	}
}

func TestSlackHandler_WithStore_DeleteOnInteraction(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	handler := NewSlackHandler(client, "#approvals").WithStore(store)

	req := &Request{
		ID: "del-int-1", TaskID: "T-2", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.len() != 1 {
		t.Fatal("expected 1 row after send")
	}

	handler.HandleInteraction(context.Background(), "approve", "approve:del-int-1", "U1", "user", "")

	if store.len() != 0 {
		t.Errorf("expected 0 rows after interaction, got %d", store.len())
	}
}

func TestSlackHandler_WithStore_DeleteOnCancel(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	handler := NewSlackHandler(client, "#approvals").WithStore(store)

	req := &Request{
		ID: "del-cancel-1", TaskID: "T-3", Stage: StagePreMerge,
		Title: "Test", ExpiresAt: time.Now().Add(time.Hour),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := handler.CancelRequest(context.Background(), "del-cancel-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.len() != 0 {
		t.Errorf("expected 0 rows after cancel, got %d", store.len())
	}
}

func TestSlackHandler_Rehydrate_RestoresNonExpired(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "live", TaskID: "T-live", Stage: "pre_merge",
		Title: "Live", PreferredChannel: "slack", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "dead", TaskID: "T-dead", Stage: "pre_merge",
		Title: "Dead", PreferredChannel: "slack", CreatedAt: time.Now(), ExpiresAt: past,
	})

	handler := NewSlackHandler(client, "#approvals").WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.RLock()
	_, livePending := handler.pending["live"]
	_, deadPending := handler.pending["dead"]
	handler.mu.RUnlock()

	if !livePending {
		t.Error("expected non-expired approval to be rehydrated")
	}
	if deadPending {
		t.Error("expected expired approval to NOT be rehydrated")
	}
	if store.get("dead") != nil {
		t.Error("expected expired row to be deleted from store")
	}
}

// TestSlackHandler_Rehydrate_SkipsOtherChannelRows is the GH-4772 regression
// test: Rehydrate must not process (or delete) a row dispatched to another
// channel — even an expired one, which the owning handler's own sweep still
// needs to see so it can edit its message / record the timeout decision.
func TestSlackHandler_Rehydrate_SkipsOtherChannelRows(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "tg-live", TaskID: "T-tg-live", Stage: "pre_merge",
		Title: "Telegram live", PreferredChannel: "telegram", CreatedAt: time.Now(), ExpiresAt: future,
	})
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "tg-dead", TaskID: "T-tg-dead", Stage: "pre_merge",
		Title: "Telegram dead", PreferredChannel: "telegram", CreatedAt: time.Now(), ExpiresAt: past,
	})

	handler := NewSlackHandler(client, "#approvals").WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.mu.RLock()
	_, gotLive := handler.pending["tg-live"]
	_, gotDead := handler.pending["tg-dead"]
	handler.mu.RUnlock()

	if gotLive {
		t.Error("expected slack Rehydrate to skip a telegram-owned row")
	}
	if gotDead {
		t.Error("expected slack Rehydrate to skip a telegram-owned row")
	}
	if store.get("tg-live") == nil {
		t.Error("expected telegram-owned live row to survive slack's Rehydrate untouched")
	}
	if store.get("tg-dead") == nil {
		t.Error("expected slack Rehydrate to NOT delete an expired telegram-owned row — that's telegram's own sweep's job")
	}
}

func TestSlackHandler_Rehydrate_NoStore(t *testing.T) {
	client := &mockSlackClient{}
	handler := NewSlackHandler(client, "#approvals")
	// No store attached — Rehydrate should be a no-op.
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("expected no error without store, got: %v", err)
	}
}

// TestSlackHandler_Rehydrate_HonorsOldButtonRequestID is the GH-4411
// regression test: a click on the ORIGINAL Slack message (posted before a
// daemon restart) must resolve, because Rehydrate re-arms the same
// requestID rather than requiring a freshly-posted message. This is the
// "Rehydrate" option chosen over "Re-post" — old buttons must keep working.
func TestSlackHandler_Rehydrate_HonorsOldButtonRequestID(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-int", TaskID: "T-R", Stage: "pre_merge",
		Title: "Rehydrated", PreferredChannel: "slack", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewSlackHandler(client, "#approvals").WithStore(store)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	handler.mu.RLock()
	_, gotPending := handler.pending["rehy-int"]
	handler.mu.RUnlock()
	if !gotPending {
		t.Fatal("expected row to actually be rehydrated into pending before testing the button click")
	}

	// Simulate a click on the pre-restart message — its button value still
	// carries "approve:rehy-int" verbatim.
	handled := handler.HandleInteraction(context.Background(), "approve", "approve:rehy-int", "U1", "tester", "")
	if !handled {
		t.Error("expected interaction to be handled after rehydrate")
	}
}

func TestSlackHandler_HandleInteraction_RecordsDecisionViaRecorder(t *testing.T) {
	client := &mockSlackClient{}
	recorder := &mockDecisionRecorder{}
	handler := NewSlackHandler(client, "#approvals").WithDecisionRecorder(recorder)

	req := &Request{ID: "req-1", TaskID: "T-1", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handler.HandleInteraction(context.Background(), "approve", "approve:req-1", "U1", "tester", "")

	calls := recorder.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 RecordDecision call, got %d", len(calls))
	}
	if calls[0].requestID != "req-1" || calls[0].decision != DecisionApproved || calls[0].by != "tester" {
		t.Errorf("unexpected recorded decision: %+v", calls[0])
	}
}

// TestSlackHandler_Rehydrate_InteractionRecordsDecisionDirectly is the
// GH-4411 regression test: after a restart, Rehydrate reconstructs the
// pending entry with a fresh ResponseCh that no goroutine is reading (the
// original waiter died with the old process). Without a DecisionRecorder,
// the decision made by a button click would only be sent into that unread
// channel and lost. With the recorder wired, HandleInteraction must persist
// the decision directly (mirrors GH-3825's Telegram fix).
func TestSlackHandler_Rehydrate_InteractionRecordsDecisionDirectly(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	recorder := &mockDecisionRecorder{}
	_ = store.InsertPendingApproval(&memory.PendingApproval{
		ID: "rehy-rec", TaskID: "T-R2", Stage: "pre_merge",
		Title: "Rehydrated", PreferredChannel: "slack", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})

	handler := NewSlackHandler(client, "#approvals").WithStore(store).WithDecisionRecorder(recorder)
	if err := handler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate error: %v", err)
	}

	handled := handler.HandleInteraction(context.Background(), "reject", "reject:rehy-rec", "U2", "reviewer", "")
	if !handled {
		t.Fatal("expected interaction to be handled")
	}

	calls := recorder.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected decision to be recorded directly after rehydrate, got %d calls", len(calls))
	}
	if calls[0].requestID != "rehy-rec" || calls[0].decision != DecisionRejected || calls[0].by != "reviewer" {
		t.Errorf("unexpected recorded decision: %+v", calls[0])
	}
}

func TestSlackHandler_PruneExpired_RemovesExpiredAndDeletesFromStore(t *testing.T) {
	client := &mockSlackClient{}
	store := newMockPendingStore()
	handler := NewSlackHandler(client, "#approvals").WithStore(store)

	req := &Request{
		ID: "exp-1", TaskID: "T-Exp", Stage: StagePreMerge,
		Title: "Expiring", ExpiresAt: time.Now().Add(10 * time.Millisecond),
	}
	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	n, err := handler.PruneExpired(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}

	handler.mu.RLock()
	_, stillPending := handler.pending["exp-1"]
	handler.mu.RUnlock()
	if stillPending {
		t.Error("expected expired request to be removed from pending")
	}
	if store.get("exp-1") != nil {
		t.Error("expected expired row to be deleted from store")
	}

	select {
	case resp, ok := <-responseCh:
		if ok && resp.Decision != DecisionTimeout {
			t.Errorf("expected timeout decision, got %v", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for response channel to resolve")
	}
}

// TestSlackHandler_HandleInteraction_RaceLoss_ShowsAlreadyDecided is the
// GH-4777 regression test (PR#4767 review, item 4): a click that loses the
// decision race — RecordDecision returns memory.ErrApprovalAlreadyDecided
// because another decider (a concurrent HTTP POST, or the same request
// answered via Telegram) already recorded the outcome first — must update
// the message with an "already decided" card, never a success card claiming
// this click's decision won.
func TestSlackHandler_HandleInteraction_RaceLoss_ShowsAlreadyDecided(t *testing.T) {
	client := &mockSlackClient{}
	recorder := &mockDecisionRecorder{err: memory.ErrApprovalAlreadyDecided}
	handler := NewSlackHandler(client, "#approvals").WithDecisionRecorder(recorder)

	req := &Request{ID: "req-race-loss", TaskID: "T-Race", Stage: StagePreMerge, Title: "Test", ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	handled := handler.HandleInteraction(context.Background(), "approve", "approve:req-race-loss", "U1", "loser", "")
	if !handled {
		t.Fatal("expected interaction to still be handled on a lost race")
	}

	if len(client.updateCalls) != 1 {
		t.Fatalf("expected 1 message update, got %d", len(client.updateCalls))
	}
	got := client.updateCalls[0]
	if !containsString(got.Text, "already decided") {
		t.Errorf("update text = %q, want it to mention already decided", got.Text)
	}
	if containsString(got.Text, "approved") {
		t.Errorf("update text = %q, must not claim the race-losing decision was applied", got.Text)
	}
}

func TestSlackHandler_HandleInteraction_ExpiredRaceIsTreatedAsTimeout(t *testing.T) {
	client := &mockSlackClient{}
	recorder := &mockDecisionRecorder{}
	handler := NewSlackHandler(client, "#approvals").WithDecisionRecorder(recorder)

	req := &Request{
		ID: "race-1", TaskID: "T-Race", Stage: StagePreMerge,
		Title: "Racing", ExpiresAt: time.Now().Add(10 * time.Millisecond),
	}
	if _, err := handler.SendApprovalRequest(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	time.Sleep(20 * time.Millisecond)

	handled := handler.HandleInteraction(context.Background(), "approve", "approve:race-1", "U1", "tester", "")
	if !handled {
		t.Error("expected interaction to be handled")
	}

	if len(recorder.getCalls()) != 0 {
		t.Error("expected no decision to be recorded for an interaction that arrived after expiry")
	}
}
