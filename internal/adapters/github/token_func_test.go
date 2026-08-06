package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// TestClient_TokenFunc_ResolvesPerRequest_GraphQL is the GraphQL-path
// counterpart of TestClient_TokenFunc_ResolvesPerRequest — token_func_test.go
// previously only exercised token rotation through the REST GetIssue path,
// leaving executeGraphQLCore's independent resolveToken call unverified
// (GH-4754 acceptance #4).
func TestClient_TokenFunc_ResolvesPerRequest_GraphQL(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()

	source := newRotatingTokenSource("token-v1")
	client := NewClientWithTokenFuncAndBaseURL(source.TokenFunc(), server.URL)

	var result struct {
		OK bool `json:"ok"`
	}
	if err := client.ExecuteGraphQL(context.Background(), "query{}", nil, &result); err != nil {
		t.Fatalf("ExecuteGraphQL (pre-rotation) failed: %v", err)
	}

	source.set("token-v2")

	if err := client.ExecuteGraphQL(context.Background(), "query{}", nil, &result); err != nil {
		t.Fatalf("ExecuteGraphQL (post-rotation) failed: %v", err)
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuth))
	}
	if gotAuth[0] != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", gotAuth[0], "Bearer token-v1")
	}
	if gotAuth[1] != "Bearer token-v2" {
		t.Errorf("second request Authorization = %q (stale boot-time token), want %q — GraphQL token rotation did not propagate", gotAuth[1], "Bearer token-v2")
	}
}

// TestClient_TokenFunc_ResolvesPerRequest_GetJobLogs is the GetJobLogs
// counterpart of TestClient_TokenFunc_ResolvesPerRequest — GetJobLogs builds
// its request by hand (it predates doRequest's shared retry path) with its
// own resolveToken call, so it needs its own rotation coverage
// (GH-4754 acceptance #4).
func TestClient_TokenFunc_ResolvesPerRequest_GetJobLogs(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("log line 1\nlog line 2\n"))
	}))
	defer server.Close()

	source := newRotatingTokenSource("token-v1")
	client := NewClientWithTokenFuncAndBaseURL(source.TokenFunc(), server.URL)

	if _, err := client.GetJobLogs(context.Background(), "owner", "repo", 42); err != nil {
		t.Fatalf("GetJobLogs (pre-rotation) failed: %v", err)
	}

	source.set("token-v2")

	if _, err := client.GetJobLogs(context.Background(), "owner", "repo", 42); err != nil {
		t.Fatalf("GetJobLogs (post-rotation) failed: %v", err)
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuth))
	}
	if gotAuth[0] != "Bearer token-v1" {
		t.Errorf("first request Authorization = %q, want %q", gotAuth[0], "Bearer token-v1")
	}
	if gotAuth[1] != "Bearer token-v2" {
		t.Errorf("second request Authorization = %q (stale boot-time token), want %q — GetJobLogs token rotation did not propagate", gotAuth[1], "Bearer token-v2")
	}
}

// TestClient_TokenFunc_RotatesBetweenRetryAttempts covers the gap the ticket
// calls out explicitly: WithRetryVoid's internal backoff loop re-runs the
// whole request closure (including resolveToken) on each attempt, so a
// token that rotates out from under a client mid-retry (e.g. a proactive
// TokenSource refresh landing between a transient 503 and its retry) must
// be picked up on attempt 2, not replay attempt 1's now-stale value
// (GH-4754 acceptance #4: "rotation between retry attempts 1 and 2").
func TestClient_TokenFunc_RotatesBetweenRetryAttempts(t *testing.T) {
	var gotAuth []string
	source := newRotatingTokenSource("token-v1")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		if len(gotAuth) == 1 {
			// Rotate the credential right as attempt 1 fails transiently —
			// attempt 2 (fired by WithRetryVoid after backoff) must resolve
			// the token fresh rather than replaying attempt 1's header.
			source.set("token-v2")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	client := NewClientWithTokenFuncAndBaseURL(source.TokenFunc(), server.URL)
	client.retryOpts = RetryOptions{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue() error = %v, want the retry to succeed on attempt 2 with the rotated token", err)
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 attempts (1 transient failure + 1 retry), got %d", len(gotAuth))
	}
	if gotAuth[0] != "Bearer token-v1" {
		t.Errorf("attempt 1 Authorization = %q, want %q", gotAuth[0], "Bearer token-v1")
	}
	if gotAuth[1] != "Bearer token-v2" {
		t.Errorf("attempt 2 Authorization = %q, want %q — retry replayed attempt 1's token instead of re-resolving", gotAuth[1], "Bearer token-v2")
	}
}

// invalidatingRotatingTokenSource models the real TokenSource + Client
// wiring from apptoken.go/client.go: it serves a stale token until
// Invalidate() is called, then serves a fresh one — letting these tests
// exercise Client.doWithAuthRetry's 401-invalidate-and-retry-once behavior
// (GH-4754 acceptance #1) without spinning up a real GitHub App mint
// server.
type invalidatingRotatingTokenSource struct {
	mu          sync.Mutex
	stale       string
	fresh       string
	invalidated bool
}

func (r *invalidatingRotatingTokenSource) TokenFunc() TokenFunc {
	return func(ctx context.Context) (string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.invalidated {
			return r.fresh, nil
		}
		return r.stale, nil
	}
}

func (r *invalidatingRotatingTokenSource) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = true
}

// newInvalidatingTestClient builds a Client wired exactly like
// newGitHubClient in cmd/pilot/main.go — a TokenFunc plus an invalidate
// callback — pointed at a test server, for exercising the 401
// invalidate-and-retry-once path added in GH-4754.
func newInvalidatingTestClient(source *invalidatingRotatingTokenSource, baseURL string) *Client {
	c := newClient(source.TokenFunc(), baseURL, RetryOptions{MaxRetries: 0})
	c.invalidateToken = source.Invalidate
	return c
}

// TestClient_401InvalidatesAndRetriesOnce_REST is the GH-4754 acceptance #1
// regression test for the REST path: a 401 on the stale token must
// invalidate it and retry exactly once with the fresh token, succeeding
// transparently to the caller instead of surfacing the 401.
func TestClient_401InvalidatesAndRetriesOnce_REST(t *testing.T) {
	const stale, fresh = "token-stale", "token-fresh"
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	source := &invalidatingRotatingTokenSource{stale: stale, fresh: fresh}
	client := newInvalidatingTestClient(source, server.URL)

	issue, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("GetIssue() error = %v, want automatic recovery via invalidate-and-retry", err)
	}
	if issue.Number != 1 {
		t.Errorf("GetIssue() = %+v, want Number=1", issue)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("server hit %d times, want 2 (initial 401 + one auth-retry)", got)
	}
}

// TestClient_401InvalidatesAndRetriesOnce_GraphQL is the GraphQL-path
// counterpart of TestClient_401InvalidatesAndRetriesOnce_REST.
func TestClient_401InvalidatesAndRetriesOnce_GraphQL(t *testing.T) {
	const stale, fresh = "token-stale", "token-fresh"
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer server.Close()

	source := &invalidatingRotatingTokenSource{stale: stale, fresh: fresh}
	client := newInvalidatingTestClient(source, server.URL)

	var result struct {
		OK bool `json:"ok"`
	}
	if err := client.ExecuteGraphQL(context.Background(), "query{}", nil, &result); err != nil {
		t.Fatalf("ExecuteGraphQL() error = %v, want automatic recovery via invalidate-and-retry", err)
	}
	if !result.OK {
		t.Errorf("ExecuteGraphQL() result = %+v, want OK=true", result)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("server hit %d times, want 2 (initial 401 + one auth-retry)", got)
	}
}

// TestClient_401InvalidatesAndRetriesOnce_GetJobLogs is the GetJobLogs-path
// counterpart of TestClient_401InvalidatesAndRetriesOnce_REST.
func TestClient_401InvalidatesAndRetriesOnce_GetJobLogs(t *testing.T) {
	const stale, fresh = "token-stale", "token-fresh"
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("log line 1\n"))
	}))
	defer server.Close()

	source := &invalidatingRotatingTokenSource{stale: stale, fresh: fresh}
	client := newInvalidatingTestClient(source, server.URL)

	logs, err := client.GetJobLogs(context.Background(), "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetJobLogs() error = %v, want automatic recovery via invalidate-and-retry", err)
	}
	if logs != "log line 1\n" {
		t.Errorf("GetJobLogs() = %q, want %q", logs, "log line 1\n")
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("server hit %d times, want 2 (initial 401 + one auth-retry)", got)
	}
}

// TestClient_401WithoutInvalidator_NoRetry guards the opt-in nature of the
// invalidate-and-retry path: a Client built without an invalidate callback
// (NewClientWithTokenFunc / NewClientWithTokenFuncAndBaseURL — every
// call site except the App-auth-aware newGitHubClient) must behave exactly
// as before GH-4754: a single 401, no retry.
func TestClient_401WithoutInvalidator_NoRetry(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	client := NewClientWithTokenFuncAndBaseURL(StaticToken("token"), server.URL)

	_, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected AuthError, got nil")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("server hit %d times, want 1 (no invalidate callback wired, so no auth-retry)", got)
	}
}
