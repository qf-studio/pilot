package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func newTestNotifierServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := NewClient(server.URL, "user@example.com", "fake-api-token", PlatformCloud)
	return client, server
}

func TestNotifyTaskStarted_WithTransitionID(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1", Body: "ok"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "21", "31")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 API calls, got %d: %v", len(calls), calls)
	}
	if calls[0] != "POST /rest/api/3/issue/PROJ-42/transitions" {
		t.Errorf("first call should be transition, got %s", calls[0])
	}
	if calls[1] != "POST /rest/api/3/issue/PROJ-42/comment" {
		t.Errorf("second call should be comment, got %s", calls[1])
	}
}

func TestNotifyTaskStarted_WithoutTransitionID(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodGet:
			resp := TransitionsResponse{
				Transitions: []Transition{
					{ID: "21", Name: "Start Progress", To: Status{Name: "In Progress"}},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1", Body: "ok"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err != nil {
		t.Fatalf("NotifyTaskStarted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Should have: GET transitions, POST transition, POST comment
	if len(calls) != 3 {
		t.Fatalf("expected 3 API calls, got %d: %v", len(calls), calls)
	}
}

func TestNotifyTaskCompleted_Success(t *testing.T) {
	var mu sync.Mutex
	var commentBody string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Extract text from ADF
			if doc, ok := body["body"].(map[string]interface{}); ok {
				if content, ok := doc["content"].([]interface{}); ok && len(content) > 0 {
					if para, ok := content[0].(map[string]interface{}); ok {
						if inner, ok := para["content"].([]interface{}); ok && len(inner) > 0 {
							if text, ok := inner[0].(map[string]interface{}); ok {
								mu.Lock()
								commentBody = text["text"].(string)
								mu.Unlock()
							}
						}
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/transitions" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "31")
	err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "https://github.com/org/repo/pull/99", "Added feature X")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(commentBody, "pull/99") {
		t.Errorf("comment should contain PR URL, got: %s", commentBody)
	}
	if !strings.Contains(commentBody, "Added feature X") {
		t.Errorf("comment should contain summary, got: %s", commentBody)
	}
}

func TestNotifyTaskCompleted_NoPRURL(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comment"):
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// Extract text from ADF
			doc := body["body"].(map[string]interface{})
			content := doc["content"].([]interface{})
			para := content[0].(map[string]interface{})
			inner := para["content"].([]interface{})
			text := inner[0].(map[string]interface{})["text"].(string)
			if strings.Contains(text, "Pull Request") {
				t.Errorf("comment should not contain PR section when no URL, got: %s", text)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(TransitionsResponse{
					Transitions: []Transition{{ID: "31", Name: "Done", To: Status{Name: "Done"}}},
				})
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskCompleted(context.Background(), "PROJ-42", "", "")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted failed: %v", err)
	}
}

func TestNotifyTaskFailed(t *testing.T) {
	var commentPosted bool

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost {
			commentPosted = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskFailed(context.Background(), "PROJ-42", "build failed")
	if err != nil {
		t.Fatalf("NotifyTaskFailed failed: %v", err)
	}
	if !commentPosted {
		t.Error("expected comment to be posted")
	}
}

func TestNotifyTaskFailed_APIError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["Internal Server Error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyTaskFailed(context.Background(), "PROJ-42", "build failed")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
	if !strings.Contains(err.Error(), "failed to add failure comment") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNotifyProgress(t *testing.T) {
	tests := []struct {
		phase string
	}{
		{"exploring"},
		{"implementing"},
		{"testing"},
		{"committing"},
		{"unknown-phase"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			})
			defer server.Close()

			notifier := NewNotifier(client, "", "")
			err := notifier.NotifyProgress(context.Background(), "PROJ-42", tt.phase, "details here")
			if err != nil {
				t.Fatalf("NotifyProgress(%s) failed: %v", tt.phase, err)
			}
		})
	}
}

func TestNotifyProgress_APIError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.NotifyProgress(context.Background(), "PROJ-42", "impl", "details")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestLinkPR(t *testing.T) {
	var mu sync.Mutex
	var calls []string

	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/remotelink" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/rest/api/3/issue/PROJ-42/comment" && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Comment{ID: "1"})
		default:
			t.Errorf("unexpected: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.LinkPR(context.Background(), "PROJ-42", 99, "https://github.com/org/repo/pull/99")
	if err != nil {
		t.Fatalf("LinkPR failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (remotelink + comment), got %d: %v", len(calls), calls)
	}
}

func TestLinkPR_RemoteLinkError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
	})
	defer server.Close()

	notifier := NewNotifier(client, "", "")
	err := notifier.LinkPR(context.Background(), "PROJ-42", 99, "https://github.com/org/repo/pull/99")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "failed to add PR link") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNotifyTaskStarted_CommentError(t *testing.T) {
	client, server := newTestNotifierServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transitions"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/comment"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errorMessages":["error"]}`))
		}
	})
	defer server.Close()

	notifier := NewNotifier(client, "21", "")
	err := notifier.NotifyTaskStarted(context.Background(), "PROJ-42", "task-123")
	if err == nil {
		t.Fatal("expected error when comment fails")
	}
	if !strings.Contains(err.Error(), "failed to add start comment") {
		t.Errorf("unexpected error: %v", err)
	}
}
