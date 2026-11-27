package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestClientVerify(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			body:       `{"ok":true,"result":{"id":1,"first_name":"Pilot","username":"pilot_test_bot"}}`,
			wantErr:    false,
		},
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       `{"ok":false,"error_code":401,"description":"Unauthorized"}`,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/getMe") {
					t.Errorf("path = %q, want to end with /getMe", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, server.URL)
			err := client.Verify(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), "telegram token invalid") {
					t.Errorf("error = %q, want to contain %q", err.Error(), "telegram token invalid")
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
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := client.Verify(ctx)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "telegram token invalid") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "telegram token invalid")
	}
}

// verifyServer answers getMe with a bot user and getUpdates with getUpdates,
// and counts the requests by the last path segment.
func verifyServer(t *testing.T, getMe, getUpdates func(w http.ResponseWriter)) (*httptest.Server, func(method string) int) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		counts[method]++
		mu.Unlock()
		switch method {
		case "getMe":
			getMe(w)
		case "getUpdates":
			getUpdates(w)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func(method string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[method]
	}
}

func getMeOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"first_name":"Pilot","username":"pilot_test_bot"}}`))
}

func getUpdatesConflict(w http.ResponseWriter) {
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(`{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`))
}

// TestClient_Verify_UsesGetMe pins the probe of Verify to getMe: a getUpdates
// probe collides with the poller of the same daemon (409). GH-6734.
func TestClient_Verify_UsesGetMe(t *testing.T) {
	srv, count := verifyServer(t, getMeOK, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	})
	client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.URL)

	if err := client.Verify(context.Background()); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if got := count("getMe"); got != 1 {
		t.Errorf("getMe requests = %d, want 1", got)
	}
	if got := count("getUpdates"); got != 0 {
		t.Errorf("getUpdates requests = %d, want 0", got)
	}
}

func TestClient_Verify_InvalidToken(t *testing.T) {
	srv, _ := verifyServer(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}, getUpdatesConflict)
	client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.URL)

	err := client.Verify(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "telegram token invalid") {
		t.Errorf("error = %q, want to contain %q", err.Error(), "telegram token invalid")
	}
}

// TestClient_Verify_NoConflictWithPoller: while a poller holds getUpdates and
// Telegram answers each other getUpdates with 409, Verify still passes.
func TestClient_Verify_NoConflictWithPoller(t *testing.T) {
	srv, _ := verifyServer(t, getMeOK, getUpdatesConflict)
	client := NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.URL)

	if err := client.Verify(context.Background()); err != nil {
		t.Errorf("Verify() error = %v, want nil", err)
	}
}
