package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/comms"
)

// TestIssueCreatorAdapter_CreateIssue_CallsGitHubAPI verifies that
// CreateIssue resolves the project path to owner/repo, validates the title,
// and calls the GitHub API.
func TestIssueCreatorAdapter_CreateIssue_CallsGitHubAPI(t *testing.T) {
	// Mock GitHub API server.
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"number": 42,
				"html_url": "https://github.com/owner/repo/issues/42",
				"title": "fix(auth): handle nil token",
				"state": "open"
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := NewClientWithBaseURL("test-token", srv.URL)

	projects := []ProjectEntry{{Path: "/my/project", Owner: "owner", Repo: "repo"}}
	adapter := NewIssueCreatorAdapter(client, AllowAllIssueRepos(), projects, "", "")

	draft := comms.IssueDraft{
		Title:  "fix(auth): handle nil token",
		Body:   "Nil token causes panic.",
		Labels: []string{"pilot"},
	}

	url, err := adapter.CreateIssue(context.Background(), "/my/project", draft)
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if url != "https://github.com/owner/repo/issues/42" {
		t.Errorf("url = %q, want issues/42", url)
	}

	// Verify labels were passed to the API.
	if labels, ok := capturedBody["labels"].([]interface{}); ok {
		found := false
		for _, l := range labels {
			if l == "pilot" {
				found = true
			}
		}
		if !found {
			t.Errorf("pilot label not sent to API, labels = %v", labels)
		}
	} else {
		t.Errorf("labels not in request body, body = %v", capturedBody)
	}
}

// TestIssueCreatorAdapter_ResolveRepo_Fallback verifies that when no project
// matches the path, the default owner/repo is used.
func TestIssueCreatorAdapter_ResolveRepo_Fallback(t *testing.T) {
	adapter := &IssueCreatorAdapter{
		projects:     []ProjectEntry{{Path: "/other", Owner: "other", Repo: "other"}},
		defaultOwner: "default-owner",
		defaultRepo:  "default-repo",
	}
	owner, repo := adapter.resolveRepo("/unknown/path")
	if owner != "default-owner" || repo != "default-repo" {
		t.Errorf("resolveRepo fallback: got %q/%q, want default-owner/default-repo", owner, repo)
	}
}

// TestIssueCreatorAdapter_ResolveRepo_Match verifies exact path matching.
func TestIssueCreatorAdapter_ResolveRepo_Match(t *testing.T) {
	adapter := &IssueCreatorAdapter{
		projects: []ProjectEntry{
			{Path: "/my/project", Owner: "my-org", Repo: "my-repo"},
		},
	}
	owner, repo := adapter.resolveRepo("/my/project")
	if owner != "my-org" || repo != "my-repo" {
		t.Errorf("resolveRepo: got %q/%q, want my-org/my-repo", owner, repo)
	}
}

// TestIssueCreatorAdapter_CreateIssue_NoRepo errors when no repo can be resolved.
func TestIssueCreatorAdapter_CreateIssue_NoRepo(t *testing.T) {
	adapter := &IssueCreatorAdapter{}
	_, err := adapter.CreateIssue(context.Background(), "/some/path", comms.IssueDraft{
		Title: "fix(x): y",
	})
	if err == nil {
		t.Error("expected error when no repo configured")
	}
}
