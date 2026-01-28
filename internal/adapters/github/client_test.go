package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.token != testutil.FakeGitHubToken {
		t.Errorf("client.token = %s, want %s", client.token, testutil.FakeGitHubToken)
	}
	if client.baseURL != githubAPIURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, githubAPIURL)
	}
}

func TestNewClientWithBaseURL(t *testing.T) {
	customURL := "https://custom.api.example.com"
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, customURL)
	if client == nil {
		t.Fatal("NewClientWithBaseURL returned nil")
	}
	if client.token != testutil.FakeGitHubToken {
		t.Errorf("client.token = %s, want %s", client.token, testutil.FakeGitHubToken)
	}
	if client.baseURL != customURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, customURL)
	}
}

func TestGetIssue(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response: Issue{
				Number:  42,
				Title:   "Test Issue",
				Body:    "Issue body",
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/issues/42",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   map[string]string{"message": "Bad credentials"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/issues/42" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Errorf("unexpected Accept header: %s", r.Header.Get("Accept"))
				}

				w.WriteHeader(tt.statusCode)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			issue, err := client.GetIssue(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetIssue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && issue.Number != 42 {
				t.Errorf("issue.Number = %d, want 42", issue.Number)
			}
		})
	}
}

func TestAddComment(t *testing.T) {
	tests := []struct {
		name        string
		commentBody string
		statusCode  int
		wantErr     bool
	}{
		{
			name:        "success",
			commentBody: "Test comment",
			statusCode:  http.StatusCreated,
			wantErr:     false,
		},
		{
			name:        "server error",
			commentBody: "Test comment",
			statusCode:  http.StatusInternalServerError,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/issues/42/comments" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["body"] != tt.commentBody {
					t.Errorf("unexpected comment body: %s", body["body"])
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					comment := Comment{
						ID:   123,
						Body: tt.commentBody,
					}
					_ = json.NewEncoder(w).Encode(comment)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			comment, err := client.AddComment(context.Background(), "owner", "repo", 42, tt.commentBody)

			if (err != nil) != tt.wantErr {
				t.Errorf("AddComment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && comment.ID != 123 {
				t.Errorf("comment.ID = %d, want 123", comment.ID)
			}
		})
	}
}

func TestAddLabels(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success - single label",
			labels:     []string{"bug"},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "success - multiple labels",
			labels:     []string{"bug", "pilot", "high-priority"},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
			labels:     []string{"bug"},
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				var body map[string][]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if len(body["labels"]) != len(tt.labels) {
					t.Errorf("expected %d labels, got %d", len(tt.labels), len(body["labels"]))
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.AddLabels(context.Background(), "owner", "repo", 42, tt.labels)

			if (err != nil) != tt.wantErr {
				t.Errorf("AddLabels() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRemoveLabel(t *testing.T) {
	tests := []struct {
		name       string
		label      string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			label:      "bug",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found (OK - label might not exist)",
			label:      "nonexistent",
			statusCode: http.StatusNotFound,
			wantErr:    false, // 404 is OK for RemoveLabel
		},
		{
			name:       "server error",
			label:      "bug",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete {
					t.Errorf("expected DELETE, got %s", r.Method)
				}
				expectedPath := "/repos/owner/repo/issues/42/labels/" + tt.label
				if r.URL.Path != expectedPath {
					t.Errorf("unexpected path: %s, want %s", r.URL.Path, expectedPath)
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.RemoveLabel(context.Background(), "owner", "repo", 42, tt.label)

			if (err != nil) != tt.wantErr {
				t.Errorf("RemoveLabel() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateIssueState(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "close issue",
			state:      "closed",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "reopen issue",
			state:      "open",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
			state:      "closed",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch {
					t.Errorf("expected PATCH, got %s", r.Method)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["state"] != tt.state {
					t.Errorf("expected state %s, got %s", tt.state, body["state"])
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.UpdateIssueState(context.Background(), "owner", "repo", 42, tt.state)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateIssueState() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetRepository(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response: Repository{
				ID:       12345,
				Name:     "repo",
				FullName: "owner/repo",
				Owner:    User{Login: "owner"},
				CloneURL: "https://github.com/owner/repo.git",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			repo, err := client.GetRepository(context.Background(), "owner", "repo")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetRepository() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && repo.Name != "repo" {
				t.Errorf("repo.Name = %s, want repo", repo.Name)
			}
		})
	}
}

func TestCreateCommitStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     *CommitStatus
		statusCode int
		wantErr    bool
	}{
		{
			name: "success - pending",
			status: &CommitStatus{
				State:       StatusPending,
				Context:     "pilot/execution",
				Description: "Running...",
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "success - success with URL",
			status: &CommitStatus{
				State:       StatusSuccess,
				Context:     "pilot/execution",
				Description: "Completed",
				TargetURL:   "https://example.com/logs/123",
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "not found",
			status: &CommitStatus{
				State:   StatusPending,
				Context: "pilot/execution",
			},
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/statuses/abc123def" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body CommitStatus
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body.State != tt.status.State {
					t.Errorf("unexpected state: %s", body.State)
				}
				if body.Context != tt.status.Context {
					t.Errorf("unexpected context: %s", body.Context)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := CommitStatus{
						ID:          12345,
						State:       body.State,
						Context:     body.Context,
						Description: body.Description,
						TargetURL:   body.TargetURL,
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.CreateCommitStatus(context.Background(), "owner", "repo", "abc123def", tt.status)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateCommitStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.ID != 12345 {
				t.Errorf("result.ID = %d, want 12345", result.ID)
			}
		})
	}
}

func TestCreateCheckRun(t *testing.T) {
	tests := []struct {
		name       string
		checkRun   *CheckRun
		statusCode int
		wantErr    bool
	}{
		{
			name: "success - queued",
			checkRun: &CheckRun{
				HeadSHA: "abc123def456",
				Name:    "Pilot Execution",
				Status:  CheckRunQueued,
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "success - in progress with output",
			checkRun: &CheckRun{
				HeadSHA: "abc123def456",
				Name:    "Pilot Execution",
				Status:  CheckRunInProgress,
				Output: &CheckOutput{
					Title:   "Running tests",
					Summary: "Currently executing test suite",
				},
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "error - bad request",
			checkRun: &CheckRun{
				HeadSHA: "",
				Name:    "Pilot Execution",
			},
			statusCode: http.StatusUnprocessableEntity,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/check-runs" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := CheckRun{
						ID:      67890,
						HeadSHA: tt.checkRun.HeadSHA,
						Name:    tt.checkRun.Name,
						Status:  tt.checkRun.Status,
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.CreateCheckRun(context.Background(), "owner", "repo", tt.checkRun)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateCheckRun() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.ID != 67890 {
				t.Errorf("result.ID = %d, want 67890", result.ID)
			}
		})
	}
}

func TestUpdateCheckRun(t *testing.T) {
	tests := []struct {
		name       string
		checkRunID int64
		checkRun   *CheckRun
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success - complete with success",
			checkRunID: 67890,
			checkRun: &CheckRun{
				Status:     CheckRunCompleted,
				Conclusion: ConclusionSuccess,
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "success - complete with failure",
			checkRunID: 67890,
			checkRun: &CheckRun{
				Status:     CheckRunCompleted,
				Conclusion: ConclusionFailure,
				Output: &CheckOutput{
					Title:   "Tests failed",
					Summary: "3 tests failed",
				},
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
			checkRunID: 99999,
			checkRun: &CheckRun{
				Status:     CheckRunCompleted,
				Conclusion: ConclusionSuccess,
			},
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch {
					t.Errorf("expected PATCH, got %s", r.Method)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := CheckRun{
						ID:         tt.checkRunID,
						Status:     tt.checkRun.Status,
						Conclusion: tt.checkRun.Conclusion,
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.UpdateCheckRun(context.Background(), "owner", "repo", tt.checkRunID, tt.checkRun)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateCheckRun() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.Status != tt.checkRun.Status {
				t.Errorf("result.Status = %s, want %s", result.Status, tt.checkRun.Status)
			}
		})
	}
}

func TestCreatePullRequest(t *testing.T) {
	tests := []struct {
		name       string
		input      *PullRequestInput
		statusCode int
		wantErr    bool
	}{
		{
			name: "success",
			input: &PullRequestInput{
				Title: "Add new feature",
				Body:  "This PR adds a new feature",
				Head:  "feature/new-feature",
				Base:  "main",
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "success - draft PR",
			input: &PullRequestInput{
				Title: "WIP: New feature",
				Head:  "feature/wip",
				Base:  "main",
				Draft: true,
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "unprocessable entity - branch doesn't exist",
			input: &PullRequestInput{
				Title: "Add new feature",
				Head:  "nonexistent-branch",
				Base:  "main",
			},
			statusCode: http.StatusUnprocessableEntity,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/pulls" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body PullRequestInput
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body.Title != tt.input.Title {
					t.Errorf("unexpected title: %s", body.Title)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := PullRequest{
						ID:      11111,
						Number:  42,
						Title:   body.Title,
						Head:    body.Head,
						Base:    body.Base,
						State:   "open",
						HTMLURL: "https://github.com/owner/repo/pull/42",
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.CreatePullRequest(context.Background(), "owner", "repo", tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreatePullRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.Number != 42 {
				t.Errorf("result.Number = %d, want 42", result.Number)
			}
		})
	}
}

func TestGetPullRequest(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response: PullRequest{
				ID:      11111,
				Number:  42,
				Title:   "Test PR",
				Head:    "feature-branch",
				Base:    "main",
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/42",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/pulls/42" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.GetPullRequest(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetPullRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.Number != 42 {
				t.Errorf("result.Number = %d, want 42", result.Number)
			}
		})
	}
}

func TestAddPRComment(t *testing.T) {
	tests := []struct {
		name        string
		commentBody string
		statusCode  int
		wantErr     bool
	}{
		{
			name:        "success",
			commentBody: "This is a PR comment",
			statusCode:  http.StatusCreated,
			wantErr:     false,
		},
		{
			name:        "not found",
			commentBody: "Comment on nonexistent PR",
			statusCode:  http.StatusNotFound,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				// PR comments go through the issues API
				if r.URL.Path != "/repos/owner/repo/issues/42/comments" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["body"] != tt.commentBody {
					t.Errorf("unexpected comment body: %s", body["body"])
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := PRComment{
						ID:      22222,
						Body:    body["body"],
						HTMLURL: "https://github.com/owner/repo/issues/42#issuecomment-22222",
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.AddPRComment(context.Background(), "owner", "repo", 42, tt.commentBody)

			if (err != nil) != tt.wantErr {
				t.Errorf("AddPRComment() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.ID != 22222 {
				t.Errorf("result.ID = %d, want 22222", result.ID)
			}
		})
	}
}

func TestListIssues(t *testing.T) {
	tests := []struct {
		name       string
		opts       *ListIssuesOptions
		statusCode int
		response   interface{}
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "success - no options",
			opts:       nil,
			statusCode: http.StatusOK,
			response: []*Issue{
				{Number: 1, Title: "Issue 1"},
				{Number: 2, Title: "Issue 2"},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name: "success - with labels",
			opts: &ListIssuesOptions{
				Labels: []string{"pilot", "bug"},
				State:  StateOpen,
			},
			statusCode: http.StatusOK,
			response: []*Issue{
				{Number: 1, Title: "Issue 1"},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name: "success - with since",
			opts: &ListIssuesOptions{
				Since: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				Sort:  "updated",
			},
			statusCode: http.StatusOK,
			response:   []*Issue{},
			wantErr:    false,
			wantCount:  0,
		},
		{
			name:       "unauthorized",
			opts:       nil,
			statusCode: http.StatusUnauthorized,
			response:   map[string]string{"message": "Bad credentials"},
			wantErr:    true,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				// Note: Query params are appended to path in this implementation
				// so we just verify the request was made to the correct base path

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			issues, err := client.ListIssues(context.Background(), "owner", "repo", tt.opts)

			if (err != nil) != tt.wantErr {
				t.Errorf("ListIssues() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(issues) != tt.wantCount {
				t.Errorf("ListIssues() returned %d issues, want %d", len(issues), tt.wantCount)
			}
		})
	}
}

func TestHasLabel(t *testing.T) {
	tests := []struct {
		name      string
		issue     *Issue
		labelName string
		want      bool
	}{
		{
			name: "has label - first",
			issue: &Issue{
				Labels: []Label{
					{Name: "pilot"},
					{Name: "bug"},
				},
			},
			labelName: "pilot",
			want:      true,
		},
		{
			name: "has label - last",
			issue: &Issue{
				Labels: []Label{
					{Name: "bug"},
					{Name: "enhancement"},
					{Name: "pilot"},
				},
			},
			labelName: "pilot",
			want:      true,
		},
		{
			name: "does not have label",
			issue: &Issue{
				Labels: []Label{
					{Name: "bug"},
					{Name: "enhancement"},
				},
			},
			labelName: "pilot",
			want:      false,
		},
		{
			name: "empty labels",
			issue: &Issue{
				Labels: []Label{},
			},
			labelName: "pilot",
			want:      false,
		},
		{
			name:      "nil labels",
			issue:     &Issue{},
			labelName: "pilot",
			want:      false,
		},
		{
			name: "case sensitive",
			issue: &Issue{
				Labels: []Label{
					{Name: "Pilot"},
				},
			},
			labelName: "pilot",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasLabel(tt.issue, tt.labelName)
			if got != tt.want {
				t.Errorf("HasLabel() = %v, want %v", got, tt.want)
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
		errMsg     string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   `{"id": 1}`,
			wantErr:    false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   `{"message": "Not Found"}`,
			wantErr:    true,
			errMsg:     "API error (status 404)",
		},
		{
			name:       "unauthorized",
			statusCode: http.StatusUnauthorized,
			response:   `{"message": "Bad credentials"}`,
			wantErr:    true,
			errMsg:     "API error (status 401)",
		},
		{
			name:       "rate limited",
			statusCode: http.StatusForbidden,
			response:   `{"message": "API rate limit exceeded"}`,
			wantErr:    true,
			errMsg:     "API error (status 403)",
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   `{"message": "Internal server error"}`,
			wantErr:    true,
			errMsg:     "API error (status 500)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			_, err := client.GetIssue(context.Background(), "owner", "repo", 1)

			if (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error = %v, want to contain %s", err, tt.errMsg)
			}
		})
	}
}

func TestDoRequest_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_, err := client.GetIssue(context.Background(), "owner", "repo", 1)

	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %v, want to contain 'failed to parse response'", err)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled != false {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}

	if cfg.PilotLabel != "pilot" {
		t.Errorf("default PilotLabel = %s, want 'pilot'", cfg.PilotLabel)
	}

	if cfg.Polling == nil {
		t.Fatal("default Polling config is nil")
	}

	if cfg.Polling.Enabled != false {
		t.Errorf("default Polling.Enabled = %v, want false", cfg.Polling.Enabled)
	}

	if cfg.Polling.Interval != 30*time.Second {
		t.Errorf("default Polling.Interval = %v, want 30s", cfg.Polling.Interval)
	}

	if cfg.Polling.Label != "pilot" {
		t.Errorf("default Polling.Label = %s, want 'pilot'", cfg.Polling.Label)
	}
}

func TestPriorityFromLabel(t *testing.T) {
	tests := []struct {
		label string
		want  Priority
	}{
		{"priority:urgent", PriorityUrgent},
		{"P0", PriorityUrgent},
		{"priority:high", PriorityHigh},
		{"P1", PriorityHigh},
		{"priority:medium", PriorityMedium},
		{"P2", PriorityMedium},
		{"priority:low", PriorityLow},
		{"P3", PriorityLow},
		{"bug", PriorityNone},
		{"", PriorityNone},
		{"random-label", PriorityNone},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := PriorityFromLabel(tt.label)
			if got != tt.want {
				t.Errorf("PriorityFromLabel(%s) = %d, want %d", tt.label, got, tt.want)
			}
		})
	}
}

func TestCommitStatusConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{StatusPending, "pending"},
		{StatusSuccess, "success"},
		{StatusFailure, "failure"},
		{StatusError, "error"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}

func TestCheckRunConstants(t *testing.T) {
	statusTests := []struct {
		constant string
		expected string
	}{
		{CheckRunQueued, "queued"},
		{CheckRunInProgress, "in_progress"},
		{CheckRunCompleted, "completed"},
	}

	for _, tt := range statusTests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}

	conclusionTests := []struct {
		constant string
		expected string
	}{
		{ConclusionSuccess, "success"},
		{ConclusionFailure, "failure"},
		{ConclusionNeutral, "neutral"},
		{ConclusionCancelled, "cancelled"},
		{ConclusionTimedOut, "timed_out"},
		{ConclusionActionRequired, "action_required"},
		{ConclusionSkipped, "skipped"},
	}

	for _, tt := range conclusionTests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}

func TestStateConstants(t *testing.T) {
	if StateOpen != "open" {
		t.Errorf("StateOpen = %s, want 'open'", StateOpen)
	}
	if StateClosed != "closed" {
		t.Errorf("StateClosed = %s, want 'closed'", StateClosed)
	}
}

func TestLabelConstants(t *testing.T) {
	if LabelInProgress != "pilot-in-progress" {
		t.Errorf("LabelInProgress = %s, want 'pilot-in-progress'", LabelInProgress)
	}
	if LabelDone != "pilot-done" {
		t.Errorf("LabelDone = %s, want 'pilot-done'", LabelDone)
	}
	if LabelFailed != "pilot-failed" {
		t.Errorf("LabelFailed = %s, want 'pilot-failed'", LabelFailed)
	}
}
