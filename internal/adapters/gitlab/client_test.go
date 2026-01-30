package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewClient(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.token != testutil.FakeGitLabToken {
		t.Errorf("client.token = %s, want %s", client.token, testutil.FakeGitLabToken)
	}
	if client.baseURL != gitlabAPIURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, gitlabAPIURL)
	}
	// Project path should be URL-encoded
	if client.projectID != "namespace%2Fproject" {
		t.Errorf("client.projectID = %s, want namespace%%2Fproject", client.projectID)
	}
}

func TestNewClientWithBaseURL(t *testing.T) {
	customURL := "https://custom.gitlab.example.com"
	client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", customURL)
	if client == nil {
		t.Fatal("NewClientWithBaseURL returned nil")
	}
	if client.token != testutil.FakeGitLabToken {
		t.Errorf("client.token = %s, want %s", client.token, testutil.FakeGitLabToken)
	}
	if client.baseURL != customURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, customURL)
	}
}

func TestGetProject(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response: Project{
				ID:                12345,
				Name:              "project",
				PathWithNamespace: "namespace/project",
				WebURL:            "https://gitlab.com/namespace/project",
				DefaultBranch:     "main",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "404 Project Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if r.Header.Get("PRIVATE-TOKEN") != testutil.FakeGitLabToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("PRIVATE-TOKEN"))
				}

				w.WriteHeader(tt.statusCode)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			project, err := client.GetProject(context.Background())

			if (err != nil) != tt.wantErr {
				t.Errorf("GetProject() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && project.Name != "project" {
				t.Errorf("project.Name = %s, want project", project.Name)
			}
		})
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
				ID:          1001,
				IID:         42,
				Title:       "Test Issue",
				Description: "Issue description",
				State:       StateOpened,
				WebURL:      "https://gitlab.com/namespace/project/-/issues/42",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "404 Issue Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/issues/42") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			issue, err := client.GetIssue(context.Background(), 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetIssue() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && issue.IID != 42 {
				t.Errorf("issue.IID = %d, want 42", issue.IID)
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
				{IID: 1, Title: "Issue 1"},
				{IID: 2, Title: "Issue 2"},
			},
			wantErr:   false,
			wantCount: 2,
		},
		{
			name: "success - with labels",
			opts: &ListIssuesOptions{
				Labels: []string{"pilot", "bug"},
				State:  StateOpened,
			},
			statusCode: http.StatusOK,
			response: []*Issue{
				{IID: 1, Title: "Issue 1"},
			},
			wantErr:   false,
			wantCount: 1,
		},
		{
			name:       "unauthorized",
			opts:       nil,
			statusCode: http.StatusUnauthorized,
			response:   map[string]string{"message": "401 Unauthorized"},
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
				if !strings.Contains(r.URL.Path, "/issues") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			issues, err := client.ListIssues(context.Background(), tt.opts)

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

func TestAddIssueNote(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			body:       "Test comment",
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name:       "server error",
			body:       "Test comment",
			statusCode: http.StatusInternalServerError,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}

				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["body"] != tt.body {
					t.Errorf("unexpected comment body: %s", body["body"])
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					note := Note{
						ID:   123,
						Body: tt.body,
					}
					_ = json.NewEncoder(w).Encode(note)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			note, err := client.AddIssueNote(context.Background(), 42, tt.body)

			if (err != nil) != tt.wantErr {
				t.Errorf("AddIssueNote() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && note.ID != 123 {
				t.Errorf("note.ID = %d, want 123", note.ID)
			}
		})
	}
}

func TestCreateMergeRequest(t *testing.T) {
	tests := []struct {
		name       string
		input      *MergeRequestInput
		statusCode int
		wantErr    bool
	}{
		{
			name: "success",
			input: &MergeRequestInput{
				Title:        "Add new feature",
				Description:  "This MR adds a new feature",
				SourceBranch: "feature/new-feature",
				TargetBranch: "main",
			},
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name: "unprocessable entity - branch doesn't exist",
			input: &MergeRequestInput{
				Title:        "Add new feature",
				SourceBranch: "nonexistent-branch",
				TargetBranch: "main",
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
				if !strings.Contains(r.URL.Path, "/merge_requests") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body MergeRequestInput
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body.Title != tt.input.Title {
					t.Errorf("unexpected title: %s", body.Title)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode < 300 {
					result := MergeRequest{
						ID:           11111,
						IID:          42,
						Title:        body.Title,
						SourceBranch: body.SourceBranch,
						TargetBranch: body.TargetBranch,
						State:        MRStateOpened,
						WebURL:       "https://gitlab.com/namespace/project/-/merge_requests/42",
					}
					_ = json.NewEncoder(w).Encode(result)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			result, err := client.CreateMergeRequest(context.Background(), tt.input)

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateMergeRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.IID != 42 {
				t.Errorf("result.IID = %d, want 42", result.IID)
			}
		})
	}
}

func TestGetMergeRequest(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response: MergeRequest{
				ID:           11111,
				IID:          42,
				Title:        "Test MR",
				SourceBranch: "feature-branch",
				TargetBranch: "main",
				State:        MRStateOpened,
				WebURL:       "https://gitlab.com/namespace/project/-/merge_requests/42",
			},
			wantErr: false,
		},
		{
			name:       "not found",
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "404 Merge Request Not Found"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/merge_requests/42") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			result, err := client.GetMergeRequest(context.Background(), 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("GetMergeRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && result.IID != 42 {
				t.Errorf("result.IID = %d, want 42", result.IID)
			}
		})
	}
}

func TestMergeMergeRequest(t *testing.T) {
	tests := []struct {
		name       string
		squash     bool
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success - no squash",
			squash:     false,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "success - with squash",
			squash:     true,
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:       "not mergeable - conflicts",
			squash:     false,
			statusCode: http.StatusMethodNotAllowed,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("expected PUT, got %s", r.Method)
				}
				if !strings.Contains(r.URL.Path, "/merge_requests/42/merge") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}

				if body["squash"] != tt.squash {
					t.Errorf("unexpected squash: %v, want %v", body["squash"], tt.squash)
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					mr := MergeRequest{
						IID:   42,
						State: MRStateMerged,
					}
					_ = json.NewEncoder(w).Encode(mr)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			_, err := client.MergeMergeRequest(context.Background(), 42, tt.squash)

			if (err != nil) != tt.wantErr {
				t.Errorf("MergeMergeRequest() error = %v, wantErr %v", err, tt.wantErr)
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
				Labels: []string{"pilot", "bug"},
			},
			labelName: "pilot",
			want:      true,
		},
		{
			name: "has label - last",
			issue: &Issue{
				Labels: []string{"bug", "enhancement", "pilot"},
			},
			labelName: "pilot",
			want:      true,
		},
		{
			name: "does not have label",
			issue: &Issue{
				Labels: []string{"bug", "enhancement"},
			},
			labelName: "pilot",
			want:      false,
		},
		{
			name: "empty labels",
			issue: &Issue{
				Labels: []string{},
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

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled != false {
		t.Errorf("default Enabled = %v, want false", cfg.Enabled)
	}

	if cfg.BaseURL != "https://gitlab.com" {
		t.Errorf("default BaseURL = %s, want 'https://gitlab.com'", cfg.BaseURL)
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

	if cfg.Polling.Label != "pilot" {
		t.Errorf("default Polling.Label = %s, want 'pilot'", cfg.Polling.Label)
	}
}

func TestPriorityFromLabel(t *testing.T) {
	tests := []struct {
		label string
		want  Priority
	}{
		{"priority::urgent", PriorityUrgent},
		{"P0", PriorityUrgent},
		{"priority::high", PriorityHigh},
		{"P1", PriorityHigh},
		{"priority::medium", PriorityMedium},
		{"P2", PriorityMedium},
		{"priority::low", PriorityLow},
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

func TestStateConstants(t *testing.T) {
	if StateOpened != "opened" {
		t.Errorf("StateOpened = %s, want 'opened'", StateOpened)
	}
	if StateClosed != "closed" {
		t.Errorf("StateClosed = %s, want 'closed'", StateClosed)
	}
}

func TestMRStateConstants(t *testing.T) {
	if MRStateOpened != "opened" {
		t.Errorf("MRStateOpened = %s, want 'opened'", MRStateOpened)
	}
	if MRStateClosed != "closed" {
		t.Errorf("MRStateClosed = %s, want 'closed'", MRStateClosed)
	}
	if MRStateMerged != "merged" {
		t.Errorf("MRStateMerged = %s, want 'merged'", MRStateMerged)
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

func TestPipelineStatusConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{PipelinePending, "pending"},
		{PipelineRunning, "running"},
		{PipelineSuccess, "success"},
		{PipelineFailed, "failed"},
		{PipelineCanceled, "canceled"},
		{PipelineSkipped, "skipped"},
		{PipelineManual, "manual"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}
