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
	recorder.SetModel("claude-sonnet-4-5")
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
	if recording.Metadata.ModelName != "claude-sonnet-4-5" {
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
