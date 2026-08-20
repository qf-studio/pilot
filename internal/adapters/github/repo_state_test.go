package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestFileExistsOnDefaultBranch_Exists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/contents/README.md" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("expected no ref query param on default branch, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  "aGVsbG8=",
			"encoding": "base64",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.FileExistsOnDefaultBranch(context.Background(), "owner", "repo", "README.md")
	if err != nil {
		t.Fatalf("FileExistsOnDefaultBranch() error = %v", err)
	}
	if !got {
		t.Errorf("FileExistsOnDefaultBranch() = false, want true")
	}
}

func TestFileExistsOnDefaultBranch_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.FileExistsOnDefaultBranch(context.Background(), "owner", "repo", "missing.md")
	if err != nil {
		t.Fatalf("FileExistsOnDefaultBranch() error = %v, want nil (404 should be absorbed)", err)
	}
	if got {
		t.Errorf("FileExistsOnDefaultBranch() = true, want false for 404 response")
	}
}

func TestFileExistsOnDefaultBranch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Internal Server Error"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.FileExistsOnDefaultBranch(context.Background(), "owner", "repo", "README.md")
	if err == nil {
		t.Fatal("FileExistsOnDefaultBranch() error = nil, want error for 500 response")
	}
	if got {
		t.Errorf("FileExistsOnDefaultBranch() = true, want false alongside a propagated error")
	}
}

func TestIssueOrPRState_PlainIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"number": 42,
			"state":  "open",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	kind, state, err := client.IssueOrPRState(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("IssueOrPRState() error = %v", err)
	}
	if kind != "issue" || state != "open" {
		t.Errorf("IssueOrPRState() = (%q, %q), want (\"issue\", \"open\")", kind, state)
	}
}

func TestIssueOrPRState_OpenPR(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues/7":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number":       7,
				"state":        "open",
				"pull_request": map[string]interface{}{},
			})
		case "/repos/owner/repo/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 7,
				"state":  "open",
				"merged": false,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	kind, state, err := client.IssueOrPRState(context.Background(), "owner", "repo", 7)
	if err != nil {
		t.Fatalf("IssueOrPRState() error = %v", err)
	}
	if kind != "pr" || state != "open" {
		t.Errorf("IssueOrPRState() = (%q, %q), want (\"pr\", \"open\")", kind, state)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 requests (issues then pulls), got %v", calls)
	}
}

func TestIssueOrPRState_MergedPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues/9":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number":       9,
				"state":        "closed",
				"pull_request": map[string]interface{}{},
			})
		case "/repos/owner/repo/pulls/9":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 9,
				"state":  "closed",
				"merged": true,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	kind, state, err := client.IssueOrPRState(context.Background(), "owner", "repo", 9)
	if err != nil {
		t.Fatalf("IssueOrPRState() error = %v", err)
	}
	if kind != "pr" || state != "merged" {
		t.Errorf("IssueOrPRState() = (%q, %q), want (\"pr\", \"merged\")", kind, state)
	}
}

func TestIssueOrPRState_ClosedUnmergedPR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues/11":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number":       11,
				"state":        "closed",
				"pull_request": map[string]interface{}{},
			})
		case "/repos/owner/repo/pulls/11":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 11,
				"state":  "closed",
				"merged": false,
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	kind, state, err := client.IssueOrPRState(context.Background(), "owner", "repo", 11)
	if err != nil {
		t.Fatalf("IssueOrPRState() error = %v", err)
	}
	if kind != "pr" || state != "closed" {
		t.Errorf("IssueOrPRState() = (%q, %q), want (\"pr\", \"closed\")", kind, state)
	}
}

func TestIssueOrPRState_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_, _, err := client.IssueOrPRState(context.Background(), "owner", "repo", 999)
	if err == nil {
		t.Fatal("IssueOrPRState() error = nil, want error for 404 response")
	}
}
