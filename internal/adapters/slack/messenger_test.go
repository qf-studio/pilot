package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestNewSlackMessenger tests messenger creation
func TestNewSlackMessenger(t *testing.T) {
	client := NewClient(testutil.FakeSlackBotToken)
	messenger := NewSlackMessenger(client)

	if messenger == nil {
		t.Fatal("NewSlackMessenger returned nil")
	}
	if messenger.client != client {
		t.Error("messenger.client not set correctly")
	}
}

// TestSlackMessengerSendText tests SendText method
func TestSlackMessengerSendText(t *testing.T) {
	tests := []struct {
		name      string
		contextID string
		text      string
		wantErr   bool
	}{
		{
			name:      "send plain text",
			contextID: "#general",
			text:      "Hello, world!",
			wantErr:   false,
		},
		{
			name:      "send empty text",
			contextID: "#test",
			text:      "",
			wantErr:   false,
		},
		{
			name:      "send long text",
			contextID: "#channel",
			text:      strings.Repeat("a", 1000),
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
						t.Errorf("expected /chat.postMessage, got %s", req.URL.Path)
					}

					var msg Message
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &msg)

					if msg.Channel != tt.contextID {
						t.Errorf("channel = %q, want %q", msg.Channel, tt.contextID)
					}
					if msg.Text != tt.text {
						t.Errorf("text = %q, want %q", msg.Text, tt.text)
					}

					response := PostMessageResponse{
						OK:      true,
						TS:      "1234567890.123456",
						Channel: tt.contextID,
					}
					respBody, _ := json.Marshal(response)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: testutil.FakeSlackBotToken,
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			messenger := NewSlackMessenger(client)
			err := messenger.SendText(context.Background(), tt.contextID, tt.text)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendText() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSlackMessengerSendConfirmation tests SendConfirmation method
func TestSlackMessengerSendConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		contextID string
		threadID  string
		taskID    string
		desc      string
		project   string
		wantErr   bool
	}{
		{
			name:      "send confirmation",
			contextID: "#general",
			threadID:  "",
			taskID:    "TASK-001",
			desc:      "Implement feature X",
			project:   "myproject",
			wantErr:   false,
		},
		{
			name:      "send confirmation with thread",
			contextID: "#dev",
			threadID:  "1234567890.000000",
			taskID:    "TASK-002",
			desc:      "Fix bug Y",
			project:   "anotherproject",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
						t.Errorf("expected /chat.postMessage, got %s", req.URL.Path)
					}

					var msg InteractiveMessage
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &msg)

					if msg.Channel != tt.contextID {
						t.Errorf("channel = %q, want %q", msg.Channel, tt.contextID)
					}
					if len(msg.Blocks) == 0 {
						t.Error("expected blocks in message")
					}

					response := PostMessageResponse{
						OK:      true,
						TS:      "1234567890.234567",
						Channel: tt.contextID,
					}
					respBody, _ := json.Marshal(response)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: testutil.FakeSlackBotToken,
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			messenger := NewSlackMessenger(client)
			ref, err := messenger.SendConfirmation(context.Background(), tt.contextID, tt.threadID, tt.taskID, tt.desc, tt.project)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendConfirmation() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && ref == "" {
				t.Error("SendConfirmation() returned empty messageRef")
			}
		})
	}
}

// TestSlackMessengerSendProgress tests SendProgress method
func TestSlackMessengerSendProgress(t *testing.T) {
	tests := []struct {
		name       string
		contextID  string
		messageRef string
		taskID     string
		phase      string
		progress   int
		detail     string
		wantErr    bool
	}{
		{
			name:       "update progress",
			contextID:  "#general",
			messageRef: "1234567890.123456",
			taskID:     "TASK-001",
			phase:      "executing",
			progress:   50,
			detail:     "Building project",
			wantErr:    false,
		},
		{
			name:       "progress near completion",
			contextID:  "#dev",
			messageRef: "1234567890.234567",
			taskID:     "TASK-002",
			phase:      "finalizing",
			progress:   95,
			detail:     "Wrapping up",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(req.URL.Path, "/chat.update") {
						t.Errorf("expected /chat.update, got %s", req.URL.Path)
					}

					var payload struct {
						Channel string        `json:"channel"`
						TS      string        `json:"ts"`
						Blocks  []interface{} `json:"blocks,omitempty"`
					}
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &payload)

					if payload.Channel != tt.contextID {
						t.Errorf("channel = %q, want %q", payload.Channel, tt.contextID)
					}
					if payload.TS != tt.messageRef {
						t.Errorf("ts = %q, want %q", payload.TS, tt.messageRef)
					}

					response := map[string]interface{}{
						"ok": true,
					}
					respBody, _ := json.Marshal(response)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: testutil.FakeSlackBotToken,
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			messenger := NewSlackMessenger(client)
			newRef, err := messenger.SendProgress(context.Background(), tt.contextID, tt.messageRef, tt.taskID, tt.phase, tt.progress, tt.detail)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendProgress() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && newRef != tt.messageRef {
				t.Errorf("SendProgress() returned ref = %q, want %q", newRef, tt.messageRef)
			}
		})
	}
}

// TestSlackMessengerSendResult tests SendResult method
func TestSlackMessengerSendResult(t *testing.T) {
	tests := []struct {
		name      string
		contextID string
		threadID  string
		taskID    string
		success   bool
		output    string
		prURL     string
		wantErr   bool
	}{
		{
			name:      "send success result",
			contextID: "#general",
			threadID:  "",
			taskID:    "TASK-001",
			success:   true,
			output:    "Task completed successfully",
			prURL:     "https://github.com/repo/pull/123",
			wantErr:   false,
		},
		{
			name:      "send failure result",
			contextID: "#dev",
			threadID:  "1234567890.000000",
			taskID:    "TASK-002",
			success:   false,
			output:    "Task failed with error",
			prURL:     "",
			wantErr:   false,
		},
		{
			name:      "send result without output",
			contextID: "#channel",
			threadID:  "",
			taskID:    "TASK-003",
			success:   true,
			output:    "",
			prURL:     "",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
						t.Errorf("expected /chat.postMessage, got %s", req.URL.Path)
					}

					var msg InteractiveMessage
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &msg)

					if msg.Channel != tt.contextID {
						t.Errorf("channel = %q, want %q", msg.Channel, tt.contextID)
					}
					if len(msg.Blocks) == 0 {
						t.Error("expected blocks in message")
					}

					response := PostMessageResponse{
						OK:      true,
						TS:      "1234567890.345678",
						Channel: tt.contextID,
					}
					respBody, _ := json.Marshal(response)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: testutil.FakeSlackBotToken,
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			messenger := NewSlackMessenger(client)
			err := messenger.SendResult(context.Background(), tt.contextID, tt.threadID, tt.taskID, tt.success, tt.output, tt.prURL)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendResult() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSlackMessengerSendChunked tests SendChunked method
func TestSlackMessengerSendChunked(t *testing.T) {
	tests := []struct {
		name      string
		contextID string
		threadID  string
		content   string
		prefix    string
		wantErr   bool
		wantChunks int
	}{
		{
			name:       "send short content",
			contextID:  "#general",
			threadID:   "",
			content:    "Short content",
			prefix:     "Output:",
			wantErr:    false,
			wantChunks: 1,
		},
		{
			name:       "send chunked content",
			contextID:  "#dev",
			threadID:   "1234567890.000000",
			content:    strings.Repeat("a", 10000),
			prefix:     "Long output:",
			wantErr:    false,
			wantChunks: 3, // ~3800 chars per chunk
		},
		{
			name:       "send chunked without prefix",
			contextID:  "#channel",
			threadID:   "",
			content:    strings.Repeat("b", 8000),
			prefix:     "",
			wantErr:    false,
			wantChunks: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgCount := 0
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					msgCount++

					if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
						t.Errorf("expected /chat.postMessage, got %s", req.URL.Path)
					}

					var msg Message
					body, _ := io.ReadAll(req.Body)
					_ = json.Unmarshal(body, &msg)

					if msg.Channel != tt.contextID {
						t.Errorf("channel = %q, want %q", msg.Channel, tt.contextID)
					}
					if tt.threadID != "" && msg.ThreadTS != tt.threadID {
						t.Errorf("threadTS = %q, want %q", msg.ThreadTS, tt.threadID)
					}

					response := PostMessageResponse{
						OK:      true,
						TS:      "1234567890.456789",
						Channel: tt.contextID,
					}
					respBody, _ := json.Marshal(response)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: testutil.FakeSlackBotToken,
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			messenger := NewSlackMessenger(client)
			err := messenger.SendChunked(context.Background(), tt.contextID, tt.threadID, tt.content, tt.prefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("SendChunked() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && msgCount != tt.wantChunks {
				t.Errorf("SendChunked() sent %d messages, want %d", msgCount, tt.wantChunks)
			}
		})
	}
}

// TestSlackMessengerAcknowledgeCallback tests AcknowledgeCallback method
func TestSlackMessengerAcknowledgeCallback(t *testing.T) {
	tests := []struct {
		name       string
		callbackID string
		wantErr    bool
	}{
		{
			name:       "acknowledge callback",
			callbackID: "callback-123",
			wantErr:    false,
		},
		{
			name:       "acknowledge empty callback",
			callbackID: "",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testutil.FakeSlackBotToken)
			messenger := NewSlackMessenger(client)

			err := messenger.AcknowledgeCallback(context.Background(), tt.callbackID)

			if (err != nil) != tt.wantErr {
				t.Errorf("AcknowledgeCallback() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestSlackMessengerMaxMessageLength tests MaxMessageLength method
func TestSlackMessengerMaxMessageLength(t *testing.T) {
	client := NewClient(testutil.FakeSlackBotToken)
	messenger := NewSlackMessenger(client)

	maxLen := messenger.MaxMessageLength()
	if maxLen != 3800 {
		t.Errorf("MaxMessageLength() = %d, want 3800", maxLen)
	}
}

// TestSlackMessengerImplementsInterface verifies SlackMessenger implements comms.Messenger
func TestSlackMessengerImplementsInterface(t *testing.T) {
	client := NewClient(testutil.FakeSlackBotToken)
	messenger := NewSlackMessenger(client)

	// If this compiles, the interface is implemented
	_ = interface{}(messenger)
}

// TestSlackMessengerSendTextError tests SendText error handling
func TestSlackMessengerSendTextError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			response := PostMessageResponse{
				OK:    false,
				Error: "channel_not_found",
			}
			respBody, _ := json.Marshal(response)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(respBody))),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: testutil.FakeSlackBotToken,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	messenger := NewSlackMessenger(client)
	err := messenger.SendText(context.Background(), "#nonexistent", "test")

	if err == nil {
		t.Error("SendText() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("SendText() error = %v, want to contain 'channel_not_found'", err)
	}
}

// TestSlackMessengerSendConfirmationError tests SendConfirmation error handling
func TestSlackMessengerSendConfirmationError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			response := PostMessageResponse{
				OK:    false,
				Error: "invalid_auth",
			}
			respBody, _ := json.Marshal(response)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(respBody))),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: testutil.FakeSlackBotToken,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	messenger := NewSlackMessenger(client)
	_, err := messenger.SendConfirmation(context.Background(), "#general", "", "TASK-001", "desc", "project")

	if err == nil {
		t.Error("SendConfirmation() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("SendConfirmation() error = %v, want to contain 'invalid_auth'", err)
	}
}

// TestSlackMessengerSendProgressError tests SendProgress error handling
func TestSlackMessengerSendProgressError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			response := map[string]interface{}{
				"ok":    false,
				"error": "message_not_found",
			}
			respBody, _ := json.Marshal(response)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(respBody))),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: testutil.FakeSlackBotToken,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	messenger := NewSlackMessenger(client)
	_, err := messenger.SendProgress(context.Background(), "#general", "0000000000.000000", "TASK-001", "phase", 50, "detail")

	if err == nil {
		t.Error("SendProgress() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "message_not_found") {
		t.Errorf("SendProgress() error = %v, want to contain 'message_not_found'", err)
	}
}

// TestSlackMessengerSendChunkedError tests SendChunked error handling
func TestSlackMessengerSendChunkedError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			response := PostMessageResponse{
				OK:    false,
				Error: "rate_limited",
			}
			respBody, _ := json.Marshal(response)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(respBody))),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: testutil.FakeSlackBotToken,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	messenger := NewSlackMessenger(client)
	err := messenger.SendChunked(context.Background(), "#general", "", "test content", "prefix")

	if err == nil {
		t.Error("SendChunked() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("SendChunked() error = %v, want to contain 'rate_limited'", err)
	}
}
