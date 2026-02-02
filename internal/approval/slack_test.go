package approval

import (
	"context"
	"testing"
	"time"
)

// mockSlackClient implements SlackClient for testing
type mockSlackClient struct {
	lastMessage *SlackInteractiveMessage
	response    *SlackPostMessageResponse
	updateError error
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
