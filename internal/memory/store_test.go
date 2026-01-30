package memory

import (
	"bytes"
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

	recent, err := store.GetRecentExecutions(3)
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
		ModelName:        "claude-sonnet-4-5",
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
	allExecs, _ := store.GetRecentExecutions(100)
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
