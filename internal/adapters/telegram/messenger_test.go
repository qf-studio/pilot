package telegram

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
var _ comms.Messenger = (*TelegramMessenger)(nil)

func newTestMessenger(t *testing.T, handler http.HandlerFunc) *TelegramMessenger {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.URL)
	return NewMessenger(client, true)
}

func okHandler(msgID int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(SendMessageResponse{
			OK:     true,
			Result: &Result{MessageID: msgID},
		})
	}
}

func TestTelegramMessenger_SendText(t *testing.T) {
	var captured string
	m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		captured = req.Text
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 1}})
	})

	err := m.SendText(context.Background(), "123", "hello world")
	if err != nil {
		t.Fatalf("SendText returned error: %v", err)
	}
	if captured != "hello world" {
		t.Errorf("expected text %q, got %q", "hello world", captured)
	}
}

func TestTelegramMessenger_SendConfirmation(t *testing.T) {
	m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify keyboard was sent
		body, _ := json.Marshal(map[string]interface{}{})
		_ = body
		var raw map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&raw)
		if raw["reply_markup"] == nil {
			t.Error("expected reply_markup in request")
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 42}})
	})

	ref, err := m.SendConfirmation(context.Background(), "123", "", "TASK-1", "Add feature", "/project")
	if err != nil {
		t.Fatalf("SendConfirmation returned error: %v", err)
	}
	if ref != "42" {
		t.Errorf("expected messageRef %q, got %q", "42", ref)
	}
}

func TestTelegramMessenger_SendProgress(t *testing.T) {
	var editedText string
	m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "editMessageText") {
			var raw map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&raw)
			editedText, _ = raw["text"].(string)
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 42}})
	})

	newRef, err := m.SendProgress(context.Background(), "123", "42", "TASK-1", "Implementing", 50, "writing code")
	if err != nil {
		t.Fatalf("SendProgress returned error: %v", err)
	}
	if newRef != "42" {
		t.Errorf("expected same messageRef %q, got %q", "42", newRef)
	}
	if editedText == "" {
		t.Error("expected edit request to be sent")
	}
	if !strings.Contains(editedText, "50%") {
		t.Errorf("expected progress text to contain '50%%', got %q", editedText)
	}
}

func TestTelegramMessenger_SendProgress_InvalidRef(t *testing.T) {
	m := newTestMessenger(t, okHandler(1))

	_, err := m.SendProgress(context.Background(), "123", "not-a-number", "TASK-1", "Testing", 10, "")
	if err == nil {
		t.Fatal("expected error for invalid messageRef")
	}
	if !strings.Contains(err.Error(), "parse message ref") {
		t.Errorf("expected parse error, got: %v", err)
	}
}

func TestTelegramMessenger_SendResult(t *testing.T) {
	tests := []struct {
		name    string
		success bool
		output  string
		prURL   string
		wantSub string
	}{
		{
			name:    "success with PR",
			success: true,
			output:  "All done",
			prURL:   "https://github.com/org/repo/pull/1",
			wantSub: "✅",
		},
		{
			name:    "failure no PR",
			success: false,
			output:  "build failed",
			prURL:   "",
			wantSub: "❌",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured string
			m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
				var req SendMessageRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				captured = req.Text
				_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 1}})
			})

			err := m.SendResult(context.Background(), "123", "", "TASK-1", tt.success, tt.output, tt.prURL)
			if err != nil {
				t.Fatalf("SendResult returned error: %v", err)
			}
			if !strings.Contains(captured, tt.wantSub) {
				t.Errorf("expected text to contain %q, got %q", tt.wantSub, captured)
			}
			if tt.prURL != "" && !strings.Contains(captured, tt.prURL) {
				t.Errorf("expected text to contain PR URL %q", tt.prURL)
			}
		})
	}
}

func TestTelegramMessenger_SendChunked(t *testing.T) {
	var messageCount int
	m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		messageCount++
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: int64(messageCount)}})
	})

	// Short content → single message
	messageCount = 0
	err := m.SendChunked(context.Background(), "123", "", "short content", "prefix")
	if err != nil {
		t.Fatalf("SendChunked returned error: %v", err)
	}
	if messageCount != 1 {
		t.Errorf("expected 1 message for short content, got %d", messageCount)
	}

	// Long content → multiple messages
	messageCount = 0
	longContent := strings.Repeat("x", 5000)
	err = m.SendChunked(context.Background(), "123", "", longContent, "")
	if err != nil {
		t.Fatalf("SendChunked returned error: %v", err)
	}
	if messageCount < 2 {
		t.Errorf("expected multiple messages for long content, got %d", messageCount)
	}
}

func TestTelegramMessenger_AcknowledgeCallback(t *testing.T) {
	var called bool
	m := newTestMessenger(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "answerCallbackQuery") {
			called = true
		}
		w.WriteHeader(http.StatusOK)
	})

	err := m.AcknowledgeCallback(context.Background(), "cb-123")
	if err != nil {
		t.Fatalf("AcknowledgeCallback returned error: %v", err)
	}
	if !called {
		t.Error("expected answerCallbackQuery to be called")
	}
}

func TestTelegramMessenger_MaxMessageLength(t *testing.T) {
	m := &TelegramMessenger{}
	if got := m.MaxMessageLength(); got != 4000 {
		t.Errorf("expected 4000, got %d", got)
	}
}
