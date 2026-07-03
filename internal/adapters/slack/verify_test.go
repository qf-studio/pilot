package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestClientAuthTestAndVerify(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			body:       `{"ok":true,"team":"Pilot","user":"pilot-bot","team_id":"T123","user_id":"U123"}`,
			wantErr:    false,
		},
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"ok":false,"error":"invalid_auth"}`,
			wantErr:    true,
		},
		{
			name:       "200 with ok false",
			statusCode: http.StatusOK,
			body:       `{"ok":false,"error":"invalid_auth"}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/auth.test") {
					t.Errorf("path = %q, want to end with /auth.test", r.URL.Path)
				}
				if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
					t.Errorf("Authorization = %q, want Bearer prefix", auth)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeSlackBotToken, server.URL)
			err := client.Verify(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "slack token invalid") {
					t.Errorf("error = %q, want to contain %q", err.Error(), "slack token invalid")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestClientVerifyTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeSlackBotToken, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := client.Verify(ctx)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "slack token invalid") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "slack token invalid")
	}
}
