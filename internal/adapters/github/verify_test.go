package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestClientVerify(t *testing.T) {
	tests := []struct {
		name        string
		tokenSource string
		statusCode  int
		body        string
		wantErr     bool
		errContains []string
	}{
		{
			name:        "success",
			tokenSource: "config",
			statusCode:  http.StatusOK,
			body:        `{"id":1,"login":"pilot-bot"}`,
			wantErr:     false,
		},
		{
			name:        "401 includes token source",
			tokenSource: "env GITHUB_TOKEN",
			statusCode:  http.StatusUnauthorized,
			body:        `{"message":"Bad credentials"}`,
			wantErr:     true,
			errContains: []string{"github token invalid", "env GITHUB_TOKEN", "Bad credentials"},
		},
		{
			name:        "401 with empty token source omits parenthetical",
			tokenSource: "",
			statusCode:  http.StatusUnauthorized,
			body:        `{"message":"Bad credentials"}`,
			wantErr:     true,
			errContains: []string{"github token invalid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/user") {
					t.Errorf("path = %q, want to end with /user", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.Verify(context.Background(), tt.tokenSource)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				for _, want := range tt.errContains {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error = %q, want to contain %q", err.Error(), want)
					}
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
		_ = json.NewEncoder(w).Encode(User{ID: 1, Login: "pilot-bot"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := client.Verify(ctx, "config")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "github token invalid") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "github token invalid")
	}
}
