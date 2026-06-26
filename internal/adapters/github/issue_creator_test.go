package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestNewIssueCreator_CreateIssue verifies the concrete comms.IssueCreator backed by
// CreatePilotIssue: exact projectPath match, correct owner/repo resolution, pilot label
// forwarded to the API, and returned HTMLURL.
func TestNewIssueCreator_CreateIssue(t *testing.T) {
	const wantURL = "https://github.com/owner/repo/issues/99"

	var gotLabels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		var input IssueInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		gotLabels = input.Labels
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		issue := Issue{Number: 99, HTMLURL: wantURL}
		_, _ = w.Write(mustMarshal(issue))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	creator := NewIssueCreator(client, AllowAllIssueRepos(),
		IssueCreatorEntry{ProjectPath: "/projects/myrepo", Owner: "owner", Repo: "repo"},
	)

	url, err := creator.CreateIssue(context.Background(), "/projects/myrepo", comms.IssueDraft{
		Title:  "feat(gateway): add rate limiting",
		Body:   "## Summary\nAdd rate limiting.",
		Labels: []string{"pilot"},
	})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if url != wantURL {
		t.Errorf("url = %q, want %q", url, wantURL)
	}
	found := false
	for _, l := range gotLabels {
		if l == "pilot" {
			found = true
		}
	}
	if !found {
		t.Errorf("pilot label not forwarded to API, got %v", gotLabels)
	}
}

// TestNewIssueCreator_FallbackToFirstEntry verifies that an unrecognised projectPath
// resolves to the first configured entry.
func TestNewIssueCreator_FallbackToFirstEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/first-owner/first-repo/issues" {
			http.Error(w, "wrong repo: "+r.URL.Path, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(mustMarshal(Issue{Number: 1, HTMLURL: "https://github.com/first-owner/first-repo/issues/1"}))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	creator := NewIssueCreator(client, AllowAllIssueRepos(),
		IssueCreatorEntry{ProjectPath: "/projects/first", Owner: "first-owner", Repo: "first-repo"},
		IssueCreatorEntry{ProjectPath: "/projects/second", Owner: "second-owner", Repo: "second-repo"},
	)

	_, err := creator.CreateIssue(context.Background(), "/projects/unknown", comms.IssueDraft{
		Title:  "fix(api): handle nil response",
		Body:   "body",
		Labels: []string{"pilot"},
	})
	if err != nil {
		t.Fatalf("CreateIssue fallback error: %v", err)
	}
}

// TestNewIssueCreator_NoRepos verifies that no configured entries returns an error.
func TestNewIssueCreator_NoRepos(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "http://localhost")
	creator := NewIssueCreator(client, AllowAllIssueRepos()) // no entries

	_, err := creator.CreateIssue(context.Background(), "/any", comms.IssueDraft{
		Title:  "feat(x): something",
		Body:   "body",
		Labels: []string{"pilot"},
	})
	if err == nil {
		t.Error("expected error for no configured repos")
	}
}

// TestNewIssueCreator_InvalidTitle verifies the conventional-commit guardrail fires.
func TestNewIssueCreator_InvalidTitle(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "http://localhost")
	creator := NewIssueCreator(client, AllowAllIssueRepos(),
		IssueCreatorEntry{Owner: "owner", Repo: "repo"},
	)

	_, err := creator.CreateIssue(context.Background(), "", comms.IssueDraft{
		Title:  "Add a new feature without conventional-commit prefix",
		Body:   "body",
		Labels: []string{"pilot"},
	})
	if err == nil {
		t.Error("expected conventional-commit validation error")
	}
}
