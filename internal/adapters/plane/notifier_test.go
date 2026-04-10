package plane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestNewNotifier(t *testing.T) {
	client := NewClient("https://plane.example.com", testutil.FakePlaneAPIKey)
	notifier := NewNotifier(client, "test-workspace")

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if notifier.workspaceSlug != "test-workspace" {
		t.Errorf("workspaceSlug = %q, want %q", notifier.workspaceSlug, "test-workspace")
	}
}

func TestPlaneNotifyTaskStarted(t *testing.T) {
	var capturedComment string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		// Verify path contains workspace/project/work-item
		if !strings.Contains(r.URL.Path, "ws/projects/proj-1/work-items/wi-42/comments") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		capturedComment = body["comment_html"]

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	notifier := NewNotifier(client, "ws")

	err := notifier.NotifyTaskStarted(context.Background(), "proj-1", "wi-42", "PLANE-abcd1234")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pilot started working") {
		t.Errorf("comment should mention Pilot started, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "PLANE-abcd1234") {
		t.Errorf("comment should contain task ID, got: %s", capturedComment)
	}
}

func TestPlaneNotifyTaskCompleted(t *testing.T) {
	var capturedComment string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		capturedComment = body["comment_html"]

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	notifier := NewNotifier(client, "ws")

	err := notifier.NotifyTaskCompleted(context.Background(), "proj-1", "wi-42", "https://github.com/org/repo/pull/5", "Added feature X")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pilot completed") {
		t.Errorf("comment should mention completion, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "pull/5") {
		t.Errorf("comment should contain PR URL, got: %s", capturedComment)
	}
}

func TestPlaneNotifyTaskFailed(t *testing.T) {
	var capturedComment string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		capturedComment = body["comment_html"]

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	notifier := NewNotifier(client, "ws")

	err := notifier.NotifyTaskFailed(context.Background(), "proj-1", "wi-42", "build failed")
	if err != nil {
		t.Fatalf("NotifyTaskFailed failed: %v", err)
	}

	if !strings.Contains(capturedComment, "could not complete") {
		t.Errorf("comment should mention failure, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "build failed") {
		t.Errorf("comment should contain reason, got: %s", capturedComment)
	}
}

func TestPlaneLinkPR(t *testing.T) {
	var capturedComment string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		capturedComment = body["comment_html"]

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL, testutil.FakePlaneAPIKey)
	notifier := NewNotifier(client, "ws")

	err := notifier.LinkPR(context.Background(), "proj-1", "wi-42", 5, "https://github.com/org/repo/pull/5")
	if err != nil {
		t.Fatalf("LinkPR failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pull Request Created") {
		t.Errorf("comment should mention PR, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "PR #5") {
		t.Errorf("comment should contain PR number, got: %s", capturedComment)
	}
}
