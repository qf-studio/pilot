package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"
)

// samplePlanningOutput is realistic Claude --print output used across tests.
const samplePlanningOutput = `Based on the codebase analysis, here is the implementation plan:

**1. Add database migration** - Create new table for user preferences with columns for theme, language, and notifications
**2. Implement repository layer** - Add CRUD methods for user preferences in the data access layer
**3. Create API endpoints** - REST endpoints for reading and updating preferences
**4. Add frontend settings page** - React component with form controls bound to the API`

func TestSubtaskParserParse_HappyPath(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": [{"order": 1, "title": "Add database migration", "description": "Create new table for user preferences"}, {"order": 2, "title": "Implement repository layer", "description": "Add CRUD methods for user preferences"}, {"order": 3, "title": "Create API endpoints", "description": "REST endpoints for reading and updating"}, {"order": 4, "title": "Add frontend settings page", "description": "React component with form controls"}]}`), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithRunner(runner, log)

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
		return nil, fmt.Errorf("subprocess exited with code 1")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from subprocess failure, got nil")
	}
}

func TestSubtaskParserParse_MalformedJSON(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("This is not JSON at all"), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from malformed JSON response, got nil")
	}
}

func TestSubtaskParserParse_EmptySubtasks(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": []}`), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

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

func TestSubtaskParserParse_CodeFenceStripping(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		// Simulate claude wrapping JSON in a code fence
		return []byte("```json\n{\"subtasks\": [{\"order\": 1, \"title\": \"feat(db): add migration\", \"description\": \"Create schema\"}]}\n```"), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err != nil {
		t.Fatalf("expected code fence to be stripped, got error: %v", err)
	}
	if len(subtasks) != 1 {
		t.Fatalf("expected 1 subtask, got %d", len(subtasks))
	}
	if subtasks[0].Title != "feat(db): add migration" {
		t.Errorf("subtask title = %q, want %q", subtasks[0].Title, "feat(db): add migration")
	}
}

func TestSubtaskParserParse_Timeout(t *testing.T) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)
	parser.timeout = 5 * time.Millisecond // very short timeout to trigger quickly

	_, err := parser.Parse(context.Background(), samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}
}

func TestSubtaskParserParse_ContextCancelled(t *testing.T) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := parser.Parse(ctx, samplePlanningOutput)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestParseSubtasksWithFallback_SubprocessSucceeds(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": [{"order": 1, "title": "API-extracted task one", "description": "From subprocess"}, {"order": 2, "title": "API-extracted task two", "description": "From subprocess"}]}`), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 2 {
		t.Fatalf("expected 2 subtasks from subprocess, got %d", len(subtasks))
	}

	if subtasks[0].Title != "API-extracted task one" {
		t.Errorf("subtask 0 title = %q, want %q (should be from subprocess, not regex)", subtasks[0].Title, "API-extracted task one")
	}
	if subtasks[1].Title != "API-extracted task two" {
		t.Errorf("subtask 1 title = %q, want %q (should be from subprocess, not regex)", subtasks[1].Title, "API-extracted task two")
	}
}

func TestParseSubtasksWithFallback_SubprocessError_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("subprocess error")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	// Regex should extract 4 subtasks from samplePlanningOutput
	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}

	if subtasks[0].Title != "Add database migration" {
		t.Errorf("subtask 0 title = %q, want %q (should be from regex fallback)", subtasks[0].Title, "Add database migration")
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
		t.Errorf("subtask 0 title = %q, want %q", subtasks[0].Title, "Add database migration")
	}
}

func TestParseSubtasksWithFallback_MalformedJSON_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte("not valid json"), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestParseSubtasksWithFallback_EmptySubtasks_FallsBackToRegex(t *testing.T) {
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"subtasks": []}`), nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := newSubtaskParserWithRunner(runner, log)

	subtasks := parseSubtasksWithFallback(parser, samplePlanningOutput)

	if len(subtasks) != 4 {
		t.Fatalf("expected 4 subtasks from regex fallback, got %d", len(subtasks))
	}
}

func TestNewSubtaskParser_BinaryMissing(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	parser := NewSubtaskParser("this-binary-does-not-exist-on-path-xyz-abc", log)
	if parser != nil {
		t.Error("expected nil parser when binary is not found on PATH")
	}
}
