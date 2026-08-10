package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestNewGitHubSDKClient_TokenRotatedBetweenRequests is the TASK-461 Leg 2
// rotation test at the pilot seam: a studio-sdk client built once via
// newGitHubSDKClient(cfg) — as every daemon-lifetime call site now does —
// must pick up a token change on its very next request instead of replaying
// the boot-time value. Mirrors sdk#107's
// TestDoRequest_TokenFunc_ResolvedPerRequest shape (WithClientBaseURL
// against a local test server) but exercises pilot's own bridge
// (githubTokenFunc -> resolveGitHubToken -> cfg.Adapters.GitHub.Token)
// instead of a synthetic TokenFunc, so it proves the actual production
// wiring, not just the SDK's own primitive.
func TestNewGitHubSDKClient_TokenRotatedBetweenRequests(t *testing.T) {
	resetGitHubTokenTestState(t)

	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{Enabled: true, Token: "token-v1"},
	}}

	client := newGitHubSDKClient(cfg, githubSDK.WithClientBaseURL(server.URL))

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue (pre-rotation) failed: %v", err)
	}

	// Simulate a token rotation the way it happens in production: the
	// credential source changes underneath a client that is never rebuilt
	// (an App installation token refreshing, or here, config reload).
	cfg.Adapters.GitHub.Token = "token-v2"

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
		t.Errorf("second request Authorization = %q (stale boot-time token), want %q — token rotation did not propagate to the daemon-lifetime SDK client", gotAuth[1], "Bearer token-v2")
	}
}

// TestNewGitHubSDKClient_NoAppConfig_ByteIdenticalAcrossRequests verifies the
// no-App-config path (current production deployments) sees byte-identical
// behavior after this change: with a static config token and no rotation,
// every request resolves to the same value it always did, just via a
// per-request TokenFunc instead of a frozen field.
func TestNewGitHubSDKClient_NoAppConfig_ByteIdenticalAcrossRequests(t *testing.T) {
	resetGitHubTokenTestState(t)

	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"number":1}`))
	}))
	defer server.Close()

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{Enabled: true, Token: "static-token"},
	}}

	client := newGitHubSDKClient(cfg, githubSDK.WithClientBaseURL(server.URL))

	for i := 0; i < 2; i++ {
		if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
			t.Fatalf("GetIssue call %d failed: %v", i+1, err)
		}
	}

	if len(gotAuth) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuth))
	}
	for i, auth := range gotAuth {
		if auth != "Bearer static-token" {
			t.Errorf("request %d Authorization = %q, want %q (no-App-config path must be byte-identical to a static token)", i+1, auth, "Bearer static-token")
		}
	}
}

// TestNewGitHubSDKClient_NoAppConfig_NoRetryOn401 verifies
// invalidateGitHubAppToken's nil-when-unconfigured contract flows all the
// way through newGitHubSDKClient: without App auth configured, no
// WithTokenInvalidate hook is attached, so a 401 is not retried — matching
// the SDK's documented behavior for a TokenFunc client built without an
// invalidate hook (TestClient_401WithoutInvalidator_NoRetry's counterpart
// for the SDK client family).
func TestNewGitHubSDKClient_NoAppConfig_NoRetryOn401(t *testing.T) {
	resetGitHubTokenTestState(t)

	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	cfg := &config.Config{Adapters: &config.AdaptersConfig{
		GitHub: &github.Config{Enabled: true, Token: "dead-token"},
	}}

	client := newGitHubSDKClient(cfg, githubSDK.WithClientBaseURL(server.URL))

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err == nil {
		t.Fatal("expected AuthError, got nil")
	}
	if attempts != 1 {
		t.Errorf("server hit %d times, want 1 (no invalidate hook wired without App auth, so no auth-retry)", attempts)
	}
}
