package replay

import (
	"context"
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
