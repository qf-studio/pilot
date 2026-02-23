package asana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewNotifier(t *testing.T) {
	client := NewClient(testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
		return
	}
	if notifier.pilotTag != "pilot" {
		t.Errorf("notifier.pilotTag = %s, want pilot", notifier.pilotTag)
	}
}

func TestNotifyTaskStarted(t *testing.T) {
	var capturedComment string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data := body["data"].(map[string]interface{})
		capturedComment = data["text"].(string)

		resp := APIResponse[Story]{
			Data: Story{
				GID:  "story-1",
				Text: capturedComment,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskStarted(context.Background(), "123456", "PILOT-001")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pilot started working") {
		t.Errorf("comment should mention Pilot started, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "PILOT-001") {
		t.Errorf("comment should contain task ID, got: %s", capturedComment)
	}
}

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		phase     string
		wantEmoji string
	}{
		{"exploring", "🔍"},
		{"research", "🔍"},
		{"implementing", "🔨"},
		{"impl", "🔨"},
		{"testing", "🧪"},
		{"verify", "🧪"},
		{"committing", "📝"},
		{"reviewing", "👀"},
		{"unknown", "⏳"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			var capturedComment string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("failed to decode body: %v", err)
				}
				data := body["data"].(map[string]interface{})
				capturedComment = data["text"].(string)

				resp := APIResponse[Story]{
					Data: Story{GID: "story-1", Text: capturedComment},
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyProgress(context.Background(), "123456", tt.phase, "Progress details")
			if err != nil {
				t.Fatalf("NotifyProgress failed: %v", err)
			}

			if !strings.Contains(capturedComment, tt.wantEmoji) {
				t.Errorf("comment should contain emoji %s, got: %s", tt.wantEmoji, capturedComment)
			}
			if !strings.Contains(capturedComment, tt.phase) {
				t.Errorf("comment should contain phase %s, got: %s", tt.phase, capturedComment)
			}
		})
	}
}

func TestNotifyTaskCompleted(t *testing.T) {
	var capturedComment string
	var completeCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/tasks/") {
			// CompleteTask call
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode body: %v", err)
			}
			data := body["data"].(map[string]interface{})
			if data["completed"] == true {
				completeCalled = true
			}
			resp := APIResponse[Task]{
				Data: Task{GID: "123456", Completed: true},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Comment (story) request
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		data := body["data"].(map[string]interface{})
		capturedComment = data["text"].(string)

		resp := APIResponse[Story]{
			Data: Story{GID: "story-1", Text: capturedComment},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskCompleted(context.Background(), "123456", "https://github.com/owner/repo/pull/42", "Added feature X")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pilot completed") {
		t.Errorf("comment should mention completion, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "github.com/owner/repo/pull/42") {
		t.Errorf("comment should contain PR URL, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "Added feature X") {
		t.Errorf("comment should contain summary, got: %s", capturedComment)
	}
	if !completeCalled {
		t.Error("expected CompleteTask to be called on success path")
	}
}

func TestNotifyTaskCompleted_NoPR(t *testing.T) {
	var capturedComment string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			// CompleteTask call
			resp := APIResponse[Task]{
				Data: Task{GID: "123456", Completed: true},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		data := body["data"].(map[string]interface{})
		capturedComment = data["text"].(string)

		resp := APIResponse[Story]{
			Data: Story{GID: "story-1", Text: capturedComment},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskCompleted(context.Background(), "123456", "", "")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	if !strings.Contains(capturedComment, "Pilot completed") {
		t.Errorf("comment should mention completion, got: %s", capturedComment)
	}
}

func TestNotifyTaskCompleted_CompleteTaskErrorDoesNotFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			// CompleteTask fails
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
			return
		}

		// Comment succeeds
		resp := APIResponse[Story]{
			Data: Story{GID: "story-1", Text: "ok"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	// Should NOT return an error even though CompleteTask fails
	err := notifier.NotifyTaskCompleted(context.Background(), "123456", "https://github.com/pr/1", "summary")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted should succeed even if CompleteTask fails, got: %v", err)
	}
}

func TestNotifier_CompleteTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		data := body["data"].(map[string]interface{})
		if data["completed"] != true {
			t.Error("expected completed to be true")
		}

		resp := APIResponse[Task]{
			Data: Task{GID: "123456", Completed: true},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.CompleteTask(context.Background(), "123456")
	if err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}
}

func TestNotifyTaskFailed(t *testing.T) {
	var capturedComment string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}
		data := body["data"].(map[string]interface{})
		capturedComment = data["text"].(string)

		resp := APIResponse[Story]{
			Data: Story{GID: "story-1", Text: capturedComment},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskFailed(context.Background(), "123456", "Build failed with errors")
	if err != nil {
		t.Fatalf("NotifyTaskFailed failed: %v", err)
	}

	if !strings.Contains(capturedComment, "could not complete") {
		t.Errorf("comment should mention failure, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "Build failed with errors") {
		t.Errorf("comment should contain reason, got: %s", capturedComment)
	}
}

func TestLinkPR(t *testing.T) {
	requestCount := 0
	var capturedComment string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if strings.Contains(r.URL.Path, "/attachments") {
			// Attachment request
			resp := APIResponse[Attachment]{
				Data: Attachment{GID: "attach-1", Name: "PR #42"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.Contains(r.URL.Path, "/stories") {
			// Comment request
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode body: %v", err)
			}
			data := body["data"].(map[string]interface{})
			capturedComment = data["text"].(string)

			resp := APIResponse[Story]{
				Data: Story{GID: "story-1", Text: capturedComment},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		t.Errorf("unexpected request path: %s", r.URL.Path)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.LinkPR(context.Background(), "123456", 42, "https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("LinkPR failed: %v", err)
	}

	if requestCount < 2 {
		t.Errorf("expected at least 2 requests (attachment + comment), got %d", requestCount)
	}

	if !strings.Contains(capturedComment, "PR #42") {
		t.Errorf("comment should mention PR number, got: %s", capturedComment)
	}
	if !strings.Contains(capturedComment, "github.com/owner/repo/pull/42") {
		t.Errorf("comment should contain PR URL, got: %s", capturedComment)
	}
}

func TestRemovePilotTag(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if strings.Contains(r.URL.Path, "/tags") && r.Method == http.MethodGet {
			// Get workspace tags
			resp := PagedResponse[Tag]{
				Data: []Tag{
					{GID: "pilot-tag-1", Name: "pilot"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.Contains(r.URL.Path, "/removeTag") {
			// Remove tag request
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.RemovePilotTag(context.Background(), "123456")
	if err != nil {
		t.Fatalf("RemovePilotTag failed: %v", err)
	}

	if requestCount != 2 {
		t.Errorf("expected 2 requests (get tags + remove tag), got %d", requestCount)
	}
}

func TestRemovePilotTag_CachedGID(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if strings.Contains(r.URL.Path, "/removeTag") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")
	notifier.pilotTagGID = "cached-tag-gid" // Pre-cache the GID

	err := notifier.RemovePilotTag(context.Background(), "123456")
	if err != nil {
		t.Fatalf("RemovePilotTag failed: %v", err)
	}

	// Should only make the remove request, not the get tags request
	if requestCount != 1 {
		t.Errorf("expected 1 request (remove tag only), got %d", requestCount)
	}
}

func TestAddPilotTag(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if strings.Contains(r.URL.Path, "/workspaces/") && strings.Contains(r.URL.Path, "/tags") {
			// Get workspace tags
			resp := PagedResponse[Tag]{
				Data: []Tag{
					{GID: "pilot-tag-1", Name: "pilot"},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.Contains(r.URL.Path, "/addTag") {
			// Add tag request
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.AddPilotTag(context.Background(), "123456")
	if err != nil {
		t.Fatalf("AddPilotTag failed: %v", err)
	}

	if requestCount != 2 {
		t.Errorf("expected 2 requests (get tags + add tag), got %d", requestCount)
	}
}

func TestAddPilotTag_CreateNew(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		if strings.Contains(r.URL.Path, "/workspaces/") && strings.Contains(r.URL.Path, "/tags") {
			// Get workspace tags - return empty (tag doesn't exist)
			resp := PagedResponse[Tag]{
				Data: []Tag{},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/tags" && r.Method == http.MethodPost {
			// Create tag request
			resp := APIResponse[Tag]{
				Data: Tag{GID: "new-tag-1", Name: "pilot"},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if strings.Contains(r.URL.Path, "/addTag") {
			// Add tag request
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{}}`))
			return
		}

		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")

	err := notifier.AddPilotTag(context.Background(), "123456")
	if err != nil {
		t.Fatalf("AddPilotTag failed: %v", err)
	}

	// Should make: get tags, create tag, add tag
	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

func TestNotifierMethodSignatures(t *testing.T) {
	client := NewClient(testutil.FakeAsanaAccessToken, testutil.FakeAsanaWorkspaceID)
	notifier := NewNotifier(client, "pilot")
	ctx := context.Background()

	// Verify method signatures compile
	var err error

	err = notifier.NotifyTaskStarted(ctx, "123", "PILOT-001")
	_ = err

	err = notifier.NotifyProgress(ctx, "123", "implementing", "details")
	_ = err

	err = notifier.NotifyTaskCompleted(ctx, "123", "https://github.com/pr", "summary")
	_ = err

	err = notifier.CompleteTask(ctx, "123")
	_ = err

	err = notifier.NotifyTaskFailed(ctx, "123", "reason")
	_ = err

	err = notifier.LinkPR(ctx, "123", 42, "https://github.com/pr")
	_ = err

	err = notifier.RemovePilotTag(ctx, "123")
	_ = err

	err = notifier.AddPilotTag(ctx, "123")
	_ = err
}
