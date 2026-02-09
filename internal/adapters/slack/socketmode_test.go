package slack

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestOpenConnection(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantURL    string
		wantErr    error
		wantErrMsg string
	}{
		{
			name:       "successful connection returns WSS URL",
			statusCode: http.StatusOK,
			response:   `{"ok":true,"url":"wss://wss-primary.slack.com/link/?ticket=abc123"}`,
			wantURL:    "wss://wss-primary.slack.com/link/?ticket=abc123",
		},
		{
			name:       "invalid auth error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"invalid_auth"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "invalid_auth",
		},
		{
			name:       "not authed error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"not_authed"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "not_authed",
		},
		{
			name:       "token revoked error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"token_revoked"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "token_revoked",
		},
		{
			name:       "account inactive error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"account_inactive"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "account_inactive",
		},
		{
			name:       "non-auth API error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"too_many_websockets"}`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "too_many_websockets",
		},
		{
			name:       "HTTP 500 error",
			statusCode: http.StatusInternalServerError,
			response:   `Internal Server Error`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "HTTP 500",
		},
		{
			name:       "empty URL in response",
			statusCode: http.StatusOK,
			response:   `{"ok":true,"url":""}`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "empty WebSocket URL",
		},
		{
			name:       "malformed JSON response",
			statusCode: http.StatusOK,
			response:   `{not json`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "failed to parse response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request method and path
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/apps.connections.open" {
					t.Errorf("expected /apps.connections.open, got %s", r.URL.Path)
				}

				// Verify auth header
				auth := r.Header.Get("Authorization")
				if auth != "Bearer "+testutil.FakeSlackAppToken {
					t.Errorf("expected Bearer %s, got %s", testutil.FakeSlackAppToken, auth)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, server.URL)
			url, err := client.OpenConnection(context.Background())

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error wrapping %v, got: %v", tt.wantErr, err)
				}
				if tt.wantErrMsg != "" {
					if got := err.Error(); !contains(got, tt.wantErrMsg) {
						t.Errorf("error %q does not contain %q", got, tt.wantErrMsg)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if url != tt.wantURL {
				t.Errorf("expected URL %q, got %q", tt.wantURL, url)
			}
		})
	}
}

func TestOpenConnectionCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://example.com"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, server.URL)
	_, err := client.OpenConnection(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, ErrConnectionOpen) {
		t.Errorf("expected ErrConnectionOpen, got: %v", err)
	}
}

func TestNewSocketModeClient(t *testing.T) {
	client := NewSocketModeClient(testutil.FakeSlackAppToken)
	if client.appToken != testutil.FakeSlackAppToken {
		t.Errorf("expected appToken %q, got %q", testutil.FakeSlackAppToken, client.appToken)
	}
	if client.apiURL != slackAPIURL {
		t.Errorf("expected apiURL %q, got %q", slackAPIURL, client.apiURL)
	}
	if client.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
}

// contains and searchString helpers are in events_test.go
