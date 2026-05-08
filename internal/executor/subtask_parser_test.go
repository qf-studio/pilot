package executor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
)

// samplePlanningOutput is realistic Claude --print output used across tests.
const samplePlanningOutput = `Based on the codebase analysis, here is the implementation plan:

**1. Add database migration** - Create new table for user preferences with columns for theme, language, and notifications
**2. Implement repository layer** - Add CRUD methods for user preferences in the data access layer
**3. Create API endpoints** - REST endpoints for reading and updating preferences
**4. Add frontend settings page** - React component with form controls bound to the API`

var testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

func TestSubtaskParserParse_HappyPath(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": [{"order": 1, "title": "Add database migration", "description": "Create new table for user preferences"}, {"order": 2, "title": "Implement repository layer", "description": "Add CRUD methods for user preferences"}, {"order": 3, "title": "Create API endpoints", "description": "REST endpoints for reading and updating"}, {"order": 4, "title": "Add frontend settings page", "description": "React component with form controls"}]}`), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	subtasks, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks, got %d", len(subtasks))
	}

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

func TestSubtaskParserParse_SubprocessError(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return nil, errors.New("subprocess failed: exit status 1")
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from subprocess failure, got nil")
	}
}

func TestSubtaskParserParse_MalformedJSON(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("This is not JSON at all"), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from malformed JSON response, got nil")
	}
}

func TestSubtaskParserParse_EmptySubtasksSlice(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": []}`), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

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

func TestSubtaskParserParse_TimeoutPropagation(t *testing.T) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from deadline exceeded, got nil")
	}
}

func TestSubtaskParserParse_JSONCodefenceStripping(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("```json\n{\"subtasks\": [{\"order\": 1, \"title\": \"feat: do x\", \"description\": \"desc\"}]}\n```"), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	subtasks, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err != nil {
		t.Fatalf("expected codefence to be stripped, got error: %v", err)
	}
	if len(subtasks) != 1 || subtasks[0].Title != "feat: do x" {
		t.Fatalf("unexpected subtasks: %+v", subtasks)
	}
}

func TestParseSubtasksWithFallback_SubprocessSucceeds(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": [{"order": 1, "title": "subprocess-extracted task one", "description": "From subprocess"}, {"order": 2, "title": "subprocess-extracted task two", "description": "From subprocess"}]}`), nil
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks from subprocess, got %d", len(subtasks))
	}
	if subtasks[0].Title != "subprocess-extracted task one" {
		t.Errorf("subtask 0 title = %q, want subprocess result", subtasks[0].Title)
	}
	if subtasks[1].Title != "subprocess-extracted task two" {
		t.Errorf("subtask 1 title = %q, want subprocess result", subtasks[1].Title)
	}
}

func TestParseSubtasksWithFallback_SubprocessFails_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return nil, errors.New("subprocess failed")
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	// Regex should extract 4 subtasks from samplePlanningOutput
	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
	if subtasks[0].Title != "Add database migration" {
		t.Errorf("subtask 0 title = %q, want regex fallback result", subtasks[0].Title)
	}
	if subtasks[0].Order != 1 {
		t.Errorf("subtask 0 order = %d, want 1", subtasks[0].Order)
	}
}

func TestParseSubtasksWithFallback_NilParser_FallsBackToRegex(t *testing.T) {
	subtasks := parseSubtasksWithFallback(nil, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
	if subtasks[0].Title != "Add database migration" {
		t.Errorf("subtask 0 title = %q, want regex fallback result", subtasks[0].Title)
	}
}

func TestParseSubtasksWithFallback_InvalidJSON_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("not valid json"), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestParseSubtasksWithFallback_EmptySubtasks_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": []}`), nil
	}
	parser := newSubtaskParserWithRunner(runner, testLog)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestNewSubtaskParser_BinaryMissing(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSubtaskParser("/nonexistent/binary/claude-does-not-exist", log)
	if parser != nil {
		t.Error("expected nil parser when claude binary is missing")
	}
}
