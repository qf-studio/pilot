package replay

import (
	"context"
	"fmt"
	"testing"
)

func TestNewPlayer(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording with events
	recorder, err := NewRecorder("TASK-PLAYER", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Analyzing..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/file.go"}}]}}`,
		`{"type":"result","result":"done"}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	// Load and create player
	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, err := NewPlayer(recording, nil)
	if err != nil {
		t.Fatalf("Failed to create player: %v", err)
	}

	if player.EventCount() != 4 {
		t.Errorf("Expected 4 events, got %d", player.EventCount())
	}
}

func TestPlayerPlay(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording
	recorder, _ := NewRecorder("TASK-PLAY", "/test/project", tmpDir)
	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`,
		`{"type":"result","result":"done"}`,
	}
	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	// Play back
	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, _ := NewPlayer(recording, nil)

	var playedEvents []*StreamEvent
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		playedEvents = append(playedEvents, event)
		return nil
	})

	if err := player.Play(context.Background()); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	if len(playedEvents) != 3 {
		t.Errorf("Expected 3 played events, got %d", len(playedEvents))
	}
}

func TestPlayerWithRange(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording with more events
	recorder, _ := NewRecorder("TASK-RANGE", "/test/project", tmpDir)
	for i := 0; i < 10; i++ {
		_ = recorder.RecordEvent(`{"type":"system","subtype":"test"}`)
	}
	_ = recorder.Finish("completed")

	// Play with range
	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	options := &ReplayOptions{
		StartAt: 3,
		StopAt:  7,
	}
	player, _ := NewPlayer(recording, options)

	var count int
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		count++
		return nil
	})

	if err := player.Play(context.Background()); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	if count != 4 { // Events 3, 4, 5, 6 (indices, stopAt is exclusive)
		t.Errorf("Expected 4 events in range, got %d", count)
	}
}

func TestPlayerCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording
	recorder, _ := NewRecorder("TASK-CANCEL", "/test/project", tmpDir)
	for i := 0; i < 100; i++ {
		_ = recorder.RecordEvent(`{"type":"system","subtype":"test"}`)
	}
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, _ := NewPlayer(recording, nil)

	ctx, cancel := context.WithCancel(context.Background())

	var count int
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		count++
		if count >= 10 {
			cancel()
		}
		return nil
	})

	err := player.Play(ctx)
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got: %v", err)
	}

	if count < 10 {
		t.Errorf("Should have played at least 10 events before cancel, got %d", count)
	}
}

func TestGetEvent(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-GET", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.RecordEvent(`{"type":"result","result":"done"}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, _ := NewPlayer(recording, nil)

	// Test valid index
	event := player.GetEvent(0)
	if event == nil {
		t.Error("Event at index 0 should not be nil")
	}

	event = player.GetEvent(1)
	if event == nil {
		t.Error("Event at index 1 should not be nil")
	}

	// Test invalid indices
	if player.GetEvent(-1) != nil {
		t.Error("Event at index -1 should be nil")
	}

	if player.GetEvent(100) != nil {
		t.Error("Event at index 100 should be nil")
	}
}

func TestFormatEvent(t *testing.T) {
	tests := []struct {
		name     string
		event    StreamEvent
		contains []string
	}{
		{
			name: "system init",
			event: StreamEvent{
				Sequence: 1,
				Parsed:   &ParsedEvent{Type: "system", Subtype: "init"},
			},
			contains: []string{"#1", "System initialized"},
		},
		{
			name: "tool call",
			event: StreamEvent{
				Sequence: 2,
				Parsed: &ParsedEvent{
					Type:     "assistant",
					ToolName: "Read",
					ToolInput: map[string]any{
						"file_path": "/test/file.go",
					},
				},
			},
			contains: []string{"#2", "Read", "file.go"},
		},
		{
			name: "text",
			event: StreamEvent{
				Sequence: 3,
				Parsed: &ParsedEvent{
					Type: "assistant",
					Text: "Analyzing the codebase...",
				},
			},
			contains: []string{"#3", "Analyzing"},
		},
		{
			name: "result success",
			event: StreamEvent{
				Sequence: 4,
				Parsed: &ParsedEvent{
					Type:         "result",
					IsError:      false,
					InputTokens:  100,
					OutputTokens: 50,
				},
			},
			contains: []string{"#4", "Completed", "tokens"},
		},
		{
			name: "result error",
			event: StreamEvent{
				Sequence: 5,
				Parsed: &ParsedEvent{
					Type:    "result",
					IsError: true,
					Result:  "Something went wrong",
				},
			},
			contains: []string{"#5", "Error", "Something went wrong"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := FormatEvent(&tc.event, false)
			for _, expected := range tc.contains {
				if !containsString(output, expected) {
					t.Errorf("Expected output to contain '%s', got: %s", expected, output)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestAnalyzer(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording with diverse events
	recorder, _ := NewRecorder("TASK-ANALYZE", "/test/project", tmpDir)

	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Phase: RESEARCH"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/a.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Phase: IMPL"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/test/b.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/test/c.go"}}]}}`,
		`{"type":"result","result":"done","usage":{"input_tokens":500,"output_tokens":200}}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	// Analyze
	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	analyzer, err := NewAnalyzer(recording)
	if err != nil {
		t.Fatalf("Failed to create analyzer: %v", err)
	}

	report, err := analyzer.Analyze()
	if err != nil {
		t.Fatalf("Analysis failed: %v", err)
	}

	// Verify tool usage was tracked
	if len(report.ToolUsage) == 0 {
		t.Error("Expected tool usage to be tracked")
	}

	// Check specific tools
	foundRead := false
	foundWrite := false
	for _, tool := range report.ToolUsage {
		if tool.Tool == "Read" {
			foundRead = true
			if tool.Count != 1 {
				t.Errorf("Expected 1 Read call, got %d", tool.Count)
			}
		}
		if tool.Tool == "Write" {
			foundWrite = true
		}
	}

	if !foundRead {
		t.Error("Read tool should be in usage stats")
	}
	if !foundWrite {
		t.Error("Write tool should be in usage stats")
	}
}

func TestExportToHTML(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-HTML", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.RecordEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	events, _ := LoadStreamEvents(recording)

	html, err := ExportToHTML(recording, events)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Check for expected HTML elements
	if !containsString(html, "<!DOCTYPE html>") {
		t.Error("HTML should contain DOCTYPE")
	}
	if !containsString(html, recording.ID) {
		t.Error("HTML should contain recording ID")
	}
	if !containsString(html, "Hello world") {
		t.Error("HTML should contain event text")
	}
}

func TestExportToJSON(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-JSON", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	events, _ := LoadStreamEvents(recording)

	jsonData, err := ExportToJSON(recording, events)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if len(jsonData) == 0 {
		t.Error("JSON export should not be empty")
	}

	// Verify it's valid JSON (contains expected fields)
	if !containsString(string(jsonData), "recording") {
		t.Error("JSON should contain recording field")
	}
	if !containsString(string(jsonData), "events") {
		t.Error("JSON should contain events field")
	}
}

func TestDefaultReplayOptions(t *testing.T) {
	opts := DefaultReplayOptions()

	if opts.StartAt != 0 {
		t.Errorf("Default StartAt should be 0, got %d", opts.StartAt)
	}
	if opts.StopAt != 0 {
		t.Errorf("Default StopAt should be 0, got %d", opts.StopAt)
	}
	if opts.Speed != 0 {
		t.Errorf("Default Speed should be 0 (instant), got %f", opts.Speed)
	}
	if !opts.ShowTools {
		t.Error("Default ShowTools should be true")
	}
	if !opts.ShowText {
		t.Error("Default ShowText should be true")
	}
}

func TestPlayerGetRecording(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-REC", "/test/project", tmpDir)
	recorder.SetBranch("test-branch")
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, err := NewPlayer(recording, nil)
	if err != nil {
		t.Fatalf("Failed to create player: %v", err)
	}

	rec := player.GetRecording()
	if rec == nil {
		t.Fatal("GetRecording should not return nil")
	}
	if rec.TaskID != "TASK-REC" {
		t.Errorf("Expected task ID TASK-REC, got %s", rec.TaskID)
	}
	if rec.Metadata.Branch != "test-branch" {
		t.Errorf("Expected branch test-branch, got %s", rec.Metadata.Branch)
	}
}

func TestFormatReport(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-REPORT", "/test/project", tmpDir)
	recorder.SetModel("claude-sonnet-4-5")

	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Phase: RESEARCH"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/a.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"func"}}]}}`,
		`{"type":"result","result":"error","is_error":true}`,
		`{"type":"result","result":"done","usage":{"input_tokens":500,"output_tokens":200}}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	analyzer, _ := NewAnalyzer(recording)
	report, _ := analyzer.Analyze()

	formatted := FormatReport(report)

	// Check for expected sections
	expectedContains := []string{
		"EXECUTION ANALYSIS REPORT",
		"Recording:",
		"Task:",
		"TASK-REPORT",
		"Status:",
		"Duration:",
		"Events:",
		"TOOL USAGE",
	}

	for _, expected := range expectedContains {
		if !containsString(formatted, expected) {
			t.Errorf("Expected report to contain '%s'", expected)
		}
	}
}

func TestFormatReportWithTokenUsage(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-TOKENS", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"result","result":"done","usage":{"input_tokens":1000,"output_tokens":500}}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	analyzer, _ := NewAnalyzer(recording)
	report, _ := analyzer.Analyze()

	formatted := FormatReport(report)

	// Check for token usage section
	if !containsString(formatted, "TOKEN USAGE") {
		t.Error("Expected report to contain TOKEN USAGE section")
	}
	if !containsString(formatted, "Input:") {
		t.Error("Expected report to contain Input token count")
	}
	if !containsString(formatted, "Output:") {
		t.Error("Expected report to contain Output token count")
	}
}

func TestFormatReportWithErrors(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-ERR", "/test/project", tmpDir)
	events := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/nonexistent"}}]}}`,
		`{"type":"result","result":"File not found","is_error":true}`,
	}
	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("failed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	analyzer, _ := NewAnalyzer(recording)
	report, _ := analyzer.Analyze()

	formatted := FormatReport(report)

	if !containsString(formatted, "ERRORS") {
		t.Error("Expected report to contain ERRORS section")
	}
}

func TestFormatToolCallAllTools(t *testing.T) {
	tests := []struct {
		name     string
		parsed   *ParsedEvent
		contains string
	}{
		{
			name: "Read tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Read",
				ToolInput: map[string]any{"file_path": "/path/to/file.go"},
			},
			contains: "Read",
		},
		{
			name: "Write tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Write",
				ToolInput: map[string]any{"file_path": "/path/to/new.go"},
			},
			contains: "Write",
		},
		{
			name: "Edit tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Edit",
				ToolInput: map[string]any{"file_path": "/path/to/edit.go"},
			},
			contains: "Edit",
		},
		{
			name: "Bash tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": "go test ./..."},
			},
			contains: "Bash",
		},
		{
			name: "Glob tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Glob",
				ToolInput: map[string]any{"pattern": "**/*.go"},
			},
			contains: "Glob",
		},
		{
			name: "Grep tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Grep",
				ToolInput: map[string]any{"pattern": "funcName"},
			},
			contains: "Grep",
		},
		{
			name: "Task tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Task",
				ToolInput: map[string]any{"description": "Run tests"},
			},
			contains: "Task",
		},
		{
			name: "Skill tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Skill",
				ToolInput: map[string]any{"skill": "commit"},
			},
			contains: "Skill",
		},
		{
			name: "Unknown tool",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "CustomTool",
				ToolInput: map[string]any{},
			},
			contains: "CustomTool",
		},
		{
			name: "Tool without detail",
			parsed: &ParsedEvent{
				Type:      "assistant",
				ToolName:  "Read",
				ToolInput: map[string]any{},
			},
			contains: "Read",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatToolCall(tc.parsed)
			if !containsString(result, tc.contains) {
				t.Errorf("Expected output to contain '%s', got: %s", tc.contains, result)
			}
		})
	}
}

func TestGetToolIcon(t *testing.T) {
	tests := []struct {
		tool     string
		expected string
	}{
		{"Read", "\U0001F4D6"},
		{"Write", "\u270F\uFE0F"},
		{"Edit", "\U0001F4DD"},
		{"Bash", "\U0001F4BB"},
		{"Glob", "\U0001F50D"},
		{"Grep", "\U0001F50E"},
		{"Task", "\U0001F916"},
		{"Skill", "\u26A1"},
		{"WebFetch", "\U0001F310"},
		{"WebSearch", "\U0001F50D"},
		{"UnknownTool", "\U0001F527"},
	}

	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			icon := getToolIcon(tc.tool)
			if icon != tc.expected {
				t.Errorf("Expected icon '%s' for tool %s, got '%s'", tc.expected, tc.tool, icon)
			}
		})
	}
}

func TestDetectPhaseFromText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected string
	}{
		{
			name:     "research phase",
			text:     "Phase: RESEARCH starting",
			expected: "Research",
		},
		{
			name:     "implement phase",
			text:     "Phase: implementing now",
			expected: "Implementing",
		},
		{
			name:     "verify phase",
			text:     "Phase: verify code",
			expected: "Verifying",
		},
		{
			name:     "complete phase",
			text:     "Phase: complete now",
			expected: "Completing",
		},
		{
			name:     "init phase",
			text:     "Phase: init started",
			expected: "Init",
		},
		{
			name:     "loop mode",
			text:     "LOOP MODE ACTIVATED",
			expected: "Init",
		},
		{
			name:     "task mode",
			text:     "TASK MODE ACTIVATED",
			expected: "Init",
		},
		{
			name:     "no phase",
			text:     "Just some regular text",
			expected: "",
		},
		{
			name:     "phase prefix without match",
			text:     "Phase: random stuff",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := detectPhaseFromText(tc.text)
			if result != tc.expected {
				t.Errorf("Expected phase '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "short path",
			path:     "a/b",
			expected: "a/b",
		},
		{
			name:     "three parts",
			path:     "a/b/c",
			expected: "a/b/c",
		},
		{
			name:     "long path",
			path:     "/home/user/projects/app/src/file.go",
			expected: ".../src/file.go",
		},
		{
			name:     "very long path",
			path:     "/a/b/c/d/e/f/g.txt",
			expected: ".../f/g.txt",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := shortenPath(tc.path)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
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
			input:    "hello world this is long",
			maxLen:   10,
			expected: "hello w...",
		},
		{
			name:     "with newlines",
			input:    "hello\nworld",
			maxLen:   20,
			expected: "hello world",
		},
		{
			name:     "with whitespace",
			input:    "  hello  ",
			maxLen:   10,
			expected: "hello",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := truncate(tc.input, tc.maxLen)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestFormatToolDetail(t *testing.T) {
	tests := []struct {
		name     string
		parsed   *ParsedEvent
		expected string
	}{
		{
			name: "Read with file_path",
			parsed: &ParsedEvent{
				ToolName:  "Read",
				ToolInput: map[string]any{"file_path": "/path/to/file.go"},
			},
			expected: "/path/to/file.go",
		},
		{
			name: "Write with file_path",
			parsed: &ParsedEvent{
				ToolName:  "Write",
				ToolInput: map[string]any{"file_path": "/path/to/new.go"},
			},
			expected: "/path/to/new.go",
		},
		{
			name: "Edit with file_path",
			parsed: &ParsedEvent{
				ToolName:  "Edit",
				ToolInput: map[string]any{"file_path": "/path/to/edit.go"},
			},
			expected: "/path/to/edit.go",
		},
		{
			name: "Bash with command",
			parsed: &ParsedEvent{
				ToolName:  "Bash",
				ToolInput: map[string]any{"command": "go test ./..."},
			},
			expected: "go test ./...",
		},
		{
			name: "Glob with pattern",
			parsed: &ParsedEvent{
				ToolName:  "Glob",
				ToolInput: map[string]any{"pattern": "**/*.go"},
			},
			expected: "**/*.go",
		},
		{
			name: "Grep with pattern",
			parsed: &ParsedEvent{
				ToolName:  "Grep",
				ToolInput: map[string]any{"pattern": "funcName"},
			},
			expected: "funcName",
		},
		{
			name: "Unknown tool",
			parsed: &ParsedEvent{
				ToolName:  "CustomTool",
				ToolInput: map[string]any{"custom": "value"},
			},
			expected: "",
		},
		{
			name: "Read without file_path",
			parsed: &ParsedEvent{
				ToolName:  "Read",
				ToolInput: map[string]any{},
			},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := formatToolDetail(tc.parsed)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestFormatEventUserType(t *testing.T) {
	event := StreamEvent{
		Sequence: 1,
		Parsed:   &ParsedEvent{Type: "user"},
	}

	output := FormatEvent(&event, false)
	if !containsString(output, "Tool result") {
		t.Errorf("Expected 'Tool result' for user type, got: %s", output)
	}
}

func TestFormatEventUnknownType(t *testing.T) {
	event := StreamEvent{
		Sequence: 1,
		Parsed:   &ParsedEvent{Type: "unknown_type"},
	}

	output := FormatEvent(&event, false)
	if !containsString(output, "unknown_type") {
		t.Errorf("Expected type in output, got: %s", output)
	}
}

func TestFormatEventNoParsed(t *testing.T) {
	event := StreamEvent{
		Sequence: 1,
		Type:     "raw_event",
		Parsed:   nil,
	}

	output := FormatEvent(&event, false)
	if !containsString(output, "raw_event") {
		t.Errorf("Expected raw type in output, got: %s", output)
	}
}

func TestFormatEventVerbose(t *testing.T) {
	longText := "This is a very long text that would normally be truncated when not in verbose mode but should be shown in full when verbose is enabled for debugging purposes and to see the complete context of what was happening during execution"

	event := StreamEvent{
		Sequence: 1,
		Parsed: &ParsedEvent{
			Type: "assistant",
			Text: longText,
		},
	}

	// Non-verbose should truncate
	outputShort := FormatEvent(&event, false)
	if containsString(outputShort, "debugging purposes") {
		t.Error("Non-verbose output should truncate long text")
	}

	// Verbose should show full text
	outputFull := FormatEvent(&event, true)
	if !containsString(outputFull, "debugging purposes") {
		t.Error("Verbose output should show full text")
	}
}

func TestFormatEventSystemSubtype(t *testing.T) {
	event := StreamEvent{
		Sequence: 1,
		Parsed:   &ParsedEvent{Type: "system", Subtype: "custom_system"},
	}

	output := FormatEvent(&event, false)
	if !containsString(output, "System:") || !containsString(output, "custom_system") {
		t.Errorf("Expected 'System: custom_system', got: %s", output)
	}
}

func TestPlayerPlayWithSpeed(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-SPEED", "/test/project", tmpDir)
	// Record events with different timestamps (simulated by sequence)
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.RecordEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Processing"}]}}`)
	_ = recorder.RecordEvent(`{"type":"result","result":"done"}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())

	// Test with very fast speed (should still work)
	options := &ReplayOptions{
		Speed: 100.0, // 100x speed
	}
	player, _ := NewPlayer(recording, options)

	var count int
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		count++
		return nil
	})

	if err := player.Play(context.Background()); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	if count != 3 {
		t.Errorf("Expected 3 events, got %d", count)
	}
}

func TestPlayerPlayEmptyRecording(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-EMPTY", "/test/project", tmpDir)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, _ := NewPlayer(recording, nil)

	var count int
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		count++
		return nil
	})

	if err := player.Play(context.Background()); err != nil {
		t.Fatalf("Play failed: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 events for empty recording, got %d", count)
	}
}

func TestPlayerPlayCallbackError(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-CBERR", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.RecordEvent(`{"type":"result","result":"done"}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	player, _ := NewPlayer(recording, nil)

	expectedErr := fmt.Errorf("callback error")
	player.OnEvent(func(event *StreamEvent, index, total int) error {
		return expectedErr
	})

	err := player.Play(context.Background())
	if err != expectedErr {
		t.Errorf("Expected callback error, got: %v", err)
	}
}

func TestAnalyzerWithPhaseChanges(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-PHASES", "/test/project", tmpDir)
	events := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Phase: RESEARCH - analyzing"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/a.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"WORKFLOW CHECK: decision point"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Decision: use approach A"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Approach: implement feature"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Phase: implementing feature"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/test/b.go"}}]}}`,
		`{"type":"result","result":"done"}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	analyzer, _ := NewAnalyzer(recording)
	report, _ := analyzer.Analyze()

	// Check decision points were captured
	if len(report.DecisionPoints) == 0 {
		t.Error("Expected decision points to be captured")
	}

	// Check phase analysis
	if len(report.PhaseAnalysis) == 0 {
		t.Error("Expected phase analysis")
	}
}

func TestExportToHTMLWithErrors(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-HTMLERR", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test"}}]}}`)
	_ = recorder.RecordEvent(`{"type":"result","result":"File not found","is_error":true}`)
	_ = recorder.Finish("failed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	events, _ := LoadStreamEvents(recording)

	html, err := ExportToHTML(recording, events)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if !containsString(html, "event-error") {
		t.Error("HTML should contain error event class")
	}
}

func TestExportToHTMLWithTokens(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-HTMLTOK", "/test/project", tmpDir)
	_ = recorder.RecordEvent(`{"type":"result","result":"done","usage":{"input_tokens":1000,"output_tokens":500}}`)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	events, _ := LoadStreamEvents(recording)

	html, err := ExportToHTML(recording, events)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if !containsString(html, "Tokens") {
		t.Error("HTML should contain token information")
	}
	if !containsString(html, "Cost") {
		t.Error("HTML should contain cost information")
	}
}

func TestEscapeHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ampersand",
			input:    "a & b",
			expected: "a &amp; b",
		},
		{
			name:     "less than",
			input:    "a < b",
			expected: "a &lt; b",
		},
		{
			name:     "greater than",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "quote",
			input:    `a "b" c`,
			expected: "a &quot;b&quot; c",
		},
		{
			name:     "all characters",
			input:    `<a href="test">A & B</a>`,
			expected: "&lt;a href=&quot;test&quot;&gt;A &amp; B&lt;/a&gt;",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := escapeHTML(tc.input)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}
