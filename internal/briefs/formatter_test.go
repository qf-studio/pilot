package briefs

import (
	"strings"
	"testing"
	"time"
)

func createTestBrief() *Brief {
	now := time.Date(2026, 1, 26, 9, 0, 0, 0, time.UTC)
	completedAt := now.Add(-30 * time.Minute)

	return &Brief{
		GeneratedAt: now,
		Period: BriefPeriod{
			Start: now.Add(-24 * time.Hour),
			End:   now,
		},
		Completed: []TaskSummary{
			{
				ID:          "TASK-001",
				Title:       "Add user auth",
				ProjectPath: "/test/project",
				Status:      "completed",
				PRUrl:       "https://github.com/test/pr/1",
				DurationMs:  120000,
				CompletedAt: &completedAt,
			},
			{
				ID:          "TASK-002",
				Title:       "Fix login bug",
				ProjectPath: "/test/project",
				Status:      "completed",
				DurationMs:  60000,
				CompletedAt: &completedAt,
			},
		},
		InProgress: []TaskSummary{
			{
				ID:          "TASK-003",
				Title:       "Add payments",
				ProjectPath: "/test/project",
				Status:      "running",
				Progress:    65,
			},
		},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:          "TASK-004",
					Title:       "API refactor",
					ProjectPath: "/test/project",
					Status:      "failed",
				},
				Error:    "tests failed: auth_test.go:42: expected 200, got 401",
				FailedAt: now.Add(-1 * time.Hour),
			},
		},
		Upcoming: []TaskSummary{
			{
				ID:          "TASK-005",
				Title:       "Dashboard redesign",
				ProjectPath: "/test/project",
				Status:      "queued",
			},
		},
		Metrics: BriefMetrics{
			TotalTasks:     20,
			CompletedCount: 17,
			FailedCount:    3,
			SuccessRate:    0.85,
			AvgDurationMs:  720000, // 12 minutes
			PRsCreated:     15,
		},
	}
}

func TestPlainTextFormatter(t *testing.T) {
	brief := createTestBrief()
	formatter := NewPlainTextFormatter()

	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Check for expected content
	expectations := []string{
		"PILOT DAILY BRIEF",
		"Jan 26, 2026",
		"COMPLETED (2)",
		"TASK-001",
		"https://github.com/test/pr/1",
		"IN PROGRESS (1)",
		"TASK-003",
		"65%",
		"BLOCKED (1)",
		"TASK-004",
		"auth_test.go:42",
		"UPCOMING (1)",
		"TASK-005",
		"METRICS",
		"85%",
		"12m",
	}

	for _, expected := range expectations {
		if !strings.Contains(text, expected) {
			t.Errorf("expected %q in output, not found", expected)
		}
	}
}

func TestSlackFormatter(t *testing.T) {
	brief := createTestBrief()
	formatter := NewSlackFormatter()

	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Check for Slack-specific formatting
	expectations := []string{
		":bar_chart: *Pilot Daily Brief*",
		":white_check_mark: Completed (2)",
		"`TASK-001`",
		"<https://github.com/test/pr/1|PR ready>",
		":arrows_counterclockwise: In Progress (1)",
		"▓", // Progress bar filled
		"░", // Progress bar empty
		":no_entry: Blocked (1)",
		":clipboard: Upcoming (1)",
		":chart_with_upwards_trend: Metrics",
		"*85%*",
	}

	for _, expected := range expectations {
		if !strings.Contains(text, expected) {
			t.Errorf("expected %q in output, not found", expected)
		}
	}
}

func TestSlackFormatterBlocks(t *testing.T) {
	brief := createTestBrief()
	formatter := NewSlackFormatter()

	blocks := formatter.SlackBlocks(brief)

	if len(blocks) == 0 {
		t.Fatal("expected blocks, got none")
	}

	// First block should be header
	if blocks[0]["type"] != "header" {
		t.Errorf("expected header block first, got %s", blocks[0]["type"])
	}

	// Should have section blocks for each section
	sectionCount := 0
	for _, block := range blocks {
		if block["type"] == "section" {
			sectionCount++
		}
	}

	// At minimum: completed, in progress, blocked, upcoming = 4 sections
	if sectionCount < 4 {
		t.Errorf("expected at least 4 section blocks, got %d", sectionCount)
	}

	// Should have a divider
	hasDivider := false
	for _, block := range blocks {
		if block["type"] == "divider" {
			hasDivider = true
			break
		}
	}
	if !hasDivider {
		t.Error("expected divider block")
	}

	// Should have context for metrics
	hasContext := false
	for _, block := range blocks {
		if block["type"] == "context" {
			hasContext = true
			break
		}
	}
	if !hasContext {
		t.Error("expected context block for metrics")
	}
}

func TestEmailFormatter(t *testing.T) {
	brief := createTestBrief()
	formatter := NewEmailFormatter()

	html, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Check for HTML structure
	expectations := []string{
		"<!DOCTYPE html>",
		"<html>",
		"<head>",
		"<style>",
		"Pilot Daily Brief",
		"TASK-001",
		"href=\"https://github.com/test/pr/1\"",
		"progress-bar",
		"progress-fill",
		"65%",
		"TASK-004",
		"auth_test.go:42",
		"85%",
		"12m",
	}

	for _, expected := range expectations {
		if !strings.Contains(html, expected) {
			t.Errorf("expected %q in output, not found", expected)
		}
	}
}

func TestEmailFormatterSubject(t *testing.T) {
	brief := createTestBrief()
	formatter := NewEmailFormatter()

	subject := formatter.Subject(brief)

	expected := "Pilot Daily Brief — Jan 26, 2026"
	if subject != expected {
		t.Errorf("expected %q, got %q", expected, subject)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		ms       int64
		expected string
	}{
		{0, "N/A"},
		{30000, "30s"},
		{60000, "1m"},
		{90000, "1m"},
		{300000, "5m"},
		{720000, "12m"},
		{3600000, "1h 0m"},
		{5400000, "1h 30m"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := formatDuration(tt.ms)
			if result != tt.expected {
				t.Errorf("formatDuration(%d) = %q, want %q", tt.ms, result, tt.expected)
			}
		})
	}
}

func TestSlackProgressBar(t *testing.T) {
	tests := []struct {
		progress int
		filled   int
		empty    int
	}{
		{0, 0, 10},
		{10, 1, 9},
		{50, 5, 5},
		{65, 6, 4},
		{100, 10, 0},
	}

	for _, tt := range tests {
		t.Run(string(rune('0'+tt.progress/10)), func(t *testing.T) {
			result := generateSlackProgressBar(tt.progress)

			filledCount := strings.Count(result, "▓")
			emptyCount := strings.Count(result, "░")

			if filledCount != tt.filled {
				t.Errorf("expected %d filled, got %d", tt.filled, filledCount)
			}
			if emptyCount != tt.empty {
				t.Errorf("expected %d empty, got %d", tt.empty, emptyCount)
			}
		})
	}
}

func TestEmptyBriefFormatting(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked:    []BlockedTask{},
		Upcoming:   []TaskSummary{},
		Metrics:    BriefMetrics{},
	}

	// All formatters should handle empty briefs
	formatters := []struct {
		name      string
		formatter Formatter
	}{
		{"plain", NewPlainTextFormatter()},
		{"slack", NewSlackFormatter()},
		{"email", NewEmailFormatter()},
	}

	for _, f := range formatters {
		t.Run(f.name, func(t *testing.T) {
			text, err := f.formatter.Format(brief)
			if err != nil {
				t.Fatalf("failed to format empty brief: %v", err)
			}
			if text == "" {
				t.Error("expected non-empty output for empty brief")
			}
		})
	}
}
