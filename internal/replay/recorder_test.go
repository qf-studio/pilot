package replay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRecorder(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	recorder, err := NewRecorder("TASK-123", "/path/to/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	// Verify recording ID format
	if recorder.id == "" {
		t.Error("Recording ID should not be empty")
	}
	if recorder.id[:3] != "TG-" {
		t.Errorf("Recording ID should start with TG-, got: %s", recorder.id)
	}

	// Verify directory structure created
	recordingDir := filepath.Join(tmpDir, recorder.id)
	if _, err := os.Stat(recordingDir); os.IsNotExist(err) {
		t.Error("Recording directory should exist")
	}

	diffsDir := filepath.Join(recordingDir, "diffs")
	if _, err := os.Stat(diffsDir); os.IsNotExist(err) {
		t.Error("Diffs directory should exist")
	}

	// Verify stream file created
	streamPath := filepath.Join(recordingDir, "stream.jsonl")
	if _, err := os.Stat(streamPath); os.IsNotExist(err) {
		t.Error("Stream file should exist")
	}

	// Clean up
	_ = recorder.Finish("completed")
}

func TestRecordEvent(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, err := NewRecorder("TASK-456", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	// Record some events
	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/file.go"}}]}}`,
		`{"type":"user","tool_use_result":{"content":"file contents"}}`,
		`{"type":"result","result":"done","is_error":false,"usage":{"input_tokens":100,"output_tokens":50}}`,
	}

	for _, event := range events {
		if err := recorder.RecordEvent(event); err != nil {
			t.Errorf("Failed to record event: %v", err)
		}
	}

	// Finish recording
	if err := recorder.Finish("completed"); err != nil {
		t.Fatalf("Failed to finish recording: %v", err)
	}

	// Verify event count
	recording := recorder.GetRecording()
	if recording.EventCount != 4 {
		t.Errorf("Expected 4 events, got %d", recording.EventCount)
	}

	// Verify token usage was tracked
	if recording.TokenUsage.InputTokens != 100 {
		t.Errorf("Expected 100 input tokens, got %d", recording.TokenUsage.InputTokens)
	}
	if recording.TokenUsage.OutputTokens != 50 {
		t.Errorf("Expected 50 output tokens, got %d", recording.TokenUsage.OutputTokens)
	}
}

func TestSetMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, err := NewRecorder("TASK-789", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	// Set various metadata
	recorder.SetBranch("feature/test")
	recorder.SetCommitSHA("abc1234")
	recorder.SetPRUrl("https://github.com/test/pr/123")
	recorder.SetNavigator(true)
	recorder.SetModel("claude-sonnet-4-6")
	recorder.SetMetadata("custom_key", "custom_value")

	if err := recorder.Finish("completed"); err != nil {
		t.Fatalf("Failed to finish recording: %v", err)
	}

	// Verify metadata
	recording := recorder.GetRecording()
	if recording.Metadata.Branch != "feature/test" {
		t.Errorf("Branch mismatch: %s", recording.Metadata.Branch)
	}
	if recording.Metadata.CommitSHA != "abc1234" {
		t.Errorf("CommitSHA mismatch: %s", recording.Metadata.CommitSHA)
	}
	if recording.Metadata.PRUrl != "https://github.com/test/pr/123" {
		t.Errorf("PRUrl mismatch: %s", recording.Metadata.PRUrl)
	}
	if !recording.Metadata.HasNavigator {
		t.Error("HasNavigator should be true")
	}
	if recording.Metadata.ModelName != "claude-sonnet-4-6" {
		t.Errorf("ModelName mismatch: %s", recording.Metadata.ModelName)
	}
	if recording.Metadata.Tags["custom_key"] != "custom_value" {
		t.Errorf("Custom tag mismatch: %v", recording.Metadata.Tags)
	}
}

func TestListRecordings(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple recordings
	for i := 0; i < 3; i++ {
		recorder, err := NewRecorder("TASK-"+string(rune('A'+i)), "/test/project", tmpDir)
		if err != nil {
			t.Fatalf("Failed to create recorder %d: %v", i, err)
		}
		_ = recorder.Finish("completed")
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	// List recordings
	recordings, err := ListRecordings(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to list recordings: %v", err)
	}

	if len(recordings) != 3 {
		t.Errorf("Expected 3 recordings, got %d", len(recordings))
	}

	// Test with limit
	filter := &RecordingFilter{Limit: 2}
	recordings, err = ListRecordings(tmpDir, filter)
	if err != nil {
		t.Fatalf("Failed to list recordings with limit: %v", err)
	}

	if len(recordings) != 2 {
		t.Errorf("Expected 2 recordings with limit, got %d", len(recordings))
	}
}

func TestLoadRecording(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording
	recorder, err := NewRecorder("TASK-LOAD", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	recordingID := recorder.GetRecordingID()
	recorder.SetBranch("test-branch")
	_ = recorder.RecordEvent(`{"type":"system","subtype":"init"}`)
	_ = recorder.Finish("completed")

	// Load the recording
	loaded, err := LoadRecording(tmpDir, recordingID)
	if err != nil {
		t.Fatalf("Failed to load recording: %v", err)
	}

	if loaded.ID != recordingID {
		t.Errorf("ID mismatch: expected %s, got %s", recordingID, loaded.ID)
	}
	if loaded.TaskID != "TASK-LOAD" {
		t.Errorf("TaskID mismatch: expected TASK-LOAD, got %s", loaded.TaskID)
	}
	if loaded.Metadata.Branch != "test-branch" {
		t.Errorf("Branch mismatch: expected test-branch, got %s", loaded.Metadata.Branch)
	}
}

func TestLoadStreamEvents(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording with events
	recorder, err := NewRecorder("TASK-EVENTS", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	events := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`,
		`{"type":"result","result":"done"}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	// Load recording and events
	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	loadedEvents, err := LoadStreamEvents(recording)
	if err != nil {
		t.Fatalf("Failed to load events: %v", err)
	}

	if len(loadedEvents) != 3 {
		t.Errorf("Expected 3 events, got %d", len(loadedEvents))
	}

	// Verify event sequence numbers
	for i, e := range loadedEvents {
		if e.Sequence != i+1 {
			t.Errorf("Event %d has wrong sequence: %d", i, e.Sequence)
		}
	}
}

func TestDeleteRecording(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a recording
	recorder, err := NewRecorder("TASK-DELETE", "/test/project", tmpDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}

	recordingID := recorder.GetRecordingID()
	_ = recorder.Finish("completed")

	// Verify it exists
	recordingDir := filepath.Join(tmpDir, recordingID)
	if _, err := os.Stat(recordingDir); os.IsNotExist(err) {
		t.Fatal("Recording should exist before deletion")
	}

	// Delete it
	if err := DeleteRecording(tmpDir, recordingID); err != nil {
		t.Fatalf("Failed to delete recording: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(recordingDir); !os.IsNotExist(err) {
		t.Error("Recording should not exist after deletion")
	}
}

func TestDefaultRecordingsPath(t *testing.T) {
	path := DefaultRecordingsPath()
	if path == "" {
		t.Error("Default recordings path should not be empty")
	}
	if !filepath.IsAbs(path) {
		t.Error("Default recordings path should be absolute")
	}
}

func TestDetectPhase(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-PHASE", "/test/project", tmpDir)
	defer func() { _ = recorder.Finish("completed") }()

	tests := []struct {
		name     string
		parsed   *ParsedEvent
		expected string
	}{
		{
			name:     "Read tool - Exploring",
			parsed:   &ParsedEvent{ToolName: "Read"},
			expected: "Exploring",
		},
		{
			name:     "Glob tool - Exploring",
			parsed:   &ParsedEvent{ToolName: "Glob"},
			expected: "Exploring",
		},
		{
			name:     "Grep tool - Exploring",
			parsed:   &ParsedEvent{ToolName: "Grep"},
			expected: "Exploring",
		},
		{
			name:     "Write tool - Implementing",
			parsed:   &ParsedEvent{ToolName: "Write"},
			expected: "Implementing",
		},
		{
			name:     "Edit tool - Implementing",
			parsed:   &ParsedEvent{ToolName: "Edit"},
			expected: "Implementing",
		},
		{
			name:     "Bash git commit - Committing",
			parsed:   &ParsedEvent{ToolName: "Bash", ToolInput: map[string]any{"command": "git commit -m 'message'"}},
			expected: "Committing",
		},
		{
			name:     "Bash test - Testing",
			parsed:   &ParsedEvent{ToolName: "Bash", ToolInput: map[string]any{"command": "go test ./..."}},
			expected: "Testing",
		},
		{
			name:     "Bash other - empty",
			parsed:   &ParsedEvent{ToolName: "Bash", ToolInput: map[string]any{"command": "ls -la"}},
			expected: "",
		},
		{
			name:     "Text with PHASE RESEARCH",
			parsed:   &ParsedEvent{Text: "PHASE: RESEARCH starting now"},
			expected: "Research",
		},
		{
			name:     "Text with Phase Research",
			parsed:   &ParsedEvent{Text: "Phase: Research starting now"},
			expected: "Research",
		},
		{
			name:     "Text with PHASE IMPL",
			parsed:   &ParsedEvent{Text: "PHASE: IMPL starting now"},
			expected: "Implementing",
		},
		{
			name:     "Text with Phase Implement",
			parsed:   &ParsedEvent{Text: "Phase: Implement starting now"},
			expected: "Implementing",
		},
		{
			name:     "Text with PHASE VERIFY",
			parsed:   &ParsedEvent{Text: "PHASE: VERIFY starting now"},
			expected: "Verifying",
		},
		{
			name:     "Text with Phase Verify",
			parsed:   &ParsedEvent{Text: "Phase: Verify starting now"},
			expected: "Verifying",
		},
		{
			name:     "Text with PHASE COMPLETE",
			parsed:   &ParsedEvent{Text: "PHASE: COMPLETE starting now"},
			expected: "Completing",
		},
		{
			name:     "Text with Phase Complete",
			parsed:   &ParsedEvent{Text: "Phase: Complete starting now"},
			expected: "Completing",
		},
		{
			name:     "Text without phase",
			parsed:   &ParsedEvent{Text: "Just some regular text"},
			expected: "",
		},
		{
			name:     "Unknown tool",
			parsed:   &ParsedEvent{ToolName: "CustomTool"},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := recorder.detectPhase(tc.parsed)
			if result != tc.expected {
				t.Errorf("Expected phase '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestDetectFileOp(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-FILEOP", "/test/project", tmpDir)
	defer func() { _ = recorder.Finish("completed") }()

	tests := []struct {
		toolName string
		expected string
	}{
		{"Read", "read"},
		{"Write", "create"},
		{"Edit", "modify"},
		{"Bash", ""},
		{"Unknown", ""},
	}

	for _, tc := range tests {
		t.Run(tc.toolName, func(t *testing.T) {
			result := recorder.detectFileOp(tc.toolName)
			if result != tc.expected {
				t.Errorf("Expected '%s', got '%s'", tc.expected, result)
			}
		})
	}
}

func TestListRecordingsWithFilters(t *testing.T) {
	tmpDir := t.TempDir()

	// Create recordings with different properties
	recorder1, _ := NewRecorder("TASK-A", "/project/a", tmpDir)
	_ = recorder1.Finish("completed")
	time.Sleep(20 * time.Millisecond)

	recorder2, _ := NewRecorder("TASK-B", "/project/b", tmpDir)
	_ = recorder2.Finish("failed")
	time.Sleep(20 * time.Millisecond)

	recorder3, _ := NewRecorder("TASK-C", "/project/a", tmpDir)
	_ = recorder3.Finish("completed")

	// Test filter by project path
	filter := &RecordingFilter{ProjectPath: "/project/a"}
	recordings, err := ListRecordings(tmpDir, filter)
	if err != nil {
		t.Fatalf("Failed to list recordings: %v", err)
	}
	if len(recordings) != 2 {
		t.Errorf("Expected 2 recordings for /project/a, got %d", len(recordings))
	}

	// Test filter by status
	filter = &RecordingFilter{Status: "failed"}
	recordings, err = ListRecordings(tmpDir, filter)
	if err != nil {
		t.Fatalf("Failed to list recordings: %v", err)
	}
	if len(recordings) != 1 {
		t.Errorf("Expected 1 failed recording, got %d", len(recordings))
	}

	// Test filter by since time
	filter = &RecordingFilter{Since: time.Now().Add(-10 * time.Millisecond)}
	recordings, err = ListRecordings(tmpDir, filter)
	if err != nil {
		t.Fatalf("Failed to list recordings: %v", err)
	}
	// Should only get the most recent one (recorder3)
	if len(recordings) != 1 {
		t.Errorf("Expected 1 recent recording, got %d", len(recordings))
	}
}

func TestListRecordingsNonExistentDir(t *testing.T) {
	recordings, err := ListRecordings("/nonexistent/path", nil)
	if err != nil {
		t.Fatalf("Should return empty slice for nonexistent path, got error: %v", err)
	}
	if len(recordings) != 0 {
		t.Errorf("Expected empty slice, got %d recordings", len(recordings))
	}
}

func TestListRecordingsSkipsInvalid(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a valid recording
	recorder, _ := NewRecorder("TASK-VALID", "/test/project", tmpDir)
	_ = recorder.Finish("completed")

	// Create an invalid directory (not a TG- prefix)
	_ = os.MkdirAll(filepath.Join(tmpDir, "invalid-dir"), 0755)

	// Create a TG- directory without metadata.json
	_ = os.MkdirAll(filepath.Join(tmpDir, "TG-invalid"), 0755)

	// Create a TG- directory with invalid JSON
	invalidDir := filepath.Join(tmpDir, "TG-badjson")
	_ = os.MkdirAll(invalidDir, 0755)
	_ = os.WriteFile(filepath.Join(invalidDir, "metadata.json"), []byte("not json"), 0644)

	recordings, err := ListRecordings(tmpDir, nil)
	if err != nil {
		t.Fatalf("Failed to list recordings: %v", err)
	}

	// Should only get the valid recording
	if len(recordings) != 1 {
		t.Errorf("Expected 1 valid recording, got %d", len(recordings))
	}
}

func TestLoadRecordingNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadRecording(tmpDir, "nonexistent-id")
	if err == nil {
		t.Error("Expected error for nonexistent recording")
	}
}

func TestLoadRecordingInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory with invalid JSON
	invalidDir := filepath.Join(tmpDir, "TG-invalid")
	_ = os.MkdirAll(invalidDir, 0755)
	_ = os.WriteFile(filepath.Join(invalidDir, "metadata.json"), []byte("not json"), 0644)

	_, err := LoadRecording(tmpDir, "TG-invalid")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestEstimateCostOpus(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-OPUS", "/test/project", tmpDir)
	recorder.SetModel("claude-opus-4")

	// Record event with tokens
	_ = recorder.RecordEvent(`{"type":"result","result":"done","usage":{"input_tokens":1000,"output_tokens":1000}}`)
	_ = recorder.Finish("completed")

	recording := recorder.GetRecording()

	// Opus 4.0 (legacy) pricing: 15.00/1M input, 75.00/1M output
	// Expected: (1000 * 15 + 1000 * 75) / 1_000_000 = 0.09
	expectedCost := 0.09
	if recording.TokenUsage.EstimatedCostUSD < expectedCost-0.001 || recording.TokenUsage.EstimatedCostUSD > expectedCost+0.001 {
		t.Errorf("Expected cost ~%.4f, got %.4f", expectedCost, recording.TokenUsage.EstimatedCostUSD)
	}
}

func TestEstimateCostSonnet(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-SONNET", "/test/project", tmpDir)
	recorder.SetModel("claude-sonnet-4")

	// Record event with tokens
	_ = recorder.RecordEvent(`{"type":"result","result":"done","usage":{"input_tokens":1000,"output_tokens":1000}}`)
	_ = recorder.Finish("completed")

	recording := recorder.GetRecording()

	// Sonnet pricing: 3.00/1M input, 15.00/1M output
	// Expected: (1000 * 3 + 1000 * 15) / 1_000_000 = 0.018
	expectedCost := 0.018
	if recording.TokenUsage.EstimatedCostUSD < expectedCost-0.001 || recording.TokenUsage.EstimatedCostUSD > expectedCost+0.001 {
		t.Errorf("Expected cost ~%.4f, got %.4f", expectedCost, recording.TokenUsage.EstimatedCostUSD)
	}
}

func TestParseEventTypes(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-PARSE", "/test/project", tmpDir)
	defer func() { _ = recorder.Finish("completed") }()

	tests := []struct {
		name        string
		rawJSON     string
		expectType  string
		expectTool  string
		expectText  string
		expectError bool
	}{
		{
			name:       "system event",
			rawJSON:    `{"type":"system","subtype":"init"}`,
			expectType: "system",
		},
		{
			name:       "result event",
			rawJSON:    `{"type":"result","result":"done","is_error":false}`,
			expectType: "result",
		},
		{
			name:        "result error event",
			rawJSON:     `{"type":"result","result":"error message","is_error":true}`,
			expectType:  "result",
			expectError: true,
		},
		{
			name:       "assistant tool use",
			rawJSON:    `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test.go"}}]}}`,
			expectType: "assistant",
			expectTool: "Read",
		},
		{
			name:       "assistant text",
			rawJSON:    `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`,
			expectType: "assistant",
			expectText: "Hello world",
		},
		{
			name:       "invalid json",
			rawJSON:    `not valid json`,
			expectType: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := recorder.parseEvent(tc.rawJSON)

			if tc.expectType == "" {
				if parsed != nil {
					t.Errorf("Expected nil for invalid JSON")
				}
				return
			}

			if parsed == nil {
				t.Fatalf("Expected parsed event, got nil")
			}

			if parsed.Type != tc.expectType {
				t.Errorf("Expected type '%s', got '%s'", tc.expectType, parsed.Type)
			}

			if tc.expectTool != "" && parsed.ToolName != tc.expectTool {
				t.Errorf("Expected tool '%s', got '%s'", tc.expectTool, parsed.ToolName)
			}

			if tc.expectText != "" && parsed.Text != tc.expectText {
				t.Errorf("Expected text '%s', got '%s'", tc.expectText, parsed.Text)
			}

			if parsed.IsError != tc.expectError {
				t.Errorf("Expected IsError=%v, got %v", tc.expectError, parsed.IsError)
			}
		})
	}
}

func TestRecordEventWithPhaseChanges(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-PHASECHANGE", "/test/project", tmpDir)

	// Record events that trigger phase changes
	events := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/new.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git commit -m 'test'"}}]}}`,
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	recording := recorder.GetRecording()

	// Should have phase timings
	if len(recording.PhaseTimings) == 0 {
		t.Error("Expected phase timings to be recorded")
	}
}

func TestRecordEventWithFileTracking(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-FILES", "/test/project", tmpDir)

	events := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test/a.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/test/b.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/test/c.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/test/c.go"}}]}}`, // Same file again
	}

	for _, e := range events {
		_ = recorder.RecordEvent(e)
	}
	_ = recorder.Finish("completed")

	// Check that diffs were tracked (3 unique files)
	if len(recorder.diffFiles) != 3 {
		t.Errorf("Expected 3 unique files tracked, got %d", len(recorder.diffFiles))
	}
}

func TestLoadStreamEventsEmpty(t *testing.T) {
	tmpDir := t.TempDir()

	recorder, _ := NewRecorder("TASK-EMPTY", "/test/project", tmpDir)
	_ = recorder.Finish("completed")

	recording, _ := LoadRecording(tmpDir, recorder.GetRecordingID())
	events, err := LoadStreamEvents(recording)
	if err != nil {
		t.Fatalf("Failed to load events: %v", err)
	}

	if len(events) != 0 {
		t.Errorf("Expected 0 events, got %d", len(events))
	}
}

func TestRecorderConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	recorder, _ := NewRecorder("TASK-CONCURRENT", "/test/project", tmpDir)

	// Concurrent metadata updates
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			recorder.SetMetadata("key", "value")
			recorder.SetBranch("branch")
			recorder.SetCommitSHA("sha")
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	_ = recorder.Finish("completed")
}
