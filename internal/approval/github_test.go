package approval

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockGitHubReviewClient implements GitHubReviewClient for testing
type mockGitHubReviewClient struct {
	hasApproval bool
	approver    string
	err         error
	callCount   int

	// approvedPRs, when non-nil, overrides hasApproval: a PR is approved only
	// if its "owner/repo#number" key is present (GH-5716 colliding-number tests).
	approvedPRs map[string]bool

	mu    sync.Mutex
	calls []string // "owner/repo#number" per call
}

func (m *mockGitHubReviewClient) HasApprovalReview(ctx context.Context, owner, repo string, number int) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	key := fmt.Sprintf("%s/%s#%d", owner, repo, number)
	m.calls = append(m.calls, key)
	if m.approvedPRs != nil {
		return m.approvedPRs[key], m.approver, m.err
	}
	return m.hasApproval, m.approver, m.err
}

func (m *mockGitHubReviewClient) recordedCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
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
		Owner:      "owner",
		Repo:       "repo",
		PRNumber:   42,
		ResponseCh: responseCh,
		CancelFn:   cancel,
	}

	// Simulate webhook event
	handled := handler.HandleReviewEvent(context.Background(), "owner", "repo", 42, "submitted", "approved", "charlie")

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
	handled := handler.HandleReviewEvent(context.Background(), "owner", "repo", 42, "dismissed", "approved", "charlie")

	if handled {
		t.Error("HandleReviewEvent should return false for non-submitted action")
	}
}

func TestGitHubHandler_HandleReviewEvent_WrongState(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// Non-approved state should not be handled
	handled := handler.HandleReviewEvent(context.Background(), "owner", "repo", 42, "submitted", "commented", "charlie")

	if handled {
		t.Error("HandleReviewEvent should return false for non-approved state")
	}
}

func TestGitHubHandler_HandleReviewEvent_NoPendingRequest(t *testing.T) {
	handler := &GitHubHandler{
		pending: make(map[string]*githubPending),
	}

	// No pending request for PR 42
	handled := handler.HandleReviewEvent(context.Background(), "owner", "repo", 42, "submitted", "approved", "charlie")

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
		Owner:      "owner",
		Repo:       "repo",
		PRNumber:   42,
		ResponseCh: responseCh,
		CancelFn:   cancel,
	}

	// Simulate changes requested event
	handled := handler.HandleChangesRequestedEvent(context.Background(), "owner", "repo", 42, "reviewer")

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

	handled := handler.HandleChangesRequestedEvent(context.Background(), "owner", "repo", 99, "reviewer")

	if handled {
		t.Error("HandleChangesRequestedEvent should return false when no pending request")
	}
}

// testProjectResolver maps canonical project paths to owner/repo for the
// per-request repo resolution tests (GH-5716).
func testProjectResolver(project string) (string, string, bool) {
	switch project {
	case "/work/repo-a":
		return "OrgA", "RepoA", true
	case "/work/repo-b":
		return "OrgA", "RepoB", true
	}
	return "", "", false
}

func newProjectHandler(client GitHubReviewClient, resolver func(string) (string, string, bool)) *GitHubHandler {
	return NewGitHubHandler(client, &GitHubHandlerConfig{
		Owner:        "OrgA",
		Repo:         "RepoA",
		PollInterval: time.Hour,
		ResolveRepo:  resolver,
	})
}

func TestGitHubHandler_SendApprovalRequest_ResolvesRepoPerRequest(t *testing.T) {
	tests := []struct {
		name        string
		project     string
		resolver    func(string) (string, string, bool)
		approved    map[string]bool
		wantCall    string
		wantErr     bool
		wantApprove bool
	}{
		{
			name:        "empty project uses default repo",
			project:     "",
			resolver:    testProjectResolver,
			approved:    map[string]bool{"OrgA/RepoA#7": true},
			wantCall:    "OrgA/RepoA#7",
			wantApprove: true,
		},
		{
			name:        "project PR resolves to the project's repo",
			project:     "/work/repo-b",
			resolver:    testProjectResolver,
			approved:    map[string]bool{"OrgA/RepoB#7": true},
			wantCall:    "OrgA/RepoB#7",
			wantApprove: true,
		},
		{
			name:     "colliding PR number: approved default-repo PR must not approve project PR",
			project:  "/work/repo-b",
			resolver: testProjectResolver,
			approved: map[string]bool{"OrgA/RepoA#7": true},
			wantCall: "OrgA/RepoB#7",
		},
		{
			name:     "unresolvable project fails closed",
			project:  "/work/unknown",
			resolver: testProjectResolver,
			approved: map[string]bool{"OrgA/RepoA#7": true},
			wantErr:  true,
		},
		{
			name:     "project set without a resolver fails closed",
			project:  "/work/repo-b",
			resolver: nil,
			approved: map[string]bool{"OrgA/RepoA#7": true},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &mockGitHubReviewClient{approver: "alice", approvedPRs: tt.approved}
			handler := newProjectHandler(client, tt.resolver)

			req := &Request{
				ID:       "req-1",
				Project:  tt.project,
				Metadata: map[string]interface{}{"pr_number": 7},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch, err := handler.SendApprovalRequest(ctx, req)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error for unresolvable project")
				}
				if got := client.recordedCalls(); len(got) != 0 {
					t.Errorf("no GitHub query expected on fail-closed, got %v", got)
				}
				if len(handler.pending) != 0 {
					t.Errorf("no pending request expected, got %d", len(handler.pending))
				}
				return
			}
			if err != nil {
				t.Fatalf("SendApprovalRequest: %v", err)
			}

			if tt.wantApprove {
				select {
				case resp := <-ch:
					if resp.Decision != DecisionApproved {
						t.Errorf("Decision = %s, want approved", resp.Decision)
					}
				case <-time.After(time.Second):
					t.Fatal("timeout waiting for approval")
				}
			} else {
				select {
				case resp := <-ch:
					t.Fatalf("request must not resolve, got %+v", resp)
				case <-time.After(50 * time.Millisecond):
				}
			}

			for _, call := range client.recordedCalls() {
				if call != tt.wantCall {
					t.Errorf("HasApprovalReview called with %s, want only %s", call, tt.wantCall)
				}
			}
			if len(client.recordedCalls()) == 0 {
				t.Errorf("expected a query to %s, got none", tt.wantCall)
			}
		})
	}
}

func TestGitHubHandler_ReviewEvents_MatchOwnerRepoAndNumber(t *testing.T) {
	events := map[string]func(h *GitHubHandler, owner, repo string) bool{
		"HandleReviewEvent": func(h *GitHubHandler, owner, repo string) bool {
			return h.HandleReviewEvent(context.Background(), owner, repo, 7, "submitted", "approved", "carol")
		},
		"HandleChangesRequestedEvent": func(h *GitHubHandler, owner, repo string) bool {
			return h.HandleChangesRequestedEvent(context.Background(), owner, repo, 7, "carol")
		},
	}
	tests := []struct {
		name        string
		owner, repo string
		want        bool
	}{
		{"same repo and number resolves", "OrgA", "RepoB", true},
		{"other repo with colliding number does not resolve", "OrgA", "RepoA", false},
		{"other owner with colliding number does not resolve", "OrgZ", "RepoB", false},
	}

	for evName, fire := range events {
		for _, tt := range tests {
			t.Run(evName+"/"+tt.name, func(t *testing.T) {
				handler := newProjectHandler(&mockGitHubReviewClient{}, testProjectResolver)
				responseCh := make(chan *Response, 1)
				_, cancel := context.WithCancel(context.Background())
				handler.pending["req-b7"] = &githubPending{
					Request:    &Request{ID: "req-b7", Project: "/work/repo-b"},
					Owner:      "OrgA",
					Repo:       "RepoB",
					PRNumber:   7,
					ResponseCh: responseCh,
					CancelFn:   cancel,
				}

				if got := fire(handler, tt.owner, tt.repo); got != tt.want {
					t.Errorf("handled = %v, want %v", got, tt.want)
				}
				_, stillPending := handler.pending["req-b7"]
				if stillPending == tt.want {
					t.Errorf("stillPending = %v, want %v", stillPending, !tt.want)
				}
			})
		}
	}
}
