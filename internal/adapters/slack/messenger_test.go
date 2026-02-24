package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/comms"
	"github.com/alekspetrov/pilot/internal/testutil"
)

// Compile-time interface check.
var _ comms.Messenger = (*SlackMessenger)(nil)

func newTestSlackMessenger(t *testing.T, handler http.HandlerFunc) *SlackMessenger {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewClientWithBaseURL(testutil.FakeSlackBotToken, srv.URL)
	return NewMessenger(client)
}

func slackOKHandler(ts string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(PostMessageResponse{
			OK:      true,
			TS:      ts,
			Channel: "C123",
		})
	}
}

func TestSlackMessenger_SendText(t *testing.T) {
	var captured string
	m := newTestSlackMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		var msg Message
		_ = json.NewDecoder(r.Body).Decode(&msg)
		captured = msg.Text
		_ = json.NewEncoder(w).Encode(PostMessageResponse{OK: true, TS: "1234.5678", Channel: msg.Channel})
	})

	err := m.SendText(context.Background(), "C123", "hello slack")
	if err != nil {
		t.Fatalf("SendText returned error: %v", err)
	}
	if captured != "hello slack" {
		t.Errorf("expected text %q, got %q", "hello slack", captured)
	}
}

func TestSlackMessenger_SendConfirmation(t *testing.T) {
	m := newTestSlackMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if raw["blocks"] == nil {
			t.Error("expected blocks in request")
		}
		_ = json.NewEncoder(w).Encode(PostMessageResponse{OK: true, TS: "1234.5678", Channel: "C123"})
	})

	ref, err := m.SendConfirmation(context.Background(), "C123", "", "TASK-1", "Add feature", "/project")
	if err != nil {
		t.Fatalf("SendConfirmation returned error: %v", err)
	}
	if ref != "1234.5678" {
		t.Errorf("expected messageRef %q, got %q", "1234.5678", ref)
	}
}

func TestSlackMessenger_SendProgress(t *testing.T) {
	var updatedText string
	m := newTestSlackMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat.update") {
			var raw map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&raw)
			updatedText, _ = raw["text"].(string)
		}
		_ = json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
	})

	newRef, err := m.SendProgress(context.Background(), "C123", "1234.5678", "TASK-1", "Implementing", 50, "writing code")
	if err != nil {
		t.Fatalf("SendProgress returned error: %v", err)
	}
	if newRef != "1234.5678" {
		t.Errorf("expected same messageRef %q, got %q", "1234.5678", newRef)
	}
	if !strings.Contains(updatedText, "50%") {
		t.Errorf("expected progress text to contain '50%%', got %q", updatedText)
	}
}

func TestSlackMessenger_SendResult(t *testing.T) {
	tests := []struct {
		name    string
		success bool
		output  string
		prURL   string
	}{
		{
			name:    "success with PR",
			success: true,
			output:  "All done",
			prURL:   "https://github.com/org/repo/pull/1",
		},
		{
			name:    "failure no PR",
			success: false,
			output:  "build failed",
			prURL:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestSlackMessenger(t, slackOKHandler("1234.5678"))

			err := m.SendResult(context.Background(), "C123", "", "TASK-1", tt.success, tt.output, tt.prURL)
			if err != nil {
				t.Fatalf("SendResult returned error: %v", err)
			}
		})
	}
}

func TestSlackMessenger_SendChunked(t *testing.T) {
	var messageCount int
	m := newTestSlackMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		messageCount++
		_ = json.NewEncoder(w).Encode(PostMessageResponse{OK: true, TS: "ts", Channel: "C123"})
	})

	// Short content → single message
	messageCount = 0
	err := m.SendChunked(context.Background(), "C123", "", "short content", "prefix")
	if err != nil {
		t.Fatalf("SendChunked returned error: %v", err)
	}
	if messageCount != 1 {
		t.Errorf("expected 1 message for short content, got %d", messageCount)
	}

	// Long content → multiple messages
	messageCount = 0
	longContent := strings.Repeat("x", 5000)
	err = m.SendChunked(context.Background(), "C123", "thread-ts", longContent, "")
	if err != nil {
		t.Fatalf("SendChunked returned error: %v", err)
	}
	if messageCount < 2 {
		t.Errorf("expected multiple messages for long content, got %d", messageCount)
	}
}

func TestSlackMessenger_SendChunked_WithThread(t *testing.T) {
	var capturedThreadTS string
	m := newTestSlackMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		var msg Message
		_ = json.NewDecoder(r.Body).Decode(&msg)
		capturedThreadTS = msg.ThreadTS
		_ = json.NewEncoder(w).Encode(PostMessageResponse{OK: true, TS: "ts", Channel: "C123"})
	})

	err := m.SendChunked(context.Background(), "C123", "parent-ts", "content", "")
	if err != nil {
		t.Fatalf("SendChunked returned error: %v", err)
	}
	if capturedThreadTS != "parent-ts" {
		t.Errorf("expected threadTS %q, got %q", "parent-ts", capturedThreadTS)
	}
}

func TestSlackMessenger_AcknowledgeCallback(t *testing.T) {
	m := &SlackMessenger{}
	err := m.AcknowledgeCallback(context.Background(), "cb-123")
	if err != nil {
		t.Fatalf("AcknowledgeCallback should be no-op, got error: %v", err)
	}
}

func TestSlackMessenger_MaxMessageLength(t *testing.T) {
	m := &SlackMessenger{}
	if got := m.MaxMessageLength(); got != 3800 {
		t.Errorf("expected 3800, got %d", got)
	}
}
