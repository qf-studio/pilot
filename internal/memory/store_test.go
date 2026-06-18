package memory

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	// Use temp directory for test
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Verify database file was created
	dbPath := filepath.Join(tmpDir, "pilot.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file not created")
	}
}

func TestExecutionCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create
	exec := &Execution{
		ID:          "exec-1",
		TaskID:      "TASK-123",
		ProjectPath: "/path/to/project",
		Status:      "completed",
		Output:      "Success!",
		DurationMs:  5000,
		PRUrl:       "https://github.com/org/repo/pull/1",
		CommitSHA:   "abc123",
	}

	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Read
	retrieved, err := store.GetExecution("exec-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}

	if retrieved.TaskID != "TASK-123" {
		t.Errorf("Expected TaskID 'TASK-123', got '%s'", retrieved.TaskID)
	}
	if retrieved.Status != "completed" {
		t.Errorf("Expected Status 'completed', got '%s'", retrieved.Status)
	}
	if retrieved.PRUrl != "https://github.com/org/repo/pull/1" {
		t.Errorf("Expected PR URL, got '%s'", retrieved.PRUrl)
	}
}

func TestGetRecentExecutions(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add multiple executions
	for i := 1; i <= 5; i++ {
		exec := &Execution{
			ID:          "exec-" + string(rune('0'+i)),
			TaskID:      "TASK-" + string(rune('0'+i)),
			ProjectPath: "/path",
			Status:      "completed",
		}
		_ = store.SaveExecution(exec)
	}

	recent, err := store.GetRecentExecutions(3, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions failed: %v", err)
	}

	if len(recent) != 3 {
		t.Errorf("Expected 3 executions, got %d", len(recent))
	}
}

func TestPatternCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	pattern := &Pattern{
		ProjectPath: "/path/to/project",
		Type:        "code",
		Content:     "Always use error wrapping",
		Confidence:  0.9,
	}

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern failed: %v", err)
	}

	if pattern.ID == 0 {
		t.Error("Pattern ID not set after save")
	}

	patterns, err := store.GetPatterns("/path/to/project")
	if err != nil {
		t.Fatalf("GetPatterns failed: %v", err)
	}

	if len(patterns) != 1 {
		t.Errorf("Expected 1 pattern, got %d", len(patterns))
	}
}

func TestProjectCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	project := &Project{
		Path:             "/path/to/project",
		Name:             "my-project",
		NavigatorEnabled: true,
		LastActive:       time.Now(),
		Settings:         map[string]interface{}{"theme": "dark"},
	}

	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject failed: %v", err)
	}

	retrieved, err := store.GetProject("/path/to/project")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}

	if retrieved.Name != "my-project" {
		t.Errorf("Expected name 'my-project', got '%s'", retrieved.Name)
	}
	if !retrieved.NavigatorEnabled {
		t.Error("Expected NavigatorEnabled to be true")
	}
}

func TestGetAllProjects(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveProject(&Project{Path: "/path/1", Name: "project-1"})
	_ = store.SaveProject(&Project{Path: "/path/2", Name: "project-2"})

	projects, err := store.GetAllProjects()
	if err != nil {
		t.Fatalf("GetAllProjects failed: %v", err)
	}

	if len(projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(projects))
	}
}

func TestExecution_FullLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer func() { _ = store.Close() }()

	completedAt := time.Now()
	exec := &Execution{
		ID:               "exec-full-1",
		TaskID:           "TASK-456",
		ProjectPath:      "/path/to/project",
		Status:           "completed",
		Output:           "Build succeeded. All tests passed.",
		Error:            "",
		DurationMs:       15000,
		PRUrl:            "https://github.com/org/repo/pull/42",
		CommitSHA:        "abc123def456",
		CompletedAt:      &completedAt,
		TokensInput:      10000,
		TokensOutput:     5000,
		TokensTotal:      15000,
		EstimatedCostUSD: 0.15,
		FilesChanged:     5,
		LinesAdded:       100,
		LinesRemoved:     20,
		ModelName:        "claude-sonnet-4-6",
	}

	// Save
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Retrieve
	retrieved, err := store.GetExecution("exec-full-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}

	// Verify all fields
	tests := []struct {
		name     string
		got      interface{}
		expected interface{}
	}{
		{"ID", retrieved.ID, exec.ID},
		{"TaskID", retrieved.TaskID, exec.TaskID},
		{"ProjectPath", retrieved.ProjectPath, exec.ProjectPath},
		{"Status", retrieved.Status, exec.Status},
		{"Output", retrieved.Output, exec.Output},
		{"DurationMs", retrieved.DurationMs, exec.DurationMs},
		{"PRUrl", retrieved.PRUrl, exec.PRUrl},
		{"CommitSHA", retrieved.CommitSHA, exec.CommitSHA},
		{"TokensInput", retrieved.TokensInput, exec.TokensInput},
		{"TokensOutput", retrieved.TokensOutput, exec.TokensOutput},
		{"TokensTotal", retrieved.TokensTotal, exec.TokensTotal},
		{"FilesChanged", retrieved.FilesChanged, exec.FilesChanged},
		{"LinesAdded", retrieved.LinesAdded, exec.LinesAdded},
		{"LinesRemoved", retrieved.LinesRemoved, exec.LinesRemoved},
		{"ModelName", retrieved.ModelName, exec.ModelName},
	}

	for _, tt := range tests {
		if tt.got != tt.expected {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
		}
	}

	if retrieved.CompletedAt == nil {
		t.Error("CompletedAt should not be nil")
	}
}

func TestGetExecution_NotFound(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_, err := store.GetExecution("nonexistent")
	if err == nil {
		t.Error("GetExecution should return error for nonexistent execution")
	}
}

func TestHasCompletedExecution(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// No executions yet — should return false
	completed, err := store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for non-existent task")
	}

	// Save a non-completed execution
	_ = store.SaveExecution(&Execution{
		ID:          "exec-pending",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "running",
	})
	completed, err = store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for running task")
	}

	// Save a completed execution with a deliverable (commit_sha set).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-done",
		TaskID:      "GH-42",
		ProjectPath: "/project",
		Status:      "completed",
		CommitSHA:   "abc123",
	})
	completed, err = store.HasCompletedExecution("GH-42", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if !completed {
		t.Error("expected true for completed task with deliverable")
	}

	// Completed but no deliverables (epic-parent false-positive pattern, TASK-296).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-epic",
		TaskID:      "GH-43",
		ProjectPath: "/project",
		Status:      "completed",
	})
	completed, err = store.HasCompletedExecution("GH-43", "/project")
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false for completed task with no deliverable (epic-parent false-positive)")
	}

	// Different project path — should return false
	completed, _ = store.HasCompletedExecution("GH-42", "/other-project")
	if completed {
		t.Error("expected false for different project path")
	}
}

// TestHasCompletedExecution_OrphanRecovery verifies that a completed execution
// with a non-empty error field (e.g., from orphan recovery) does NOT count as
// completed. This prevents orphan-recovered executions from blocking re-dispatch.
// GH-2315: Defense-in-depth against orphan recovery blocking re-dispatch.
func TestHasCompletedExecution_OrphanRecovery(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	taskID := "GH-2305"
	projectPath := "/project"

	// Simulate 5 failed executions (original scenario from GH-2314)
	for i := 0; i < 5; i++ {
		execID := fmt.Sprintf("exec-failed-%d", i)
		_ = store.SaveExecution(&Execution{
			ID:          execID,
			TaskID:      taskID,
			ProjectPath: projectPath,
			Status:      "failed",
		})
	}

	// Simulate orphan recovery: marks stale running task as "completed" with error
	_ = store.SaveExecution(&Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
	})
	// Orphan recovery calls UpdateExecutionStatus with error message
	_ = store.UpdateExecutionStatus("exec-orphan", "completed", "stale running task recovered (orphaned worker)")

	// The orphan-recovered "completed" execution should NOT count as completed
	completed, err := store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if completed {
		t.Error("expected false — orphan-recovered execution with error should not block re-dispatch")
	}

	// Now add a genuine completed execution (no error, has deliverable).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-genuine",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "completed",
		CommitSHA:   "deadbeef",
	})
	completed, err = store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution failed: %v", err)
	}
	if !completed {
		t.Error("expected true — genuine completed execution with deliverable should be found")
	}
}

func TestPattern_Update(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create pattern
	pattern := &Pattern{
		ProjectPath: "/path/to/project",
		Type:        "code",
		Content:     "Original content",
		Confidence:  0.7,
	}

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern (create) failed: %v", err)
	}

	originalID := pattern.ID
	if originalID == 0 {
		t.Fatal("Pattern ID should be set after create")
	}

	// Update pattern
	pattern.Content = "Updated content"
	pattern.Confidence = 0.9

	if err := store.SavePattern(pattern); err != nil {
		t.Fatalf("SavePattern (update) failed: %v", err)
	}

	// Verify update
	patterns, err := store.GetPatterns("/path/to/project")
	if err != nil {
		t.Fatalf("GetPatterns failed: %v", err)
	}

	if len(patterns) != 1 {
		t.Fatalf("Expected 1 pattern, got %d", len(patterns))
	}

	if patterns[0].Content != "Updated content" {
		t.Errorf("Content = %q, want 'Updated content'", patterns[0].Content)
	}
	if patterns[0].Confidence != 0.9 {
		t.Errorf("Confidence = %f, want 0.9", patterns[0].Confidence)
	}
}

func TestGetActiveExecutions(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with different statuses
	executions := []*Execution{
		{ID: "1", TaskID: "T1", ProjectPath: "/p", Status: "running"},
		{ID: "2", TaskID: "T2", ProjectPath: "/p", Status: "completed"},
		{ID: "3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
		{ID: "4", TaskID: "T4", ProjectPath: "/p", Status: "failed"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	active, err := store.GetActiveExecutions()
	if err != nil {
		t.Fatalf("GetActiveExecutions failed: %v", err)
	}

	if len(active) != 2 {
		t.Errorf("Expected 2 active executions, got %d", len(active))
	}

	for _, e := range active {
		if e.Status != "running" {
			t.Errorf("Active execution has status %q, want 'running'", e.Status)
		}
	}
}

func TestGetProject_InvalidSettingsJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert project with invalid JSON settings directly into DB
	_, err := store.db.Exec(`
		INSERT INTO projects (path, name, navigator_enabled, settings)
		VALUES (?, ?, ?, ?)
	`, "/test/project", "test", true, "invalid-json{{{")
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Capture slog output
	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	// Should not return error, but should log warning
	project, err := store.GetProject("/test/project")
	if err != nil {
		t.Errorf("GetProject should not error on invalid settings JSON: %v", err)
	}
	if project == nil {
		t.Fatal("project should not be nil")
	}
	if project.Settings != nil {
		t.Errorf("Settings should be nil after unmarshal failure, got %v", project.Settings)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal project settings") {
		t.Errorf("expected warning log about unmarshal failure, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "/test/project") {
		t.Errorf("expected project path in log, got: %s", logOutput)
	}
}

func TestGetAllProjects_InvalidSettingsJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert valid and invalid projects
	_, _ = store.db.Exec(`INSERT INTO projects (path, name, navigator_enabled, settings) VALUES (?, ?, ?, ?)`,
		"/valid/project", "valid", true, `{"theme":"dark"}`)
	_, _ = store.db.Exec(`INSERT INTO projects (path, name, navigator_enabled, settings) VALUES (?, ?, ?, ?)`,
		"/invalid/project", "invalid", true, "not-valid-json")

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	projects, err := store.GetAllProjects()
	if err != nil {
		t.Errorf("GetAllProjects should not error: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal project settings") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
}

func TestGetCrossPattern_InvalidExamplesJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert pattern with invalid examples JSON
	_, err := store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-1", "testing", "Test Pattern", "desc", "ctx", "invalid[json", 0.9, 5, false, "global")
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	pattern, err := store.GetCrossPattern("pat-1")
	if err != nil {
		t.Errorf("GetCrossPattern should not error: %v", err)
	}
	if pattern == nil {
		t.Fatal("pattern should not be nil")
	}
	if pattern.Examples != nil {
		t.Errorf("Examples should be nil after unmarshal failure")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal cross pattern examples") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "pat-1") {
		t.Errorf("expected pattern ID in log, got: %s", logOutput)
	}
}

func TestScanCrossPatterns_InvalidExamplesJSON(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Insert patterns with valid and invalid examples
	_, _ = store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-valid", "testing", "Valid", "desc", "ctx", `["example1","example2"]`, 0.9, 3, false, "global")
	_, _ = store.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "pat-invalid", "testing", "Invalid", "desc", "ctx", "{broken", 0.8, 2, false, "global")

	var buf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(oldLogger)

	patterns, err := store.GetCrossPatternsByType("testing")
	if err != nil {
		t.Errorf("GetCrossPatternsByType should not error: %v", err)
	}
	if len(patterns) != 2 {
		t.Errorf("expected 2 patterns, got %d", len(patterns))
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to unmarshal cross pattern examples") {
		t.Errorf("expected warning log, got: %s", logOutput)
	}
}

func TestGetQueuedTasks(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with different statuses
	executions := []*Execution{
		{ID: "1", TaskID: "T1", ProjectPath: "/p", Status: "queued"},
		{ID: "2", TaskID: "T2", ProjectPath: "/p", Status: "pending"},
		{ID: "3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
		{ID: "4", TaskID: "T4", ProjectPath: "/p", Status: "queued"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	queued, err := store.GetQueuedTasks(10)
	if err != nil {
		t.Fatalf("GetQueuedTasks failed: %v", err)
	}

	if len(queued) != 3 {
		t.Errorf("Expected 3 queued/pending tasks, got %d", len(queued))
	}
}

// TestTaskLabelsRoundTrip verifies that Task.Labels survive the queue round-trip
// (SaveExecution → GetExecution and GetQueuedTasksForProject). Without this, labels
// like "no-decompose" are silently dropped and runner-side gates bypassed (GH-2326).
func TestTaskLabelsRoundTrip(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	cases := []struct {
		name   string
		labels []string
	}{
		{"nil labels", nil},
		{"empty slice", []string{}},
		{"single label", []string{"no-decompose"}},
		{"multiple labels", []string{"pilot", "no-decompose", "priority:high"}},
		{"special chars", []string{"kind/bug", "area/executor", "v1.0+"}},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execID := fmt.Sprintf("exec-labels-%d", i)
			input := &Execution{
				ID:          execID,
				TaskID:      fmt.Sprintf("T-%d", i),
				ProjectPath: "/project/a",
				Status:      "queued",
				TaskTitle:   "test",
				TaskLabels:  tc.labels,
			}
			if err := store.SaveExecution(input); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			got, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			// nil and empty slice both normalize to nil on read
			wantLen := len(tc.labels)
			if len(got.TaskLabels) != wantLen {
				t.Fatalf("labels length: got %d (%v), want %d (%v)", len(got.TaskLabels), got.TaskLabels, wantLen, tc.labels)
			}
			for j, l := range tc.labels {
				if got.TaskLabels[j] != l {
					t.Errorf("labels[%d]: got %q, want %q", j, got.TaskLabels[j], l)
				}
			}

			// Also verify the worker-facing read path returns labels.
			queued, err := store.GetQueuedTasksForProject("/project/a", 100)
			if err != nil {
				t.Fatalf("GetQueuedTasksForProject: %v", err)
			}
			var found *Execution
			for _, e := range queued {
				if e.ID == execID {
					found = e
					break
				}
			}
			if found == nil {
				t.Fatalf("execution %s not in queued list", execID)
			}
			if len(found.TaskLabels) != wantLen {
				t.Errorf("queued read labels length: got %d (%v), want %d", len(found.TaskLabels), found.TaskLabels, wantLen)
			}
		})
	}
}

func TestGetExecutionsInPeriod(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add some executions
	for i := 0; i < 5; i++ {
		exec := &Execution{
			ID:          "exec-period-" + string(rune('a'+i)),
			TaskID:      "TASK-" + string(rune('1'+i)),
			ProjectPath: "/project/a",
			Status:      "completed",
		}
		_ = store.SaveExecution(exec)
	}

	// Add execution for different project
	_ = store.SaveExecution(&Execution{
		ID:          "exec-other",
		TaskID:      "TASK-99",
		ProjectPath: "/project/b",
		Status:      "completed",
	})

	// Verify the executions were created
	allExecs, _ := store.GetRecentExecutions(100, "")
	t.Logf("Total executions in DB: %d", len(allExecs))

	tests := []struct {
		name    string
		query   BriefQuery
		wantMin int
	}{
		{
			name: "all projects",
			query: BriefQuery{
				Start: time.Now().Add(-24 * time.Hour),
				End:   time.Now().Add(24 * time.Hour),
			},
			wantMin: 6,
		},
		{
			name: "specific project",
			query: BriefQuery{
				Start:    time.Now().Add(-24 * time.Hour),
				End:      time.Now().Add(24 * time.Hour),
				Projects: []string{"/project/a"},
			},
			wantMin: 5,
		},
		{
			name: "multiple projects",
			query: BriefQuery{
				Start:    time.Now().Add(-24 * time.Hour),
				End:      time.Now().Add(24 * time.Hour),
				Projects: []string{"/project/a", "/project/b"},
			},
			wantMin: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetExecutionsInPeriod(tt.query)
			if err != nil {
				t.Fatalf("GetExecutionsInPeriod failed: %v", err)
			}

			if len(results) < tt.wantMin {
				t.Errorf("got %d executions, want at least %d", len(results), tt.wantMin)
			}
		})
	}
}

func TestGetBriefMetrics(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Add executions with various statuses
	executions := []*Execution{
		{ID: "metrics-1", TaskID: "T1", ProjectPath: "/p", Status: "completed", DurationMs: 1000, PRUrl: "https://github.com/a/b/pull/1"},
		{ID: "metrics-2", TaskID: "T2", ProjectPath: "/p", Status: "completed", DurationMs: 2000, PRUrl: ""},
		{ID: "metrics-3", TaskID: "T3", ProjectPath: "/p", Status: "failed", DurationMs: 500},
		{ID: "metrics-4", TaskID: "T4", ProjectPath: "/p", Status: "completed", DurationMs: 3000, PRUrl: "https://github.com/a/b/pull/2"},
	}

	for _, e := range executions {
		_ = store.SaveExecution(e)
	}

	query := BriefQuery{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(24 * time.Hour),
	}

	metrics, err := store.GetBriefMetrics(query)
	if err != nil {
		t.Fatalf("GetBriefMetrics failed: %v", err)
	}

	if metrics.TotalTasks < 4 {
		t.Errorf("TotalTasks = %d, want at least 4", metrics.TotalTasks)
	}
	if metrics.CompletedCount < 3 {
		t.Errorf("CompletedCount = %d, want at least 3", metrics.CompletedCount)
	}
	if metrics.FailedCount < 1 {
		t.Errorf("FailedCount = %d, want at least 1", metrics.FailedCount)
	}
	if metrics.PRsCreated < 2 {
		t.Errorf("PRsCreated = %d, want at least 2", metrics.PRsCreated)
	}
}

func TestProjectSettings(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create project with complex settings
	settings := map[string]interface{}{
		"theme":        "dark",
		"autoCommit":   true,
		"maxTokens":    100000,
		"excludePaths": []interface{}{"/vendor", "/node_modules"},
	}

	project := &Project{
		Path:             "/path/to/project",
		Name:             "test-project",
		NavigatorEnabled: true,
		LastActive:       time.Now(),
		Settings:         settings,
	}

	if err := store.SaveProject(project); err != nil {
		t.Fatalf("SaveProject failed: %v", err)
	}

	retrieved, err := store.GetProject("/path/to/project")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}

	if retrieved.Settings["theme"] != "dark" {
		t.Errorf("Settings[theme] = %v, want 'dark'", retrieved.Settings["theme"])
	}
	if retrieved.Settings["autoCommit"] != true {
		t.Errorf("Settings[autoCommit] = %v, want true", retrieved.Settings["autoCommit"])
	}
}

func TestGetProject_NotFound(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_, err := store.GetProject("/nonexistent/path")
	if err == nil {
		t.Error("GetProject should return error for nonexistent project")
	}
}

func TestGetTopCrossPatterns(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with varying confidence
	patterns := []*CrossPattern{
		{ID: "high", Type: "code", Title: "High Confidence", Confidence: 0.95, Occurrences: 10, Scope: "org"},
		{ID: "medium", Type: "code", Title: "Medium Confidence", Confidence: 0.7, Occurrences: 5, Scope: "org"},
		{ID: "low", Type: "code", Title: "Low Confidence", Confidence: 0.4, Occurrences: 2, Scope: "org"},
	}

	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	tests := []struct {
		name          string
		limit         int
		minConfidence float64
		wantCount     int
	}{
		{name: "all patterns", limit: 10, minConfidence: 0, wantCount: 3},
		{name: "high confidence only", limit: 10, minConfidence: 0.9, wantCount: 1},
		{name: "medium and above", limit: 10, minConfidence: 0.6, wantCount: 2},
		{name: "limited results", limit: 2, minConfidence: 0, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetTopCrossPatterns(tt.limit, tt.minConfidence)
			if err != nil {
				t.Fatalf("GetTopCrossPatterns failed: %v", err)
			}

			if len(results) != tt.wantCount {
				t.Errorf("got %d patterns, want %d", len(results), tt.wantCount)
			}
		})
	}
}

func TestGetCrossPatternsForProject(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with different scopes
	_ = store.SaveCrossPattern(&CrossPattern{ID: "org-1", Type: "code", Title: "Org Pattern", Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "global-1", Type: "code", Title: "Global Pattern", Scope: "global"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "project-1", Type: "code", Title: "Project Pattern", Scope: "project"})

	// Link project pattern
	_ = store.LinkPatternToProject("project-1", "/project/a")

	tests := []struct {
		name          string
		projectPath   string
		includeGlobal bool
		wantMin       int
	}{
		{name: "with global", projectPath: "/project/a", includeGlobal: true, wantMin: 2},
		{name: "without global", projectPath: "/project/a", includeGlobal: false, wantMin: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := store.GetCrossPatternsForProject(tt.projectPath, tt.includeGlobal)
			if err != nil {
				t.Fatalf("GetCrossPatternsForProject failed: %v", err)
			}

			if len(results) < tt.wantMin {
				t.Errorf("got %d patterns, want at least %d", len(results), tt.wantMin)
			}
		})
	}
}

func TestGetLifetimeTokens(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Empty table should return zeros
	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens (empty): %v", err)
	}
	if lt.TotalTokens != 0 || lt.TotalCostUSD != 0 {
		t.Errorf("empty: want zeros, got tokens=%d cost=%.4f", lt.TotalTokens, lt.TotalCostUSD)
	}

	// Insert executions with token data
	execs := []struct {
		id     string
		input  int64
		output int64
		cost   float64
	}{
		{"exec-lt-1", 1000, 500, 0.05},
		{"exec-lt-2", 2000, 1000, 0.10},
		{"exec-lt-3", 3000, 1500, 0.15},
	}
	for _, e := range execs {
		if err := store.SaveExecution(&Execution{
			ID:          e.id,
			TaskID:      "TASK-" + e.id,
			ProjectPath: "/test",
			Status:      "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID:      e.id,
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      e.input + e.output,
			EstimatedCostUSD: e.cost,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", e.id, err)
		}
	}

	lt, err = store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}

	wantInput := int64(6000)
	wantOutput := int64(3000)
	wantTotal := int64(9000)
	wantCost := 0.30

	if lt.InputTokens != wantInput {
		t.Errorf("InputTokens = %d, want %d", lt.InputTokens, wantInput)
	}
	if lt.OutputTokens != wantOutput {
		t.Errorf("OutputTokens = %d, want %d", lt.OutputTokens, wantOutput)
	}
	if lt.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", lt.TotalTokens, wantTotal)
	}
	if lt.TotalCostUSD != wantCost {
		t.Errorf("TotalCostUSD = %.4f, want %.4f", lt.TotalCostUSD, wantCost)
	}
}

func TestGetLifetimeTokens_CacheFields(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert two executions with cache token data via SaveExecution.
	execs := []struct {
		id         string
		input      int64
		output     int64
		cacheRead  int64
		cacheWrite int64
	}{
		{"lt-cache-1", 1000, 500, 80000, 3000},
		{"lt-cache-2", 2000, 1000, 40000, 2000},
	}
	for _, e := range execs {
		total := e.input + e.output
		if err := store.SaveExecution(&Execution{
			ID:               e.id,
			TaskID:           "TASK-" + e.id,
			ProjectPath:      "/p",
			Status:           "completed",
			TokensInput:      e.input,
			TokensOutput:     e.output,
			TokensTotal:      total,
			TokensCacheRead:  e.cacheRead,
			TokensCacheWrite: e.cacheWrite,
			EstimatedCostUSD: 0.01,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.id, err)
		}
	}

	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}

	if lt.CacheReadTokens != 120000 {
		t.Errorf("CacheReadTokens = %d, want 120000", lt.CacheReadTokens)
	}
	if lt.CacheWriteTokens != 5000 {
		t.Errorf("CacheWriteTokens = %d, want 5000", lt.CacheWriteTokens)
	}
	// Regular token fields unaffected
	if lt.TotalTokens != 4500 {
		t.Errorf("TotalTokens = %d, want 4500", lt.TotalTokens)
	}
}

func TestGetLifetimeTaskCounts(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Empty table should return zeros
	tc, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts (empty): %v", err)
	}
	if tc.Total != 0 || tc.Succeeded != 0 || tc.Failed != 0 {
		t.Errorf("empty: want zeros, got total=%d succeeded=%d failed=%d", tc.Total, tc.Succeeded, tc.Failed)
	}

	// Insert mix of completed and failed executions
	statuses := []struct {
		id     string
		status string
	}{
		{"exec-tc-1", "completed"},
		{"exec-tc-2", "completed"},
		{"exec-tc-3", "failed"},
		{"exec-tc-4", "completed"},
		{"exec-tc-5", "failed"},
	}
	for _, s := range statuses {
		if err := store.SaveExecution(&Execution{
			ID:          s.id,
			TaskID:      "TASK-" + s.id,
			ProjectPath: "/test",
			Status:      s.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", s.id, err)
		}
	}

	tc, err = store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts: %v", err)
	}

	if tc.Total != 5 {
		t.Errorf("Total = %d, want 5", tc.Total)
	}
	if tc.Succeeded != 3 {
		t.Errorf("Succeeded = %d, want 3", tc.Succeeded)
	}
	if tc.Failed != 2 {
		t.Errorf("Failed = %d, want 2", tc.Failed)
	}
}

func TestBriefHistory(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*Store)
		channel       string
		wantNil       bool
		wantBriefType string
		wantRecipient string
	}{
		{
			name:    "empty table returns nil",
			setup:   func(s *Store) {},
			channel: "telegram",
			wantNil: true,
		},
		{
			name: "single insert returns that record",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
					Recipient: "user123",
				})
			},
			channel:       "telegram",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "user123",
		},
		{
			name: "multiple inserts returns most recent",
			setup: func(s *Store) {
				// Insert older record first
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now().Add(-2 * time.Hour),
					Channel:   "slack",
					BriefType: "daily",
					Recipient: "old-user",
				})
				// Insert newer record
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now().Add(-1 * time.Hour),
					Channel:   "slack",
					BriefType: "weekly",
					Recipient: "new-user",
				})
				// Insert most recent
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "slack",
					BriefType: "daily",
					Recipient: "latest-user",
				})
			},
			channel:       "slack",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "latest-user",
		},
		{
			name: "filters by channel",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
					Recipient: "tg-user",
				})
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "slack",
					BriefType: "weekly",
					Recipient: "slack-user",
				})
			},
			channel:       "telegram",
			wantNil:       false,
			wantBriefType: "daily",
			wantRecipient: "tg-user",
		},
		{
			name: "non-existent channel returns nil",
			setup: func(s *Store) {
				_ = s.RecordBriefSent(&BriefRecord{
					SentAt:    time.Now(),
					Channel:   "telegram",
					BriefType: "daily",
				})
			},
			channel: "email",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			store, err := NewStore(tmpDir)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			tt.setup(store)

			record, err := store.GetLastBriefSent(tt.channel)
			if err != nil {
				t.Fatalf("GetLastBriefSent: %v", err)
			}

			if tt.wantNil {
				if record != nil {
					t.Errorf("expected nil, got %+v", record)
				}
				return
			}

			if record == nil {
				t.Fatal("expected non-nil record, got nil")
			}

			if record.Channel != tt.channel {
				t.Errorf("Channel = %q, want %q", record.Channel, tt.channel)
			}
			if record.BriefType != tt.wantBriefType {
				t.Errorf("BriefType = %q, want %q", record.BriefType, tt.wantBriefType)
			}
			if record.Recipient != tt.wantRecipient {
				t.Errorf("Recipient = %q, want %q", record.Recipient, tt.wantRecipient)
			}
			if record.ID == 0 {
				t.Error("ID should be set after insert")
			}
		})
	}
}

func TestRecordBriefSent_SetsID(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	record := &BriefRecord{
		SentAt:    time.Now(),
		Channel:   "telegram",
		BriefType: "daily",
	}

	if record.ID != 0 {
		t.Error("ID should be 0 before insert")
	}

	if err := store.RecordBriefSent(record); err != nil {
		t.Fatalf("RecordBriefSent: %v", err)
	}

	if record.ID == 0 {
		t.Error("ID should be set after insert")
	}
}

func TestStore_WithRetry(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("succeeds on first attempt", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed: %v", err)
		}
		if attempts != 1 {
			t.Errorf("should only attempt once, got %d", attempts)
		}
	})

	t.Run("retries on database locked error", func(t *testing.T) {
		var buf bytes.Buffer
		oldLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		defer slog.SetDefault(oldLogger)

		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			if attempts < 3 {
				return fmt.Errorf("database is locked (SQLITE_BUSY)")
			}
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed after retries: %v", err)
		}
		if attempts != 3 {
			t.Errorf("should retry until success, got %d attempts", attempts)
		}

		logOutput := buf.String()
		if !strings.Contains(logOutput, "Database locked, retrying") {
			t.Errorf("expected retry warning in logs, got: %s", logOutput)
		}
	})

	t.Run("does not retry non-retryable errors", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			return fmt.Errorf("syntax error: invalid SQL")
		})
		if err == nil {
			t.Error("withRetry should return error")
		}
		if attempts != 1 {
			t.Errorf("should not retry non-retryable error, got %d attempts", attempts)
		}
		if !strings.Contains(err.Error(), "syntax error") {
			t.Errorf("should return original error, got: %v", err)
		}
	})

	t.Run("fails after max retries", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("TestOp", func() error {
			attempts++
			return fmt.Errorf("database is locked (SQLITE_BUSY)")
		})
		if err == nil {
			t.Error("withRetry should return error after max retries")
		}
		if attempts != 5 {
			t.Errorf("should attempt 5 times, got %d", attempts)
		}
		if !strings.Contains(err.Error(), "TestOp failed after 5 retries") {
			t.Errorf("error should mention operation and retry count, got: %v", err)
		}
	})

	t.Run("retries on sqlite_locked", func(t *testing.T) {
		attempts := 0
		err := store.withRetry("test", func() error {
			attempts++
			if attempts < 2 {
				return fmt.Errorf("table is locked (SQLITE_LOCKED)")
			}
			return nil
		})
		if err != nil {
			t.Errorf("withRetry should succeed: %v", err)
		}
		if attempts != 2 {
			t.Errorf("should retry, got %d attempts", attempts)
		}
	})
}

func TestStore_ConnectionPoolSettings(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Verify connection pool settings by checking stats
	stats := store.db.Stats()

	// MaxOpenConns should be 1
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
}

func TestStore_SetApprovalRequestID_HappyPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	exec := &Execution{
		ID:          "exec-approval-1",
		TaskID:      "GH-999",
		ProjectPath: "/proj",
		Status:      "completed",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	if err := store.SetApprovalRequestID(ctx, "GH-999", "req-abc"); err != nil {
		t.Fatalf("SetApprovalRequestID: %v", err)
	}

	// Verify via SetApprovalDecision — it matches on approval_request_id.
	if err := store.SetApprovalDecision(ctx, "req-abc", "approved", "tester"); err != nil {
		t.Fatalf("SetApprovalDecision after SetApprovalRequestID: %v", err)
	}

	got, err := store.GetExecution("exec-approval-1")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.ApprovalRequestID != "req-abc" {
		t.Errorf("ApprovalRequestID = %q, want %q", got.ApprovalRequestID, "req-abc")
	}
	if got.ApprovalDecision != "approved" {
		t.Errorf("ApprovalDecision = %q, want %q", got.ApprovalDecision, "approved")
	}
	if got.ApprovalDecisionBy != "tester" {
		t.Errorf("ApprovalDecisionBy = %q, want %q", got.ApprovalDecisionBy, "tester")
	}
}

func TestStore_SetApprovalRequestID_ZeroRowCase(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// No execution row exists for this task.
	err = store.SetApprovalRequestID(ctx, "GH-000", "req-xyz")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for missing task, got %v", err)
	}
}

func TestLogEntryCRUD(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Save entries
	entries := []*LogEntry{
		{ExecutionID: "exec-1", Timestamp: time.Now().Add(-2 * time.Second), Level: "info", Message: "Task started", Component: "executor"},
		{ExecutionID: "exec-1", Timestamp: time.Now().Add(-1 * time.Second), Level: "warn", Message: "Slow build", Component: "executor"},
		{ExecutionID: "exec-1", Timestamp: time.Now(), Level: "error", Message: "Build failed", Component: "executor"},
	}

	for _, e := range entries {
		if err := store.SaveLogEntry(e); err != nil {
			t.Fatalf("SaveLogEntry failed: %v", err)
		}
		if e.ID == 0 {
			t.Error("Expected non-zero ID after save")
		}
	}

	// Get recent logs
	recent, err := store.GetRecentLogs(10)
	if err != nil {
		t.Fatalf("GetRecentLogs failed: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(recent))
	}

	// Should be ordered DESC by timestamp — most recent first
	if recent[0].Message != "Build failed" {
		t.Errorf("Expected most recent entry first, got %q", recent[0].Message)
	}
	if recent[0].Level != "error" {
		t.Errorf("Expected level 'error', got %q", recent[0].Level)
	}

	// Test limit
	limited, err := store.GetRecentLogs(2)
	if err != nil {
		t.Fatalf("GetRecentLogs with limit failed: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Expected 2 entries with limit, got %d", len(limited))
	}

	// Empty result
	tmpDir2, _ := os.MkdirTemp("", "pilot-test-empty-*")
	defer func() { _ = os.RemoveAll(tmpDir2) }()
	store2, _ := NewStore(tmpDir2)
	defer func() { _ = store2.Close() }()

	empty, err := store2.GetRecentLogs(10)
	if err != nil {
		t.Fatalf("GetRecentLogs on empty store failed: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Expected 0 entries on empty store, got %d", len(empty))
	}
}

func TestLogSubscribeLogs(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Subscribe before saving
	ch := store.SubscribeLogs()

	entry := &LogEntry{
		ExecutionID: "exec-sub",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "hello subscriber",
		Component:   "test",
	}

	if err := store.SaveLogEntry(entry); err != nil {
		t.Fatalf("SaveLogEntry failed: %v", err)
	}

	select {
	case got := <-ch:
		if got.Message != "hello subscriber" {
			t.Errorf("Expected 'hello subscriber', got %q", got.Message)
		}
		if got.ID == 0 {
			t.Error("Expected non-zero ID on received entry")
		}
	case <-time.After(time.Second):
		t.Fatal("Timed out waiting for subscriber notification")
	}

	// Unsubscribe and verify channel is closed
	store.UnsubscribeLogs(ch)

	// Save another entry — should not panic or block
	entry2 := &LogEntry{
		ExecutionID: "exec-sub",
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "after unsubscribe",
		Component:   "test",
	}
	if err := store.SaveLogEntry(entry2); err != nil {
		t.Fatalf("SaveLogEntry after unsubscribe failed: %v", err)
	}
}

func TestLogSubscribeMultipleSubscribers(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	ch1 := store.SubscribeLogs()
	ch2 := store.SubscribeLogs()

	entry := &LogEntry{
		ExecutionID: "exec-multi",
		Timestamp:   time.Now(),
		Level:       "warn",
		Message:     "broadcast test",
		Component:   "test",
	}

	if err := store.SaveLogEntry(entry); err != nil {
		t.Fatalf("SaveLogEntry failed: %v", err)
	}

	// Both subscribers should receive the entry
	for i, ch := range []<-chan *LogEntry{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Message != "broadcast test" {
				t.Errorf("subscriber %d: expected 'broadcast test', got %q", i, got.Message)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}

	store.UnsubscribeLogs(ch1)
	store.UnsubscribeLogs(ch2)
}

func TestCrossPatternsIndexes(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Insert test data so the planner has something to work with
	pattern := &CrossPattern{
		ID:    "test-idx-1",
		Type:  "naming",
		Title: "Use camelCase",
		Scope: "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	tests := []struct {
		name  string
		query string
		index string
	}{
		{
			name:  "scope filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE scope = 'org'`,
			index: "idx_cross_patterns_scope",
		},
		{
			name:  "updated_at filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE updated_at > '2025-01-01'`,
			index: "idx_cross_patterns_updated",
		},
		{
			name:  "title filter uses index",
			query: `EXPLAIN QUERY PLAN SELECT * FROM cross_patterns WHERE title = 'Use camelCase'`,
			index: "idx_cross_patterns_title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := store.db.Query(tt.query)
			if err != nil {
				t.Fatalf("EXPLAIN QUERY PLAN failed: %v", err)
			}
			defer func() { _ = rows.Close() }()

			var plan strings.Builder
			for rows.Next() {
				var id, parent, notused int
				var detail string
				if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
					t.Fatalf("scan failed: %v", err)
				}
				_, _ = fmt.Fprintf(&plan, "%s\n", detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows iteration failed: %v", err)
			}

			if !strings.Contains(plan.String(), tt.index) {
				t.Errorf("expected query plan to use %s, got:\n%s", tt.index, plan.String())
			}
		})
	}
}

func TestGetStaleQueuedExecutions(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	staleDuration := time.Hour

	now := time.Now()

	insertAt := func(exec *Execution, createdAt time.Time) {
		t.Helper()
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution %s: %v", exec.ID, err)
		}
		if _, err := store.db.Exec(
			`UPDATE executions SET created_at = ? WHERE id = ?`, createdAt, exec.ID,
		); err != nil {
			t.Fatalf("set created_at for %s: %v", exec.ID, err)
		}
	}

	// Fresh queued execution (created now — should NOT be returned).
	insertAt(&Execution{ID: "queued-fresh", TaskID: "TASK-fresh", ProjectPath: "/proj", Status: "queued"}, now)

	// Stale queued execution (created 2 hours ago — should be returned).
	insertAt(&Execution{ID: "queued-stale", TaskID: "TASK-stale", ProjectPath: "/proj", Status: "queued"}, now.Add(-2*time.Hour))

	// Stale running execution — must NOT appear in queued results.
	insertAt(&Execution{ID: "running-stale", TaskID: "TASK-running", ProjectPath: "/proj", Status: "running"}, now.Add(-2*time.Hour))

	results, err := store.GetStaleQueuedExecutions(staleDuration)
	if err != nil {
		t.Fatalf("GetStaleQueuedExecutions: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 stale queued execution, got %d", len(results))
	}
	if results[0].ID != "queued-stale" {
		t.Errorf("expected ID %q, got %q", "queued-stale", results[0].ID)
	}
	if results[0].Status != "queued" {
		t.Errorf("expected status 'queued', got %q", results[0].Status)
	}
}

func TestUpdateExecutionStatusByTaskID_UpdatesFailedToCompleted(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-fail-1",
		TaskID:      "GH-100",
		ProjectPath: "/tmp/proj",
		Status:      "failed",
		Error:       "quality gate failed",
	})

	if err := store.UpdateExecutionStatusByTaskID("GH-100", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID failed: %v", err)
	}

	exec, err := store.GetExecution("exec-fail-1")
	if err != nil {
		t.Fatalf("GetExecution failed: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
	if exec.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestUpdateExecutionStatusByTaskID_SkipsNonFailed(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{
		ID:          "exec-ok-1",
		TaskID:      "GH-200",
		ProjectPath: "/tmp/proj",
		Status:      "completed",
	})

	if err := store.UpdateExecutionStatusByTaskID("GH-200", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID failed: %v", err)
	}

	exec, _ := store.GetExecution("exec-ok-1")
	// Status should remain "completed" — the WHERE clause only targets "failed"
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
}

func TestUpdateExecutionStatusByTaskID_NoMatchingTask(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Should not error even with no matching rows
	if err := store.UpdateExecutionStatusByTaskID("GH-999", "/tmp/proj", "completed"); err != nil {
		t.Fatalf("expected no error for non-existent task, got: %v", err)
	}
}

// TestUpdateExecutionStatusByTaskID_ScopedToProject verifies that updating by task ID
// only affects rows matching the given project path (D3 regression).
func TestUpdateExecutionStatusByTaskID_ScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Same task ID, different projects
	_ = store.SaveExecution(&Execution{ID: "exec-a", TaskID: "GH-300", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "exec-b", TaskID: "GH-300", ProjectPath: "/proj/b", Status: "failed"})

	// Only heal project a
	if err := store.UpdateExecutionStatusByTaskID("GH-300", "/proj/a", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatusByTaskID: %v", err)
	}

	execA, _ := store.GetExecution("exec-a")
	if execA.Status != "completed" {
		t.Errorf("exec-a: expected 'completed', got %q", execA.Status)
	}
	execB, _ := store.GetExecution("exec-b")
	if execB.Status != "failed" {
		t.Errorf("exec-b: expected 'failed' (unaffected), got %q", execB.Status)
	}
}

// TestSelfHealExecutionAfterMerge_ScopedToProject verifies that self-heal only
// promotes rows matching the given project path (D3 regression).
func TestSelfHealExecutionAfterMerge_ScopedToProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "heal-a", TaskID: "GH-400", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "heal-b", TaskID: "GH-400", ProjectPath: "/proj/b", Status: "failed"})

	if err := store.SelfHealExecutionAfterMerge("GH-400", "/proj/a", "https://github.com/org/repo/pull/1"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	execA, _ := store.GetExecution("heal-a")
	if execA.Status != "completed" {
		t.Errorf("heal-a: expected 'completed', got %q", execA.Status)
	}
	if execA.PRUrl != "https://github.com/org/repo/pull/1" {
		t.Errorf("heal-a: expected pr_url to be stamped, got %q", execA.PRUrl)
	}
	execB, _ := store.GetExecution("heal-b")
	if execB.Status != "failed" {
		t.Errorf("heal-b: expected 'failed' (unaffected), got %q", execB.Status)
	}
}

// TestSelfHealExecutionAfterMerge_EmptyProjectPath verifies that an empty
// projectPath falls back to task_id-only matching (legacy single-repo behavior),
// so a caller that cannot supply the discriminator still heals every matching row
// rather than silently matching nothing. TASK-352.
func TestSelfHealExecutionAfterMerge_EmptyProjectPath(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-500", ProjectPath: "/proj/a", Status: "failed"})
	_ = store.SaveExecution(&Execution{ID: "e2", TaskID: "GH-500", ProjectPath: "/proj/b", Status: "failed"})

	if err := store.SelfHealExecutionAfterMerge("GH-500", "", "https://github.com/org/repo/pull/9"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}

	for _, id := range []string{"e1", "e2"} {
		ex, _ := store.GetExecution(id)
		if ex.Status != "completed" {
			t.Errorf("%s: expected 'completed' with empty projectPath fallback, got %q", id, ex.Status)
		}
	}
}

// TestRecentCompletedTelemetryStats verifies the zero-token telemetry gap
// query: rows are filtered to completed runs with a real commit, and rows
// without commit_sha (e.g. epic orchestrators) are excluded so they don't
// inflate the gap ratio. GH-2428.
func TestRecentCompletedTelemetryStats(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	type rec struct {
		id     string
		status string
		commit string
		tokens int64
	}
	rows := []rec{
		{"a", "completed", "deadbeef", 100}, // counts, not zero
		{"b", "completed", "cafe1234", 0},   // counts, zero
		{"c", "completed", "ba5eba11", 0},   // counts, zero
		{"d", "completed", "", 0},           // SKIPPED (no commit — epic orchestrator)
		{"e", "failed", "feedface", 0},      // SKIPPED (not completed)
	}
	for _, r := range rows {
		if err := store.SaveExecution(&Execution{
			ID:          r.id,
			TaskID:      "T-" + r.id,
			ProjectPath: "/x",
			Status:      r.status,
			CommitSHA:   r.commit,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", r.id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID: r.id,
			TokensTotal: r.tokens,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", r.id, err)
		}
	}

	stats, err := store.RecentCompletedTelemetryStats(50)
	if err != nil {
		t.Fatalf("RecentCompletedTelemetryStats: %v", err)
	}
	if stats.CompletedRuns != 3 {
		t.Errorf("CompletedRuns = %d, want 3 (only completed+commit rows)", stats.CompletedRuns)
	}
	if stats.ZeroTokenRuns != 2 {
		t.Errorf("ZeroTokenRuns = %d, want 2", stats.ZeroTokenRuns)
	}
}

func TestInvalidateCompletion(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	taskID := "GH-500"
	projectPath := "/project"

	// Insert a genuine completed execution (no error, with deliverable).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-genuine",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "completed",
		CommitSHA:   "sha-genuine",
	})

	// Insert an orphan-recovered execution (status=completed, error set).
	_ = store.SaveExecution(&Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
	})
	_ = store.UpdateExecutionStatus("exec-orphan", "completed", "stale running task recovered (orphaned worker)")

	// Confirm HasCompletedExecution sees the genuine one.
	completed, err := store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution: %v", err)
	}
	if !completed {
		t.Fatal("expected true before invalidation")
	}

	// Invalidate: should remove the genuine row only.
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Fatalf("InvalidateCompletion: %v", err)
	}

	// HasCompletedExecution should now return false.
	completed, err = store.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("HasCompletedExecution after invalidation: %v", err)
	}
	if completed {
		t.Error("expected false after invalidation")
	}

	// Calling again on already-empty set should be a no-op (no error).
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Errorf("InvalidateCompletion on empty set: %v", err)
	}

	// Different project path should be unaffected — add a new genuine row and check.
	otherPath := "/other-project"
	_ = store.SaveExecution(&Execution{
		ID:          "exec-other",
		TaskID:      taskID,
		ProjectPath: otherPath,
		Status:      "completed",
		CommitSHA:   "sha-other",
	})
	if err := store.InvalidateCompletion(taskID, projectPath); err != nil {
		t.Fatalf("InvalidateCompletion: %v", err)
	}
	completed, _ = store.HasCompletedExecution(taskID, otherPath)
	if !completed {
		t.Error("InvalidateCompletion should not affect different project path")
	}
}

func TestGetLifetimeTokens_ExcludesZeroTokenRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Row with real tokens
	if err := store.SaveExecution(&Execution{ID: "exec-real", TaskID: "T-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "exec-real", TokensInput: 5000, TokensOutput: 2000, TokensTotal: 7000, EstimatedCostUSD: 0.50}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	// Dispatcher queue / early-failure row with zero tokens
	if err := store.SaveExecution(&Execution{ID: "exec-zero", TaskID: "T-2", ProjectPath: "/p", Status: "failed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	lt, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens: %v", err)
	}
	if lt.TotalTokens != 7000 {
		t.Errorf("TotalTokens = %d, want 7000 (zero-token row must be excluded)", lt.TotalTokens)
	}
	if lt.TotalCostUSD != 0.50 {
		t.Errorf("TotalCostUSD = %.4f, want 0.5000", lt.TotalCostUSD)
	}

	// Project-scoped filter must also exclude zero-token rows.
	ltScoped, err := store.GetLifetimeTokens("/p")
	if err != nil {
		t.Fatalf("GetLifetimeTokens(/p): %v", err)
	}
	if ltScoped.TotalTokens != 7000 {
		t.Errorf("scoped TotalTokens = %d, want 7000 (zero-token row must be excluded under filter)", ltScoped.TotalTokens)
	}
	if ltScoped.TotalCostUSD != 0.50 {
		t.Errorf("scoped TotalCostUSD = %.4f, want 0.5000", ltScoped.TotalCostUSD)
	}
}

func TestGetDailyMetrics_ExcludesZeroTokenRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)

	// Real execution
	if err := store.SaveExecution(&Execution{ID: "dm-real", TaskID: "T-1", ProjectPath: "/p", Status: "completed", CreatedAt: yesterday}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.SaveExecutionMetrics(&ExecutionMetrics{ExecutionID: "dm-real", TokensInput: 3000, TokensOutput: 1000, TokensTotal: 4000, EstimatedCostUSD: 0.30}); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	// Zero-token row (same day) — should not appear in daily metrics
	if err := store.SaveExecution(&Execution{ID: "dm-zero", TaskID: "T-2", ProjectPath: "/p", Status: "failed", CreatedAt: yesterday}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	q := MetricsQuery{Start: yesterday.Add(-time.Hour), End: now.Add(time.Hour)}
	days, err := store.GetDailyMetrics(q)
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if len(days) == 0 {
		t.Fatal("GetDailyMetrics: want at least 1 day row")
	}
	// Only the real execution should be counted
	if days[0].ExecutionCount != 1 {
		t.Errorf("ExecutionCount = %d, want 1 (zero-token row must be excluded)", days[0].ExecutionCount)
	}
}

// TestEffortLevelColumns_MigrationAddsColumns verifies that a fresh DB created by NewStore
// has the effort_level and complexity_level columns in the executions table.
func TestEffortLevelColumns_MigrationAddsColumns(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Query sqlite_master to verify columns exist
	var effortCount, complexityCount int
	row := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('executions') WHERE name='effort_level'`)
	if err := row.Scan(&effortCount); err != nil {
		t.Fatalf("pragma_table_info effort_level: %v", err)
	}
	row = store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('executions') WHERE name='complexity_level'`)
	if err := row.Scan(&complexityCount); err != nil {
		t.Fatalf("pragma_table_info complexity_level: %v", err)
	}

	if effortCount != 1 {
		t.Errorf("effort_level column missing from executions table")
	}
	if complexityCount != 1 {
		t.Errorf("complexity_level column missing from executions table")
	}
}

// TestEffortLevelColumns_BackwardsCompat verifies that opening a DB that already has an
// executions table (but lacks effort_level/complexity_level) runs the migration cleanly
// via the idempotent ALTER TABLE pattern.
func TestEffortLevelColumns_BackwardsCompat(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a DB without the new columns
	store1, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	// Insert a row before the migration adds the columns; simulate a pre-existing row.
	_ = store1.SaveExecution(&Execution{
		ID: "pre-migration", TaskID: "T-pre", ProjectPath: "/p", Status: "completed",
	})
	_ = store1.Close()

	// Re-open: migration must run the ALTER TABLE statements idempotently.
	store2, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore (second open, post-migration): %v", err)
	}
	defer func() { _ = store2.Close() }()

	// Pre-existing row must still be readable with null columns coerced to "".
	exec, err := store2.GetExecution("pre-migration")
	if err != nil {
		t.Fatalf("GetExecution after migration: %v", err)
	}
	if exec.EffortLevel != "" {
		t.Errorf("pre-migration row: EffortLevel = %q, want empty", exec.EffortLevel)
	}
	if exec.ComplexityLevel != "" {
		t.Errorf("pre-migration row: ComplexityLevel = %q, want empty", exec.ComplexityLevel)
	}
}

// TestEffortLevelColumns_RoundTrip verifies that SaveExecution persists EffortLevel and
// ComplexityLevel, and GetExecution returns the same values.
func TestEffortLevelColumns_RoundTrip(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-test-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	exec := &Execution{
		ID:              "exec-effort-rt",
		TaskID:          "T-effort",
		ProjectPath:     "/project",
		Status:          "completed",
		EffortLevel:     "medium",
		ComplexityLevel: "simple",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	got, err := store.GetExecution("exec-effort-rt")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.EffortLevel != "medium" {
		t.Errorf("EffortLevel = %q, want %q", got.EffortLevel, "medium")
	}
	if got.ComplexityLevel != "simple" {
		t.Errorf("ComplexityLevel = %q, want %q", got.ComplexityLevel, "simple")
	}

	// Also verify UpdateExecutionEffort writes new values.
	if err := store.UpdateExecutionEffort("exec-effort-rt", "high", "complex"); err != nil {
		t.Fatalf("UpdateExecutionEffort: %v", err)
	}
	got2, err := store.GetExecution("exec-effort-rt")
	if err != nil {
		t.Fatalf("GetExecution after update: %v", err)
	}
	if got2.EffortLevel != "high" {
		t.Errorf("after update: EffortLevel = %q, want %q", got2.EffortLevel, "high")
	}
	if got2.ComplexityLevel != "complex" {
		t.Errorf("after update: ComplexityLevel = %q, want %q", got2.ComplexityLevel, "complex")
	}
}

// TestPruneExecutionLogs verifies D5: deletes logs older than the cutoff and
// leaves newer ones untouched.
func TestPruneExecutionLogs(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-10 * time.Minute)

	// Insert two old entries and one recent entry directly.
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'old1', 'test')`, old)
	if err != nil {
		t.Fatalf("insert old1: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'old2', 'test')`, old)
	if err != nil {
		t.Fatalf("insert old2: %v", err)
	}
	_, err = store.db.Exec(`INSERT INTO execution_logs (timestamp, level, message, component) VALUES (?, 'info', 'recent', 'test')`, recent)
	if err != nil {
		t.Fatalf("insert recent: %v", err)
	}

	deleted, err := store.PruneExecutionLogs(time.Hour)
	if err != nil {
		t.Fatalf("PruneExecutionLogs: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_logs`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("remaining rows = %d, want 1", count)
	}
}

// TestRecordPatternFeedback_TransactionAtomicity verifies D6: all three writes
// inside RecordPatternFeedback succeed together — the feedback row, the
// confidence update, and the project-link update.
func TestRecordPatternFeedback_TransactionAtomicity(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Prerequisites: a cross pattern and a project link must exist for the
	// confidence/link UPDATE statements to match rows.
	pattern := &CrossPattern{
		ID:          "pat-tx-1",
		Type:        "code",
		Title:       "test pattern",
		Description: "desc",
		Confidence:  0.5,
		Occurrences: 1,
		Scope:       "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern: %v", err)
	}
	if err := store.LinkPatternToProject("pat-tx-1", "/proj/tx"); err != nil {
		t.Fatalf("LinkPatternToProject: %v", err)
	}
	// Seed an execution row so the FK constraint on pattern_feedback is satisfied.
	if err := store.SaveExecution(&Execution{ID: "exec-tx-1", TaskID: "GH-TX", ProjectPath: "/proj/tx", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	fb := &PatternFeedback{
		PatternID:       "pat-tx-1",
		ExecutionID:     "exec-tx-1",
		ProjectPath:     "/proj/tx",
		Outcome:         "success",
		ConfidenceDelta: 0.1,
	}
	if err := store.RecordPatternFeedback(fb); err != nil {
		t.Fatalf("RecordPatternFeedback: %v", err)
	}
	if fb.ID == 0 {
		t.Error("expected feedback ID to be set after insert")
	}

	// Confidence should have increased.
	updated, err := store.GetCrossPattern("pat-tx-1")
	if err != nil {
		t.Fatalf("GetCrossPattern: %v", err)
	}
	if updated.Confidence <= 0.5 {
		t.Errorf("confidence = %.3f, want > 0.5", updated.Confidence)
	}

	// Project link success_count should be 1.
	links, err := store.GetProjectsForPattern("pat-tx-1")
	if err != nil {
		t.Fatalf("GetProjectsForPattern: %v", err)
	}
	if len(links) == 0 || links[0].SuccessCount != 1 {
		t.Errorf("success_count = %d, want 1", func() int {
			if len(links) > 0 {
				return links[0].SuccessCount
			}
			return -1
		}())
	}
}

func TestGetRecentExecutions_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	fixture := []struct {
		id   string
		path string
	}{
		{"pf-exec-a1", pathA},
		{"pf-exec-a2", pathA},
		{"pf-exec-a3", pathA},
		{"pf-exec-b1", pathB},
		{"pf-exec-b2", pathB},
	}
	for _, f := range fixture {
		if err := store.SaveExecution(&Execution{
			ID:          f.id,
			TaskID:      "TASK-" + f.id,
			ProjectPath: f.path,
			Status:      "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", f.id, err)
		}
	}

	all, err := store.GetRecentExecutions(100, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions (all): %v", err)
	}
	if len(all) != 5 {
		t.Errorf("unfiltered: got %d, want 5", len(all))
	}

	forA, err := store.GetRecentExecutions(100, pathA)
	if err != nil {
		t.Fatalf("GetRecentExecutions (pathA): %v", err)
	}
	if len(forA) != 3 {
		t.Errorf("filter=%s: got %d, want 3", pathA, len(forA))
	}
	for _, e := range forA {
		if e.ProjectPath != pathA {
			t.Errorf("unexpected ProjectPath %q in filtered results", e.ProjectPath)
		}
	}

	forB, err := store.GetRecentExecutions(100, pathB)
	if err != nil {
		t.Fatalf("GetRecentExecutions (pathB): %v", err)
	}
	if len(forB) != 2 {
		t.Errorf("filter=%s: got %d, want 2", pathB, len(forB))
	}
}

func TestGetLifetimeTokens_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	saveWithTokens := func(id, path string, input, output int64, cost float64) {
		t.Helper()
		if err := store.SaveExecution(&Execution{
			ID: id, TaskID: "TASK-" + id, ProjectPath: path, Status: "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", id, err)
		}
		if err := store.SaveExecutionMetrics(&ExecutionMetrics{
			ExecutionID:      id,
			TokensInput:      input,
			TokensOutput:     output,
			TokensTotal:      input + output,
			EstimatedCostUSD: cost,
		}); err != nil {
			t.Fatalf("SaveExecutionMetrics %s: %v", id, err)
		}
	}

	saveWithTokens("lt-pf-a1", pathA, 1000, 500, 0.10)
	saveWithTokens("lt-pf-a2", pathA, 2000, 1000, 0.20)
	saveWithTokens("lt-pf-b1", pathB, 500, 250, 0.05)

	approxEqual := func(a, b float64) bool {
		diff := a - b
		if diff < 0 {
			diff = -diff
		}
		return diff < 1e-9
	}

	ltA, err := store.GetLifetimeTokens(pathA)
	if err != nil {
		t.Fatalf("GetLifetimeTokens(pathA): %v", err)
	}
	if ltA.TotalTokens != 4500 {
		t.Errorf("pathA TotalTokens = %d, want 4500", ltA.TotalTokens)
	}
	if !approxEqual(ltA.TotalCostUSD, 0.30) {
		t.Errorf("pathA TotalCostUSD = %.10f, want ~0.30", ltA.TotalCostUSD)
	}

	ltB, err := store.GetLifetimeTokens(pathB)
	if err != nil {
		t.Fatalf("GetLifetimeTokens(pathB): %v", err)
	}
	if ltB.TotalTokens != 750 {
		t.Errorf("pathB TotalTokens = %d, want 750", ltB.TotalTokens)
	}

	ltAll, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens(all): %v", err)
	}
	if ltAll.TotalTokens != 5250 {
		t.Errorf("unfiltered TotalTokens = %d, want 5250", ltAll.TotalTokens)
	}
}

func TestGetLifetimeTaskCounts_ProjectFilter(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const pathA = "/project/alpha"
	const pathB = "/project/beta"

	fixture := []struct {
		id     string
		path   string
		status string
	}{
		{"tc-pf-a1", pathA, "completed"},
		{"tc-pf-a2", pathA, "completed"},
		{"tc-pf-a3", pathA, "failed"},
		{"tc-pf-b1", pathB, "completed"},
		{"tc-pf-b2", pathB, "no_op"},
	}
	for _, f := range fixture {
		if err := store.SaveExecution(&Execution{
			ID:          f.id,
			TaskID:      "TASK-" + f.id,
			ProjectPath: f.path,
			Status:      f.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", f.id, err)
		}
	}

	tcA, err := store.GetLifetimeTaskCounts(pathA)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(pathA): %v", err)
	}
	if tcA.Total != 3 {
		t.Errorf("pathA Total = %d, want 3", tcA.Total)
	}
	if tcA.Succeeded != 2 {
		t.Errorf("pathA Succeeded = %d, want 2", tcA.Succeeded)
	}
	if tcA.Failed != 1 {
		t.Errorf("pathA Failed = %d, want 1", tcA.Failed)
	}

	tcB, err := store.GetLifetimeTaskCounts(pathB)
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(pathB): %v", err)
	}
	if tcB.Total != 2 {
		t.Errorf("pathB Total = %d, want 2", tcB.Total)
	}
	if tcB.Succeeded != 1 {
		t.Errorf("pathB Succeeded = %d, want 1", tcB.Succeeded)
	}
	if tcB.NoOp != 1 {
		t.Errorf("pathB NoOp = %d, want 1", tcB.NoOp)
	}
	if tcB.Failed != 0 {
		t.Errorf("pathB Failed = %d, want 0", tcB.Failed)
	}

	tcAll, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts(all): %v", err)
	}
	if tcAll.Total != 5 {
		t.Errorf("unfiltered Total = %d, want 5", tcAll.Total)
	}
}
