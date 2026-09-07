package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// mockEffortRunner creates a test runner that returns canned effort JSON.
func mockEffortRunner(effort, reason string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"effort":"` + effort + `","reason":"` + reason + `"}`), nil
	}
}

// mockEffortRunnerError creates a test runner that returns an error.
func mockEffortRunnerError(err error) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, err
	}
}

func TestEffortClassifier_LowEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("low", "Simple typo fix"))
	task := &Task{
		ID:          "GH-100",
		Title:       "Fix typo in README",
		Description: "Fix typo in README.md: change 'teh' to 'the'",
	}

	result := classifier.Classify(context.Background(), task)
	if result != "low" {
		t.Errorf("expected 'low', got %q", result)
	}
}

func TestEffortClassifier_MediumEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("medium", "Standard work with clear requirements"))
	task := &Task{
		ID:    "GH-200",
		Title: "Add email field to user struct",
		Description: `Add an email field to the user struct:
1. Add field to models/user.go
2. Update CreateUser function
3. Add validation for email format
4. Write unit tests`,
	}

	result := classifier.Classify(context.Background(), task)
	if result != "medium" {
		t.Errorf("expected 'medium', got %q", result)
	}
}

func TestEffortClassifier_HighEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "Security-sensitive with multiple considerations"))
	task := &Task{
		ID:          "GH-300",
		Title:       "Fix authentication bypass vulnerability",
		Description: "There's a subtle bug in the session validation that allows bypassing auth under certain conditions. Need to investigate the root cause and fix without breaking existing sessions.",
	}

	result := classifier.Classify(context.Background(), task)
	if result != "high" {
		t.Errorf("expected 'high', got %q", result)
	}
}

func TestEffortClassifier_CachesResult(t *testing.T) {
	callCount := 0
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		callCount++
		return []byte(`{"effort":"medium","reason":"standard work"}`), nil
	}

	classifier := newEffortClassifierWithRunner(runner)
	task := &Task{
		ID:          "GH-400",
		Title:       "Add logging",
		Description: "Add structured logging to the API layer",
	}

	// First call hits subprocess
	result1 := classifier.Classify(context.Background(), task)
	// Second call should use cache
	result2 := classifier.Classify(context.Background(), task)

	if result1 != result2 {
		t.Errorf("cached result differs: %q vs %q", result1, result2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 subprocess call (cached), got %d", callCount)
	}
}

func TestEffortClassifier_ReturnsEmptyOnError(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunnerError(errors.New("subprocess failed")))
	task := &Task{
		ID:          "GH-500",
		Title:       "Fix typo in README",
		Description: "Fix typo in README.md",
	}

	// Should return empty string on error (signals fallback)
	result := classifier.Classify(context.Background(), task)
	if result != "" {
		t.Errorf("expected empty string on error, got %q", result)
	}
}

func TestEffortClassifier_NilTask(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("medium", "n/a"))
	result := classifier.Classify(context.Background(), nil)
	if result != "" {
		t.Errorf("expected empty string for nil task, got %q", result)
	}
}

func TestNewEffortClassifier(t *testing.T) {
	classifier := NewEffortClassifier()
	if classifier == nil {
		t.Fatal("expected non-nil classifier")
	}
	if classifier.model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected haiku model, got %s", classifier.model)
	}
	// Test structured output configuration
	if classifier.useStructuredOutput != false {
		t.Errorf("expected structured output to default to false")
	}
	classifier.SetUseStructuredOutput(true)
	if classifier.useStructuredOutput != true {
		t.Errorf("expected structured output to be set to true")
	}
}

func TestParseEffortResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "simple JSON low",
			input:    `{"effort":"low","reason":"typo fix"}`,
			expected: "low",
		},
		{
			name:     "simple JSON medium",
			input:    `{"effort":"medium","reason":"standard work"}`,
			expected: "medium",
		},
		{
			name:     "simple JSON high",
			input:    `{"effort":"high","reason":"security sensitive"}`,
			expected: "high",
		},
		{
			name:     "markdown wrapped",
			input:    "```json\n{\"effort\":\"high\",\"reason\":\"complex analysis\"}\n```",
			expected: "high",
		},
		{
			name:     "uppercase",
			input:    `{"effort":"HIGH","reason":"arch change"}`,
			expected: "high",
		},
		{
			name:     "mixed case",
			input:    `{"effort":"Medium","reason":"standard"}`,
			expected: "medium",
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:    "unknown level",
			input:   `{"effort":"max","reason":"unknown"}`,
			wantErr: true,
		},
		{
			name:    "empty effort",
			input:   `{"effort":"","reason":"missing"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseEffortResponse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestEffortClassifier_TaskWithoutID(t *testing.T) {
	callCount := 0
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		callCount++
		return []byte(`{"effort":"low","reason":"simple"}`), nil
	}

	classifier := newEffortClassifierWithRunner(runner)
	task := &Task{
		Title:       "Quick fix",
		Description: "A quick fix",
		// No ID - should still work but not cache
	}

	result1 := classifier.Classify(context.Background(), task)
	result2 := classifier.Classify(context.Background(), task)

	if result1 != "low" || result2 != "low" {
		t.Errorf("expected 'low' for both, got %q and %q", result1, result2)
	}
	// Without ID, should call subprocess twice (no caching)
	if callCount != 2 {
		t.Errorf("expected 2 subprocess calls (no cache without ID), got %d", callCount)
	}
}

// newTestEffortClassifier builds a classifier pointed at a local test
// server, bypassing NewEffortClassifier's env-based key discovery so tests
// are hermetic regardless of the ambient environment.
func newTestEffortClassifier(t *testing.T, serverURL, apiKey string, runner func(ctx context.Context, args ...string) ([]byte, error)) *EffortClassifier {
	t.Helper()
	return &EffortClassifier{
		model:     "claude-haiku-4-5-20251001",
		apiURL:    serverURL,
		apiKey:    apiKey,
		timeout:   5 * time.Second,
		log:       logging.WithComponent("effort-classifier-test"),
		cache:     make(map[string]string),
		cmdRunner: runner,
	}
}

func TestEffortClassifier_OAuthTokenUsesBearerAndBetaHeader(t *testing.T) {
	var gotXAPIKey, gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"effort\":\"low\",\"reason\":\"n/a\"}"}]}`))
	}))
	defer srv.Close()

	classifier := newTestEffortClassifier(t, srv.URL, "sk-ant-oat01-test-oauth-token", mockEffortRunner("medium", "should not be used"))
	task := &Task{ID: "GH-600", Title: "t", Description: "d"}

	result := classifier.Classify(context.Background(), task)
	if result != "low" {
		t.Fatalf("expected direct API result 'low', got %q", result)
	}
	if gotXAPIKey != "" {
		t.Errorf("x-api-key should be empty for OAuth token, got %q", gotXAPIKey)
	}
	if gotAuth != "Bearer sk-ant-oat01-test-oauth-token" {
		t.Errorf("Authorization = %q, want Bearer sk-ant-oat01-test-oauth-token", gotAuth)
	}
	if gotBeta != anthropicOAuthBetaHeader {
		t.Errorf("anthropic-beta = %q, want %q", gotBeta, anthropicOAuthBetaHeader)
	}
}

func TestEffortClassifier_APIKeyUsesXAPIKeyHeader(t *testing.T) {
	var gotXAPIKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXAPIKey = r.Header.Get("x-api-key")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"effort\":\"low\",\"reason\":\"n/a\"}"}]}`))
	}))
	defer srv.Close()

	classifier := newTestEffortClassifier(t, srv.URL, "sk-ant-api03-test-key", mockEffortRunner("medium", "should not be used"))
	task := &Task{ID: "GH-601", Title: "t", Description: "d"}

	result := classifier.Classify(context.Background(), task)
	if result != "low" {
		t.Fatalf("expected direct API result 'low', got %q", result)
	}
	if gotXAPIKey != "sk-ant-api03-test-key" {
		t.Errorf("x-api-key = %q, want sk-ant-api03-test-key", gotXAPIKey)
	}
	if gotAuth != "" {
		t.Errorf("Authorization should be empty for API key, got %q", gotAuth)
	}
}

func TestEffortClassifier_OAuth401DisablesDirectModeForSubsequentCalls(t *testing.T) {
	var apiHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"API key is invalid"}}`))
	}))
	defer srv.Close()

	var subprocessHits int32
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		atomic.AddInt32(&subprocessHits, 1)
		return []byte(`{"effort":"low","reason":"n/a"}`), nil
	}

	classifier := newTestEffortClassifier(t, srv.URL, "sk-ant-oat01-test-oauth-token", runner)

	task1 := &Task{ID: "GH-700", Title: "t1", Description: "d1"}
	if result := classifier.Classify(context.Background(), task1); result != "low" {
		t.Fatalf("expected subprocess fallback result 'low', got %q", result)
	}
	if got := atomic.LoadInt32(&apiHits); got != 1 {
		t.Fatalf("expected 1 API hit after first call, got %d", got)
	}
	if got := atomic.LoadInt32(&subprocessHits); got != 1 {
		t.Fatalf("expected 1 subprocess call after first call, got %d", got)
	}
	if !classifier.directModeDisabled.Load() {
		t.Fatal("expected directModeDisabled to be true after a 401 on an OAuth token")
	}

	// Second call, different task (so cache doesn't short-circuit): direct
	// mode must be skipped entirely — no second failing round trip.
	task2 := &Task{ID: "GH-701", Title: "t2", Description: "d2"}
	if result := classifier.Classify(context.Background(), task2); result != "low" {
		t.Fatalf("expected subprocess fallback result 'low', got %q", result)
	}
	if got := atomic.LoadInt32(&apiHits); got != 1 {
		t.Fatalf("expected direct mode to stay disabled (still 1 API hit), got %d", got)
	}
	if got := atomic.LoadInt32(&subprocessHits); got != 2 {
		t.Fatalf("expected 2 subprocess calls total, got %d", got)
	}
}

func TestEffortClassifier_APIKey401DoesNotDisableDirectMode(t *testing.T) {
	// A rejected API key (not an OAuth token) is not the "OAuth token can
	// never work" case this latch targets — direct mode should keep being
	// attempted (e.g. a misconfigured key could be fixed via env reload in
	// a longer-lived process, and non-OAuth 401s aren't guaranteed to be
	// permanent in the same way).
	var apiHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"API key is invalid"}}`))
	}))
	defer srv.Close()

	runner := mockEffortRunner("low", "n/a")
	classifier := newTestEffortClassifier(t, srv.URL, "sk-ant-api03-bad-key", runner)

	task1 := &Task{ID: "GH-702", Title: "t1", Description: "d1"}
	classifier.Classify(context.Background(), task1)

	task2 := &Task{ID: "GH-703", Title: "t2", Description: "d2"}
	classifier.Classify(context.Background(), task2)

	if got := atomic.LoadInt32(&apiHits); got != 2 {
		t.Errorf("expected direct mode to still be attempted for a rejected API key, got %d hits", got)
	}
	if classifier.directModeDisabled.Load() {
		t.Error("directModeDisabled should not latch for a non-OAuth token 401")
	}
}

func TestParseStructuredEffortResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name: "valid low effort",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session",
				"structured_output": {"effort": "low", "reason": "Simple change"}
			}`,
			expected: "low",
			wantErr:  false,
		},
		{
			name: "valid medium effort",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session",
				"structured_output": {"effort": "medium", "reason": "Standard work"}
			}`,
			expected: "medium",
			wantErr:  false,
		},
		{
			name: "valid high effort",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session",
				"structured_output": {"effort": "high", "reason": "Complex analysis needed"}
			}`,
			expected: "high",
			wantErr:  false,
		},
		{
			name: "case insensitive",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session",
				"structured_output": {"effort": "HIGH", "reason": "Complex analysis"}
			}`,
			expected: "high",
			wantErr:  false,
		},
		{
			name:    "invalid JSON wrapper",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name: "missing structured_output",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session"
			}`,
			wantErr: true,
		},
		{
			name: "unknown effort level",
			input: `{
				"result": "Effort classification complete",
				"session_id": "test-session",
				"structured_output": {"effort": "extreme", "reason": "Invalid level"}
			}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseStructuredEffortResponse([]byte(tt.input))

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
