package approval

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
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

// --- persistence / rehydrate tests (GH-5757) ---

func newStoreHandler(client GitHubReviewClient, store PendingApprovalStore, rec DecisionRecorder) *GitHubHandler {
	h := newProjectHandler(client, testProjectResolver)
	h.pollInterval = 5 * time.Millisecond
	if store != nil {
		h.WithStore(store)
	}
	if rec != nil {
		h.WithDecisionRecorder(rec)
	}
	return h
}

func githubReviewRequest(id, project string, pr int) *Request {
	return &Request{
		ID:               id,
		TaskID:           "task-" + id,
		Stage:            StagePreMerge,
		Project:          project,
		PreferredChannel: githubReviewAlias,
		CreatedAt:        time.Now(),
		ExpiresAt:        time.Now().Add(time.Hour),
		Metadata:         map[string]interface{}{"pr_number": pr},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestGitHubHandler_RehydrateResumesPolling(t *testing.T) {
	store := newMockPendingStore()

	// Pre-restart handler: sends the request, which must persist a row. Its
	// poll never sees an approval and is torn down to simulate the restart.
	oldCtx, killOld := context.WithCancel(context.Background())
	oldHandler := newStoreHandler(&mockGitHubReviewClient{}, store, nil)
	if _, err := oldHandler.SendApprovalRequest(oldCtx, githubReviewRequest("req-1", "/work/repo-b", 7)); err != nil {
		t.Fatalf("SendApprovalRequest: %v", err)
	}
	killOld()

	row := store.get("req-1")
	if row == nil {
		t.Fatal("send did not persist a pending approval row")
	}
	if row.Project != "/work/repo-b" || row.PreferredChannel != githubReviewAlias {
		t.Errorf("persisted row project=%q channel=%q", row.Project, row.PreferredChannel)
	}

	// Post-restart handler: the reviewer approved the current head SHA while the
	// daemon was down.
	client := &mockGitHubReviewClient{
		approvedPRs: map[string]bool{"OrgA/RepoB#7": true},
		approver:    "alice",
	}
	rec := &mockDecisionRecorder{}
	newHandler := newStoreHandler(client, store, rec)
	if err := newHandler.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}

	waitFor(t, "decision to be recorded", func() bool { return len(rec.getCalls()) == 1 })
	call := rec.getCalls()[0]
	if call.requestID != "req-1" || call.decision != DecisionApproved || call.by != "alice" {
		t.Errorf("recorded decision = %+v, want req-1/approved/alice", call)
	}
	for _, c := range client.recordedCalls() {
		if c != "OrgA/RepoB#7" {
			t.Errorf("GitHub call %q, want only the project's repo OrgA/RepoB#7", c)
		}
	}
	waitFor(t, "row to be deleted after decision", func() bool { return store.get("req-1") == nil })
	newHandler.mu.RLock()
	defer newHandler.mu.RUnlock()
	if len(newHandler.pending) != 0 {
		t.Errorf("pending = %d after decision, want 0", len(newHandler.pending))
	}
}

func TestGitHubHandler_Rehydrate_JSONRoundTripPRNumber(t *testing.T) {
	store := newMockPendingStore()
	// pr_number comes back from the SQLite JSON column as float64.
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:               "req-json",
		Stage:            string(StagePreMerge),
		PreferredChannel: "github",
		Metadata:         map[string]interface{}{"pr_number": float64(42)},
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	client := &mockGitHubReviewClient{hasApproval: true, approver: "bob"}
	rec := &mockDecisionRecorder{}
	h := newStoreHandler(client, store, rec)
	if err := h.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	waitFor(t, "decision to be recorded", func() bool { return len(rec.getCalls()) == 1 })
	// Empty project resolves to the handler's default repo.
	if got := client.recordedCalls()[0]; got != "OrgA/RepoA#42" {
		t.Errorf("GitHub call = %q, want OrgA/RepoA#42", got)
	}
}

func TestGitHubHandler_Rehydrate_UnresolvedProjectSkipped(t *testing.T) {
	store := newMockPendingStore()
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:               "req-gone",
		Stage:            string(StagePreMerge),
		Project:          "/work/removed-from-config",
		PreferredChannel: githubReviewAlias,
		Metadata:         map[string]interface{}{"pr_number": float64(7)},
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Approve every PR: if the row fell back to the default repo it would resolve.
	client := &mockGitHubReviewClient{hasApproval: true, approver: "mallory"}
	rec := &mockDecisionRecorder{}
	h := newStoreHandler(client, store, rec)
	var logs bytes.Buffer
	h.log = slog.New(slog.NewTextHandler(&logs, nil))

	if err := h.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // several poll intervals

	if calls := client.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub called for unresolved project: %v", calls)
	}
	if n := len(rec.getCalls()); n != 0 {
		t.Errorf("recorded %d decisions for unresolved project", n)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "req-gone") {
		t.Errorf("expected WARN log naming req-gone, got: %s", out)
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.pending) != 0 {
		t.Errorf("pending = %d, want 0 (row skipped)", len(h.pending))
	}
}

func TestGitHubHandler_Rehydrate_OwnershipAndExpiry(t *testing.T) {
	store := newMockPendingStore()
	future := time.Now().Add(time.Hour)
	for _, r := range []*memory.PendingApproval{
		{ID: "slack-row", PreferredChannel: "slack", Metadata: map[string]interface{}{"pr_number": 1}, ExpiresAt: future},
		{ID: "legacy-row", PreferredChannel: "", Metadata: map[string]interface{}{"pr_number": 2}, ExpiresAt: future},
		{ID: "expired-row", PreferredChannel: "github", Metadata: map[string]interface{}{"pr_number": 3}, ExpiresAt: time.Now().Add(-time.Minute)},
	} {
		r.Stage = string(StagePreMerge)
		if err := store.InsertPendingApproval(r); err != nil {
			t.Fatal(err)
		}
	}

	client := &mockGitHubReviewClient{hasApproval: true}
	h := newStoreHandler(client, store, &mockDecisionRecorder{})
	if err := h.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	time.Sleep(30 * time.Millisecond)

	if calls := client.recordedCalls(); len(calls) != 0 {
		t.Errorf("GitHub called for rows this handler does not own / expired: %v", calls)
	}
	if store.get("slack-row") == nil || store.get("legacy-row") == nil {
		t.Error("handler deleted a row owned by another channel")
	}
	if store.get("expired-row") != nil {
		t.Error("expired owned row was not deleted")
	}
}

func TestGitHubHandler_Rehydrate_NoStore(t *testing.T) {
	h := newStoreHandler(&mockGitHubReviewClient{}, nil, nil)
	if err := h.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate without store: %v", err)
	}
}

func TestGitHubHandler_DeletesPersistedRowOnCancelWebhookAndExpiry(t *testing.T) {
	ctx := context.Background()
	store := newMockPendingStore()
	h := newStoreHandler(&mockGitHubReviewClient{}, store, &mockDecisionRecorder{})

	for _, id := range []string{"cancel", "approve", "reject", "expire"} {
		if _, err := h.SendApprovalRequest(ctx, githubReviewRequest(id, "/work/repo-a", map[string]int{"cancel": 1, "approve": 2, "reject": 3, "expire": 4}[id])); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	if store.len() != 4 {
		t.Fatalf("store has %d rows, want 4", store.len())
	}

	if err := h.CancelRequest(ctx, "cancel"); err != nil {
		t.Fatal(err)
	}
	if store.get("cancel") != nil {
		t.Error("row not deleted on cancel")
	}

	if !h.HandleReviewEvent(ctx, "OrgA", "RepoA", 2, "submitted", "approved", "carol") {
		t.Fatal("approve event not handled")
	}
	if store.get("approve") != nil {
		t.Error("row not deleted on webhook approval")
	}

	if !h.HandleChangesRequestedEvent(ctx, "OrgA", "RepoA", 3, "dave") {
		t.Fatal("changes-requested event not handled")
	}
	if store.get("reject") != nil {
		t.Error("row not deleted on changes requested")
	}

	h.mu.Lock()
	h.pending["expire"].Request.ExpiresAt = time.Now().Add(-time.Minute)
	h.mu.Unlock()
	n, err := h.PruneExpired(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PruneExpired = %d, %v; want 1, nil", n, err)
	}
	if store.get("expire") != nil {
		t.Error("row not deleted on expiry")
	}
}

func TestGitHubHandler_PruneExpired_SweepsOrphanedAliasRows(t *testing.T) {
	store := newMockPendingStore()
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID: "orphan", Stage: string(StagePreMerge), PreferredChannel: githubReviewAlias,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	h := newStoreHandler(&mockGitHubReviewClient{}, store, nil)
	if _, err := h.PruneExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.get("orphan") != nil {
		t.Error("expired github-review row (raw alias) was not swept")
	}
}

// --- GH-5803: decision must be recorded on a ctx that survives the poll cancel ---

// ctxCheckingRecorder mimics memory.Store.SetApprovalDecision: it fails with
// ctx.Err() when the ctx it is handed is already done.
type ctxCheckingRecorder struct {
	mu       sync.Mutex
	ctxErr   error
	decision Decision
	by       string
	calls    int
}

func (r *ctxCheckingRecorder) RecordDecision(ctx context.Context, _ string, decision Decision, by string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if err := ctx.Err(); err != nil {
		r.ctxErr = err
		return err
	}
	r.decision, r.by = decision, by
	return nil
}

// driveCheckApproval wires cancelPoll as the pending entry's CancelFn exactly
// like SendApprovalRequest/Rehydrate do, then runs one checkApproval on pollCtx.
func driveCheckApproval(t *testing.T, h *GitHubHandler, req *Request, prNumber int) <-chan *Response {
	t.Helper()
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	t.Cleanup(cancelPoll)
	ch := make(chan *Response, 1)
	h.mu.Lock()
	h.pending[req.ID] = &githubPending{
		Request: req, Owner: "OrgA", Repo: "RepoA", PRNumber: prNumber,
		ResponseCh: ch, CancelFn: cancelPoll,
	}
	h.mu.Unlock()

	if !h.checkApproval(pollCtx, req.ID) {
		t.Fatal("checkApproval returned false for an approved PR")
	}
	return ch
}

func assertDetachedApproval(t *testing.T, rec *ctxCheckingRecorder, ch <-chan *Response) {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.ctxErr != nil {
		t.Fatalf("recorder saw a done ctx (%v); the decision write ran on the cancelled poll ctx", rec.ctxErr)
	}
	if rec.calls != 1 || rec.decision != DecisionApproved || rec.by != "alice" {
		t.Errorf("recorder calls=%d decision=%q by=%q, want 1/approved/alice", rec.calls, rec.decision, rec.by)
	}
	select {
	case resp := <-ch:
		if resp == nil || resp.Decision != DecisionApproved || resp.ApprovedBy != "alice" {
			t.Errorf("waiter response = %+v, want approved by alice", resp)
		}
	default:
		t.Fatal("waiter received no response")
	}
}

func TestCheckApproval_RecordsDecisionOnDetachedContext(t *testing.T) {
	client := &mockGitHubReviewClient{hasApproval: true, approver: "alice"}
	rec := &ctxCheckingRecorder{}
	h := newStoreHandler(client, nil, rec)

	ch := driveCheckApproval(t, h, githubReviewRequest("req-detached", "", 11), 11)
	assertDetachedApproval(t, rec, ch)
}

func TestCheckApproval_Rehydrated_RecordsDecisionOnDetachedContext(t *testing.T) {
	store := newMockPendingStore()
	if err := store.InsertPendingApproval(&memory.PendingApproval{
		ID:               "req-rehydrated",
		Stage:            string(StagePreMerge),
		PreferredChannel: githubReviewAlias,
		Metadata:         map[string]interface{}{"pr_number": float64(12)},
		ExpiresAt:        time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	client := &mockGitHubReviewClient{hasApproval: true, approver: "alice"}
	rec := &ctxCheckingRecorder{}
	h := newStoreHandler(client, store, rec)
	// Hold the real poll off so Rehydrate's entry can be checked deterministically.
	h.pollInterval = time.Hour
	client.mu.Lock()
	client.hasApproval = false
	client.mu.Unlock()

	if err := h.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	waitFor(t, "initial poll", func() bool { return len(client.recordedCalls()) >= 1 })

	h.mu.RLock()
	p := h.pending["req-rehydrated"]
	h.mu.RUnlock()
	if p == nil {
		t.Fatal("rehydrate did not restore the pending entry")
	}

	// The reviewer approves; drive a check on a ctx tied to the restored entry's CancelFn.
	client.mu.Lock()
	client.hasApproval = true
	client.mu.Unlock()
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	defer cancelPoll()
	origCancel := p.CancelFn
	p.CancelFn = func() { origCancel(); cancelPoll() }

	if !h.checkApproval(pollCtx, "req-rehydrated") {
		t.Fatal("checkApproval returned false for an approved PR")
	}
	assertDetachedApproval(t, rec, p.ResponseCh)
}
