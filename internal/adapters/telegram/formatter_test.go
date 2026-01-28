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
			name:     "removes EXIT_SIGNAL",
			input:    "Task done\nEXIT_SIGNAL: true\nCompleted",
			expected: "Task done\nCompleted",
		},
		{
			name:     "removes LOOP COMPLETE",
			input:    "Done\nLOOP COMPLETE\nEnd",
			expected: "Done\nEnd",
		},
		{
			name:     "removes NAVIGATOR_STATUS block",
			input:    "Start\n━━━━━━━━━━\nNAVIGATOR_STATUS\nPhase: IMPL\nIteration: 2\n━━━━━━━━━━\nContinuing",
			expected: "Start\nContinuing",
		},
		{
			name:     "removes Phase and Progress lines",
			input:    "Working\nPhase: VERIFY\nProgress: 80%\nDone",
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

// TestFormatProgressUpdateBranchingPhase tests branching phase emoji
func TestFormatProgressUpdateBranchingPhase(t *testing.T) {
	got := FormatProgressUpdate("TG-100", "Branching", 5, "Creating branch")
	if !strings.Contains(got, "🌿") {
		t.Errorf("FormatProgressUpdate() should contain branching emoji, got:\n%s", got)
	}
}

// TestFormatProgressUpdateInstallingPhase tests installing phase emoji
func TestFormatProgressUpdateInstallingPhase(t *testing.T) {
	got := FormatProgressUpdate("TG-100", "Installing", 20, "npm install")
	if !strings.Contains(got, "📦") {
		t.Errorf("FormatProgressUpdate() should contain installing emoji, got:\n%s", got)
	}
}

// TestFormatProgressUpdateNegativeProgress tests negative progress clamping
func TestFormatProgressUpdateNegativeProgress(t *testing.T) {
	got := FormatProgressUpdate("TG-100", "Starting", -10, "")
	// Should have empty progress bar (all ░)
	if !strings.Contains(got, "░░░░░░░░░░░░░░░░░░░░") {
		t.Errorf("FormatProgressUpdate() should have empty bar for negative progress, got:\n%s", got)
	}
}

// TestFormatTaskStarted tests task started message formatting
func TestFormatTaskStarted(t *testing.T) {
	tests := []struct {
		name        string
		taskID      string
		description string
		contains    []string
	}{
		{
			name:        "basic task",
			taskID:      "TASK-01",
			description: "Create auth handler",
			contains:    []string{"🚀", "Executing", "TASK-01", "auth handler"},
		},
		{
			name:        "long description truncated",
			taskID:      "TG-123",
			description: strings.Repeat("a", 200),
			contains:    []string{"TG-123", "..."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTaskStarted(tt.taskID, tt.description)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatTaskStarted() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

// TestFormatQuestionAck tests question acknowledgment
func TestFormatQuestionAck(t *testing.T) {
	got := FormatQuestionAck()
	if !strings.Contains(got, "🔍") {
		t.Errorf("FormatQuestionAck() should contain search emoji, got: %q", got)
	}
	if !strings.Contains(got, "Looking") {
		t.Errorf("FormatQuestionAck() should contain 'Looking', got: %q", got)
	}
}

// TestFormatQuestionAnswer tests question answer formatting
func TestFormatQuestionAnswer(t *testing.T) {
	tests := []struct {
		name     string
		answer   string
		contains []string
		excludes []string
	}{
		{
			name:     "simple answer",
			answer:   "The auth module handles user authentication.",
			contains: []string{"auth module", "authentication"},
		},
		{
			name:     "answer with internal signals cleaned",
			answer:   "Answer here\nEXIT_SIGNAL: true\nMore text",
			contains: []string{"Answer here", "More text"},
			excludes: []string{"EXIT_SIGNAL"},
		},
		{
			name:     "answer with table converted",
			answer:   "Info:\n| Col1 | Col2 |\n|---|---|\n| A | B |",
			contains: []string{"A", "B"},
			excludes: []string{"|---|"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatQuestionAnswer(tt.answer)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatQuestionAnswer() = %q, want to contain %q", got, want)
				}
			}
			for _, exclude := range tt.excludes {
				if strings.Contains(got, exclude) {
					t.Errorf("FormatQuestionAnswer() = %q, should NOT contain %q", got, exclude)
				}
			}
		})
	}
}

// TestFormatQuestionAnswerTruncation tests long answer truncation
func TestFormatQuestionAnswerTruncation(t *testing.T) {
	longAnswer := strings.Repeat("a", 4000)
	got := FormatQuestionAnswer(longAnswer)

	if len(got) > 3600 { // 3500 + buffer for truncation message
		t.Errorf("FormatQuestionAnswer() length = %d, want <= 3600", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("FormatQuestionAnswer() should contain truncation indicator")
	}
}

// TestParseTableRow tests table row parsing
func TestParseTableRow(t *testing.T) {
	tests := []struct {
		row      string
		expected []string
	}{
		{
			row:      "| Col1 | Col2 | Col3 |",
			expected: []string{"Col1", "Col2", "Col3"},
		},
		{
			row:      "|A|B|",
			expected: []string{"A", "B"},
		},
		{
			row:      "| Single |",
			expected: []string{"Single"},
		},
		{
			row:      "| --- | --- |",
			expected: []string{},
		},
		{
			row:      "|  Spaces  |  Here  |",
			expected: []string{"Spaces", "Here"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			got := parseTableRow(tt.row)
			if len(got) != len(tt.expected) {
				t.Errorf("parseTableRow() len = %d, want %d", len(got), len(tt.expected))
				return
			}
			for i, v := range got {
				if v != tt.expected[i] {
					t.Errorf("parseTableRow()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}

// TestMin tests the min helper function
func TestMin(t *testing.T) {
	tests := []struct {
		a, b, expected int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 50, 50},
	}

	for _, tt := range tests {
		got := min(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.expected)
		}
	}
}

// TestFormatSuccessResultWithFiles tests output with file operations
func TestFormatSuccessResultWithFiles(t *testing.T) {
	result := &executor.ExecutionResult{
		TaskID:   "TASK-01",
		Success:  true,
		Duration: 30 * time.Second,
		Output:   "Created `auth.go`\nModified `main.go`\nAdded `test.go`",
	}

	got := FormatTaskResult(result)

	wantContains := []string{"✅", "TASK-01", "30s", "auth.go", "main.go", "test.go"}
	for _, want := range wantContains {
		if !strings.Contains(got, want) {
			t.Errorf("FormatTaskResult() should contain %q, got:\n%s", want, got)
		}
	}
}

// TestFormatFailureResultTruncation tests error truncation
func TestFormatFailureResultTruncation(t *testing.T) {
	longError := strings.Repeat("error ", 100)
	result := &executor.ExecutionResult{
		TaskID:   "TASK-ERR",
		Success:  false,
		Duration: 5 * time.Second,
		Error:    longError,
	}

	got := FormatTaskResult(result)

	if len(got) > 600 { // Error truncated to 400 + metadata
		t.Errorf("FormatTaskResult() too long: %d chars", len(got))
	}
	if !strings.Contains(got, "...") {
		t.Error("FormatTaskResult() should contain truncation indicator")
	}
}

// TestCleanInternalSignalsNavigatorBlock tests NAVIGATOR_STATUS block removal
func TestCleanInternalSignalsNavigatorBlock(t *testing.T) {
	input := `Start of output
━━━━━━━━━━
NAVIGATOR_STATUS
Phase: IMPL
Iteration: 2
Progress: 50%
━━━━━━━━━━
End of output`

	got := cleanInternalSignals(input)

	if strings.Contains(got, "NAVIGATOR_STATUS") {
		t.Error("cleanInternalSignals should remove NAVIGATOR_STATUS")
	}
	if strings.Contains(got, "Phase: IMPL") {
		t.Error("cleanInternalSignals should remove Phase line")
	}
	if !strings.Contains(got, "Start of output") {
		t.Error("cleanInternalSignals should keep Start of output")
	}
	if !strings.Contains(got, "End of output") {
		t.Error("cleanInternalSignals should keep End of output")
	}
}

// TestCleanInternalSignalsAllSignals tests all signal types
func TestCleanInternalSignalsAllSignals(t *testing.T) {
	signals := []string{
		"EXIT_SIGNAL: true",
		"EXIT_SIGNAL:true",
		"LOOP COMPLETE",
		"TASK MODE COMPLETE",
		"Iteration: 5",
		"Completion Indicators: done",
		"Exit Conditions: met",
		"State Hash: abc123",
		"Next Action: verify",
	}

	for _, signal := range signals {
		t.Run(signal, func(t *testing.T) {
			input := "Before\n" + signal + "\nAfter"
			got := cleanInternalSignals(input)

			if strings.Contains(got, signal) {
				t.Errorf("cleanInternalSignals should remove %q, got:\n%s", signal, got)
			}
		})
	}
}

// TestExtractSummaryPatterns tests all extraction patterns
func TestExtractSummaryPatterns(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "created pattern",
			input:    "Created `newfile.go`",
			contains: []string{"📁 Created:", "newfile.go"},
		},
		{
			name:     "modified pattern",
			input:    "Modified existing.go",
			contains: []string{"📝 Modified:", "existing.go"},
		},
		{
			name:     "added pattern",
			input:    "Added feature.ts to the project",
			contains: []string{"➕ Added:", "feature.ts"},
		},
		{
			name:     "deleted pattern",
			input:    "Deleted old_file.txt",
			contains: []string{"🗑 Deleted:", "old_file.txt"},
		},
		{
			name:     "multiple files",
			input:    "Created a.go\nModified b.go\nAdded c.go",
			contains: []string{"a.go", "b.go", "c.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSummary(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("extractSummary() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

// TestExtractSummaryLimit tests the 5-item limit
func TestExtractSummaryLimit(t *testing.T) {
	// The function extracts max 5 matches per pattern, and we have multiple patterns
	// With only one pattern type, we get max 5 items
	input := `Created a.go
Created b.go
Created c.go
Created d.go
Created e.go
Created f.go
Created g.go`

	got := extractSummary(input)

	// Count Created entries - should be 5 (max per pattern)
	count := strings.Count(got, "Created:")
	if count != 5 {
		t.Errorf("extractSummary() has %d items, want 5", count)
	}
}

// TestEscapeMarkdownAllChars tests all markdown special characters
func TestEscapeMarkdownAllChars(t *testing.T) {
	specialChars := []struct {
		char    string
		escaped string
	}{
		{"_", "\\_"},
		{"*", "\\*"},
		{"[", "\\["},
		{"]", "\\]"},
		{"(", "\\("},
		{")", "\\)"},
		{"~", "\\~"},
		{">", "\\>"},
		{"#", "\\#"},
		{"+", "\\+"},
		{"-", "\\-"},
		{"=", "\\="},
		{"|", "\\|"},
		{"{", "\\{"},
		{"}", "\\}"},
		{".", "\\."},
		{"!", "\\!"},
	}

	for _, tc := range specialChars {
		t.Run("char_"+tc.char, func(t *testing.T) {
			input := "text" + tc.char + "more"
			got := escapeMarkdown(input)
			if !strings.Contains(got, tc.escaped) {
				t.Errorf("escapeMarkdown(%q) = %q, want to contain %q", input, got, tc.escaped)
			}
		})
	}
}

// TestConvertTablesToListsComplex tests complex table scenarios
func TestConvertTablesToListsComplex(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		excludes []string
	}{
		{
			name: "table with many columns",
			input: `| Name | Status | Priority | Owner |
|------|--------|----------|-------|
| Task1 | Done | High | Alice |
| Task2 | WIP | Low | Bob |`,
			contains: []string{"Task1: Done", "Task2: WIP"},
			excludes: []string{"|---"},
		},
		{
			name: "multiple tables",
			input: `First table:
| A | B |
|---|---|
| 1 | 2 |

Second table:
| C | D |
|---|---|
| 3 | 4 |`,
			contains: []string{"1", "2", "3", "4"},
		},
		{
			name: "text between tables",
			input: `Before
| X | Y |
|---|---|
| a | b |
Middle text
| P | Q |
|---|---|
| c | d |
After`,
			contains: []string{"Before", "Middle text", "After", "a", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertTablesToLists(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("convertTablesToLists() should contain %q, got:\n%s", want, got)
				}
			}
			for _, exclude := range tt.excludes {
				if strings.Contains(got, exclude) {
					t.Errorf("convertTablesToLists() should NOT contain %q, got:\n%s", exclude, got)
				}
			}
		})
	}
}

// TestFormatFailureResultEmpty tests failure with empty error
func TestFormatFailureResultEmpty(t *testing.T) {
	result := &executor.ExecutionResult{
		TaskID:   "TASK-ERR",
		Success:  false,
		Duration: 5 * time.Second,
		Error:    "",
	}

	got := FormatTaskResult(result)

	if !strings.Contains(got, "Unknown error") {
		t.Errorf("FormatTaskResult() should contain 'Unknown error' for empty error, got:\n%s", got)
	}
}

// TestFormatTaskResultSuccessNoOutput tests success with no output
func TestFormatTaskResultSuccessNoOutput(t *testing.T) {
	result := &executor.ExecutionResult{
		TaskID:   "TASK-01",
		Success:  true,
		Duration: 10 * time.Second,
		Output:   "",
	}

	got := FormatTaskResult(result)

	if !strings.Contains(got, "completed") {
		t.Errorf("FormatTaskResult() should contain 'completed', got:\n%s", got)
	}
	if strings.Contains(got, "Summary") {
		t.Error("FormatTaskResult() should not have Summary section for empty output")
	}
}

// TestFormatTaskResultSuccessWithInternalSignals tests cleanup in success output
func TestFormatTaskResultSuccessWithInternalSignals(t *testing.T) {
	result := &executor.ExecutionResult{
		TaskID:   "TASK-01",
		Success:  true,
		Duration: 20 * time.Second,
		Output:   "Created file.go\nEXIT_SIGNAL: true\nLOOP COMPLETE",
	}

	got := FormatTaskResult(result)

	if strings.Contains(got, "EXIT_SIGNAL") {
		t.Error("FormatTaskResult() should clean EXIT_SIGNAL from output")
	}
	if strings.Contains(got, "LOOP COMPLETE") {
		t.Error("FormatTaskResult() should clean LOOP COMPLETE from output")
	}
}

// TestInternalSignalsArray tests the internal signals slice
func TestInternalSignalsArray(t *testing.T) {
	expectedSignals := []string{
		"EXIT_SIGNAL: true",
		"EXIT_SIGNAL:true",
		"LOOP COMPLETE",
		"TASK MODE COMPLETE",
		"NAVIGATOR_STATUS",
	}

	for _, expected := range expectedSignals {
		found := false
		for _, signal := range internalSignals {
			if signal == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("internalSignals should contain %q", expected)
		}
	}
}

// TestCleanInternalSignalsWithSeparators tests separator handling
func TestCleanInternalSignalsWithSeparators(t *testing.T) {
	input := `Before content
━━━━━━━━━━
More text
━━━━━━━━━━
After content`

	got := cleanInternalSignals(input)

	// Separators should be removed
	if strings.Contains(got, "━━━━━━━━━━") {
		t.Errorf("cleanInternalSignals should remove separators, got:\n%s", got)
	}
}

// TestTruncateDescriptionEdgeCases tests edge cases
func TestTruncateDescriptionEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
		{
			name:     "only whitespace",
			input:    "   \n\t  ",
			maxLen:   10,
			expected: "",
		},
		{
			name:     "exactly max length",
			input:    "12345",
			maxLen:   5,
			expected: "12345",
		},
		{
			name:     "one char over",
			input:    "123456",
			maxLen:   5,
			expected: "12...",
		},
		{
			name:     "multiple newlines",
			input:    "line1\nline2\nline3",
			maxLen:   50,
			expected: "line1 line2 line3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateDescription(tt.input, tt.maxLen)
			if got != tt.expected {
				t.Errorf("truncateDescription(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expected)
			}
		})
	}
}
