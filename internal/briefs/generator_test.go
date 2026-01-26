package briefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

func setupTestStore(t *testing.T) (*memory.Store, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "briefs_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestGeneratorGenerate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed test data
	now := time.Now()
	executions := []*memory.Execution{
		{
			ID:          "exec-1",
			TaskID:      "TASK-001",
			ProjectPath: "/test/project",
			Status:      "completed",
			DurationMs:  120000,
			PRUrl:       "https://github.com/test/pr/1",
			CreatedAt:   now.Add(-1 * time.Hour),
			CompletedAt: timePtr(now.Add(-30 * time.Minute)),
		},
		{
			ID:          "exec-2",
			TaskID:      "TASK-002",
			ProjectPath: "/test/project",
			Status:      "completed",
			DurationMs:  60000,
			CreatedAt:   now.Add(-2 * time.Hour),
			CompletedAt: timePtr(now.Add(-1 * time.Hour)),
		},
		{
			ID:          "exec-3",
			TaskID:      "TASK-003",
			ProjectPath: "/test/project",
			Status:      "failed",
			Error:       "tests failed: auth_test.go:42",
			CreatedAt:   now.Add(-3 * time.Hour),
			CompletedAt: timePtr(now.Add(-2 * time.Hour)),
		},
	}

	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Create generator
	config := DefaultBriefConfig()
	config.Content.IncludeErrors = true
	generator := NewGenerator(store, config)

	// Generate brief for the past 24 hours
	period := BriefPeriod{
		Start: now.Add(-24 * time.Hour),
		End:   now,
	}

	brief, err := generator.Generate(period)
	if err != nil {
		t.Fatalf("failed to generate brief: %v", err)
	}

	// Verify results
	if len(brief.Completed) != 2 {
		t.Errorf("expected 2 completed tasks, got %d", len(brief.Completed))
	}

	if len(brief.Blocked) != 1 {
		t.Errorf("expected 1 blocked task, got %d", len(brief.Blocked))
	}

	if brief.Blocked[0].Error != "tests failed: auth_test.go:42" {
		t.Errorf("expected error message, got %s", brief.Blocked[0].Error)
	}

	if brief.Metrics.CompletedCount != 2 {
		t.Errorf("expected 2 completed in metrics, got %d", brief.Metrics.CompletedCount)
	}

	if brief.Metrics.FailedCount != 1 {
		t.Errorf("expected 1 failed in metrics, got %d", brief.Metrics.FailedCount)
	}
}

func TestGeneratorGenerateDaily(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	config := DefaultBriefConfig()
	config.Timezone = "UTC"
	generator := NewGenerator(store, config)

	brief, err := generator.GenerateDaily()
	if err != nil {
		t.Fatalf("failed to generate daily brief: %v", err)
	}

	if brief == nil {
		t.Fatal("expected brief, got nil")
	}

	// Period should be 24 hours
	duration := brief.Period.End.Sub(brief.Period.Start)
	if duration != 24*time.Hour {
		t.Errorf("expected 24h period, got %v", duration)
	}
}

func TestGeneratorGenerateWeekly(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	config := DefaultBriefConfig()
	config.Timezone = "UTC"
	generator := NewGenerator(store, config)

	brief, err := generator.GenerateWeekly()
	if err != nil {
		t.Fatalf("failed to generate weekly brief: %v", err)
	}

	if brief == nil {
		t.Fatal("expected brief, got nil")
	}

	// Period should be 7 days
	duration := brief.Period.End.Sub(brief.Period.Start)
	if duration != 7*24*time.Hour {
		t.Errorf("expected 168h (7 day) period, got %v", duration)
	}
}

func TestGeneratorWithProjectFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed test data with different projects
	now := time.Now()
	executions := []*memory.Execution{
		{
			ID:          "exec-1",
			TaskID:      "TASK-001",
			ProjectPath: "/project/alpha",
			Status:      "completed",
			CreatedAt:   now.Add(-1 * time.Hour),
			CompletedAt: timePtr(now.Add(-30 * time.Minute)),
		},
		{
			ID:          "exec-2",
			TaskID:      "TASK-002",
			ProjectPath: "/project/beta",
			Status:      "completed",
			CreatedAt:   now.Add(-2 * time.Hour),
			CompletedAt: timePtr(now.Add(-1 * time.Hour)),
		},
	}

	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Create generator with filter
	config := DefaultBriefConfig()
	config.Filters.Projects = []string{"/project/alpha"}
	generator := NewGenerator(store, config)

	period := BriefPeriod{
		Start: now.Add(-24 * time.Hour),
		End:   now,
	}

	brief, err := generator.Generate(period)
	if err != nil {
		t.Fatalf("failed to generate brief: %v", err)
	}

	// Should only have alpha project
	if len(brief.Completed) != 1 {
		t.Errorf("expected 1 completed task (filtered), got %d", len(brief.Completed))
	}

	if brief.Completed[0].ProjectPath != "/project/alpha" {
		t.Errorf("expected alpha project, got %s", brief.Completed[0].ProjectPath)
	}
}

func TestDefaultBriefConfig(t *testing.T) {
	config := DefaultBriefConfig()

	if config.Enabled {
		t.Error("expected disabled by default")
	}

	if config.Schedule != "0 9 * * 1-5" {
		t.Errorf("unexpected schedule: %s", config.Schedule)
	}

	if config.Timezone != "America/New_York" {
		t.Errorf("unexpected timezone: %s", config.Timezone)
	}

	if !config.Content.IncludeMetrics {
		t.Error("expected metrics included by default")
	}

	if !config.Content.IncludeErrors {
		t.Error("expected errors included by default")
	}

	if config.Content.MaxItemsPerSection != 10 {
		t.Errorf("expected max items 10, got %d", config.Content.MaxItemsPerSection)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func TestEstimateProgress(t *testing.T) {
	tests := []struct {
		name       string
		durationMs int64
		expected   int
	}{
		{"no duration", 0, 10},
		{"short duration", 30000, 10},     // 30s
		{"mid duration", 150000, 50},      // 2.5min -> 50%
		{"long duration", 600000, 95},     // 10min -> capped at 95%
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &memory.Execution{DurationMs: tt.durationMs}
			result := estimateProgress(exec)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

// Integration test to verify the full path works
func TestGeneratorIntegration(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "briefs_integration")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create store
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create generator with full config
	config := &BriefConfig{
		Enabled:  true,
		Schedule: "0 9 * * *",
		Timezone: "UTC",
		Channels: []ChannelConfig{
			{Type: "slack", Channel: "#test"},
		},
		Content: ContentConfig{
			IncludeMetrics:     true,
			IncludeErrors:      true,
			MaxItemsPerSection: 5,
		},
		Filters: FilterConfig{
			Projects: []string{},
		},
	}

	generator := NewGenerator(store, config)

	// Test that empty brief works
	brief, err := generator.GenerateDaily()
	if err != nil {
		t.Fatalf("failed to generate empty brief: %v", err)
	}

	if brief == nil {
		t.Fatal("expected brief, got nil")
	}

	// Verify empty brief has correct structure
	if len(brief.Completed) != 0 {
		t.Errorf("expected 0 completed, got %d", len(brief.Completed))
	}

	if brief.Metrics.TotalTasks != 0 {
		t.Errorf("expected 0 total tasks, got %d", brief.Metrics.TotalTasks)
	}

	// Verify brief can be formatted
	formatter := NewPlainTextFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format brief: %v", err)
	}

	if text == "" {
		t.Error("expected non-empty formatted text")
	}

	// Verify directory is clean
	files, _ := filepath.Glob(filepath.Join(tmpDir, "*"))
	if len(files) != 1 { // Should only have pilot.db
		t.Logf("warning: unexpected files in temp dir: %v", files)
	}
}
