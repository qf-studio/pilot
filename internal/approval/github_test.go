package approval

import (
	"context"
	"testing"
	"time"
)

// mockGitHubReviewClient implements GitHubReviewClient for testing
type mockGitHubReviewClient struct {
	hasApproval bool
	approver    string
	err         error
	callCount   int
}

func (m *mockGitHubReviewClient) HasApprovalReview(ctx context.Context, owner, repo string, number int) (bool, string, error) {
	m.callCount++
	return m.hasApproval, m.approver, m.err
}

func TestNewGitHubHandler(t *testing.T) {
	client := &mockGitHubReviewClient{}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: 10 * time.Second,
	}

	handler := NewGitHubHandler(client, cfg)

	if handler == nil {
		t.Fatal("NewGitHubHandler returned nil")
	}
	if handler.owner != "owner" {
		t.Errorf("handler.owner = %s, want owner", handler.owner)
	}
	if handler.repo != "repo" {
		t.Errorf("handler.repo = %s, want repo", handler.repo)
	}
	if handler.pollInterval != 10*time.Second {
		t.Errorf("handler.pollInterval = %v, want 10s", handler.pollInterval)
	}
}

func TestNewGitHubHandler_DefaultPollInterval(t *testing.T) {
	client := &mockGitHubReviewClient{}
	cfg := &GitHubHandlerConfig{
		Owner: "owner",
		Repo:  "repo",
		// PollInterval not set
	}

	handler := NewGitHubHandler(client, cfg)

	if handler.pollInterval != 30*time.Second {
		t.Errorf("handler.pollInterval = %v, want 30s default", handler.pollInterval)
	}
}

func TestGitHubHandler_Name(t *testing.T) {
	handler := &GitHubHandler{}
	if handler.Name() != "github" {
		t.Errorf("Name() = %s, want github", handler.Name())
	}
}

func TestGitHubHandler_SendApprovalRequest_MissingPRNumber(t *testing.T) {
	client := &mockGitHubReviewClient{}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Millisecond,
	}
	handler := NewGitHubHandler(client, cfg)

	req := &Request{
		ID:       "test-123",
		TaskID:   "task-1",
		Stage:    StagePreMerge,
		Metadata: map[string]interface{}{}, // Missing pr_number
	}

	_, err := handler.SendApprovalRequest(context.Background(), req)
	if err == nil {
		t.Error("expected error for missing pr_number")
	}
}

func TestGitHubHandler_SendApprovalRequest_ImmediateApproval(t *testing.T) {
	client := &mockGitHubReviewClient{
		hasApproval: true,
		approver:    "alice",
	}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Millisecond,
	}
	handler := NewGitHubHandler(client, cfg)

	req := &Request{
		ID:     "test-123",
		TaskID: "task-1",
		Stage:  StagePreMerge,
		Metadata: map[string]interface{}{
			"pr_number": 42,
		},
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("SendApprovalRequest failed: %v", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("response.Decision = %s, want approved", resp.Decision)
		}
		if resp.ApprovedBy != "alice" {
			t.Errorf("response.ApprovedBy = %s, want alice", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for approval response")
	}
}

func TestGitHubHandler_SendApprovalRequest_PRNumberAsFloat64(t *testing.T) {
	client := &mockGitHubReviewClient{
		hasApproval: true,
		approver:    "bob",
	}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Millisecond,
	}
	handler := NewGitHubHandler(client, cfg)

	// JSON unmarshaling produces float64 for numbers
	req := &Request{
		ID:     "test-456",
		TaskID: "task-2",
		Stage:  StagePreMerge,
		Metadata: map[string]interface{}{
			"pr_number": float64(99),
		},
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("SendApprovalRequest failed: %v", err)
	}

	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("response.Decision = %s, want approved", resp.Decision)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for approval response")
	}
}

func TestGitHubHandler_CancelRequest(t *testing.T) {
	client := &mockGitHubReviewClient{
		hasApproval: false,
	}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Hour, // Long interval to ensure polling doesn't complete
	}
	handler := NewGitHubHandler(client, cfg)

	req := &Request{
		ID:     "test-cancel",
		TaskID: "task-3",
		Stage:  StagePreMerge,
		Metadata: map[string]interface{}{
			"pr_number": 50,
		},
	}

	responseCh, err := handler.SendApprovalRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("SendApprovalRequest failed: %v", err)
	}

	// Cancel the request
	err = handler.CancelRequest(context.Background(), "test-cancel")
	if err != nil {
		t.Fatalf("CancelRequest failed: %v", err)
	}

	// Channel should be closed
	select {
	case _, ok := <-responseCh:
		if ok {
			t.Error("expected channel to be closed after cancel")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for channel close")
	}
}

func TestGitHubHandler_CancelRequest_NotFound(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// Should not error when cancelling non-existent request
	err := handler.CancelRequest(context.Background(), "nonexistent")
	if err != nil {
		t.Errorf("CancelRequest should not error for non-existent request: %v", err)
	}
}

func TestGitHubHandler_HandleReviewEvent_Approved(t *testing.T) {
	client := &mockGitHubReviewClient{}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Hour,
	}
	handler := NewGitHubHandler(client, cfg)

	// Create a pending request
	responseCh := make(chan *Response, 1)
	_, cancel := context.WithCancel(context.Background())
	handler.pending["test-webhook"] = &githubPending{
		Request:    &Request{ID: "test-webhook"},
		PRNumber:   42,
		ResponseCh: responseCh,
		CancelFn:   cancel,
	}

	// Simulate webhook event
	handled := handler.HandleReviewEvent(context.Background(), 42, "submitted", "approved", "charlie")

	if !handled {
		t.Error("HandleReviewEvent should return true for matching PR")
	}

	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionApproved {
			t.Errorf("response.Decision = %s, want approved", resp.Decision)
		}
		if resp.ApprovedBy != "charlie" {
			t.Errorf("response.ApprovedBy = %s, want charlie", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for response")
	}
}

func TestGitHubHandler_HandleReviewEvent_WrongAction(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// "dismissed" action should not be handled
	handled := handler.HandleReviewEvent(context.Background(), 42, "dismissed", "approved", "charlie")

	if handled {
		t.Error("HandleReviewEvent should return false for non-submitted action")
	}
}

func TestGitHubHandler_HandleReviewEvent_WrongState(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// Non-approved state should not be handled
	handled := handler.HandleReviewEvent(context.Background(), 42, "submitted", "commented", "charlie")

	if handled {
		t.Error("HandleReviewEvent should return false for non-approved state")
	}
}

func TestGitHubHandler_HandleReviewEvent_NoPendingRequest(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// No pending request for PR 42
	handled := handler.HandleReviewEvent(context.Background(), 42, "submitted", "approved", "charlie")

	if handled {
		t.Error("HandleReviewEvent should return false when no pending request")
	}
}

func TestGitHubHandler_HandleChangesRequestedEvent(t *testing.T) {
	client := &mockGitHubReviewClient{}
	cfg := &GitHubHandlerConfig{
		Owner:        "owner",
		Repo:         "repo",
		PollInterval: time.Hour,
	}
	handler := NewGitHubHandler(client, cfg)

	// Create a pending request
	responseCh := make(chan *Response, 1)
	_, cancel := context.WithCancel(context.Background())
	handler.pending["test-changes"] = &githubPending{
		Request:    &Request{ID: "test-changes"},
		PRNumber:   42,
		ResponseCh: responseCh,
		CancelFn:   cancel,
	}

	// Simulate changes requested event
	handled := handler.HandleChangesRequestedEvent(context.Background(), 42, "reviewer")

	if !handled {
		t.Error("HandleChangesRequestedEvent should return true for matching PR")
	}

	select {
	case resp := <-responseCh:
		if resp.Decision != DecisionRejected {
			t.Errorf("response.Decision = %s, want rejected", resp.Decision)
		}
		if resp.ApprovedBy != "reviewer" {
			t.Errorf("response.ApprovedBy = %s, want reviewer", resp.ApprovedBy)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for response")
	}
}

func TestGitHubHandler_HandleChangesRequestedEvent_NoPendingRequest(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	handled := handler.HandleChangesRequestedEvent(context.Background(), 99, "reviewer")

	if handled {
		t.Error("HandleChangesRequestedEvent should return false when no pending request")
	}
}
