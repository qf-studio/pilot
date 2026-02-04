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
		"FAILED (1)",
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

func TestPlainTextFormatterWithLongError(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:     "TASK-001",
					Status: "failed",
				},
				// Error longer than 60 characters should be truncated
				Error: "This is a very long error message that should be truncated because it exceeds the maximum length allowed in the plain text formatter output",
			},
		},
		Upcoming: []TaskSummary{},
		Metrics:  BriefMetrics{},
	}

	formatter := NewPlainTextFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Should contain truncated error with "..."
	if !strings.Contains(text, "...") {
		t.Error("expected truncated error with '...'")
	}
}

func TestPlainTextFormatterWithMultilineError(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:     "TASK-001",
					Status: "failed",
				},
				// Multi-line error - should only show first line
				Error: "First line of error\nSecond line\nThird line",
			},
		},
		Upcoming: []TaskSummary{},
		Metrics:  BriefMetrics{},
	}

	formatter := NewPlainTextFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Should contain first line
	if !strings.Contains(text, "First line of error") {
		t.Error("expected first line of error")
	}
}

func TestSlackFormatterWithLongError(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:     "TASK-001",
					Status: "failed",
				},
				// Error longer than 80 characters should be truncated
				Error: "This is a very long error message that should definitely be truncated because it is way too long for the Slack format",
			},
		},
		Upcoming: []TaskSummary{},
		Metrics:  BriefMetrics{},
	}

	formatter := NewSlackFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Should contain truncated error with "..."
	if !strings.Contains(text, "...") {
		t.Error("expected truncated error with '...'")
	}
}

func TestSlackFormatterBlocksWithLongError(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:     "TASK-001",
					Status: "failed",
				},
				// Error longer than 50 characters should be truncated in blocks
				Error: "This error message is longer than fifty characters and will be truncated",
			},
		},
		Upcoming: []TaskSummary{},
		Metrics:  BriefMetrics{},
	}

	formatter := NewSlackFormatter()
	blocks := formatter.SlackBlocks(brief)

	if len(blocks) == 0 {
		t.Fatal("expected blocks, got none")
	}

	// Find the blocked section and verify truncation
	found := false
	for _, block := range blocks {
		if block["type"] == "section" {
			if text, ok := block["text"].(map[string]interface{}); ok {
				if textStr, ok := text["text"].(string); ok {
					if strings.Contains(textStr, "Blocked") && strings.Contains(textStr, "...") {
						found = true
						break
					}
				}
			}
		}
	}

	if !found {
		t.Error("expected blocked section with truncated error")
	}
}

func TestEmailFormatterWithLongError(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked: []BlockedTask{
			{
				TaskSummary: TaskSummary{
					ID:     "TASK-001",
					Status: "failed",
				},
				// Error longer than 100 characters should be truncated
				Error: "This is a very very very long error message that should definitely be truncated because it is much too long for the email format output display",
			},
		},
		Upcoming: []TaskSummary{},
		Metrics:  BriefMetrics{},
	}

	formatter := NewEmailFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Should contain truncated error with "..."
	if !strings.Contains(text, "...") {
		t.Error("expected truncated error with '...'")
	}
}

func TestFormatDurationEdgeCases(t *testing.T) {
	tests := []struct {
		ms       int64
		expected string
	}{
		{45000, "45s"},     // 45 seconds
		{59000, "59s"},     // Just under a minute
		{61000, "1m"},      // Just over a minute
		{119000, "1m"},     // Just under 2 minutes
		{3599000, "59m"},   // Just under an hour
		{3601000, "1h 0m"}, // Just over an hour
		{7200000, "2h 0m"}, // 2 hours exact
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

func TestSlackFormatterWithCompletedTasksNoPR(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed: []TaskSummary{
			{
				ID:         "TASK-001",
				Title:      "Task without PR",
				Status:     "completed",
				PRUrl:      "", // No PR URL
				DurationMs: 60000,
			},
		},
		InProgress: []TaskSummary{},
		Blocked:    []BlockedTask{},
		Upcoming:   []TaskSummary{},
		Metrics:    BriefMetrics{CompletedCount: 1, TotalTasks: 1, SuccessRate: 1.0},
	}

	formatter := NewSlackFormatter()
	text, err := formatter.Format(brief)
	if err != nil {
		t.Fatalf("failed to format: %v", err)
	}

	// Should contain task ID but not PR link
	if !strings.Contains(text, "TASK-001") {
		t.Error("expected task ID in output")
	}
	if strings.Contains(text, "PR ready") {
		t.Error("should not contain PR link for task without PR")
	}
}

func TestSlackFormatterBlocksWithEmptySections(t *testing.T) {
	brief := &Brief{
		GeneratedAt: time.Now(),
		Period: BriefPeriod{
			Start: time.Now().Add(-24 * time.Hour),
			End:   time.Now(),
		},
		Completed:  []TaskSummary{},
		InProgress: []TaskSummary{},
		Blocked:    []BlockedTask{}, // Empty blocked - should not have blocked section
		Upcoming:   []TaskSummary{},
		Metrics:    BriefMetrics{},
	}

	formatter := NewSlackFormatter()
	blocks := formatter.SlackBlocks(brief)

	// Should not have blocked section when empty
	for _, block := range blocks {
		if block["type"] == "section" {
			if text, ok := block["text"].(map[string]interface{}); ok {
				if textStr, ok := text["text"].(string); ok {
					if strings.Contains(textStr, "Blocked") {
						t.Error("should not have blocked section when empty")
					}
				}
			}
		}
	}
}
