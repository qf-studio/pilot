package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestGetFileContent_Success(t *testing.T) {
	want := "package main\n\nfunc main() {}\n"
	encoded := "cGFja2FnZSBtYWlu\nCgpmdW5jIG1haW4oKSB7fQo=\n" // base64, wrapped like GitHub does

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/contents/main.go" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if got := r.URL.Query().Get("ref"); got != "main" {
			t.Errorf("ref query param = %q, want %q", got, "main")
		}
		if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  encoded,
			"encoding": "base64",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.GetFileContent(context.Background(), "owner", "repo", "main.go", "main")
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
	if got != want {
		t.Errorf("GetFileContent() = %q, want %q", got, want)
	}
}

func TestGetFileContent_EmptyRef(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("expected no query string when ref is empty, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  "aGVsbG8=",
			"encoding": "base64",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	got, err := client.GetFileContent(context.Background(), "owner", "repo", "hello.txt", "")
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
	if got != "hello" {
		t.Errorf("GetFileContent() = %q, want %q", got, "hello")
	}
}

func TestGetFileContent_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_, err := client.GetFileContent(context.Background(), "owner", "repo", "missing.go", "main")
	if err == nil {
		t.Fatal("GetFileContent() error = nil, want error for 404 response")
	}
}

func TestGetFileContent_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Internal Server Error"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_, err := client.GetFileContent(context.Background(), "owner", "repo", "main.go", "main")
	if err == nil {
		t.Fatal("GetFileContent() error = nil, want error for 500 response")
	}
}

func TestGetFileContent_MalformedBase64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  "not-valid-base64!!!",
			"encoding": "base64",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	_, err := client.GetFileContent(context.Background(), "owner", "repo", "main.go", "main")
	if err == nil {
		t.Fatal("GetFileContent() error = nil, want error for malformed base64 payload")
	}
}
