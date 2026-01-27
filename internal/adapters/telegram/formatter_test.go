package telegram

import (
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
)

func TestCleanInternalSignals(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "clean text stays clean",
			input:    "Created file.go\nModified main.go",
			expected: "Created file.go\nModified main.go",
		},
		{
			name:  "removes EXIT_SIGNAL",
			input: "Task done\nEXIT_SIGNAL: true\nCompleted",
			expected: "Task done\nCompleted",
		},
		{
			name:  "removes LOOP COMPLETE",
			input: "Done\nLOOP COMPLETE\nEnd",
			expected: "Done\nEnd",
		},
		{
			name:  "removes NAVIGATOR_STATUS block",
			input: "Start\n━━━━━━━━━━\nNAVIGATOR_STATUS\nPhase: IMPL\nIteration: 2\n━━━━━━━━━━\nContinuing",
			expected: "Start\nContinuing",
		},
		{
			name:  "removes Phase and Progress lines",
			input: "Working\nPhase: VERIFY\nProgress: 80%\nDone",
			expected: "Working\nDone",
		},
		{
			name:     "trims leading empty lines",
			input:    "\n\n\nActual content",
			expected: "Actual content",
		},
		{
			name:     "trims trailing empty lines",
			input:    "Content\n\n\n",
			expected: "Content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanInternalSignals(tt.input)
			if got != tt.expected {
				t.Errorf("cleanInternalSignals() =\n%q\nwant\n%q", got, tt.expected)
			}
		})
	}
}

func TestExtractSummary(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		empty    bool
	}{
		{
			name:  "empty input",
			input: "",
			empty: true,
		},
		{
			name:     "finds created files",
			input:    "Created `handler.go` in internal/",
			contains: []string{"📁 Created:", "handler.go"},
		},
		{
			name:     "finds modified files",
			input:    "Modified main.go",
			contains: []string{"📝 Modified:", "main.go"},
		},
		{
			name:     "finds added files",
			input:    "Added new feature to app.tsx",
			contains: []string{"➕ Added:", "app.tsx"},
		},
		{
			name:     "multiple patterns",
			input:    "Created auth.go\nModified config.go",
			contains: []string{"auth.go", "config.go"},
		},
		{
			name:  "no matches",
			input: "Some random text without file operations",
			empty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSummary(tt.input)
			if tt.empty {
				if got != "" {
					t.Errorf("extractSummary() = %q, want empty", got)
				}
				return
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("extractSummary() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestFormatTaskResult(t *testing.T) {
	tests := []struct {
		name     string
		result   *executor.ExecutionResult
		contains []string
	}{
		{
			name: "success result",
			result: &executor.ExecutionResult{
				TaskID:   "TG-123",
				Success:  true,
				Duration: 45 * time.Second,
				Output:   "Created auth.go",
			},
			contains: []string{"✅", "TG-123", "45s"},
		},
		{
			name: "success with commit",
			result: &executor.ExecutionResult{
				TaskID:    "TG-456",
				Success:   true,
				Duration:  30 * time.Second,
				CommitSHA: "abc12345def",
			},
			contains: []string{"✅", "Commit:", "abc12345"},
		},
		{
			name: "success with PR",
			result: &executor.ExecutionResult{
				TaskID:   "TG-789",
				Success:  true,
				Duration: 60 * time.Second,
				PRUrl:    "https://github.com/org/repo/pull/123",
			},
			contains: []string{"✅", "PR:", "github.com"},
		},
		{
			name: "failure result",
			result: &executor.ExecutionResult{
				TaskID:   "TG-ERR",
				Success:  false,
				Duration: 10 * time.Second,
				Error:    "Build failed: missing dependency",
			},
			contains: []string{"❌", "failed", "missing dependency"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTaskResult(tt.result)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatTaskResult() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestFormatGreeting(t *testing.T) {
	tests := []struct {
		name     string
		username string
		contains []string
	}{
		{
			name:     "with username",
			username: "Alice",
			contains: []string{"👋", "Alice"},
		},
		{
			name:     "without username",
			username: "",
			contains: []string{"👋", "there"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatGreeting(tt.username)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatGreeting() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestFormatTaskConfirmation(t *testing.T) {
	got := FormatTaskConfirmation("TG-123", "Add auth handler", "/project/path")

	wants := []string{"📋", "TG-123", "auth handler", "/project/path"}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("FormatTaskConfirmation() = %q, want to contain %q", got, want)
		}
	}
}

func TestTruncateDescription(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length",
			input:    "hello",
			maxLen:   5,
			expected: "hello",
		},
		{
			name:     "needs truncation",
			input:    "hello world this is a long string",
			maxLen:   15,
			expected: "hello world ...",
		},
		{
			name:     "removes newlines",
			input:    "hello\nworld",
			maxLen:   20,
			expected: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDescription(tt.input, tt.maxLen)
			if got != tt.expected {
				t.Errorf("truncateDescription() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestEscapeMarkdown(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"hello_world", "\\_"},
		{"*bold*", "\\*"},
		{"[link]", "\\["},
		{"plain text", "plain text"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeMarkdown(tt.input)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("escapeMarkdown(%q) = %q, want to contain %q", tt.input, got, tt.contains)
			}
		})
	}
}

func TestConvertTablesToLists(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		excludes []string
	}{
		{
			name: "simple table",
			input: `Here's a table:
| Task | Status |
|------|--------|
| TASK-01 | Done |
| TASK-02 | Pending |`,
			contains: []string{"• TASK-01: Done", "• TASK-02: Pending"},
			excludes: []string{"|---"},
		},
		{
			name: "table with description",
			input: `| Name | Description | Priority |
|------|-------------|----------|
| Fix bug | Critical issue | High |`,
			contains: []string{"• Fix bug: Critical issue | High"},
		},
		{
			name:     "no table",
			input:    "Just regular text\nNo tables here",
			contains: []string{"Just regular text", "No tables here"},
		},
		{
			name: "mixed content",
			input: `## Summary
Some text before.

| Item | Value |
|------|-------|
| A | 1 |

Some text after.`,
			contains: []string{"## Summary", "• A: 1", "Some text after"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertTablesToLists(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("convertTablesToLists() =\n%s\nwant to contain %q", got, want)
				}
			}
			for _, exclude := range tt.excludes {
				if strings.Contains(got, exclude) {
					t.Errorf("convertTablesToLists() =\n%s\nshould NOT contain %q", got, exclude)
				}
			}
		})
	}
}

func TestFormatProgressUpdate(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		phase    string
		progress int
		message  string
		contains []string
	}{
		{
			name:     "starting phase",
			taskID:   "TG-123",
			phase:    "Starting",
			progress: 0,
			message:  "Initializing...",
			contains: []string{"🚀", "Starting", "(0%)", "TG-123", "░░░░░░░░░░░░░░░░░░░░", "Initializing"},
		},
		{
			name:     "exploring phase",
			taskID:   "TG-456",
			phase:    "Exploring",
			progress: 25,
			message:  "Reading files",
			contains: []string{"🔍", "Exploring", "25%", "█████░░░░░░░░░░░░░░░"},
		},
		{
			name:     "implementing phase 50%",
			taskID:   "TG-789",
			phase:    "Implementing",
			progress: 50,
			message:  "Creating handler.go",
			contains: []string{"⚙️", "Implementing", "50%", "██████████░░░░░░░░░░", "handler"}, // .go gets escaped to \.go
		},
		{
			name:     "testing phase",
			taskID:   "TG-999",
			phase:    "Testing",
			progress: 75,
			message:  "Running tests...",
			contains: []string{"🧪", "Testing", "75%", "███████████████░░░░░"},
		},
		{
			name:     "committing phase",
			taskID:   "TG-111",
			phase:    "Committing",
			progress: 90,
			message:  "",
			contains: []string{"💾", "Committing", "90%", "██████████████████░░"},
		},
		{
			name:     "completed phase",
			taskID:   "TG-222",
			phase:    "Completed",
			progress: 100,
			message:  "",
			contains: []string{"✅", "Completed", "100%", "████████████████████"},
		},
		{
			name:     "navigator phase",
			taskID:   "TG-333",
			phase:    "Navigator",
			progress: 10,
			message:  "Loading session",
			contains: []string{"🧭", "Navigator", "10%"},
		},
		{
			name:     "unknown phase uses default emoji",
			taskID:   "TG-444",
			phase:    "CustomPhase",
			progress: 40,
			message:  "",
			contains: []string{"⏳", "CustomPhase", "40%"},
		},
		{
			name:     "progress clamped to max 100",
			taskID:   "TG-555",
			phase:    "Completed",
			progress: 150,
			message:  "",
			contains: []string{"████████████████████"}, // Full bar
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatProgressUpdate(tt.taskID, tt.phase, tt.progress, tt.message)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatProgressUpdate() =\n%s\nwant to contain %q", got, want)
				}
			}
		})
	}
}
