package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewNotifier(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	notifier := NewNotifier(client)

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if notifier.client != client {
		t.Error("notifier.client not set correctly")
	}
}

func TestNotifyTaskStarted_Success(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// Verify it's a commentCreate mutation
		if !strings.Contains(reqBody.Query, "commentCreate") {
			t.Errorf("query should contain 'commentCreate', got: %s", reqBody.Query)
		}

		capturedBody = reqBody.Variables["body"].(string)

		// Verify variables
		if reqBody.Variables["issueId"] != "issue-123" {
			t.Errorf("variables[issueId] = %v, want issue-123", reqBody.Variables["issueId"])
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskStarted(context.Background(), "issue-123", "TASK-456")
	if err != nil {
		t.Fatalf("NotifyTaskStarted() error = %v", err)
	}

	if !strings.Contains(capturedBody, "Pilot started working") {
		t.Errorf("comment should contain 'Pilot started working', got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "TASK-456") {
		t.Errorf("comment should contain task ID 'TASK-456', got: %s", capturedBody)
	}
}

func TestNotifyTaskStarted_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "Internal error"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskStarted(context.Background(), "issue-123", "TASK-456")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "failed to add start comment") {
		t.Errorf("error = %v, want to contain 'failed to add start comment'", err)
	}
}

func TestNotifyProgress_Phases(t *testing.T) {
	tests := []struct {
		name    string
		phase   string
		details string
	}{
		{name: "exploring phase", phase: "exploring", details: "Analyzing codebase"},
		{name: "research phase", phase: "research", details: "Reading docs"},
		{name: "implementing phase", phase: "implementing", details: "Writing code"},
		{name: "impl phase", phase: "impl", details: "Building feature"},
		{name: "testing phase", phase: "testing", details: "Running tests"},
		{name: "verify phase", phase: "verify", details: "Verifying build"},
		{name: "committing phase", phase: "committing", details: "Creating commit"},
		{name: "unknown phase", phase: "planning", details: "Planning work"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody GraphQLRequest
				_ = json.NewDecoder(r.Body).Decode(&reqBody)
				capturedBody = reqBody.Variables["body"].(string)

				resp := GraphQLResponse{
					Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
			notifier := NewNotifier(client)

			err := notifier.NotifyProgress(context.Background(), "issue-123", tt.phase, tt.details)
			if err != nil {
				t.Fatalf("NotifyProgress() error = %v", err)
			}

			if !strings.Contains(capturedBody, tt.phase) {
				t.Errorf("comment should contain phase %q, got: %s", tt.phase, capturedBody)
			}
			if !strings.Contains(capturedBody, tt.details) {
				t.Errorf("comment should contain details %q, got: %s", tt.details, capturedBody)
			}
		})
	}
}

func TestNotifyProgress_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{{Message: "Rate limited"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyProgress(context.Background(), "issue-123", "testing", "Running tests")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "failed to add progress comment") {
		t.Errorf("error = %v, want to contain 'failed to add progress comment'", err)
	}
}

func TestNotifyTaskCompleted_WithPRAndSummary(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskCompleted(context.Background(), "issue-123", "https://github.com/org/repo/pull/42", "Added auth feature with tests")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted() error = %v", err)
	}

	if !strings.Contains(capturedBody, "Pilot completed") {
		t.Errorf("comment should contain 'Pilot completed', got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "https://github.com/org/repo/pull/42") {
		t.Errorf("comment should contain PR URL, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "Added auth feature with tests") {
		t.Errorf("comment should contain summary, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "can be closed when the PR is merged") {
		t.Errorf("comment should contain closing hint, got: %s", capturedBody)
	}
}

func TestNotifyTaskCompleted_WithoutPRURL(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskCompleted(context.Background(), "issue-123", "", "Fixed the bug")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted() error = %v", err)
	}

	if strings.Contains(capturedBody, "Pull Request") {
		t.Errorf("comment should NOT contain 'Pull Request' when prURL is empty, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "Fixed the bug") {
		t.Errorf("comment should contain summary, got: %s", capturedBody)
	}
}

func TestNotifyTaskCompleted_WithoutSummary(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskCompleted(context.Background(), "issue-123", "https://github.com/org/repo/pull/42", "")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted() error = %v", err)
	}

	if !strings.Contains(capturedBody, "https://github.com/org/repo/pull/42") {
		t.Errorf("comment should contain PR URL, got: %s", capturedBody)
	}
	if strings.Contains(capturedBody, "Summary") {
		t.Errorf("comment should NOT contain 'Summary' when summary is empty, got: %s", capturedBody)
	}
}

func TestNotifyTaskCompleted_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "server error"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskCompleted(context.Background(), "issue-123", "https://github.com/org/repo/pull/42", "summary")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "failed to add completion comment") {
		t.Errorf("error = %v, want to contain 'failed to add completion comment'", err)
	}
}

func TestNotifyTaskFailed_Success(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskFailed(context.Background(), "issue-123", "Tests failed with 3 errors")
	if err != nil {
		t.Fatalf("NotifyTaskFailed() error = %v", err)
	}

	if !strings.Contains(capturedBody, "could not complete") {
		t.Errorf("comment should contain 'could not complete', got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "Tests failed with 3 errors") {
		t.Errorf("comment should contain failure reason, got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "manual intervention") {
		t.Errorf("comment should suggest manual intervention, got: %s", capturedBody)
	}
}

func TestNotifyTaskFailed_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := GraphQLResponse{
			Errors: []GraphQLError{{Message: "Unauthorized"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskFailed(context.Background(), "issue-123", "Build failed")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "failed to add failure comment") {
		t.Errorf("error = %v, want to contain 'failed to add failure comment'", err)
	}
}

func TestLinkPR_Success(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		if reqBody.Variables["issueId"] != "issue-123" {
			t.Errorf("variables[issueId] = %v, want issue-123", reqBody.Variables["issueId"])
		}

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.LinkPR(context.Background(), "issue-123", "https://github.com/org/repo/pull/42")
	if err != nil {
		t.Fatalf("LinkPR() error = %v", err)
	}

	if !strings.Contains(capturedBody, "Pull Request Created") {
		t.Errorf("comment should contain 'Pull Request Created', got: %s", capturedBody)
	}
	if !strings.Contains(capturedBody, "https://github.com/org/repo/pull/42") {
		t.Errorf("comment should contain PR URL, got: %s", capturedBody)
	}
}

func TestLinkPR_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "server error"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.LinkPR(context.Background(), "issue-123", "https://github.com/org/repo/pull/42")
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "failed to add PR link comment") {
		t.Errorf("error = %v, want to contain 'failed to add PR link comment'", err)
	}
}

func TestNotifyTaskCompleted_EmptyPRAndSummary(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody GraphQLRequest
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		capturedBody = reqBody.Variables["body"].(string)

		resp := GraphQLResponse{
			Data: json.RawMessage(`{"commentCreate": {"success": true}}`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearAPIKey, server.URL)
	notifier := NewNotifier(client)

	err := notifier.NotifyTaskCompleted(context.Background(), "issue-123", "", "")
	if err != nil {
		t.Fatalf("NotifyTaskCompleted() error = %v", err)
	}

	if !strings.Contains(capturedBody, "Pilot completed") {
		t.Errorf("comment should contain 'Pilot completed', got: %s", capturedBody)
	}
	if strings.Contains(capturedBody, "Pull Request") {
		t.Errorf("comment should NOT contain 'Pull Request' when both fields empty, got: %s", capturedBody)
	}
	if strings.Contains(capturedBody, "Summary") {
		t.Errorf("comment should NOT contain 'Summary' when both fields empty, got: %s", capturedBody)
	}
}
