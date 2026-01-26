package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClient(t *testing.T) {
	client := NewClient("https://company.atlassian.net", "user@example.com", "api-token", PlatformCloud)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.baseURL != "https://company.atlassian.net" {
		t.Errorf("client.baseURL = %s, want https://company.atlassian.net", client.baseURL)
	}
	if client.platform != PlatformCloud {
		t.Errorf("client.platform = %s, want cloud", client.platform)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	client := NewClient("https://company.atlassian.net/", "user@example.com", "api-token", PlatformCloud)
	if client.baseURL != "https://company.atlassian.net" {
		t.Errorf("client.baseURL = %s, want https://company.atlassian.net (no trailing slash)", client.baseURL)
	}
}

func TestAPIPath(t *testing.T) {
	tests := []struct {
		platform string
		want     string
	}{
		{PlatformCloud, "/rest/api/3"},
		{PlatformServer, "/rest/api/2"},
	}

	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			client := NewClient("https://jira.example.com", "user", "token", tt.platform)
			got := client.apiPath()
			if got != tt.want {
				t.Errorf("apiPath() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGetIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.URL.Path != "/rest/api/3/issue/PROJ-42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}

		issue := Issue{
			ID:  "10001",
			Key: "PROJ-42",
			Fields: Fields{
				Summary:     "Test Issue",
				Description: "Issue description",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	// Test structure verification (actual API call would fail without proper URL injection)
	client := NewClient(server.URL, "user@example.com", "api-token", PlatformCloud)

	issue, err := client.GetIssue(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if issue.Key != "PROJ-42" {
		t.Errorf("issue.Key = %s, want PROJ-42", issue.Key)
	}
}

func TestAddComment_Cloud(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		// Cloud uses ADF format
		bodyContent, ok := body["body"].(map[string]interface{})
		if !ok {
			t.Error("expected ADF body format for Cloud")
		}
		if bodyContent["type"] != "doc" {
			t.Errorf("expected body type 'doc', got %v", bodyContent["type"])
		}

		comment := Comment{
			ID:   "10001",
			Body: "Test comment",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comment)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", "api-token", PlatformCloud)
	_, err := client.AddComment(context.Background(), "PROJ-42", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
}

func TestAddComment_Server(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/2/issue/PROJ-42/comment" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		// Server uses plain text
		if body["body"] != "Test comment" {
			t.Errorf("expected plain text body for Server, got %v", body)
		}

		comment := Comment{
			ID:   "10001",
			Body: "Test comment",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(comment)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "token", PlatformServer)
	_, err := client.AddComment(context.Background(), "PROJ-42", "Test comment")
	if err != nil {
		t.Fatalf("AddComment failed: %v", err)
	}
}

func TestGetTransitions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/transitions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		resp := TransitionsResponse{
			Transitions: []Transition{
				{ID: "21", Name: "Start Progress", To: Status{Name: "In Progress"}},
				{ID: "31", Name: "Done", To: Status{Name: "Done"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", "api-token", PlatformCloud)
	transitions, err := client.GetTransitions(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("GetTransitions failed: %v", err)
	}
	if len(transitions) != 2 {
		t.Errorf("expected 2 transitions, got %d", len(transitions))
	}
}

func TestTransitionIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		transition := body["transition"].(map[string]interface{})
		if transition["id"] != "21" {
			t.Errorf("expected transition id '21', got %v", transition["id"])
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", "api-token", PlatformCloud)
	err := client.TransitionIssue(context.Background(), "PROJ-42", "21")
	if err != nil {
		t.Fatalf("TransitionIssue failed: %v", err)
	}
}

func TestAddPRLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/3/issue/PROJ-42/remotelink" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body RemoteLink
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		if body.Object.URL != "https://github.com/owner/repo/pull/123" {
			t.Errorf("unexpected PR URL: %s", body.Object.URL)
		}
		if body.Object.Title != "PR #123" {
			t.Errorf("unexpected PR title: %s", body.Object.Title)
		}

		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user@example.com", "api-token", PlatformCloud)
	err := client.AddPRLink(context.Background(), "PROJ-42", "https://github.com/owner/repo/pull/123", "PR #123")
	if err != nil {
		t.Fatalf("AddPRLink failed: %v", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled != false {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}
	if cfg.Platform != "cloud" {
		t.Errorf("default Platform = %s, want 'cloud'", cfg.Platform)
	}
	if cfg.PilotLabel != "pilot" {
		t.Errorf("default PilotLabel = %s, want 'pilot'", cfg.PilotLabel)
	}
}

func TestPriorityFromJira(t *testing.T) {
	tests := []struct {
		name string
		want Priority
	}{
		{"Highest", PriorityHighest},
		{"Blocker", PriorityHighest},
		{"Critical", PriorityHighest},
		{"High", PriorityHigh},
		{"Major", PriorityHigh},
		{"Medium", PriorityMedium},
		{"Low", PriorityLow},
		{"Minor", PriorityLow},
		{"Lowest", PriorityLowest},
		{"Trivial", PriorityLowest},
		{"Unknown", PriorityNone},
		{"", PriorityNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriorityFromJira(tt.name)
			if got != tt.want {
				t.Errorf("PriorityFromJira(%s) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestDoRequest_ErrorHandling(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   `{"id": "1"}`,
			wantErr:    false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   `{"errorMessages": ["Issue Does Not Exist"]}`,
			wantErr:    true,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   `{"errorMessages": ["Unauthorized"]}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClient(server.URL, "user", "token", PlatformCloud)
			_, err := client.GetIssue(context.Background(), "TEST-1")

			if tt.wantErr && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Integration test helper - verifies client can be created and method signatures are correct
func TestClientMethodSignatures(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	ctx := context.Background()

	// These won't actually work without a real API, but verify the signatures compile
	var err error

	// GetIssue
	_, err = client.GetIssue(ctx, "PROJ-1")
	_ = err

	// AddComment
	_, err = client.AddComment(ctx, "PROJ-1", "comment")
	_ = err

	// GetTransitions
	_, err = client.GetTransitions(ctx, "PROJ-1")
	_ = err

	// TransitionIssue
	err = client.TransitionIssue(ctx, "PROJ-1", "21")
	_ = err

	// TransitionIssueTo
	err = client.TransitionIssueTo(ctx, "PROJ-1", "In Progress")
	_ = err

	// AddRemoteLink
	err = client.AddRemoteLink(ctx, "PROJ-1", &RemoteLink{})
	_ = err

	// AddPRLink
	err = client.AddPRLink(ctx, "PROJ-1", "https://github.com/owner/repo/pull/1", "PR #1")
	_ = err

	// GetProject
	_, err = client.GetProject(ctx, "PROJ")
	_ = err
}
