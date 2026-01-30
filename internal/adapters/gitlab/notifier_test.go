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

func TestNewNotifier(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	notifier := NewNotifier(client, "pilot")

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}

	if notifier.pilotLabel != "pilot" {
		t.Errorf("notifier.pilotLabel = %s, want 'pilot'", notifier.pilotLabel)
	}
}

func TestNotifyTaskStarted(t *testing.T) {
	// Track requests
	var labelsRequest map[string]interface{}
	var noteRequest map[string]string
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		switch {
		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodGet:
			// Get issue request for adding labels
			issue := Issue{IID: 42, Labels: []string{"bug"}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)

		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodPut:
			// Update labels request
			_ = json.NewDecoder(r.Body).Decode(&labelsRequest)
			w.WriteHeader(http.StatusOK)

		case strings.Contains(r.URL.Path, "/notes") && r.Method == http.MethodPost:
			// Add note request
			_ = json.NewDecoder(r.Body).Decode(&noteRequest)
			note := Note{ID: 123, Body: noteRequest["body"]}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(note)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskStarted(context.Background(), 42, "GL-42")
	if err != nil {
		t.Errorf("NotifyTaskStarted() error = %v", err)
	}

	// Verify labels were updated
	if labelsRequest == nil {
		t.Error("expected labels to be updated")
	}

	// Verify note was added with correct content
	if noteRequest == nil {
		t.Error("expected note to be added")
	}
	if !strings.Contains(noteRequest["body"], "Pilot started working") {
		t.Errorf("note body should contain 'Pilot started working', got: %s", noteRequest["body"])
	}
	if !strings.Contains(noteRequest["body"], "GL-42") {
		t.Errorf("note body should contain task ID 'GL-42', got: %s", noteRequest["body"])
	}
}

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		name      string
		phase     string
		details   string
		wantEmoji string
	}{
		{
			name:      "exploring phase",
			phase:     "exploring",
			details:   "Analyzing codebase",
			wantEmoji: "🔍",
		},
		{
			name:      "implementing phase",
			phase:     "implementing",
			details:   "Writing code",
			wantEmoji: "🔨",
		},
		{
			name:      "testing phase",
			phase:     "testing",
			details:   "Running tests",
			wantEmoji: "🧪",
		},
		{
			name:      "committing phase",
			phase:     "committing",
			details:   "Creating commit",
			wantEmoji: "📝",
		},
		{
			name:      "unknown phase",
			phase:     "unknown",
			details:   "Doing something",
			wantEmoji: "⏳",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var noteRequest map[string]string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/notes") && r.Method == http.MethodPost {
					_ = json.NewDecoder(r.Body).Decode(&noteRequest)
					note := Note{ID: 123}
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(note)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
			notifier := NewNotifier(client, "pilot")

			err := notifier.NotifyProgress(context.Background(), 42, tt.phase, tt.details)
			if err != nil {
				t.Errorf("NotifyProgress() error = %v", err)
			}

			if !strings.Contains(noteRequest["body"], tt.wantEmoji) {
				t.Errorf("note body should contain emoji %s, got: %s", tt.wantEmoji, noteRequest["body"])
			}
			if !strings.Contains(noteRequest["body"], tt.phase) {
				t.Errorf("note body should contain phase name, got: %s", noteRequest["body"])
			}
		})
	}
}

func TestNotifyTaskCompleted(t *testing.T) {
	var noteRequest map[string]string
	var labelsUpdated bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodGet:
			issue := Issue{IID: 42, Labels: []string{"pilot", "pilot-in-progress"}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)

		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodPut:
			labelsUpdated = true
			w.WriteHeader(http.StatusOK)

		case strings.Contains(r.URL.Path, "/notes") && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&noteRequest)
			note := Note{ID: 123}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(note)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskCompleted(context.Background(), 42, "https://gitlab.com/namespace/project/-/merge_requests/10", "Added new feature")
	if err != nil {
		t.Errorf("NotifyTaskCompleted() error = %v", err)
	}

	if !labelsUpdated {
		t.Error("expected labels to be updated")
	}

	if !strings.Contains(noteRequest["body"], "Pilot completed") {
		t.Errorf("note should contain completion message, got: %s", noteRequest["body"])
	}
	if !strings.Contains(noteRequest["body"], "merge_requests/10") {
		t.Errorf("note should contain MR URL, got: %s", noteRequest["body"])
	}
}

func TestNotifyTaskFailed(t *testing.T) {
	var noteRequest map[string]string
	var labelsUpdated bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodGet:
			issue := Issue{IID: 42, Labels: []string{"pilot", "pilot-in-progress"}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)

		case strings.Contains(r.URL.Path, "/issues/42") && r.Method == http.MethodPut:
			labelsUpdated = true
			w.WriteHeader(http.StatusOK)

		case strings.Contains(r.URL.Path, "/notes") && r.Method == http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&noteRequest)
			note := Note{ID: 123}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(note)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
	notifier := NewNotifier(client, "pilot")

	err := notifier.NotifyTaskFailed(context.Background(), 42, "Build failed with errors")
	if err != nil {
		t.Errorf("NotifyTaskFailed() error = %v", err)
	}

	if !labelsUpdated {
		t.Error("expected labels to be updated")
	}

	if !strings.Contains(noteRequest["body"], "could not complete") {
		t.Errorf("note should contain failure message, got: %s", noteRequest["body"])
	}
	if !strings.Contains(noteRequest["body"], "Build failed with errors") {
		t.Errorf("note should contain failure reason, got: %s", noteRequest["body"])
	}
}

func TestLinkMR(t *testing.T) {
	var noteRequest map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/notes") && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&noteRequest)
			note := Note{ID: 123}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(note)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitLabToken, "namespace/project", server.URL)
	notifier := NewNotifier(client, "pilot")

	err := notifier.LinkMR(context.Background(), 42, 10, "https://gitlab.com/namespace/project/-/merge_requests/10")
	if err != nil {
		t.Errorf("LinkMR() error = %v", err)
	}

	if !strings.Contains(noteRequest["body"], "!10") {
		t.Errorf("note should contain MR reference !10, got: %s", noteRequest["body"])
	}
	if !strings.Contains(noteRequest["body"], "merge_requests/10") {
		t.Errorf("note should contain MR URL, got: %s", noteRequest["body"])
	}
}
