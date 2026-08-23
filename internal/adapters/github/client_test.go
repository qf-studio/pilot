package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if tok, err := client.resolveToken(context.Background()); err != nil || tok != testutil.FakeGitHubToken {
		t.Errorf("resolveToken() = (%s, %v), want (%s, nil)", tok, err, testutil.FakeGitHubToken)
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
	if tok, err := client.resolveToken(context.Background()); err != nil || tok != testutil.FakeGitHubToken {
		t.Errorf("resolveToken() = (%s, %v), want (%s, nil)", tok, err, testutil.FakeGitHubToken)
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
				// Labels are normalized to lowercase in URL path
				expectedPath := "/repos/owner/repo/issues/42/labels/" + strings.ToLower(tt.label)
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

func TestRemoveLabel_NormalizesToLowercase(t *testing.T) {
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_ = client.RemoveLabel(context.Background(), "owner", "repo", 42, "Pilot-Failed")

	expectedPath := "/repos/owner/repo/issues/42/labels/pilot-failed"
	if receivedPath != expectedPath {
		t.Errorf("expected label to be lowercased in path, got: %s, want: %s", receivedPath, expectedPath)
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
						Head:    PRRef{Ref: body.Head, SHA: "abc123"},
						Base:    PRRef{Ref: body.Base, SHA: "def456"},
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
				Head:    PRRef{Ref: "feature-branch", SHA: "abc123"},
				Base:    PRRef{Ref: "main", SHA: "def456"},
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

func TestClosePullRequest(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
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
				if r.URL.Path != "/repos/owner/repo/pulls/42" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.ClosePullRequest(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("ClosePullRequest() error = %v, wantErr %v", err, tt.wantErr)
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
				{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}, {Name: "bug"}}},
				{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}}, // missing bug, won't match
			},
			wantErr:   false,
			wantCount: 1, // Only issue 1 has both labels
		},
		{
			name: "labels filtered case-insensitively in code",
			opts: &ListIssuesOptions{
				Labels: []string{"pilot"},
				State:  StateOpen,
			},
			statusCode: http.StatusOK,
			response: []*Issue{
				{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "Pilot"}}}, // uppercase
				{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "bug"}}},   // no match
			},
			wantErr:   false,
			wantCount: 1, // Only issue 1 matches (Pilot == pilot case-insensitive)
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

func TestListIssues_LabelsFilteredCaseInsensitively(t *testing.T) {
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.WriteHeader(http.StatusOK)
		// Return issues with various label cases
		_ = json.NewEncoder(w).Encode([]*Issue{
			{Number: 1, Title: "Has pilot lowercase", Labels: []Label{{Name: "pilot"}}},
			{Number: 2, Title: "Has Pilot uppercase", Labels: []Label{{Name: "Pilot"}}},
			{Number: 3, Title: "Has PILOT all caps", Labels: []Label{{Name: "PILOT"}}},
			{Number: 4, Title: "No pilot label", Labels: []Label{{Name: "bug"}}},
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	issues, err := client.ListIssues(context.Background(), "owner", "repo", &ListIssuesOptions{
		Labels: []string{"Pilot"}, // Request with mixed case
	})
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}

	// Verify labels are NOT passed to API (filtered in code instead)
	if strings.Contains(receivedPath, "labels=") {
		t.Errorf("labels should not be in API query (filtered in code), got: %s", receivedPath)
	}

	// Should return 3 issues (all with pilot/Pilot/PILOT label, excluding the one with only "bug")
	if len(issues) != 3 {
		t.Errorf("expected 3 issues with pilot label (case-insensitive), got %d", len(issues))
	}

	// Verify the correct issues were returned
	for _, issue := range issues {
		if issue.Number == 4 {
			t.Errorf("issue #4 should not be returned (has 'bug' not 'pilot')")
		}
	}
}

// TestListIssues_Paginates verifies C6 (TASK-346): ListIssues requests per_page=100
// and follows pages until a short page, returning the full set (not just the first ~30).
func TestListIssues_Paginates(t *testing.T) {
	var perPageSeen string
	var pageRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageRequests++
		perPageSeen = r.URL.Query().Get("per_page")
		var batch []*Issue
		switch r.URL.Query().Get("page") {
		case "1":
			batch = make([]*Issue, 100) // full page → keep paging
			for i := range batch {
				batch[i] = &Issue{Number: i + 1}
			}
		case "2":
			batch = make([]*Issue, 15) // short page → stop
			for i := range batch {
				batch[i] = &Issue{Number: 101 + i}
			}
		default:
			batch = []*Issue{}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(batch)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	issues, err := client.ListIssues(context.Background(), "owner", "repo", nil)
	if err != nil {
		t.Fatalf("ListIssues() error = %v", err)
	}
	if len(issues) != 115 {
		t.Errorf("expected 115 issues across 2 pages, got %d", len(issues))
	}
	if perPageSeen != "100" {
		t.Errorf("expected per_page=100, got %q", perPageSeen)
	}
	if pageRequests != 2 {
		t.Errorf("expected 2 page requests (100 + short 15 stops paging), got %d", pageRequests)
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
			name: "case insensitive",
			issue: &Issue{
				Labels: []Label{
					{Name: "Pilot"},
				},
			},
			labelName: "pilot",
			want:      true,
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
	// GH-1015: Added pilot-retry-ready for PRs closed without merge
	if LabelRetryReady != "pilot-retry-ready" {
		t.Errorf("LabelRetryReady = %s, want 'pilot-retry-ready'", LabelRetryReady)
	}
}

func TestMergePullRequest(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		commitTitle string
		statusCode  int
		wantErr     bool
	}{
		{
			name:        "success - squash merge",
			method:      MergeMethodSquash,
			commitTitle: "feat: add new feature (#42)",
			statusCode:  http.StatusOK,
			wantErr:     false,
		},
		{
			name:        "success - merge commit",
			method:      MergeMethodMerge,
			commitTitle: "",
			statusCode:  http.StatusOK,
			wantErr:     false,
		},
		{
			name:        "success - rebase",
			method:      MergeMethodRebase,
			commitTitle: "",
			statusCode:  http.StatusOK,
			wantErr:     false,
		},
		{
			name:        "not mergeable - conflicts",
			method:      MergeMethodSquash,
			commitTitle: "",
			statusCode:  http.StatusMethodNotAllowed,
			wantErr:     true,
		},
		{
			name:        "not found",
			method:      MergeMethodSquash,
			commitTitle: "",
			statusCode:  http.StatusNotFound,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("expected PUT, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/pulls/42/merge" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["merge_method"] != tt.method {
					t.Errorf("unexpected merge_method: %s, want %s", body["merge_method"], tt.method)
				}
				if tt.commitTitle != "" && body["commit_title"] != tt.commitTitle {
					t.Errorf("unexpected commit_title: %s, want %s", body["commit_title"], tt.commitTitle)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					_, _ = w.Write([]byte(`{"sha": "abc123", "merged": true}`))
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.MergePullRequest(context.Background(), "owner", "repo", 42, tt.method, tt.commitTitle)

			if (err != nil) != tt.wantErr {
				t.Errorf("MergePullRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGetCombinedStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
		wantState  string
	}{
		{
			name:       "success - all passing",
			statusCode: http.StatusOK,
			response: CombinedStatus{
				State:      StatusSuccess,
				SHA:        "abc123def456",
				TotalCount: 2,
				Statuses: []CommitStatus{
					{Context: "ci/build", State: StatusSuccess},
					{Context: "ci/test", State: StatusSuccess},
				},
			},
			wantErr:   false,
			wantState: StatusSuccess,
		},
		{
			name:       "success - pending",
			statusCode: http.StatusOK,
			response: CombinedStatus{
				State:      StatusPending,
				SHA:        "abc123def456",
				TotalCount: 1,
				Statuses: []CommitStatus{
					{Context: "ci/build", State: StatusPending},
				},
			},
			wantErr:   false,
			wantState: StatusPending,
		},
		{
			name:       "success - failure",
			statusCode: http.StatusOK,
			response: CombinedStatus{
				State:      StatusFailure,
				SHA:        "abc123def456",
				TotalCount: 2,
				Statuses: []CommitStatus{
					{Context: "ci/build", State: StatusSuccess},
					{Context: "ci/test", State: StatusFailure},
				},
			},
			wantErr:   false,
			wantState: StatusFailure,
		},
		{
			name:       "success - no statuses",
			statusCode: http.StatusOK,
			response: CombinedStatus{
				State:      StatusPending,
				SHA:        "abc123def456",
				TotalCount: 0,
				Statuses:   []CommitStatus{},
			},
			wantErr:   false,
			wantState: StatusPending,
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
				if r.URL.Path != "/repos/owner/repo/commits/abc123def456/status" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			status, err := client.GetCombinedStatus(context.Background(), "owner", "repo", "abc123def456")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetCombinedStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && status.State != tt.wantState {
				t.Errorf("status.State = %s, want %s", status.State, tt.wantState)
			}
		})
	}
}

func TestListCheckRuns(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "success - multiple check runs",
			statusCode: http.StatusOK,
			response: CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []CheckRun{
					{ID: 1, Name: "build", Status: CheckRunCompleted, Conclusion: ConclusionSuccess},
					{ID: 2, Name: "test", Status: CheckRunCompleted, Conclusion: ConclusionSuccess},
					{ID: 3, Name: "lint", Status: CheckRunCompleted, Conclusion: ConclusionSuccess},
				},
			},
			wantErr:   false,
			wantCount: 3,
		},
		{
			name:       "success - in progress",
			statusCode: http.StatusOK,
			response: CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []CheckRun{
					{ID: 1, Name: "build", Status: CheckRunCompleted, Conclusion: ConclusionSuccess},
					{ID: 2, Name: "test", Status: CheckRunInProgress},
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "success - no check runs",
			statusCode: http.StatusOK,
			response: CheckRunsResponse{
				TotalCount: 0,
				CheckRuns:  []CheckRun{},
			},
			wantErr:   false,
			wantCount: 0,
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
				if r.URL.Path != "/repos/owner/repo/commits/abc123def456/check-runs" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Header.Get("Accept") != "application/vnd.github+json" {
					t.Errorf("unexpected Accept header: %s", r.Header.Get("Accept"))
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			result, err := client.ListCheckRuns(context.Background(), "owner", "repo", "abc123def456")

			if (err != nil) != tt.wantErr {
				t.Errorf("ListCheckRuns() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.TotalCount != tt.wantCount {
				t.Errorf("result.TotalCount = %d, want %d", result.TotalCount, tt.wantCount)
			}
		})
	}
}

func TestApprovePullRequest(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success - with comment",
			body:       "LGTM! Great work.",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "success - without comment",
			body:       "",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
			body:       "",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
		{
			name:       "forbidden - no permission",
			body:       "",
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
		{
			name:       "conflict - already reviewed",
			body:       "",
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
				if r.URL.Path != "/repos/owner/repo/pulls/42/reviews" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["event"] != ReviewEventApprove {
					t.Errorf("unexpected event: %s, want %s", body["event"], ReviewEventApprove)
				}
				if tt.body != "" && body["body"] != tt.body {
					t.Errorf("unexpected body: %s, want %s", body["body"], tt.body)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					_, _ = w.Write([]byte(`{"id": 123, "state": "APPROVED"}`))
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.ApprovePullRequest(context.Background(), "owner", "repo", 42, tt.body)

			if (err != nil) != tt.wantErr {
				t.Errorf("ApprovePullRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMergeMethodConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{MergeMethodMerge, "merge"},
		{MergeMethodSquash, "squash"},
		{MergeMethodRebase, "rebase"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}

func TestReviewEventConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{ReviewEventApprove, "APPROVE"},
		{ReviewEventRequestChanges, "REQUEST_CHANGES"},
		{ReviewEventComment, "COMMENT"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}

func TestReviewStateConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{ReviewStateApproved, "APPROVED"},
		{ReviewStateChangesRequested, "CHANGES_REQUESTED"},
		{ReviewStateCommented, "COMMENTED"},
		{ReviewStateDismissed, "DISMISSED"},
		{ReviewStatePending, "PENDING"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}

func TestListPullRequestReviews(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "success - multiple reviews",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateApproved},
				{ID: 2, User: User{Login: "bob"}, State: ReviewStateCommented},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "success - no reviews",
			statusCode: http.StatusOK,
			response:   []*PullRequestReview{},
			wantErr:    false,
			wantCount:  0,
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
				if r.URL.Path != "/repos/owner/repo/pulls/42/reviews" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			reviews, err := client.ListPullRequestReviews(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("ListPullRequestReviews() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(reviews) != tt.wantCount {
				t.Errorf("ListPullRequestReviews() returned %d reviews, want %d", len(reviews), tt.wantCount)
			}
		})
	}
}

func TestHasApprovalReview(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		response     interface{}
		wantApproved bool
		wantApprover string
		wantErr      bool
	}{
		{
			name:       "approved - single review",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateApproved},
			},
			wantApproved: true,
			wantApprover: "alice",
			wantErr:      false,
		},
		{
			name:       "approved - multiple reviews from same user, latest is approval",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateChangesRequested},
				{ID: 2, User: User{Login: "alice"}, State: ReviewStateApproved},
			},
			wantApproved: true,
			wantApprover: "alice",
			wantErr:      false,
		},
		{
			name:       "not approved - changes requested after approval",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateApproved},
				{ID: 2, User: User{Login: "alice"}, State: ReviewStateChangesRequested},
			},
			wantApproved: false,
			wantApprover: "",
			wantErr:      false,
		},
		{
			name:       "approved - one approves, one requests changes",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateApproved},
				{ID: 2, User: User{Login: "bob"}, State: ReviewStateChangesRequested},
			},
			wantApproved: true,
			wantApprover: "alice",
			wantErr:      false,
		},
		{
			name:       "not approved - only comments",
			statusCode: http.StatusOK,
			response: []*PullRequestReview{
				{ID: 1, User: User{Login: "alice"}, State: ReviewStateCommented},
			},
			wantApproved: false,
			wantApprover: "",
			wantErr:      false,
		},
		{
			name:         "not approved - no reviews",
			statusCode:   http.StatusOK,
			response:     []*PullRequestReview{},
			wantApproved: false,
			wantApprover: "",
			wantErr:      false,
		},
		{
			name:       "error - not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			approved, approver, err := client.HasApprovalReview(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("HasApprovalReview() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if approved != tt.wantApproved {
					t.Errorf("HasApprovalReview() approved = %v, want %v", approved, tt.wantApproved)
				}
				if approved && approver != tt.wantApprover {
					t.Errorf("HasApprovalReview() approver = %s, want %s", approver, tt.wantApprover)
				}
			}
		})
	}
}

func TestRequestReviewers(t *testing.T) {
	tests := []struct {
		name          string
		reviewers     []string
		teamReviewers []string
		statusCode    int
		wantErr       bool
		wantSkipped   bool // expect no HTTP call
	}{
		{
			name:       "success - individual reviewers",
			reviewers:  []string{"alice", "bob"},
			statusCode: http.StatusCreated,
		},
		{
			name:          "success - team reviewers",
			teamReviewers: []string{"backend-team"},
			statusCode:    http.StatusCreated,
		},
		{
			name:          "success - both individual and team",
			reviewers:     []string{"alice"},
			teamReviewers: []string{"frontend-team"},
			statusCode:    http.StatusCreated,
		},
		{
			name:        "skip - no reviewers",
			wantSkipped: true,
		},
		{
			name:       "error - not found",
			reviewers:  []string{"nonexistent"},
			statusCode: http.StatusUnprocessableEntity,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/requested_reviewers") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string][]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode request body: %v", err)
				}
				if len(tt.reviewers) > 0 {
					if len(body["reviewers"]) != len(tt.reviewers) {
						t.Errorf("reviewers = %v, want %v", body["reviewers"], tt.reviewers)
					}
				}
				if len(tt.teamReviewers) > 0 {
					if len(body["team_reviewers"]) != len(tt.teamReviewers) {
						t.Errorf("team_reviewers = %v, want %v", body["team_reviewers"], tt.teamReviewers)
					}
				}

				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.RequestReviewers(context.Background(), "owner", "repo", 42, tt.reviewers, tt.teamReviewers)

			if (err != nil) != tt.wantErr {
				t.Errorf("RequestReviewers() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantSkipped && called {
				t.Error("RequestReviewers() should not make HTTP call when no reviewers specified")
			}
			if !tt.wantSkipped && !tt.wantErr && !called {
				t.Error("RequestReviewers() should have made HTTP call")
			}
		})
	}
}

func TestListPullRequests(t *testing.T) {
	tests := []struct {
		name       string
		state      string
		statusCode int
		response   interface{}
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "success - open PRs",
			state:      "open",
			statusCode: http.StatusOK,
			response: []*PullRequest{
				{
					Number:  1,
					Title:   "PR 1",
					Head:    PRRef{Ref: "pilot/GH-100", SHA: "abc123"},
					Base:    PRRef{Ref: "main", SHA: "def456"},
					State:   "open",
					HTMLURL: "https://github.com/owner/repo/pull/1",
				},
				{
					Number:  2,
					Title:   "PR 2",
					Head:    PRRef{Ref: "pilot/GH-101", SHA: "xyz789"},
					Base:    PRRef{Ref: "main", SHA: "def456"},
					State:   "open",
					HTMLURL: "https://github.com/owner/repo/pull/2",
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "success - no PRs",
			state:      "open",
			statusCode: http.StatusOK,
			response:   []*PullRequest{},
			wantErr:    false,
			wantCount:  0,
		},
		{
			name:       "success - closed PRs",
			state:      "closed",
			statusCode: http.StatusOK,
			response: []*PullRequest{
				{
					Number: 3,
					Title:  "Closed PR",
					Head:   PRRef{Ref: "feature/old", SHA: "old123"},
					Base:   PRRef{Ref: "main", SHA: "def456"},
					State:  "closed",
				},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "unauthorized",
			state:      "open",
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
				expectedPath := "/repos/owner/repo/pulls"
				if !strings.HasPrefix(r.URL.Path, expectedPath) {
					t.Errorf("unexpected path: %s, want prefix %s", r.URL.Path, expectedPath)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			prs, err := client.ListPullRequests(context.Background(), "owner", "repo", tt.state)

			if (err != nil) != tt.wantErr {
				t.Errorf("ListPullRequests() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(prs) != tt.wantCount {
				t.Errorf("ListPullRequests() returned %d PRs, want %d", len(prs), tt.wantCount)
			}
		})
	}
}

func TestGetTagForSHA(t *testing.T) {
	tests := []struct {
		name       string
		sha        string
		tags       []*Tag
		wantTag    string
		statusCode int
		wantErr    bool
	}{
		{
			name: "found tag",
			sha:  "abc123",
			tags: []*Tag{
				{Name: "v1.0.0", Commit: struct {
					SHA string `json:"sha"`
				}{SHA: "def456"}},
				{Name: "v1.0.1", Commit: struct {
					SHA string `json:"sha"`
				}{SHA: "abc123"}},
			},
			wantTag:    "v1.0.1",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "no matching tag",
			sha:  "abc123",
			tags: []*Tag{
				{Name: "v1.0.0", Commit: struct {
					SHA string `json:"sha"`
				}{SHA: "def456"}},
			},
			wantTag:    "",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "empty tags",
			sha:        "abc123",
			tags:       []*Tag{},
			wantTag:    "",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "api error",
			sha:        "abc123",
			tags:       nil,
			wantTag:    "",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.tags)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			tagName, err := client.GetTagForSHA(context.Background(), "owner", "repo", tt.sha)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetTagForSHA() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tagName != tt.wantTag {
				t.Errorf("GetTagForSHA() = %q, want %q", tagName, tt.wantTag)
			}
		})
	}
}

// TestGetTagForSHA_Paginated verifies that GetTagForSHA paginates exhaustively and
// finds a tag that is NOT in the first page (i.e. beyond the old 20-tag window).
func TestGetTagForSHA_Paginated(t *testing.T) {
	const targetSHA = "targetsha001"
	const targetTag = "v5.0.42-target"

	// Page 1: 100 tags, none matching targetSHA.
	page1 := make([]*Tag, 100)
	for i := range page1 {
		page1[i] = &Tag{Name: fmt.Sprintf("v5.0.%d", i)}
		page1[i].Commit.SHA = fmt.Sprintf("bulksha%03d", i)
	}
	// Page 2: 50 tags; targetSHA is at position 7.
	page2 := make([]*Tag, 50)
	for i := range page2 {
		page2[i] = &Tag{Name: fmt.Sprintf("v5.1.%d", i)}
		page2[i].Commit.SHA = fmt.Sprintf("page2sha%03d", i)
	}
	page2[7].Commit.SHA = targetSHA
	page2[7].Name = targetTag

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		page := r.URL.Query().Get("page")
		w.WriteHeader(http.StatusOK)
		if page == "2" {
			_ = json.NewEncoder(w).Encode(page2)
		} else {
			_ = json.NewEncoder(w).Encode(page1)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	tag, err := client.GetTagForSHA(context.Background(), "owner", "repo", targetSHA)
	if err != nil {
		t.Fatalf("GetTagForSHA() error = %v", err)
	}
	if tag != targetTag {
		t.Errorf("GetTagForSHA() = %q, want %q", tag, targetTag)
	}
}

// TestGetTagForSHA_NotFoundAfterFullScan verifies that GetTagForSHA returns empty
// string (no error) when the SHA does not appear in any page.
func TestGetTagForSHA_NotFoundAfterFullScan(t *testing.T) {
	// Single page with 10 tags, none matching.
	tags := make([]*Tag, 10)
	for i := range tags {
		tags[i] = &Tag{Name: fmt.Sprintf("v6.0.%d", i)}
		tags[i].Commit.SHA = fmt.Sprintf("other%03d", i)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(tags)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	tag, err := client.GetTagForSHA(context.Background(), "owner", "repo", "notpresent")
	if err != nil {
		t.Fatalf("GetTagForSHA() unexpected error: %v", err)
	}
	if tag != "" {
		t.Errorf("GetTagForSHA() = %q, want empty string when SHA not found", tag)
	}
}

func TestDeleteBranch(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success - branch deleted",
			branch:     "pilot/GH-123",
			statusCode: http.StatusNoContent,
			wantErr:    false,
		},
		{
			name:       "success - branch already deleted (404)",
			branch:     "pilot/GH-456",
			statusCode: http.StatusNotFound,
			wantErr:    false, // 404 is OK - branch may have been deleted by GitHub setting
		},
		{
			name:       "success - branch already deleted (422)",
			branch:     "pilot/GH-789",
			statusCode: http.StatusUnprocessableEntity,
			wantErr:    false, // 422 is OK - branch reference doesn't exist
		},
		{
			name:       "server error",
			branch:     "pilot/GH-999",
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
				// r.URL.RawPath preserves percent-encoding; verify slash is encoded
				expectedRaw := "/repos/owner/repo/git/refs/heads/" + url.PathEscape(tt.branch)
				if r.URL.RawPath != "" {
					if r.URL.RawPath != expectedRaw {
						t.Errorf("unexpected raw path: %s, want %s", r.URL.RawPath, expectedRaw)
					}
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.DeleteBranch(context.Background(), "owner", "repo", tt.branch)

			if (err != nil) != tt.wantErr {
				t.Errorf("DeleteBranch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsUnprocessableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "422 error",
			err:  fmt.Errorf("API error (status 422): Reference does not exist"),
			want: true,
		},
		{
			name: "404 error",
			err:  fmt.Errorf("API error (status 404): Not Found"),
			want: false,
		},
		{
			name: "500 error",
			err:  fmt.Errorf("API error (status 500): Internal Server Error"),
			want: false,
		},
		{
			name: "other error",
			err:  fmt.Errorf("connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUnprocessableError(tt.err)
			if got != tt.want {
				t.Errorf("isUnprocessableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetJobLogs(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
		wantLogs   string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			body:       "2024-01-01T00:00:00Z Error: lint failed\nSA5011: possible nil pointer",
			wantErr:    false,
			wantLogs:   "2024-01-01T00:00:00Z Error: lint failed\nSA5011: possible nil pointer",
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			body:       "Not Found",
			wantErr:    true,
		},
		{
			name:       "empty logs",
			statusCode: http.StatusOK,
			body:       "",
			wantErr:    false,
			wantLogs:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/actions/jobs/123/logs" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			logs, err := client.GetJobLogs(context.Background(), "owner", "repo", 123)

			if tt.wantErr {
				if err == nil {
					t.Error("GetJobLogs() expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetJobLogs() unexpected error: %v", err)
			}
			if logs != tt.wantLogs {
				t.Errorf("GetJobLogs() = %q, want %q", logs, tt.wantLogs)
			}
		})
	}
}

func TestGetPullRequestComments(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
		wantCount  int
	}{
		{
			name:       "success - multiple comments",
			statusCode: http.StatusOK,
			response: []*PRReviewComment{
				{
					ID:        1,
					Body:      "Consider using a constant here",
					Path:      "internal/executor/runner.go",
					Line:      42,
					Side:      "RIGHT",
					User:      User{Login: "reviewer1"},
					CreatedAt: "2026-01-15T10:00:00Z",
					HTMLURL:   "https://github.com/owner/repo/pull/10#discussion_r1",
				},
				{
					ID:        2,
					Body:      "This nil check is redundant",
					Path:      "internal/gateway/server.go",
					Line:      100,
					Side:      "RIGHT",
					User:      User{Login: "reviewer2"},
					CreatedAt: "2026-01-15T11:00:00Z",
					HTMLURL:   "https://github.com/owner/repo/pull/10#discussion_r2",
				},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name:       "success - no comments",
			statusCode: http.StatusOK,
			response:   []*PRReviewComment{},
			wantErr:    false,
			wantCount:  0,
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
				if r.URL.Path != "/repos/owner/repo/pulls/10/comments" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			comments, err := client.GetPullRequestComments(context.Background(), "owner", "repo", 10)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetPullRequestComments() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(comments) != tt.wantCount {
					t.Errorf("GetPullRequestComments() returned %d comments, want %d", len(comments), tt.wantCount)
				}
				// Verify fields are parsed correctly for success case with comments
				if tt.wantCount > 0 {
					c := comments[0]
					if c.Body != "Consider using a constant here" {
						t.Errorf("comment.Body = %q, want %q", c.Body, "Consider using a constant here")
					}
					if c.Path != "internal/executor/runner.go" {
						t.Errorf("comment.Path = %q, want %q", c.Path, "internal/executor/runner.go")
					}
					if c.Line != 42 {
						t.Errorf("comment.Line = %d, want %d", c.Line, 42)
					}
					if c.Side != "RIGHT" {
						t.Errorf("comment.Side = %q, want %q", c.Side, "RIGHT")
					}
					if c.User.Login != "reviewer1" {
						t.Errorf("comment.User.Login = %q, want %q", c.User.Login, "reviewer1")
					}
				}
			}
		})
	}
}

func TestUpdatePullRequestBranch(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusAccepted,
			wantErr:    false,
		},
		{
			name:       "conflict cannot be resolved",
			statusCode: http.StatusUnprocessableEntity,
			wantErr:    true,
		},
		{
			name:       "forbidden",
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("expected PUT, got %s", r.Method)
				}
				if r.URL.Path != "/repos/owner/repo/pulls/42/update-branch" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.UpdatePullRequestBranch(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdatePullRequestBranch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExecuteGraphQL(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		variables   map[string]interface{}
		statusCode  int
		response    string
		wantErr     bool
		errContains string
	}{
		{
			name:       "success with result",
			query:      `query { viewer { login } }`,
			statusCode: http.StatusOK,
			response:   `{"data":{"viewer":{"login":"octocat"}}}`,
		},
		{
			name:       "success with variables",
			query:      `mutation($id: ID!) { addItem(id: $id) { id } }`,
			variables:  map[string]interface{}{"id": "abc123"},
			statusCode: http.StatusOK,
			response:   `{"data":{"addItem":{"id":"abc123"}}}`,
		},
		{
			name:        "graphql error in response",
			query:       `query { bad }`,
			statusCode:  http.StatusOK,
			response:    `{"data":null,"errors":[{"message":"Field 'bad' not found"}]}`,
			wantErr:     true,
			errContains: "graphql error: Field 'bad' not found",
		},
		{
			name:        "http error",
			query:       `query { viewer { login } }`,
			statusCode:  http.StatusUnauthorized,
			response:    `{"message":"Bad credentials"}`,
			wantErr:     true,
			errContains: "API error (status 401)",
		},
		{
			name:       "nil result ignores data",
			query:      `mutation { doSomething }`,
			statusCode: http.StatusOK,
			response:   `{"data":{"doSomething":true}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/graphql" {
					t.Errorf("unexpected path: %s, want /graphql", r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected Authorization header: %s", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected Content-Type: %s", r.Header.Get("Content-Type"))
				}

				var reqBody GraphQLRequest
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Fatalf("failed to decode request body: %v", err)
				}
				if reqBody.Query != tt.query {
					t.Errorf("query = %q, want %q", reqBody.Query, tt.query)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			if tt.name == "nil result ignores data" {
				err := client.ExecuteGraphQL(context.Background(), tt.query, tt.variables, nil)
				if (err != nil) != tt.wantErr {
					t.Errorf("ExecuteGraphQL() error = %v, wantErr %v", err, tt.wantErr)
				}
				return
			}

			var result map[string]interface{}
			err := client.ExecuteGraphQL(context.Background(), tt.query, tt.variables, &result)

			if (err != nil) != tt.wantErr {
				t.Errorf("ExecuteGraphQL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
			}
			if !tt.wantErr && result == nil {
				t.Error("expected non-nil result")
			}
		})
	}
}

func TestExecuteGraphQLTolerant(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		wantPartial    bool // want *PartialGraphQLError
		wantFatal      bool // want a non-partial error
		errContains    string
		wantResultKey  string // key that must exist in unmarshalled result when wantPartial
	}{
		{
			name:          "partial response: FORBIDDEN node error + good data",
			response:      `{"data":{"items":{"nodes":[{"id":"good-1"}]}},"errors":[{"message":"Could not resolve","type":"FORBIDDEN","path":["node","items","nodes",1]}]}`,
			wantPartial:   true,
			errContains:   "FORBIDDEN",
			wantResultKey: "items",
		},
		{
			name:          "partial response: NOT_FOUND node error + good data",
			response:      `{"data":{"items":{"nodes":[{"id":"good-2"}]}},"errors":[{"message":"Not found","type":"NOT_FOUND","path":["node","items","nodes",0]}]}`,
			wantPartial:   true,
			errContains:   "NOT_FOUND",
			wantResultKey: "items",
		},
		{
			name:        "fatal response: RATE_LIMITED error",
			response:    `{"data":null,"errors":[{"message":"rate limit exceeded","type":"RATE_LIMITED"}]}`,
			wantFatal:   true,
			errContains: "RATE_LIMITED",
		},
		{
			name:        "fatal response: empty Type (unknown error)",
			response:    `{"data":null,"errors":[{"message":"something broke"}]}`,
			wantFatal:   true,
			errContains: "something broke",
		},
		{
			name:        "mixed: one tolerable + one fatal → fatal",
			response:    `{"data":{"x":1},"errors":[{"message":"not found","type":"NOT_FOUND"},{"message":"rate limit","type":"RATE_LIMITED"}]}`,
			wantFatal:   true,
			errContains: "RATE_LIMITED",
		},
		{
			name:          "no errors: behaves same as strict",
			response:      `{"data":{"viewer":{"login":"octocat"}}}`,
			wantResultKey: "viewer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			var result map[string]interface{}
			err := client.ExecuteGraphQLTolerant(context.Background(), `query { x }`, nil, &result)

			if tt.wantPartial {
				var partialErr *PartialGraphQLError
				if !errors.As(err, &partialErr) {
					t.Fatalf("expected *PartialGraphQLError, got %T: %v", err, err)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("partial error = %q, want containing %q", err.Error(), tt.errContains)
				}
				if tt.wantResultKey != "" && result[tt.wantResultKey] == nil {
					t.Errorf("result[%q] is nil — data should have been unmarshalled alongside partial error", tt.wantResultKey)
				}
				return
			}

			if tt.wantFatal {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var partialErr *PartialGraphQLError
				if errors.As(err, &partialErr) {
					t.Fatalf("expected fatal error, got *PartialGraphQLError: %v", err)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("fatal error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}

			// No errors expected
			if err != nil {
				t.Fatalf("ExecuteGraphQLTolerant() unexpected error = %v", err)
			}
			if tt.wantResultKey != "" && result[tt.wantResultKey] == nil {
				t.Errorf("result[%q] is nil, want populated", tt.wantResultKey)
			}
		})
	}
}

// TestExecuteGraphQL_StrictUnchanged verifies that ExecuteGraphQL returns a fatal
// error for any GraphQL error, including tolerable types like FORBIDDEN/NOT_FOUND.
func TestExecuteGraphQL_StrictUnchanged(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		errContains string
	}{
		{
			name:        "FORBIDDEN is fatal in strict mode",
			response:    `{"data":{"items":[{"id":"x"}]},"errors":[{"message":"no access","type":"FORBIDDEN"}]}`,
			errContains: "graphql error",
		},
		{
			name:        "NOT_FOUND is fatal in strict mode",
			response:    `{"data":{"items":[{"id":"x"}]},"errors":[{"message":"gone","type":"NOT_FOUND"}]}`,
			errContains: "graphql error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			var result map[string]interface{}
			err := client.ExecuteGraphQL(context.Background(), `query { x }`, nil, &result)
			if err == nil {
				t.Fatal("ExecuteGraphQL() should return error for any GraphQL error")
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
			}
			// result must not be populated — strict mode does not unmarshal on error
			if result != nil {
				t.Errorf("result should be nil when ExecuteGraphQL returns an error, got %v", result)
			}
		})
	}
}

func TestSearchMergedPRsForIssue(t *testing.T) {
	tests := []struct {
		name        string
		issueNumber int
		statusCode  int
		response    string
		wantFound   bool
		wantErr     bool
	}{
		{
			name:        "merged PRs exist",
			issueNumber: 42,
			statusCode:  http.StatusOK,
			response:    `{"total_count": 2}`,
			wantFound:   true,
		},
		{
			name:        "no merged PRs",
			issueNumber: 99,
			statusCode:  http.StatusOK,
			response:    `{"total_count": 0}`,
			wantFound:   false,
		},
		{
			name:        "open PRs only - not counted",
			issueNumber: 55,
			statusCode:  http.StatusOK,
			response:    `{"total_count": 0}`,
			wantFound:   false,
		},
		{
			name:        "API error",
			issueNumber: 10,
			statusCode:  http.StatusForbidden,
			response:    `{"message": "rate limit"}`,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/search/issues") {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				q := r.URL.Query().Get("q")
				expectedQ := fmt.Sprintf("repo:owner/repo GH-%d in:title is:pr is:merged", tt.issueNumber)
				if q != expectedQ {
					t.Errorf("query = %q, want %q", q, expectedQ)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			found, err := client.SearchMergedPRsForIssue(context.Background(), "owner", "repo", tt.issueNumber)

			if (err != nil) != tt.wantErr {
				t.Errorf("SearchMergedPRsForIssue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && found != tt.wantFound {
				t.Errorf("SearchMergedPRsForIssue() = %v, want %v", found, tt.wantFound)
			}
		})
	}
}

func TestFindMergedPRByBranch(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		response   string
		wantFound  bool
		wantErr    bool
		statusCode int
	}{
		{
			name:       "merged PR on branch",
			branch:     "pilot/GH-42",
			statusCode: http.StatusOK,
			response:   `[{"number": 100, "merged_at": "2026-04-17T14:01:40Z"}]`,
			wantFound:  true,
		},
		{
			name:       "closed but not merged",
			branch:     "pilot/GH-43",
			statusCode: http.StatusOK,
			response:   `[{"number": 101, "merged_at": ""}]`,
			wantFound:  false,
		},
		{
			name:       "no PRs on branch",
			branch:     "pilot/GH-44",
			statusCode: http.StatusOK,
			response:   `[]`,
			wantFound:  false,
		},
		{
			name:       "API error",
			branch:     "pilot/GH-45",
			statusCode: http.StatusForbidden,
			response:   `{"message":"rate limit"}`,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/pulls" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if got := r.URL.Query().Get("head"); got != "owner:"+tt.branch {
					t.Errorf("head filter = %q, want %q", got, "owner:"+tt.branch)
				}
				if got := r.URL.Query().Get("state"); got != "closed" {
					t.Errorf("state = %q, want closed", got)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			found, err := client.FindMergedPRByBranch(context.Background(), "owner", "repo", tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindMergedPRByBranch() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && found != tt.wantFound {
				t.Errorf("FindMergedPRByBranch() = %v, want %v", found, tt.wantFound)
			}
		})
	}
}

func TestSearchOpenSubIssues(t *testing.T) {
	tests := []struct {
		name       string
		parentNum  int
		statusCode int
		response   string
		wantCount  int
		wantErr    bool
	}{
		{
			name:       "parent with open siblings",
			parentNum:  100,
			statusCode: http.StatusOK,
			response:   `{"total_count": 3}`,
			wantCount:  3,
		},
		{
			name:       "all siblings closed",
			parentNum:  200,
			statusCode: http.StatusOK,
			response:   `{"total_count": 0}`,
			wantCount:  0,
		},
		{
			name:       "API error",
			parentNum:  300,
			statusCode: http.StatusForbidden,
			response:   `{"message": "rate limit"}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/search/issues") {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				q := r.URL.Query().Get("q")
				expectedQ := fmt.Sprintf(`repo:owner/repo "Parent: GH-%d" is:issue is:open`, tt.parentNum)
				if q != expectedQ {
					t.Errorf("query = %q, want %q", q, expectedQ)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			count, err := client.SearchOpenSubIssues(context.Background(), "owner", "repo", tt.parentNum)

			if (err != nil) != tt.wantErr {
				t.Errorf("SearchOpenSubIssues() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && count != tt.wantCount {
				t.Errorf("SearchOpenSubIssues() = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestGetReleaseByTag(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantNil    bool
		wantErr    bool
	}{
		{
			name:       "found",
			statusCode: http.StatusOK,
			response: Release{
				ID:      123,
				TagName: "v1.0.0",
				Body:    "changelog here",
			},
			wantNil: false,
			wantErr: false,
		},
		{
			name:       "not found returns nil",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantNil:    true,
			wantErr:    false,
		},
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			response:   map[string]string{"message": "Internal Server Error"},
			wantNil:    true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/releases/tags/v1.0.0" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("unexpected method: %s", r.Method)
				}
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			release, err := client.GetReleaseByTag(context.Background(), "owner", "repo", "v1.0.0")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetReleaseByTag() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if (release == nil) != tt.wantNil {
				t.Errorf("GetReleaseByTag() release nil = %v, wantNil %v", release == nil, tt.wantNil)
			}
			if release != nil && release.ID != 123 {
				t.Errorf("GetReleaseByTag() release.ID = %d, want 123", release.ID)
			}
		})
	}
}

func TestGetIssueNodeID(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		response    string
		wantNodeID  string
		wantErr     bool
		errContains string
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   `{"node_id":"I_kwDOTest123","number":42,"title":"Test"}`,
			wantNodeID: "I_kwDOTest123",
		},
		{
			name:        "empty node_id",
			statusCode:  http.StatusOK,
			response:    `{"node_id":"","number":42}`,
			wantErr:     true,
			errContains: "empty node_id",
		},
		{
			name:        "not found",
			statusCode:  http.StatusNotFound,
			response:    `{"message":"Not Found"}`,
			wantErr:     true,
			errContains: "get issue node ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/issues/42" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			nodeID, err := client.GetIssueNodeID(context.Background(), "owner", "repo", 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetIssueNodeID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}
			if nodeID != tt.wantNodeID {
				t.Errorf("GetIssueNodeID() = %q, want %q", nodeID, tt.wantNodeID)
			}
		})
	}
}

func TestLinkSubIssue(t *testing.T) {
	tests := []struct {
		name            string
		parentResponse  string
		childResponse   string
		graphqlResponse string
		parentStatus    int
		childStatus     int
		graphqlStatus   int
		wantErr         bool
		errContains     string
	}{
		{
			name:            "success",
			parentResponse:  `{"node_id":"I_parent123","number":10}`,
			childResponse:   `{"node_id":"I_child456","number":20}`,
			graphqlResponse: `{"data":{"addSubIssue":{"issue":{"id":"I_parent123"},"subIssue":{"id":"I_child456"}}}}`,
			parentStatus:    http.StatusOK,
			childStatus:     http.StatusOK,
			graphqlStatus:   http.StatusOK,
		},
		{
			name:           "parent not found",
			parentStatus:   http.StatusNotFound,
			parentResponse: `{"message":"Not Found"}`,
			wantErr:        true,
			errContains:    "resolve parent node ID",
		},
		{
			name:           "child not found",
			parentResponse: `{"node_id":"I_parent123","number":10}`,
			parentStatus:   http.StatusOK,
			childStatus:    http.StatusNotFound,
			childResponse:  `{"message":"Not Found"}`,
			wantErr:        true,
			errContains:    "resolve child node ID",
		},
		{
			name:            "graphql mutation error",
			parentResponse:  `{"node_id":"I_parent123","number":10}`,
			childResponse:   `{"node_id":"I_child456","number":20}`,
			graphqlResponse: `{"data":null,"errors":[{"message":"addSubIssue not available"}]}`,
			parentStatus:    http.StatusOK,
			childStatus:     http.StatusOK,
			graphqlStatus:   http.StatusOK,
			wantErr:         true,
			errContains:     "graphql error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					w.WriteHeader(tt.parentStatus)
					_, _ = w.Write([]byte(tt.parentResponse))
				case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodGet:
					w.WriteHeader(tt.childStatus)
					_, _ = w.Write([]byte(tt.childResponse))
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					var reqBody GraphQLRequest
					_ = json.NewDecoder(r.Body).Decode(&reqBody)
					if reqBody.Variables["parentID"] != "I_parent123" {
						t.Errorf("parentID = %v, want I_parent123", reqBody.Variables["parentID"])
					}
					if reqBody.Variables["childID"] != "I_child456" {
						t.Errorf("childID = %v, want I_child456", reqBody.Variables["childID"])
					}
					w.WriteHeader(tt.graphqlStatus)
					_, _ = w.Write([]byte(tt.graphqlResponse))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.LinkSubIssue(context.Background(), "owner", "repo", 10, 20)

			if (err != nil) != tt.wantErr {
				t.Errorf("LinkSubIssue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestGetOpenSubIssueNumbers(t *testing.T) {
	tests := []struct {
		name            string
		restResponse    string
		restStatus      int
		graphqlResponse string
		graphqlStatus   int
		wantNumbers     []int
		wantNativeLinks bool
		wantErr         bool
		errContains     string
	}{
		{
			name:            "some open sub-issues",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":3,"nodes":[{"number":1,"state":"OPEN"},{"number":2,"state":"CLOSED"},{"number":3,"state":"OPEN"}]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantNumbers:     []int{1, 3},
			wantNativeLinks: true,
		},
		{
			name:            "all closed",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":2,"nodes":[{"number":1,"state":"CLOSED"},{"number":2,"state":"CLOSED"}]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantNumbers:     nil,
			wantNativeLinks: true,
		},
		{
			name:            "no native links",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":0,"nodes":[]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantNumbers:     nil,
			wantNativeLinks: false,
		},
		{
			name:         "parent REST error",
			restResponse: `{"message":"Not Found"}`,
			restStatus:   http.StatusNotFound,
			wantErr:      true,
			errContains:  "resolve parent node ID",
		},
		{
			name:            "graphql error",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":null,"errors":[{"message":"something broke"}]}`,
			graphqlStatus:   http.StatusOK,
			wantErr:         true,
			errContains:     "query sub-issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/50" && r.Method == http.MethodGet:
					w.WriteHeader(tt.restStatus)
					_, _ = w.Write([]byte(tt.restResponse))
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					w.WriteHeader(tt.graphqlStatus)
					_, _ = w.Write([]byte(tt.graphqlResponse))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			numbers, hasNative, err := client.GetOpenSubIssueNumbers(context.Background(), "owner", "repo", 50)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetOpenSubIssueNumbers() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}
			if !reflect.DeepEqual(numbers, tt.wantNumbers) {
				t.Errorf("GetOpenSubIssueNumbers() numbers = %v, want %v", numbers, tt.wantNumbers)
			}
			if hasNative != tt.wantNativeLinks {
				t.Errorf("GetOpenSubIssueNumbers() hasNativeLinks = %v, want %v", hasNative, tt.wantNativeLinks)
			}
		})
	}
}

func TestGetOpenSubIssueCount(t *testing.T) {
	tests := []struct {
		name            string
		restResponse    string
		restStatus      int
		graphqlResponse string
		graphqlStatus   int
		wantCount       int
		wantNativeLinks bool
		wantErr         bool
		errContains     string
	}{
		{
			name:            "some open sub-issues",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":3,"nodes":[{"state":"OPEN"},{"state":"CLOSED"},{"state":"OPEN"}]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantCount:       2,
			wantNativeLinks: true,
		},
		{
			name:            "all closed",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":2,"nodes":[{"state":"CLOSED"},{"state":"CLOSED"}]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantCount:       0,
			wantNativeLinks: true,
		},
		{
			name:            "no native links",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":{"node":{"subIssues":{"totalCount":0,"nodes":[]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantCount:       0,
			wantNativeLinks: false,
		},
		{
			name:         "parent REST error",
			restResponse: `{"message":"Not Found"}`,
			restStatus:   http.StatusNotFound,
			wantErr:      true,
			errContains:  "resolve parent node ID",
		},
		{
			name:            "graphql error",
			restResponse:    `{"node_id":"I_parent123","number":50}`,
			restStatus:      http.StatusOK,
			graphqlResponse: `{"data":null,"errors":[{"message":"something broke"}]}`,
			graphqlStatus:   http.StatusOK,
			wantErr:         true,
			errContains:     "query sub-issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/50" && r.Method == http.MethodGet:
					w.WriteHeader(tt.restStatus)
					_, _ = w.Write([]byte(tt.restResponse))
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					w.WriteHeader(tt.graphqlStatus)
					_, _ = w.Write([]byte(tt.graphqlResponse))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			count, hasNative, err := client.GetOpenSubIssueCount(context.Background(), "owner", "repo", 50)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetOpenSubIssueCount() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}
			if count != tt.wantCount {
				t.Errorf("GetOpenSubIssueCount() count = %d, want %d", count, tt.wantCount)
			}
			if hasNative != tt.wantNativeLinks {
				t.Errorf("GetOpenSubIssueCount() hasNativeLinks = %v, want %v", hasNative, tt.wantNativeLinks)
			}
		})
	}
}

func TestSearchOpenPilotIssuesWithSubIssues(t *testing.T) {
	tests := []struct {
		name            string
		graphqlResponse string
		graphqlStatus   int
		limit           int
		wantNumbers     []int
		wantErr         bool
		errContains     string
	}{
		{
			name:  "returns issues with sub-issues",
			limit: 10,
			graphqlResponse: `{"data":{"repository":{"issues":{"nodes":[
				{"number":1,"subIssuesSummary":{"total":3,"completed":1}},
				{"number":2,"subIssuesSummary":{"total":0,"completed":0}},
				{"number":3,"subIssuesSummary":{"total":2,"completed":2}}
			]}}}}`,
			graphqlStatus: http.StatusOK,
			wantNumbers:   []int{1, 3},
		},
		{
			name:  "empty results when no sub-issues",
			limit: 10,
			graphqlResponse: `{"data":{"repository":{"issues":{"nodes":[
				{"number":5,"subIssuesSummary":{"total":0,"completed":0}},
				{"number":6,"subIssuesSummary":{"total":0,"completed":0}}
			]}}}}`,
			graphqlStatus: http.StatusOK,
			wantNumbers:   nil,
		},
		{
			name:            "empty results when no issues",
			limit:           10,
			graphqlResponse: `{"data":{"repository":{"issues":{"nodes":[]}}}}`,
			graphqlStatus:   http.StatusOK,
			wantNumbers:     nil,
		},
		{
			name:  "limit truncation applied to API request",
			limit: 2,
			graphqlResponse: `{"data":{"repository":{"issues":{"nodes":[
				{"number":10,"subIssuesSummary":{"total":1,"completed":0}},
				{"number":11,"subIssuesSummary":{"total":4,"completed":2}}
			]}}}}`,
			graphqlStatus: http.StatusOK,
			wantNumbers:   []int{10, 11},
		},
		{
			name:            "API error propagation",
			limit:           10,
			graphqlResponse: `{"data":null,"errors":[{"message":"could not resolve repository"}]}`,
			graphqlStatus:   http.StatusOK,
			wantErr:         true,
			errContains:     "search pilot issues with sub-issues",
		},
		{
			name:            "HTTP error propagation",
			limit:           10,
			graphqlStatus:   http.StatusInternalServerError,
			graphqlResponse: `internal error`,
			wantErr:         true,
			errContains:     "search pilot issues with sub-issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedFirst int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/graphql" && r.Method == http.MethodPost {
					var req GraphQLRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
						if v, ok := req.Variables["first"]; ok {
							switch n := v.(type) {
							case float64:
								capturedFirst = int(n)
							}
						}
					}
					w.WriteHeader(tt.graphqlStatus)
					_, _ = w.Write([]byte(tt.graphqlResponse))
					return
				}
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			numbers, err := client.SearchOpenPilotIssuesWithSubIssues(context.Background(), "owner", "repo", tt.limit)

			if (err != nil) != tt.wantErr {
				t.Errorf("SearchOpenPilotIssuesWithSubIssues() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}
			if !tt.wantErr && capturedFirst != tt.limit {
				t.Errorf("GraphQL first = %d, want %d (limit not forwarded)", capturedFirst, tt.limit)
			}
			if len(numbers) != len(tt.wantNumbers) {
				t.Errorf("SearchOpenPilotIssuesWithSubIssues() = %v, want %v", numbers, tt.wantNumbers)
				return
			}
			for i, n := range numbers {
				if n != tt.wantNumbers[i] {
					t.Errorf("numbers[%d] = %d, want %d", i, n, tt.wantNumbers[i])
				}
			}
		})
	}
}

func TestUpdateRelease(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedBody map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/releases/123" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodPatch {
					t.Errorf("unexpected method: %s, want PATCH", r.Method)
				}
				_ = json.NewDecoder(r.Body).Decode(&receivedBody)
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprintf(w, `{"id":123,"tag_name":"v1.0.0","body":"updated body"}`)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			release, err := client.UpdateRelease(context.Background(), "owner", "repo", 123, &ReleaseInput{
				Body: "enriched changelog",
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateRelease() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if release == nil {
					t.Fatal("UpdateRelease() returned nil release")
				}
				if receivedBody["body"] != "enriched changelog" {
					t.Errorf("UpdateRelease() sent body = %v, want 'enriched changelog'", receivedBody["body"])
				}
			}
		})
	}
}

func TestSearchPRsForIssue(t *testing.T) {
	tests := []struct {
		name        string
		issueNumber int
		statusCode  int
		response    string
		wantPRNums  []int
		wantErr     bool
	}{
		{
			name:        "two PRs found",
			issueNumber: 42,
			statusCode:  http.StatusOK,
			response: `{"total_count":2,"items":[
				{"id":101,"number":10,"title":"fix: closes #42","state":"open","html_url":"https://github.com/owner/repo/pull/10","pull_request":{"merged_at":""}},
				{"id":102,"number":11,"title":"feat: implements #42","state":"closed","html_url":"https://github.com/owner/repo/pull/11","pull_request":{"merged_at":"2026-04-01T12:00:00Z"}}
			]}`,
			wantPRNums: []int{10, 11},
		},
		{
			name:        "no PRs",
			issueNumber: 99,
			statusCode:  http.StatusOK,
			response:    `{"total_count":0,"items":[]}`,
			wantPRNums:  []int{},
		},
		{
			name:        "API error",
			issueNumber: 1,
			statusCode:  http.StatusForbidden,
			response:    `{"message":"rate limit exceeded"}`,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/search/issues") {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				q := r.URL.Query().Get("q")
				expectedQ := fmt.Sprintf("repo:owner/repo is:pr #%d", tt.issueNumber)
				if q != expectedQ {
					t.Errorf("query = %q, want %q", q, expectedQ)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			prs, err := client.SearchPRsForIssue(context.Background(), "owner", "repo", tt.issueNumber)

			if (err != nil) != tt.wantErr {
				t.Errorf("SearchPRsForIssue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			if len(prs) != len(tt.wantPRNums) {
				t.Fatalf("SearchPRsForIssue() returned %d PRs, want %d", len(prs), len(tt.wantPRNums))
			}
			for i, pr := range prs {
				if pr.Number != tt.wantPRNums[i] {
					t.Errorf("prs[%d].Number = %d, want %d", i, pr.Number, tt.wantPRNums[i])
				}
			}
			// Verify merged flag is set correctly for second PR in "two PRs found" case
			if tt.name == "two PRs found" {
				if prs[0].Merged {
					t.Error("prs[0].Merged should be false (merged_at empty)")
				}
				if !prs[1].Merged {
					t.Error("prs[1].Merged should be true (merged_at set)")
				}
				if prs[1].MergedAt != "2026-04-01T12:00:00Z" {
					t.Errorf("prs[1].MergedAt = %q, want 2026-04-01T12:00:00Z", prs[1].MergedAt)
				}
			}
		})
	}
}

// TestListPullRequests_Pagination verifies that ListPullRequests fetches
// multiple pages when a page returns the full per_page count.
func TestListPullRequests_Pagination(t *testing.T) {
	// Build two pages: page 1 has 100 PRs, page 2 has 3 PRs.
	page1 := make([]*PullRequest, 100)
	for i := range page1 {
		n := i + 1
		page1[i] = &PullRequest{
			Number:  n,
			HTMLURL: fmt.Sprintf("https://github.com/owner/repo/pull/%d", n),
			Head:    PRRef{Ref: fmt.Sprintf("pilot/GH-%d", n), SHA: "sha"},
		}
	}
	page2 := []*PullRequest{
		{Number: 101, HTMLURL: "url101", Head: PRRef{Ref: "pilot/GH-101", SHA: "sha"}},
		{Number: 102, HTMLURL: "url102", Head: PRRef{Ref: "pilot/GH-102", SHA: "sha"}},
		{Number: 103, HTMLURL: "url103", Head: PRRef{Ref: "pilot/GH-103", SHA: "sha"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		pageParam := q.Get("page")
		perPage := q.Get("per_page")
		if perPage != "100" {
			t.Errorf("per_page = %q, want 100", perPage)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if pageParam == "2" {
			_ = json.NewEncoder(w).Encode(page2)
		} else {
			_ = json.NewEncoder(w).Encode(page1)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	prs, err := client.ListPullRequests(context.Background(), "owner", "repo", "open")
	if err != nil {
		t.Fatalf("ListPullRequests() error = %v", err)
	}
	if len(prs) != 103 {
		t.Errorf("got %d PRs, want 103", len(prs))
	}
}

// TestListPullRequests_SinglePage verifies that a response shorter than
// per_page stops pagination after one request.
func TestListPullRequests_SinglePage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		prs := []*PullRequest{
			{Number: 1, HTMLURL: "url1", Head: PRRef{Ref: "pilot/GH-1", SHA: "sha"}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(prs)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	prs, err := client.ListPullRequests(context.Background(), "owner", "repo", "open")
	if err != nil {
		t.Fatalf("ListPullRequests() error = %v", err)
	}
	if len(prs) != 1 {
		t.Errorf("got %d PRs, want 1", len(prs))
	}
	if calls != 1 {
		t.Errorf("server called %d times, want 1 (single page should stop)", calls)
	}
}

// TestDoRequest_RetryOn429 verifies that doRequest retries once on 429 and succeeds.
func TestDoRequest_RetryOn429(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{Number: 1})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	// Use fast retry so the test doesn't sleep for 1s
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	var issue Issue
	err := client.doRequest(context.Background(), "GET", "/repos/owner/repo/issues/1", nil, &issue)
	if err != nil {
		t.Fatalf("doRequest() error = %v, want nil", err)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2 (1 initial + 1 retry)", calls)
	}
	if issue.Number != 1 {
		t.Errorf("issue.Number = %d, want 1", issue.Number)
	}
}

// TestDoRequest_RateLimitError verifies that doRequest returns *RateLimitError for rate-limit
// 403/429 responses and a plain error for non-rate-limit 403 responses.
func TestDoRequest_RateLimitError(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		body           string
		headers        map[string]string
		wantRateLimit  bool
		wantRetryAfter time.Duration
	}{
		{
			name:          "429 always rate limit",
			statusCode:    http.StatusTooManyRequests,
			body:          `{"message": "too many requests"}`,
			wantRateLimit: true,
		},
		{
			name:           "429 with Retry-After header",
			statusCode:     http.StatusTooManyRequests,
			body:           `{"message": "too many requests"}`,
			headers:        map[string]string{"Retry-After": "30"},
			wantRateLimit:  true,
			wantRetryAfter: 30 * time.Second,
		},
		{
			name:          "403 secondary rate limit body",
			statusCode:    http.StatusForbidden,
			body:          `{"message": "You have exceeded a secondary rate limit"}`,
			wantRateLimit: true,
		},
		{
			name:           "403 secondary rate limit with Retry-After",
			statusCode:     http.StatusForbidden,
			body:           `{"message": "You have exceeded a secondary rate limit"}`,
			headers:        map[string]string{"Retry-After": "60"},
			wantRateLimit:  true,
			wantRetryAfter: 60 * time.Second,
		},
		{
			name:          "403 rate limit exceeded body",
			statusCode:    http.StatusForbidden,
			body:          `{"message": "API rate limit exceeded"}`,
			wantRateLimit: true,
		},
		{
			name:          "403 X-RateLimit-Remaining zero",
			statusCode:    http.StatusForbidden,
			body:          `{"message": "Forbidden"}`,
			headers:       map[string]string{"X-RateLimit-Remaining": "0"},
			wantRateLimit: true,
		},
		{
			name:          "403 plain forbidden - not a rate limit",
			statusCode:    http.StatusForbidden,
			body:          `{"message": "Resource not accessible by integration"}`,
			wantRateLimit: false,
		},
		{
			name:          "403 permission denied - not a rate limit",
			statusCode:    http.StatusForbidden,
			body:          `{"message": "Must have push access to the repository"}`,
			wantRateLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.doRequest(context.Background(), "GET", "/repos/owner/repo/issues/1", nil, nil)

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var rlErr *RateLimitError
			gotRateLimit := errors.As(err, &rlErr)

			if gotRateLimit != tt.wantRateLimit {
				t.Errorf("errors.As(*RateLimitError) = %v, want %v (err: %v)", gotRateLimit, tt.wantRateLimit, err)
				return
			}

			if tt.wantRateLimit && tt.wantRetryAfter > 0 {
				if rlErr.RetryAfter != tt.wantRetryAfter {
					t.Errorf("RateLimitError.RetryAfter = %v, want %v", rlErr.RetryAfter, tt.wantRetryAfter)
				}
			}

			// Error string must always contain the status code for callers checking string.
			if !strings.Contains(err.Error(), fmt.Sprintf("status %d", tt.statusCode)) {
				t.Errorf("error %q does not contain expected status", err.Error())
			}
		})
	}
}

// TestDoRequest_Secondary403Retried verifies that a 403 secondary rate-limit response
// is retried, while a plain 403 (permission error) is not.
func TestDoRequest_Secondary403Retried(t *testing.T) {
	t.Run("secondary rate limit 403 is retried", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message": "You have exceeded a secondary rate limit"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(Issue{Number: 1})
		}))
		defer server.Close()

		client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

		var issue Issue
		err := client.doRequest(context.Background(), "GET", "/repos/owner/repo/issues/1", nil, &issue)
		if err != nil {
			t.Fatalf("expected success after retry, got: %v", err)
		}
		if calls != 2 {
			t.Errorf("server called %d times, want 2 (1 rate-limited + 1 retry)", calls)
		}
	})

	t.Run("plain 403 forbidden is not retried", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message": "Resource not accessible by integration"}`))
		}))
		defer server.Close()

		client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

		err := client.doRequest(context.Background(), "GET", "/repos/owner/repo/issues/1", nil, nil)
		if err == nil {
			t.Fatal("expected error for plain 403, got nil")
		}
		if calls != 1 {
			t.Errorf("server called %d times, want 1 (no retry on plain 403)", calls)
		}
	})
}

// TestDoRequest_NoRetryOn404 verifies that doRequest does not retry non-retriable errors.
func TestDoRequest_NoRetryOn404(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	err := client.doRequest(context.Background(), "GET", "/repos/owner/repo/issues/999", nil, nil)
	if err == nil {
		t.Fatal("doRequest() expected error for 404, got nil")
	}
	if calls != 1 {
		t.Errorf("server called %d times, want 1 (no retry on 404)", calls)
	}
}

// TestDoRequest_ReplayBodyOnRetry verifies that POST body is replayed correctly on retry.
func TestDoRequest_ReplayBodyOnRetry(t *testing.T) {
	calls := 0
	var receivedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		receivedBodies = append(receivedBodies, buf.String())
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Comment{ID: 42, Body: "hello"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}

	var comment Comment
	err := client.doRequest(context.Background(), "POST", "/repos/owner/repo/issues/1/comments",
		map[string]string{"body": "hello"}, &comment)
	if err != nil {
		t.Fatalf("doRequest() error = %v", err)
	}
	if calls != 2 {
		t.Errorf("server called %d times, want 2", calls)
	}
	// Both attempts should have sent the same body
	for i, body := range receivedBodies {
		if !strings.Contains(body, "hello") {
			t.Errorf("attempt %d body %q does not contain 'hello'", i+1, body)
		}
	}
}

// TestGetTagForSHA_ExhaustivePagination verifies that GetTagForSHA finds a tag
// whose SHA sits beyond the first page of results (i.e. would have been missed
// by the old ListTags(...,20) window). The test serves three pages of tags —
// page 1 and 2 return 100 tags each, page 3 returns the target tag plus a
// sentinel tag — and asserts the correct tag name is returned. GH-3558.
func TestGetTagForSHA_ExhaustivePagination(t *testing.T) {
	const targetSHA = "deeplypaginatedsha"
	const targetTag = "v0.99.0"

	// Build a full page of dummy tags (none match targetSHA).
	makePage := func(n int, prefix string) []Tag {
		tags := make([]Tag, n)
		for i := range tags {
			tags[i] = Tag{Name: fmt.Sprintf("%s-tag-%d", prefix, i)}
			tags[i].Commit.SHA = fmt.Sprintf("%s-sha-%d", prefix, i)
		}
		return tags
	}

	page1 := makePage(100, "p1")
	page2 := makePage(100, "p2")
	// Page 3: target tag + one filler (fewer than 100 → last page).
	targetTagObj := Tag{Name: targetTag}
	targetTagObj.Commit.SHA = targetSHA
	page3 := []Tag{targetTagObj, {Name: "extra"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageParam := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch pageParam {
		case "1":
			_ = json.NewEncoder(w).Encode(page1)
		case "2":
			_ = json.NewEncoder(w).Encode(page2)
		default:
			_ = json.NewEncoder(w).Encode(page3)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.GetTagForSHA(context.Background(), "owner", "repo", targetSHA)
	if err != nil {
		t.Fatalf("GetTagForSHA() error = %v", err)
	}
	if got != targetTag {
		t.Errorf("GetTagForSHA() = %q, want %q", got, targetTag)
	}
}

// TestListIssueEvents_ParsesLabeledAndReopenedEvents is GH-5139's ListIssueEvents
// coverage — the re-arm probe (cmd/pilot/rearm_canceled.go) relies on this
// endpoint returning real timestamps for labeled/reopened events, unlike
// Issue.UpdatedAt (which bumps on any activity, e.g. a comment).
func TestListIssueEvents_ParsesLabeledAndReopenedEvents(t *testing.T) {
	var receivedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id": 1, "event": "labeled", "created_at": "2026-08-01T10:00:00Z", "label": {"name": "pilot"}},
			{"id": 2, "event": "unlabeled", "created_at": "2026-08-10T10:00:00Z", "label": {"name": "pilot"}},
			{"id": 3, "event": "reopened", "created_at": "2026-08-20T10:00:00Z"},
			{"id": 4, "event": "labeled", "created_at": "2026-08-20T10:05:00Z", "label": {"name": "pilot"}}
		]`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	events, err := client.ListIssueEvents(context.Background(), "owner", "repo", 5139)
	if err != nil {
		t.Fatalf("ListIssueEvents() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	if events[3].Event != "labeled" || events[3].Label == nil || events[3].Label.Name != "pilot" {
		t.Errorf("expected last event to be a labeled event naming 'pilot', got %+v", events[3])
	}
	wantCreated := time.Date(2026, 8, 20, 10, 5, 0, 0, time.UTC)
	if !events[3].CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", events[3].CreatedAt, wantCreated)
	}
	if events[2].Event != "reopened" || events[2].Label != nil {
		t.Errorf("expected reopened event with no label, got %+v", events[2])
	}
	if receivedPath != "/repos/owner/repo/issues/5139/events?per_page=100" {
		t.Errorf("unexpected request path: %s", receivedPath)
	}
}
