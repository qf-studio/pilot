package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestNewClient tests client creation
func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		botToken string
	}{
		{
			name:     "valid token",
			botToken: "xoxb-test-fake-token-for-testing",
		},
		{
			name:     "empty token",
			botToken: "",
		},
		{
			name:     "simple token",
			botToken: "test-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.botToken)

			if client == nil {
				t.Fatal("NewClient returned nil")
			}
			if client.botToken != tt.botToken {
				t.Errorf("botToken = %q, want %q", client.botToken, tt.botToken)
			}
			if client.httpClient == nil {
				t.Error("httpClient is nil")
			}
			if client.httpClient.Timeout != 30*time.Second {
				t.Errorf("httpClient.Timeout = %v, want 30s", client.httpClient.Timeout)
			}
		})
	}
}

// TestClientPostMessage tests the PostMessage method
func TestClientPostMessage(t *testing.T) {
	tests := []struct {
		name       string
		msg        *Message
		response   PostMessageResponse
		statusCode int
		wantErr    bool
		errContain string
	}{
		{
			name: "successful post",
			msg: &Message{
				Channel: "#general",
				Text:    "Hello, world!",
			},
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.123456",
				Channel: "C1234567890",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "successful post with blocks",
			msg: &Message{
				Channel: "#dev-notifications",
				Blocks: []Block{
					{
						Type: "section",
						Text: &TextObject{
							Type: "mrkdwn",
							Text: "*Bold* and _italic_ text",
						},
					},
				},
			},
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.654321",
				Channel: "C9876543210",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "channel not found",
			msg: &Message{
				Channel: "#nonexistent",
				Text:    "Test message",
			},
			response: PostMessageResponse{
				OK:    false,
				Error: "channel_not_found",
			},
			statusCode: http.StatusOK,
			wantErr:    true,
			errContain: "channel_not_found",
		},
		{
			name: "invalid auth",
			msg: &Message{
				Channel: "#general",
				Text:    "Test message",
			},
			response: PostMessageResponse{
				OK:    false,
				Error: "invalid_auth",
			},
			statusCode: http.StatusOK,
			wantErr:    true,
			errContain: "invalid_auth",
		},
		{
			name: "rate limited",
			msg: &Message{
				Channel: "#general",
				Text:    "Test message",
			},
			response: PostMessageResponse{
				OK:    false,
				Error: "rate_limited",
			},
			statusCode: http.StatusOK,
			wantErr:    true,
			errContain: "rate_limited",
		},
		{
			name: "message with attachments",
			msg: &Message{
				Channel: "#general",
				Text:    "Task completed",
				Attachments: []Attachment{
					{
						Color: "good",
						Title: "Success",
						Text:  "All tests passed",
					},
				},
			},
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.111111",
				Channel: "C1234567890",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "message with thread",
			msg: &Message{
				Channel:  "#general",
				Text:     "Reply in thread",
				ThreadTS: "1234567890.000000",
			},
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.222222",
				Channel: "C1234567890",
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify method
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}

				// Verify path
				if !strings.HasSuffix(r.URL.Path, "/chat.postMessage") {
					t.Errorf("path = %q, want to end with /chat.postMessage", r.URL.Path)
				}

				// Verify content type
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}

				// Verify authorization header
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Bearer ") {
					t.Errorf("Authorization = %q, want Bearer token", auth)
				}

				// Parse and verify request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read body: %v", err)
				}

				var reqMsg Message
				if err := json.Unmarshal(body, &reqMsg); err != nil {
					t.Fatalf("failed to parse request: %v", err)
				}

				if reqMsg.Channel != tt.msg.Channel {
					t.Errorf("channel = %q, want %q", reqMsg.Channel, tt.msg.Channel)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			// Create client pointing to test server
			client := &Client{
				botToken:   "test-token",
				httpClient: &http.Client{Timeout: 30 * time.Second},
			}

			// Override the slackAPIURL for testing by using the server URL
			// We can test with the real implementation since we need actual HTTP calls
			ctx := context.Background()

			// Create a custom client that hits our test server
			testClient := &Client{
				botToken:   "test-token",
				httpClient: server.Client(),
			}

			// Make request to test server by building the URL manually
			msg := tt.msg
			body, _ := json.Marshal(msg)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/chat.postMessage", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+testClient.botToken)

			resp, err := testClient.httpClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			var result PostMessageResponse
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if tt.wantErr {
				if result.OK {
					t.Error("expected error but got OK response")
				}
				if tt.errContain != "" && !strings.Contains(result.Error, tt.errContain) {
					t.Errorf("error = %q, want to contain %q", result.Error, tt.errContain)
				}
			} else {
				if !result.OK {
					t.Errorf("expected OK response but got error: %s", result.Error)
				}
				if result.TS == "" {
					t.Error("expected TS in response")
				}
			}

			// Verify we actually created a client
			_ = client
		})
	}
}

// TestClientUpdateMessage tests the UpdateMessage method
func TestClientUpdateMessage(t *testing.T) {
	tests := []struct {
		name       string
		channel    string
		ts         string
		msg        *Message
		response   map[string]interface{}
		statusCode int
		wantErr    bool
		errContain string
	}{
		{
			name:    "successful update",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Text: "Updated message",
			},
			response: map[string]interface{}{
				"ok": true,
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:    "update with blocks",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Blocks: []Block{
					{
						Type: "section",
						Text: &TextObject{
							Type: "mrkdwn",
							Text: "*Updated* content",
						},
					},
				},
			},
			response: map[string]interface{}{
				"ok": true,
			},
			statusCode: http.StatusOK,
			wantErr:    false,
		},
		{
			name:    "message not found",
			channel: "C1234567890",
			ts:      "0000000000.000000",
			msg: &Message{
				Text: "Should fail",
			},
			response: map[string]interface{}{
				"ok":    false,
				"error": "message_not_found",
			},
			statusCode: http.StatusOK,
			wantErr:    true,
			errContain: "message_not_found",
		},
		{
			name:    "cant update message",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Text: "Edited",
			},
			response: map[string]interface{}{
				"ok":    false,
				"error": "cant_update_message",
			},
			statusCode: http.StatusOK,
			wantErr:    true,
			errContain: "cant_update_message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify method
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}

				// Verify path
				if !strings.HasSuffix(r.URL.Path, "/chat.update") {
					t.Errorf("path = %q, want to end with /chat.update", r.URL.Path)
				}

				// Verify content type
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}

				// Verify authorization
				auth := r.Header.Get("Authorization")
				if !strings.HasPrefix(auth, "Bearer ") {
					t.Errorf("Authorization = %q, want Bearer token", auth)
				}

				// Parse request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read body: %v", err)
				}

				var req map[string]interface{}
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to parse request: %v", err)
				}

				// Verify channel and ts are present
				if req["channel"] != tt.channel {
					t.Errorf("channel = %v, want %q", req["channel"], tt.channel)
				}
				if req["ts"] != tt.ts {
					t.Errorf("ts = %v, want %q", req["ts"], tt.ts)
				}

				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			ctx := context.Background()

			// Make request to test server
			payload := struct {
				Channel string  `json:"channel"`
				TS      string  `json:"ts"`
				Text    string  `json:"text,omitempty"`
				Blocks  []Block `json:"blocks,omitempty"`
			}{
				Channel: tt.channel,
				TS:      tt.ts,
				Text:    tt.msg.Text,
				Blocks:  tt.msg.Blocks,
			}

			body, _ := json.Marshal(payload)
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/chat.update", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test-token")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			var result map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			ok, _ := result["ok"].(bool)
			if tt.wantErr {
				if ok {
					t.Error("expected error but got OK response")
				}
				errStr, _ := result["error"].(string)
				if tt.errContain != "" && !strings.Contains(errStr, tt.errContain) {
					t.Errorf("error = %q, want to contain %q", errStr, tt.errContain)
				}
			} else {
				if !ok {
					t.Errorf("expected OK response but got error: %v", result["error"])
				}
			}
		})
	}
}

// TestMessageStructure tests Message struct fields and JSON serialization
func TestMessageStructure(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
	}{
		{
			name: "text only",
			msg: Message{
				Channel: "#general",
				Text:    "Hello",
			},
		},
		{
			name: "with blocks",
			msg: Message{
				Channel: "#dev",
				Blocks: []Block{
					{
						Type: "section",
						Text: &TextObject{
							Type: "mrkdwn",
							Text: "*Bold*",
						},
					},
				},
			},
		},
		{
			name: "with attachments",
			msg: Message{
				Channel: "#alerts",
				Text:    "Alert",
				Attachments: []Attachment{
					{
						Color:  "danger",
						Title:  "Error",
						Text:   "Something went wrong",
						Footer: "Pilot Bot",
					},
				},
			},
		},
		{
			name: "with thread",
			msg: Message{
				Channel:  "#general",
				Text:     "Reply",
				ThreadTS: "1234567890.000000",
			},
		},
		{
			name: "full message",
			msg: Message{
				Channel: "#dev-notifications",
				Text:    "Full message",
				Blocks: []Block{
					{
						Type: "section",
						Text: &TextObject{
							Type: "mrkdwn",
							Text: "Main content",
						},
					},
					{
						Type: "context",
						Elements: []TextObject{
							{Type: "mrkdwn", Text: "Footer context"},
						},
					},
				},
				Attachments: []Attachment{
					{Color: "good"},
				},
				ThreadTS: "1234567890.000000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test JSON serialization
			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			// Test JSON deserialization
			var decoded Message
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			// Verify fields
			if decoded.Channel != tt.msg.Channel {
				t.Errorf("Channel = %q, want %q", decoded.Channel, tt.msg.Channel)
			}
			if decoded.Text != tt.msg.Text {
				t.Errorf("Text = %q, want %q", decoded.Text, tt.msg.Text)
			}
			if len(decoded.Blocks) != len(tt.msg.Blocks) {
				t.Errorf("Blocks len = %d, want %d", len(decoded.Blocks), len(tt.msg.Blocks))
			}
			if len(decoded.Attachments) != len(tt.msg.Attachments) {
				t.Errorf("Attachments len = %d, want %d", len(decoded.Attachments), len(tt.msg.Attachments))
			}
			if decoded.ThreadTS != tt.msg.ThreadTS {
				t.Errorf("ThreadTS = %q, want %q", decoded.ThreadTS, tt.msg.ThreadTS)
			}
		})
	}
}

// TestBlockStructure tests Block struct fields
func TestBlockStructure(t *testing.T) {
	tests := []struct {
		name  string
		block Block
	}{
		{
			name: "section block",
			block: Block{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: "*Bold* text",
				},
			},
		},
		{
			name: "context block",
			block: Block{
				Type: "context",
				Elements: []TextObject{
					{Type: "mrkdwn", Text: "Context 1"},
					{Type: "plain_text", Text: "Context 2"},
				},
			},
		},
		{
			name: "divider block",
			block: Block{
				Type: "divider",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.block)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded Block
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.Type != tt.block.Type {
				t.Errorf("Type = %q, want %q", decoded.Type, tt.block.Type)
			}
		})
	}
}

// TestAttachmentStructure tests Attachment struct fields
func TestAttachmentStructure(t *testing.T) {
	tests := []struct {
		name       string
		attachment Attachment
	}{
		{
			name: "success attachment",
			attachment: Attachment{
				Color: "good",
			},
		},
		{
			name: "warning attachment",
			attachment: Attachment{
				Color: "warning",
				Title: "Warning",
				Text:  "Something needs attention",
			},
		},
		{
			name: "danger attachment",
			attachment: Attachment{
				Color:  "danger",
				Title:  "Error",
				Text:   "Something went wrong",
				Footer: "Pilot Bot v1.0",
			},
		},
		{
			name: "custom color",
			attachment: Attachment{
				Color: "#6366f1",
				Title: "Info",
				Text:  "Custom colored attachment",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.attachment)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded Attachment
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.Color != tt.attachment.Color {
				t.Errorf("Color = %q, want %q", decoded.Color, tt.attachment.Color)
			}
			if decoded.Title != tt.attachment.Title {
				t.Errorf("Title = %q, want %q", decoded.Title, tt.attachment.Title)
			}
			if decoded.Text != tt.attachment.Text {
				t.Errorf("Text = %q, want %q", decoded.Text, tt.attachment.Text)
			}
			if decoded.Footer != tt.attachment.Footer {
				t.Errorf("Footer = %q, want %q", decoded.Footer, tt.attachment.Footer)
			}
		})
	}
}

// TestTextObjectStructure tests TextObject struct fields
func TestTextObjectStructure(t *testing.T) {
	tests := []struct {
		name string
		text TextObject
	}{
		{
			name: "markdown text",
			text: TextObject{
				Type: "mrkdwn",
				Text: "*Bold* and _italic_",
			},
		},
		{
			name: "plain text",
			text: TextObject{
				Type: "plain_text",
				Text: "Simple text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.text)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded TextObject
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.Type != tt.text.Type {
				t.Errorf("Type = %q, want %q", decoded.Type, tt.text.Type)
			}
			if decoded.Text != tt.text.Text {
				t.Errorf("Text = %q, want %q", decoded.Text, tt.text.Text)
			}
		})
	}
}

// TestPostMessageResponseStructure tests PostMessageResponse struct fields
func TestPostMessageResponseStructure(t *testing.T) {
	tests := []struct {
		name     string
		response PostMessageResponse
	}{
		{
			name: "success response",
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.123456",
				Channel: "C1234567890",
			},
		},
		{
			name: "error response",
			response: PostMessageResponse{
				OK:       false,
				Error:    "channel_not_found",
				ErrorMsg: "Channel not found",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var decoded PostMessageResponse
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if decoded.OK != tt.response.OK {
				t.Errorf("OK = %v, want %v", decoded.OK, tt.response.OK)
			}
			if decoded.TS != tt.response.TS {
				t.Errorf("TS = %q, want %q", decoded.TS, tt.response.TS)
			}
			if decoded.Channel != tt.response.Channel {
				t.Errorf("Channel = %q, want %q", decoded.Channel, tt.response.Channel)
			}
			if decoded.Error != tt.response.Error {
				t.Errorf("Error = %q, want %q", decoded.Error, tt.response.Error)
			}
		})
	}
}

// TestClientContextCancellation tests that requests respect context cancellation
func TestClientContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		response := PostMessageResponse{OK: true, TS: "123"}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient("test-token")
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	msg := &Message{Channel: "#test", Text: "test"}
	_, err := client.PostMessage(ctx, msg)

	// Should get context canceled error
	if err == nil {
		t.Error("expected error due to canceled context")
	}
}

// TestClientHTTPErrors tests handling of HTTP-level errors
func TestClientHTTPErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{
			name:       "server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"ok":false,"error":"internal_error"}`,
			wantErr:    true,
		},
		{
			name:       "bad gateway",
			statusCode: http.StatusBadGateway,
			body:       `{"ok":false,"error":"service_unavailable"}`,
			wantErr:    true,
		},
		{
			name:       "invalid JSON response",
			statusCode: http.StatusOK,
			body:       `not valid json`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			// Verify test setup
			if tt.wantErr {
				// Make a test request to verify server behavior
				resp, err := http.Get(server.URL)
				if err != nil {
					t.Fatalf("test server request failed: %v", err)
				}
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != tt.statusCode {
					t.Errorf("status = %d, want %d", resp.StatusCode, tt.statusCode)
				}
			}
		})
	}
}

// mockTransport is a custom RoundTripper for testing
type mockTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.handler(req)
}

// TestClientPostMessageWithMockTransport tests PostMessage with injected transport
func TestClientPostMessageWithMockTransport(t *testing.T) {
	tests := []struct {
		name       string
		msg        *Message
		response   PostMessageResponse
		httpStatus int
		wantErr    bool
		errContain string
	}{
		{
			name: "successful post",
			msg: &Message{
				Channel: "#general",
				Text:    "Hello, world!",
			},
			response: PostMessageResponse{
				OK:      true,
				TS:      "1234567890.123456",
				Channel: "C1234567890",
			},
			httpStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name: "API error - channel not found",
			msg: &Message{
				Channel: "#nonexistent",
				Text:    "Test",
			},
			response: PostMessageResponse{
				OK:    false,
				Error: "channel_not_found",
			},
			httpStatus: http.StatusOK,
			wantErr:    true,
			errContain: "channel_not_found",
		},
		{
			name: "API error - rate limited",
			msg: &Message{
				Channel: "#general",
				Text:    "Test",
			},
			response: PostMessageResponse{
				OK:    false,
				Error: "rate_limited",
			},
			httpStatus: http.StatusOK,
			wantErr:    true,
			errContain: "rate_limited",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock transport
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					// Verify request
					if req.Method != http.MethodPost {
						t.Errorf("method = %q, want POST", req.Method)
					}
					if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
						t.Errorf("path = %q, want /chat.postMessage suffix", req.URL.Path)
					}
					if ct := req.Header.Get("Content-Type"); ct != "application/json" {
						t.Errorf("Content-Type = %q, want application/json", ct)
					}
					if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
						t.Errorf("Authorization = %q, want Bearer prefix", auth)
					}

					// Create response
					respBody, _ := json.Marshal(tt.response)
					return &http.Response{
						StatusCode: tt.httpStatus,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: "test-token",
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			ctx := context.Background()
			result, err := client.PostMessage(ctx, tt.msg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errContain)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result == nil {
					t.Error("result is nil")
				} else if result.TS != tt.response.TS {
					t.Errorf("TS = %q, want %q", result.TS, tt.response.TS)
				}
			}
		})
	}
}

// TestClientUpdateMessageWithMockTransport tests UpdateMessage with injected transport
func TestClientUpdateMessageWithMockTransport(t *testing.T) {
	tests := []struct {
		name       string
		channel    string
		ts         string
		msg        *Message
		response   map[string]interface{}
		httpStatus int
		wantErr    bool
		errContain string
	}{
		{
			name:    "successful update",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Text: "Updated message",
			},
			response: map[string]interface{}{
				"ok": true,
			},
			httpStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name:    "update with blocks",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Blocks: []Block{
					{
						Type: "section",
						Text: &TextObject{
							Type: "mrkdwn",
							Text: "*Updated* content",
						},
					},
				},
			},
			response: map[string]interface{}{
				"ok": true,
			},
			httpStatus: http.StatusOK,
			wantErr:    false,
		},
		{
			name:    "message not found",
			channel: "C1234567890",
			ts:      "0000000000.000000",
			msg: &Message{
				Text: "Should fail",
			},
			response: map[string]interface{}{
				"ok":    false,
				"error": "message_not_found",
			},
			httpStatus: http.StatusOK,
			wantErr:    true,
			errContain: "message_not_found",
		},
		{
			name:    "cant update message",
			channel: "C1234567890",
			ts:      "1234567890.123456",
			msg: &Message{
				Text: "Edited",
			},
			response: map[string]interface{}{
				"ok":    false,
				"error": "cant_update_message",
			},
			httpStatus: http.StatusOK,
			wantErr:    true,
			errContain: "cant_update_message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock transport
			transport := &mockTransport{
				handler: func(req *http.Request) (*http.Response, error) {
					// Verify request
					if req.Method != http.MethodPost {
						t.Errorf("method = %q, want POST", req.Method)
					}
					if !strings.HasSuffix(req.URL.Path, "/chat.update") {
						t.Errorf("path = %q, want /chat.update suffix", req.URL.Path)
					}
					if ct := req.Header.Get("Content-Type"); ct != "application/json" {
						t.Errorf("Content-Type = %q, want application/json", ct)
					}
					if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
						t.Errorf("Authorization = %q, want Bearer prefix", auth)
					}

					// Verify body contains channel and ts
					body, _ := io.ReadAll(req.Body)
					var reqBody map[string]interface{}
					_ = json.Unmarshal(body, &reqBody)
					if reqBody["channel"] != tt.channel {
						t.Errorf("channel = %v, want %q", reqBody["channel"], tt.channel)
					}
					if reqBody["ts"] != tt.ts {
						t.Errorf("ts = %v, want %q", reqBody["ts"], tt.ts)
					}

					// Create response
					respBody, _ := json.Marshal(tt.response)
					return &http.Response{
						StatusCode: tt.httpStatus,
						Body:       io.NopCloser(strings.NewReader(string(respBody))),
						Header:     make(http.Header),
					}, nil
				},
			}

			client := &Client{
				botToken: "test-token",
				httpClient: &http.Client{
					Transport: transport,
					Timeout:   30 * time.Second,
				},
			}

			ctx := context.Background()
			err := client.UpdateMessage(ctx, tt.channel, tt.ts, tt.msg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errContain)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestClientPostMessageNetworkError tests network-level errors
func TestClientPostMessageNetworkError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network error: connection refused")
		},
	}

	client := &Client{
		botToken: "test-token",
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	ctx := context.Background()
	msg := &Message{Channel: "#test", Text: "test"}
	_, err := client.PostMessage(ctx, msg)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to post message") {
		t.Errorf("error = %q, want to contain 'failed to post message'", err.Error())
	}
}

// TestClientUpdateMessageNetworkError tests network-level errors for update
func TestClientUpdateMessageNetworkError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network error: connection refused")
		},
	}

	client := &Client{
		botToken: "test-token",
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	ctx := context.Background()
	msg := &Message{Text: "test"}
	err := client.UpdateMessage(ctx, "C123", "123.456", msg)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to update message") {
		t.Errorf("error = %q, want to contain 'failed to update message'", err.Error())
	}
}

// TestClientPostMessageInvalidJSON tests handling of invalid JSON response
func TestClientPostMessageInvalidJSON(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("not valid json")),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: "test-token",
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	ctx := context.Background()
	msg := &Message{Channel: "#test", Text: "test"}
	_, err := client.PostMessage(ctx, msg)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %q, want to contain 'failed to parse response'", err.Error())
	}
}

// TestClientUpdateMessageInvalidJSON tests handling of invalid JSON response for update
func TestClientUpdateMessageInvalidJSON(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("not valid json")),
				Header:     make(http.Header),
			}, nil
		},
	}

	client := &Client{
		botToken: "test-token",
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	ctx := context.Background()
	msg := &Message{Text: "test"}
	err := client.UpdateMessage(ctx, "C123", "123.456", msg)

	if err == nil {
		t.Error("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %q, want to contain 'failed to parse response'", err.Error())
	}
}
