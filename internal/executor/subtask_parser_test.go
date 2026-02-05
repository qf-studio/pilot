package executor

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// samplePlanningOutput is realistic Claude --print output used across tests.
const samplePlanningOutput = `Based on the codebase analysis, here is the implementation plan:

**1. Add database migration** - Create new table for user preferences with columns for theme, language, and notifications
**2. Implement repository layer** - Add CRUD methods for user preferences in the data access layer
**3. Create API endpoints** - REST endpoints for reading and updating preferences
**4. Add frontend settings page** - React component with form controls bound to the API`

func TestSubtaskParserParse_HappyPath(t *testing.T) {
	// Mock Haiku endpoint returns structured JSON
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("x-api-key") != "test-api-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return structured subtasks via Anthropic API response format
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": [{"order": 1, "title": "Add database migration", "description": "Create new table for user preferences"}, {"order": 2, "title": "Implement repository layer", "description": "Add CRUD methods for user preferences"}, {"order": 3, "title": "Create API endpoints", "description": "REST endpoints for reading and updating"}, {"order": 4, "title": "Add frontend settings page", "description": "React component with form controls"}]}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	subtasks, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks, got %d", len(subtasks))
	}

	// Verify structured extraction
	expected := []struct {
		order int
		title string
	}{
		{1, "Add database migration"},
		{2, "Implement repository layer"},
		{3, "Create API endpoints"},
		{4, "Add frontend settings page"},
	}

	for i, want := range expected {
		if subtasks[i].Order != want.order {
			t.Errorf("subtask %d: order = %d, want %d", i, subtasks[i].Order, want.order)
		}
		if subtasks[i].Title != want.title {
			t.Errorf("subtask %d: title = %q, want %q", i, subtasks[i].Title, want.title)
		}
		if subtasks[i].Description == "" {
			t.Errorf("subtask %d: description should not be empty", i)
		}
	}
}

func TestSubtaskParserParse_APIError(t *testing.T) {
	// Mock Haiku endpoint returns 500
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from 500 response, got nil")
	}
}

func TestSubtaskParserParse_InvalidJSON(t *testing.T) {
	// Mock returns 200 but with non-JSON text content
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "This is not JSON at all",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from invalid JSON response, got nil")
	}
}

func TestSubtaskParserParse_EmptySubtasks(t *testing.T) {
	// Mock returns valid JSON but empty subtasks array
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": []}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from empty subtasks, got nil")
	}
}

func TestSubtaskParserParse_NilParser(t *testing.T) {
	var parser *SubtaskParser
	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from nil parser, got nil")
	}
}

func TestParseSubtasksWithFallback_HaikuSucceeds(t *testing.T) {
	// When Haiku API succeeds, its structured results are used
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": [{"order": 1, "title": "API-extracted task one", "description": "From Haiku"}, {"order": 2, "title": "API-extracted task two", "description": "From Haiku"}]}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks from Haiku, got %d", len(subtasks))
	}

	// Verify these are from the API (unique titles), not regex
	if subtasks[0].Title != "API-extracted task one" {
		t.Errorf("subtask 0 title = %q, want %q (should be from API, not regex)", subtasks[0].Title, "API-extracted task one")
	}
	if subtasks[1].Title != "API-extracted task two" {
		t.Errorf("subtask 1 title = %q, want %q (should be from API, not regex)", subtasks[1].Title, "API-extracted task two")
	}
}

func TestParseSubtasksWithFallback_HaikuFails_FallsBackToRegex(t *testing.T) {
	// When Haiku API returns 500, fallback to regex parsing
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	// Regex should extract 4 subtasks from samplePlanningOutput
	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}

	// Verify these are from regex (titles parsed from the bold-wrapped output)
	if subtasks[0].Title != "Add database migration" {
		t.Errorf("subtask 0 title = %q, want %q (should be from regex fallback)", subtasks[0].Title, "Add database migration")
	}
	if subtasks[0].Order != 1 {
		t.Errorf("subtask 0 order = %d, want 1", subtasks[0].Order)
	}
}

func TestParseSubtasksWithFallback_NilParser_FallsBackToRegex(t *testing.T) {
	// When parser is nil (no API key), regex is used directly
	subtasks := parseSubtasksWithFallback(nil, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}

	if subtasks[0].Title != "Add database migration" {
		t.Errorf("subtask 0 title = %q, want %q", subtasks[0].Title, "Add database migration")
	}
}

func TestParseSubtasksWithFallback_HaikuReturnsInvalidJSON_FallsBackToRegex(t *testing.T) {
	// When Haiku returns invalid JSON, fallback to regex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": "not valid json",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestParseSubtasksWithFallback_HaikuReturnsEmptySubtasks_FallsBackToRegex(t *testing.T) {
	// When Haiku returns empty subtasks array, fallback to regex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": `{"subtasks": []}`,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestNewSubtaskParser_NoAPIKey(t *testing.T) {
	// Temporarily unset the env var
	original := os.Getenv("ANTHROPIC_API_KEY")
	_ = os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if original != "" {
			_ = os.Setenv("ANTHROPIC_API_KEY", original)
		}
	}()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSubtaskParser(log)
	if parser != nil {
		t.Error("expected nil parser when ANTHROPIC_API_KEY is not set")
	}
}

func TestSubtaskParserParse_ContextCancelled(t *testing.T) {
	// Mock server that blocks (simulating slow response)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wait for context cancellation
		<-r.Context().Done()
	}))
	defer server.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithURL("test-api-key", server.URL, log)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := parser.Parse(ctx, samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
