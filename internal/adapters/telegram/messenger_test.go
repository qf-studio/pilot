package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTelegramMessenger_NewTelegramMessenger(t *testing.T) {
	client := NewClient("test-token")

	tests := []struct {
		name          string
		plainTextMode bool
		expectedMode  string
	}{
		{
			name:          "markdown mode",
			plainTextMode: false,
			expectedMode:  "MarkdownV2",
		},
		{
			name:          "plain text mode",
			plainTextMode: true,
			expectedMode:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := NewTelegramMessenger(client, tt.plainTextMode)
			if messenger == nil {
				t.Fatal("NewTelegramMessenger returned nil")
			}
			if messenger.getParseMode() != tt.expectedMode {
				t.Errorf("getParseMode() = %q, want %q", messenger.getParseMode(), tt.expectedMode)
			}
		})
	}
}

func TestTelegramMessenger_MaxMessageLength(t *testing.T) {
	client := NewClient("test-token")
	messenger := NewTelegramMessenger(client, false)

	if messenger.MaxMessageLength() != 4000 {
		t.Errorf("MaxMessageLength() = %d, want 4000", messenger.MaxMessageLength())
	}
}

func TestTelegramMessenger_SendText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"message_id": 12345,
				"chat_id": 67890,
				"date": 1234567890,
				"text": "test message"
			}
		}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)
	messenger := NewTelegramMessenger(client, false)

	err := messenger.SendText(context.Background(), "67890", "test message")
	if err != nil {
		t.Fatalf("SendText() error = %v", err)
	}
}

func TestTelegramMessenger_SendConfirmation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"message_id": 54321,
				"chat_id": 67890
			}
		}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)
	messenger := NewTelegramMessenger(client, false)

	messageRef, err := messenger.SendConfirmation(context.Background(), "67890", "", "TASK-001", "Test task", "")
	if err != nil {
		t.Fatalf("SendConfirmation() error = %v", err)
	}

	if messageRef != "54321" {
		t.Errorf("SendConfirmation() messageRef = %q, want %q", messageRef, "54321")
	}
}

func TestTelegramMessenger_SendProgress(t *testing.T) {
	t.Run("new message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ok": true,
				"result": {
					"message_id": 99999,
					"chat_id": 67890
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithBaseURL("test-token", server.URL)
		messenger := NewTelegramMessenger(client, false)

		messageRef, err := messenger.SendProgress(context.Background(), "67890", "", "TASK-001", "Implementing", 50, "Working...")
		if err != nil {
			t.Fatalf("SendProgress() error = %v", err)
		}

		if messageRef != "99999" {
			t.Errorf("SendProgress() messageRef = %q, want %q", messageRef, "99999")
		}
	})

	t.Run("update existing message", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ok": true,
				"result": {
					"message_id": 88888,
					"chat_id": 67890
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithBaseURL("test-token", server.URL)
		messenger := NewTelegramMessenger(client, false)

		messageRef, err := messenger.SendProgress(context.Background(), "67890", "88888", "TASK-001", "Testing", 75, "Running tests...")
		if err != nil {
			t.Fatalf("SendProgress() error = %v", err)
		}

		if messageRef != "88888" {
			t.Errorf("SendProgress() messageRef = %q, want %q", messageRef, "88888")
		}
	})
}

func TestTelegramMessenger_SendResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"ok": true,
			"result": {
				"message_id": 77777,
				"chat_id": 67890
			}
		}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)
	messenger := NewTelegramMessenger(client, false)

	err := messenger.SendResult(context.Background(), "67890", "", "TASK-001", true, "Task completed successfully", "")
	if err != nil {
		t.Fatalf("SendResult() error = %v", err)
	}
}

func TestTelegramMessenger_SendChunked(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		messageID := 10000 + callCount
		_, _ = fmt.Fprintf(w, `{
			"ok": true,
			"result": {
				"message_id": %d,
				"chat_id": 67890
			}
		}`, messageID)
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)
	messenger := NewTelegramMessenger(client, false)

	// Create a message longer than 4000 characters
	longMessage := ""
	for len(longMessage) < 5000 {
		longMessage += "This is a test message that is repeated to make it longer. "
	}

	err := messenger.SendChunked(context.Background(), "67890", "", longMessage, "")
	if err != nil {
		t.Fatalf("SendChunked() error = %v", err)
	}

	if callCount < 2 {
		t.Errorf("SendChunked() should have made at least 2 API calls, got %d", callCount)
	}
}

func TestTelegramMessenger_AcknowledgeCallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"ok": true
		}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)
	messenger := NewTelegramMessenger(client, false)

	err := messenger.AcknowledgeCallback(context.Background(), "callback-id-123")
	if err != nil {
		t.Fatalf("AcknowledgeCallback() error = %v", err)
	}
}

func TestTelegramMessenger_PlainTextMode(t *testing.T) {
	t.Run("plaintext mode disabled sends MarkdownV2", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ok": true,
				"result": {
					"message_id": 11111,
					"chat_id": 67890
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithBaseURL("test-token", server.URL)
		messenger := NewTelegramMessenger(client, false)

		if messenger.getParseMode() != "MarkdownV2" {
			t.Errorf("plainTextMode=false: getParseMode() = %q, want MarkdownV2", messenger.getParseMode())
		}

		err := messenger.SendText(context.Background(), "67890", "test")
		if err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
	})

	t.Run("plaintext mode enabled sends empty parse mode", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"ok": true,
				"result": {
					"message_id": 22222,
					"chat_id": 67890
				}
			}`))
		}))
		defer server.Close()

		client := NewClientWithBaseURL("test-token", server.URL)
		messenger := NewTelegramMessenger(client, true)

		if messenger.getParseMode() != "" {
			t.Errorf("plainTextMode=true: getParseMode() = %q, want empty string", messenger.getParseMode())
		}

		err := messenger.SendText(context.Background(), "67890", "test")
		if err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
	})
}
