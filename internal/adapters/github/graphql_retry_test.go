package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// fastRetryOpts returns retry options with minimal delays so tests run quickly.
func fastRetryOpts() RetryOptions {
	return RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   5 * time.Millisecond,
	}
}

// TestExecuteGraphQL_RetryOn502 verifies that a transient 502 on the first
// attempt is retried and the second (successful) response is returned.
func TestExecuteGraphQL_RetryOn502(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"testuser"}}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = fastRetryOpts()

	var result struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	err := client.ExecuteGraphQL(context.Background(), `{ viewer { login } }`, nil, &result)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if result.Viewer.Login != "testuser" {
		t.Errorf("login = %q, want %q", result.Viewer.Login, "testuser")
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 retry), got %d", calls.Load())
	}
}

// TestExecuteGraphQL_RetryOnRateLimited verifies that an HTTP 200 response
// with a RATE_LIMITED GraphQL error is retried and ultimately succeeds.
func TestExecuteGraphQL_RetryOnRateLimited(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// HTTP 200 with GraphQL-level rate limit error
			resp := GraphQLResponse{
				Errors: []GraphQLError{{Message: "RATE_LIMITED"}},
			}
			body, _ := json.Marshal(resp)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"testuser"}}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = fastRetryOpts()

	var result struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	err := client.ExecuteGraphQL(context.Background(), `{ viewer { login } }`, nil, &result)
	if err != nil {
		t.Fatalf("expected success after RATE_LIMITED retry, got: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (RATE_LIMITED + retry), got %d", calls.Load())
	}
}

// TestExecuteGraphQL_RetryOnSubmittedTooQuickly verifies that the "was submitted
// too quickly" GraphQL error message is treated as retryable.
func TestExecuteGraphQL_RetryOnSubmittedTooQuickly(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			resp := GraphQLResponse{
				Errors: []GraphQLError{{Message: "was submitted too quickly"}},
			}
			body, _ := json.Marshal(resp)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"testuser"}}}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = fastRetryOpts()

	var result struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	err := client.ExecuteGraphQL(context.Background(), `{ viewer { login } }`, nil, &result)
	if err != nil {
		t.Fatalf("expected success after 'submitted too quickly' retry, got: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", calls.Load())
	}
}

// TestExecuteGraphQL_ContextCancellation verifies that context cancellation
// aborts the retry loop without waiting for the full retry budget.
func TestExecuteGraphQL_ContextCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = RetryOptions{
		MaxRetries: 10,
		BaseDelay:  50 * time.Millisecond, // long enough for cancel to fire
		MaxDelay:   100 * time.Millisecond,
	}

	// Cancel after first failure.
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	err := client.ExecuteGraphQL(ctx, `{ viewer { login } }`, nil, nil)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}

	// Should have made at most 2 calls before the cancel fires.
	if calls.Load() > 2 {
		t.Errorf("expected ≤2 calls before cancellation, got %d", calls.Load())
	}
}

// TestIsRetryableError_GraphQLRateLimit verifies the predicate recognises
// GraphQL-level rate limit messages as retryable.
func TestIsRetryableError_GraphQLRateLimit(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		retryable bool
	}{
		{"RATE_LIMITED", "graphql error: RATE_LIMITED", true},
		{"was submitted too quickly", "graphql error: was submitted too quickly", true},
		{"non-retryable graphql error", "graphql error: FORBIDDEN", false},
		{"unrelated error", "some other problem", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(errors.New(tt.msg))
			if got != tt.retryable {
				t.Errorf("isRetryableError(%q) = %v, want %v", tt.msg, got, tt.retryable)
			}
		})
	}
}
