package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

// rotatingTokenSource is a fake TokenFunc that starts out returning current
// and can be rotated mid-test via set(), simulating a GitHub App installation
// token refreshing out from under a long-lived Client (GH-4747).
type rotatingTokenSource struct {
	mu      sync.Mutex
	current string
	calls   int32
}

func newRotatingTokenSource(initial string) *rotatingTokenSource {
	return &rotatingTokenSource{current: initial}
}

func (r *rotatingTokenSource) TokenFunc() TokenFunc {
	return func(ctx context.Context) (string, error) {
		atomic.AddInt32(&r.calls, 1)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.current, nil
	}
}

func (r *rotatingTokenSource) set(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = token
}

// TestClient_TokenFunc_ResolvesPerRequest is the GH-4747 regression test: a
// Client built once (as the daemon does at startup) and held across a token
// rotation must pick up the new token on its very next request instead of
// replaying the boot-time value and getting a 401. Reproduces the incident
// this ticket fixes — a long-lived autopilot/poller client surviving past
// the ~1h GitHub App installation token expiry.
func TestClient_TokenFunc_ResolvesPerRequest(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	source := newRotatingTokenSource("token-v1")
	client := NewClientWithTokenFuncAndBaseURL(source.TokenFunc(), server.URL)

	// First request rides the boot-time token.
	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue (pre-rotation) failed: %v", err)
	}

	// Simulate the App-auth TokenSource proactively refreshing mid-process —
	// the Client is never rebuilt, exactly as it lives inside autopilot's
	// Controller or the adapter-readiness verifier for the daemon's lifetime.
	source.set("token-v2")

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue (post-rotation) failed: %v", err)
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuth))
	}
	if gotAuth[0] != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", gotAuth[0], "Bearer token-v1")
	}
	if gotAuth[1] != "Bearer token-v2" {
		t.Errorf("second request Authorization = %q (stale boot-time token), want %q — token rotation did not propagate to the long-lived client", gotAuth[1], "Bearer token-v2")
	}
}

// TestClient_TokenFunc_401AfterRotation_WithoutPickup guards against a
// regression where resolveToken stops being called per-request (e.g. someone
// "optimizes" it by caching the first result on the Client) — that would
// silently reintroduce the GH-4747 bug: the fake token source below returns
// a token GitHub would reject once expired, and this proves a Client that
// requests the token fresh each time never sends the stale one after a
// rotation.
func TestClient_TokenFunc_401AfterRotation_WithoutPickup(t *testing.T) {
	const staleToken = "token-stale"
	const freshToken = "token-fresh"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+freshToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	source := newRotatingTokenSource(staleToken)
	client := NewClientWithTokenFuncAndBaseURL(source.TokenFunc(), server.URL)
	client.retryOpts = RetryOptions{MaxRetries: 0}

	// The server only accepts freshToken, so a request with the still-stale
	// source must fail with AuthError...
	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err == nil {
		t.Fatal("expected AuthError with stale token, got nil")
	} else {
		var authErr *AuthError
		if !errors.As(err, &authErr) {
			t.Fatalf("expected *AuthError, got %T: %v", err, err)
		}
	}

	// ...and once the source rotates, the very next call (same Client
	// instance, no rebuild) must succeed without any code change on our side.
	source.set(freshToken)
	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue after rotation to fresh token failed: %v", err)
	}
}

// TestClient_TokenFunc_ErrorPropagates ensures a TokenFunc failure (e.g. the
// GitHub App installation token mint erroring) surfaces as a request error
// rather than silently sending an empty/garbage Authorization header.
func TestClient_TokenFunc_ErrorPropagates(t *testing.T) {
	wantErr := errors.New("mint installation token: boom")
	client := NewClientWithTokenFuncAndBaseURL(func(ctx context.Context) (string, error) {
		return "", wantErr
	}, "http://example.invalid")

	_, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected error from failing TokenFunc, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected error to wrap %v, got %v", wantErr, err)
	}
}
