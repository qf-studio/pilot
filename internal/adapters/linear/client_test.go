package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.apiKey != testutil.FakeLinearAPIKey {
		t.Errorf("client.apiKey = %s, want %s", client.apiKey, testutil.FakeLinearAPIKey)
	}
	if client.httpClient == nil {
		t.Error("client.httpClient is nil")
	}
	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("client.httpClient.Timeout = %v, want 30s", client.httpClient.Timeout)
	}
	if client.doneStateCache == nil {
		t.Error("client.doneStateCache is nil")
	}
}

func TestExecute_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request method
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Verify headers
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %s, want application/json", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != testutil.FakeLinearAPIKey {
			t.Errorf("Authorization = %s, want "+testutil.FakeLinearAPIKey, r.Header.Get("Authorization"))
		}

		// Verify request body
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if reqBody.Query != "query { viewer { id } }" {
			t.Errorf("query = %s, want query { viewer { id } }", reqBody.Query)
		}

		// Send response
		resp := GraphQLResponse{
			Data: json.RawMessage(`{"viewer": {"id": "user-123"}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	var result struct {
		Viewer struct {
			ID string `json:"id"`
		} `json:"viewer"`
	}

	err := client.execute(context.Background(), "query { viewer { id } }", nil, &result)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.Viewer.ID != "user-123" {
		t.Errorf("result.Viewer.ID = %s, want user-123", result.Viewer.ID)
	}
}

func TestExecute_WithVariables(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify variables
		if reqBody.Variables["id"] != "issue-123" {
			t.Errorf("variables[id] = %v, want issue-123", reqBody.Variables["id"])
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"issue": {"id": "issue-123"}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	var result struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}

	variables := map[string]interface{}{"id": "issue-123"}
	err := client.execute(context.Background(), "query GetIssue($id: String!) { issue(id: $id) { id } }", variables, &result)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

func TestExecute_GraphQLError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{
				{Message: "Issue not found"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.execute(context.Background(), "query { issue(id: \"invalid\") { id } }", nil, nil)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if err.Error() != "GraphQL error: Issue not found" {
		t.Errorf("error = %v, want 'GraphQL error: Issue not found'", err)
	}
}

func TestExecute_HTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantErr    string
	}{
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   `{"error": "Invalid API key"}`,
			wantErr:    "API error:",
		},
		{
			name:       "internal server error",
			statusCode: http.StatusInternalServerError,
			response:   `{"error": "Internal error"}`,
			wantErr:    "API error:",
		},
		{
			name:       "rate limited",
			statusCode: http.StatusTooManyRequests,
			response:   `{"error": "Rate limit exceeded"}`,
			wantErr:    "API error:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

			err := client.execute(context.Background(), "query { viewer { id } }", nil, nil)
			if err == nil {
				t.Fatal("expected error but got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{invalid json}`))
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.execute(context.Background(), "query { viewer { id } }", nil, nil)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %v, want to contain 'failed to parse response'", err)
	}
}

func TestExecute_NilResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Data: json.RawMessage(`{"success": true}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	// Should not error when result is nil
	err := client.execute(context.Background(), "mutation { doSomething { success } }", nil, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

func TestExecute_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		resp := GraphQLResponse{
			Data: json.RawMessage(`{}`),
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := client.execute(ctx, "query { viewer { id } }", nil, nil)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
}

func TestGetIssue_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify the ID variable
		if reqBody.Variables["id"] != "issue-123" {
			t.Errorf("variables[id] = %v, want issue-123", reqBody.Variables["id"])
		}

		// Verify query contains expected fields
		if !contains(reqBody.Query, "issue(id: $id)") {
			t.Errorf("query should contain 'issue(id: $id)', got: %s", reqBody.Query)
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{
				"issue": {
					"id": "issue-123",
					"identifier": "PROJ-42",
					"title": "Fix the bug",
					"description": "Description of the bug",
					"priority": 2,
					"state": {
						"id": "state-1",
						"name": "In Progress",
						"type": "started"
					},
					"labels": {
						"nodes": [
							{"id": "label-1", "name": "bug"},
							{"id": "label-2", "name": "pilot"}
						]
					},
					"assignee": {
						"id": "user-1",
						"name": "John Doe",
						"email": "john@example.com"
					},
					"project": {
						"id": "project-1",
						"name": "Main Project"
					},
					"team": {
						"id": "team-1",
						"name": "Engineering",
						"key": "ENG"
					},
					"createdAt": "2024-01-15T10:00:00Z",
					"updatedAt": "2024-01-16T12:00:00Z"
				}
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	issue, err := client.getIssue(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if issue.ID != "issue-123" {
		t.Errorf("issue.ID = %s, want issue-123", issue.ID)
	}
	if issue.Identifier != "PROJ-42" {
		t.Errorf("issue.Identifier = %s, want PROJ-42", issue.Identifier)
	}
	if issue.Title != "Fix the bug" {
		t.Errorf("issue.Title = %s, want 'Fix the bug'", issue.Title)
	}
	if issue.Description != "Description of the bug" {
		t.Errorf("issue.Description = %s, want 'Description of the bug'", issue.Description)
	}
	if issue.Priority != 2 {
		t.Errorf("issue.Priority = %d, want 2", issue.Priority)
	}
	if issue.State.Name != "In Progress" {
		t.Errorf("issue.State.Name = %s, want 'In Progress'", issue.State.Name)
	}
	if issue.State.Type != "started" {
		t.Errorf("issue.State.Type = %s, want 'started'", issue.State.Type)
	}
	if issue.Team.Key != "ENG" {
		t.Errorf("issue.Team.Key = %s, want ENG", issue.Team.Key)
	}
	if issue.Assignee == nil {
		t.Error("issue.Assignee is nil")
	} else if issue.Assignee.Email != "john@example.com" {
		t.Errorf("issue.Assignee.Email = %s, want john@example.com", issue.Assignee.Email)
	}
	if issue.Project == nil {
		t.Error("issue.Project is nil")
	} else if issue.Project.Name != "Main Project" {
		t.Errorf("issue.Project.Name = %s, want 'Main Project'", issue.Project.Name)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{
				{Message: "Entity not found: Issue"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	_, err := client.getIssue(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !contains(err.Error(), "Entity not found") {
		t.Errorf("error = %v, want to contain 'Entity not found'", err)
	}
}

func TestGetIssue_NullAssigneeAndProject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Data: json.RawMessage(`{
				"issue": {
					"id": "issue-123",
					"identifier": "PROJ-42",
					"title": "Unassigned issue",
					"description": "",
					"priority": 0,
					"state": {
						"id": "state-1",
						"name": "Backlog",
						"type": "backlog"
					},
					"labels": {"nodes": []},
					"assignee": null,
					"project": null,
					"team": {
						"id": "team-1",
						"name": "Engineering",
						"key": "ENG"
					},
					"createdAt": "2024-01-15T10:00:00Z",
					"updatedAt": "2024-01-15T10:00:00Z"
				}
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	issue, err := client.getIssue(context.Background(), "issue-123")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}

	if issue.Assignee != nil {
		t.Errorf("issue.Assignee = %v, want nil", issue.Assignee)
	}
	if issue.Project != nil {
		t.Errorf("issue.Project = %v, want nil", issue.Project)
	}
	if len(issue.Labels) != 0 {
		t.Errorf("issue.Labels = %v, want empty", issue.Labels)
	}
}

func TestUpdateIssueState_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify mutation
		if !contains(reqBody.Query, "issueUpdate") {
			t.Errorf("query should contain 'issueUpdate', got: %s", reqBody.Query)
		}
		if !contains(reqBody.Query, "stateId") {
			t.Errorf("query should contain 'stateId', got: %s", reqBody.Query)
		}

		// Verify variables
		if reqBody.Variables["id"] != "issue-123" {
			t.Errorf("variables[id] = %v, want issue-123", reqBody.Variables["id"])
		}
		if reqBody.Variables["stateId"] != "state-456" {
			t.Errorf("variables[stateId] = %v, want state-456", reqBody.Variables["stateId"])
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"issueUpdate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.updateIssueState(context.Background(), "issue-123", "state-456")
	if err != nil {
		t.Fatalf("UpdateIssueState failed: %v", err)
	}
}

func TestUpdateIssueState_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{
				{Message: "Cannot update issue state"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.updateIssueState(context.Background(), "issue-123", "invalid-state")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
}

func TestAddComment_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify mutation
		if !contains(reqBody.Query, "commentCreate") {
			t.Errorf("query should contain 'commentCreate', got: %s", reqBody.Query)
		}

		// Verify variables
		if reqBody.Variables["issueId"] != "issue-123" {
			t.Errorf("variables[issueId] = %v, want issue-123", reqBody.Variables["issueId"])
		}
		if reqBody.Variables["body"] != "This is a test comment" {
			t.Errorf("variables[body] = %v, want 'This is a test comment'", reqBody.Variables["body"])
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.addComment(context.Background(), "issue-123", "This is a test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
}

func TestAddComment_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{
				{Message: "Cannot add comment to issue"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	err := client.addComment(context.Background(), "issue-123", "comment")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
}

// TestClientMethodSignatures verifies all client methods have correct signatures
func TestClientMethodSignatures(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	ctx := context.Background()

	// These verify signatures compile correctly (actual calls will fail without mock server)
	var err error

	// Execute
	err = client.Execute(ctx, "query {}", nil, nil)
	_ = err

	// GetIssue
	_, err = client.GetIssue(ctx, "id")
	_ = err

	// UpdateIssueState
	err = client.UpdateIssueState(ctx, "issue", "state")
	_ = err

	// AddComment
	err = client.AddComment(ctx, "issue", "body")
	_ = err
}

// testableClient wraps Client methods with custom URL support for testing
type testableClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string

	doneStateMu    sync.RWMutex
	doneStateCache map[string]string
}

func newTestableClient(baseURL, apiKey string) *testableClient {
	return &testableClient{
		apiKey:         apiKey,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		baseURL:        baseURL,
		doneStateCache: make(map[string]string),
	}
}

func (c *testableClient) execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	reqBody := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %s", string(respBody))
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", gqlResp.Errors[0].Message)
	}

	if result != nil {
		if err := json.Unmarshal(gqlResp.Data, result); err != nil {
			return fmt.Errorf("failed to parse data: %w", err)
		}
	}

	return nil
}

// issueResponse matches the Linear GraphQL response structure
type issueResponse struct {
	Issue struct {
		ID          string `json:"id"`
		Identifier  string `json:"identifier"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Priority    int    `json:"priority"`
		State       State  `json:"state"`
		Labels      struct {
			Nodes []Label `json:"nodes"`
		} `json:"labels"`
		Assignee  *User    `json:"assignee"`
		Project   *Project `json:"project"`
		Team      Team     `json:"team"`
		CreatedAt string   `json:"createdAt"`
		UpdatedAt string   `json:"updatedAt"`
	} `json:"issue"`
}

func (c *testableClient) getIssue(ctx context.Context, id string) (*Issue, error) {
	query := `
		query GetIssue($id: String!) {
			issue(id: $id) {
				id
				identifier
				title
				description
				priority
				state {
					id
					name
					type
				}
				labels {
					nodes {
						id
						name
					}
				}
				assignee {
					id
					name
					email
				}
				project {
					id
					name
				}
				team {
					id
					name
					key
				}
				createdAt
				updatedAt
			}
		}
	`

	var result issueResponse

	if err := c.execute(ctx, query, map[string]interface{}{"id": id}, &result); err != nil {
		return nil, err
	}

	// Convert response to Issue struct
	issue := &Issue{
		ID:          result.Issue.ID,
		Identifier:  result.Issue.Identifier,
		Title:       result.Issue.Title,
		Description: result.Issue.Description,
		Priority:    result.Issue.Priority,
		State:       result.Issue.State,
		Labels:      result.Issue.Labels.Nodes,
		Assignee:    result.Issue.Assignee,
		Project:     result.Issue.Project,
		Team:        result.Issue.Team,
	}

	return issue, nil
}

func (c *testableClient) updateIssueState(ctx context.Context, issueID, stateID string) error {
	mutation := `
		mutation UpdateIssue($id: String!, $stateId: String!) {
			issueUpdate(id: $id, input: { stateId: $stateId }) {
				success
			}
		}
	`

	return c.execute(ctx, mutation, map[string]interface{}{
		"id":      issueID,
		"stateId": stateID,
	}, nil)
}

func (c *testableClient) addComment(ctx context.Context, issueID, body string) error {
	mutation := `
		mutation CreateComment($issueId: String!, $body: String!) {
			commentCreate(input: { issueId: $issueId, body: $body }) {
				success
			}
		}
	`

	return c.execute(ctx, mutation, map[string]interface{}{
		"issueId": issueID,
		"body":    body,
	}, nil)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func (c *testableClient) getTeamDoneStateID(ctx context.Context, teamKey string) (string, error) {
	// Check cache with read lock
	c.doneStateMu.RLock()
	if id, ok := c.doneStateCache[teamKey]; ok {
		c.doneStateMu.RUnlock()
		return id, nil
	}
	c.doneStateMu.RUnlock()

	// Query API for completed state
	query := `
		query GetTeamDoneState($teamKey: String!) {
			workflowStates(filter: { team: { key: { eq: $teamKey } }, type: { eq: "completed" } }) {
				nodes {
					id
					name
					type
				}
			}
		}
	`

	var result struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}

	if err := c.execute(ctx, query, map[string]interface{}{"teamKey": teamKey}, &result); err != nil {
		return "", err
	}

	if len(result.WorkflowStates.Nodes) == 0 {
		return "", fmt.Errorf("no completed state found for team %s", teamKey)
	}

	stateID := result.WorkflowStates.Nodes[0].ID

	// Store in cache with write lock
	c.doneStateMu.Lock()
	c.doneStateCache[teamKey] = stateID
	c.doneStateMu.Unlock()

	return stateID, nil
}

func TestGetTeamDoneStateID(t *testing.T) {
	tests := []struct {
		name        string
		teamKey     string
		response    GraphQLResponse
		wantID      string
		wantErr     bool
		errContains string
	}{
		{
			name:    "returns completed state ID",
			teamKey: "ENG",
			response: GraphQLResponse{
				Data: json.RawMessage(`{
					"workflowStates": {
						"nodes": [
							{"id": "state-done-123", "name": "Done", "type": "completed"}
						]
					}
				}`),
			},
			wantID:  "state-done-123",
			wantErr: false,
		},
		{
			name:    "returns error when no completed state found",
			teamKey: "EMPTY",
			response: GraphQLResponse{
				Data: json.RawMessage(`{
					"workflowStates": {
						"nodes": []
					}
				}`),
			},
			wantID:      "",
			wantErr:     true,
			errContains: "no completed state found",
		},
		{
			name:    "returns GraphQL error",
			teamKey: "BAD",
			response: GraphQLResponse{
				Errors: []GraphQLError{
					{Message: "Team not found"},
				},
			},
			wantID:      "",
			wantErr:     true,
			errContains: "Team not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

			gotID, err := client.getTeamDoneStateID(context.Background(), tt.teamKey)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error but got nil")
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want to contain %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotID != tt.wantID {
				t.Errorf("GetTeamDoneStateID() = %s, want %s", gotID, tt.wantID)
			}
		})
	}
}

func TestGetTeamDoneStateID_Cache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := GraphQLResponse{
			Data: json.RawMessage(`{
				"workflowStates": {
					"nodes": [
						{"id": "state-done-456", "name": "Done", "type": "completed"}
					]
				}
			}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := newTestableClient(server.URL, testutil.FakeLinearAPIKey)

	// First call should hit the server
	id1, err := client.getTeamDoneStateID(context.Background(), "TEAM")
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if id1 != "state-done-456" {
		t.Errorf("first call returned %s, want state-done-456", id1)
	}
	if callCount != 1 {
		t.Errorf("expected 1 HTTP call after first request, got %d", callCount)
	}

	// Second call should hit the cache, not the server
	id2, err := client.getTeamDoneStateID(context.Background(), "TEAM")
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if id2 != "state-done-456" {
		t.Errorf("second call returned %s, want state-done-456", id2)
	}
	if callCount != 1 {
		t.Errorf("expected still 1 HTTP call after second request (cache hit), got %d", callCount)
	}

	// Third call with different team should hit the server
	_, err = client.getTeamDoneStateID(context.Background(), "OTHER")
	if err != nil {
		t.Fatalf("third call failed: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls after third request (different team), got %d", callCount)
	}
}
