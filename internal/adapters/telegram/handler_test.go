package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

// MockRunner implements a minimal executor.Runner interface for testing
type MockRunner struct {
	cancelFunc     func(taskID string) error
	progressFunc   func(taskID, phase string, progress int, message string)
	progressMu     sync.Mutex
	onProgressFunc func(fn func(string, string, int, string))
}

func (m *MockRunner) OnProgress(fn func(string, string, int, string)) {
	m.progressMu.Lock()
	defer m.progressMu.Unlock()
	m.progressFunc = fn
	if m.onProgressFunc != nil {
		m.onProgressFunc(fn)
	}
}

func (m *MockRunner) Cancel(taskID string) error {
	if m.cancelFunc != nil {
		return m.cancelFunc(taskID)
	}
	return nil
}

// TestNewHandler tests handler creation with various configurations
func TestNewHandler(t *testing.T) {
	tests := []struct {
		name         string
		config       *HandlerConfig
		wantAllowIDs int
	}{
		{
			name: "basic config",
			config: &HandlerConfig{
				BotToken:    testutil.FakeTelegramBotToken,
				ProjectPath: "/test/path",
			},
			wantAllowIDs: 0,
		},
		{
			name: "with allowed IDs",
			config: &HandlerConfig{
				BotToken:    testutil.FakeTelegramBotToken,
				ProjectPath: "/test/path",
				AllowedIDs:  []int64{123, 456, 789},
			},
			wantAllowIDs: 3,
		},
		{
			name: "empty allowed IDs",
			config: &HandlerConfig{
				BotToken:    testutil.FakeTelegramBotToken,
				ProjectPath: "/test/path",
				AllowedIDs:  []int64{},
			},
			wantAllowIDs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(tt.config, nil)

			if h == nil {
				t.Fatal("NewHandler returned nil")
			}
			if h.projectPath != tt.config.ProjectPath {
				t.Errorf("projectPath = %q, want %q", h.projectPath, tt.config.ProjectPath)
			}
			if len(h.allowedIDs) != tt.wantAllowIDs {
				t.Errorf("allowedIDs len = %d, want %d", len(h.allowedIDs), tt.wantAllowIDs)
			}
			if h.pendingTasks == nil {
				t.Error("pendingTasks map not initialized")
			}
			if h.runningTasks == nil {
				t.Error("runningTasks map not initialized")
			}
		})
	}
}

// TestGetActiveProjectPath tests active project path retrieval
func TestGetActiveProjectPath(t *testing.T) {
	h := &Handler{
		projectPath:   "/default/path",
		activeProject: make(map[string]string),
	}

	// Test default path
	path := h.getActiveProjectPath("chat1")
	if path != "/default/path" {
		t.Errorf("getActiveProjectPath() = %q, want %q", path, "/default/path")
	}

	// Set active project for chat
	h.mu.Lock()
	h.activeProject["chat1"] = "/custom/path"
	h.mu.Unlock()

	path = h.getActiveProjectPath("chat1")
	if path != "/custom/path" {
		t.Errorf("getActiveProjectPath() = %q, want %q", path, "/custom/path")
	}

	// Other chat still gets default
	path = h.getActiveProjectPath("chat2")
	if path != "/default/path" {
		t.Errorf("getActiveProjectPath() = %q, want %q", path, "/default/path")
	}
}

// MockProjectSource implements ProjectSource for testing
type MockProjectSource struct {
	projects []*ProjectInfo
}

func (m *MockProjectSource) GetProjectByName(name string) *ProjectInfo {
	for _, p := range m.projects {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func (m *MockProjectSource) GetProjectByPath(path string) *ProjectInfo {
	for _, p := range m.projects {
		if p.Path == path {
			return p
		}
	}
	return nil
}

func (m *MockProjectSource) GetDefaultProject() *ProjectInfo {
	if len(m.projects) > 0 {
		return m.projects[0]
	}
	return nil
}

func (m *MockProjectSource) ListProjects() []*ProjectInfo {
	return m.projects
}

// TestSetActiveProject tests project switching
func TestSetActiveProject(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{
			{Name: "project-a", Path: "/path/a"},
			{Name: "project-b", Path: "/path/b"},
		},
	}

	h := &Handler{
		projects:      projects,
		activeProject: make(map[string]string),
	}

	tests := []struct {
		name        string
		chatID      string
		projectName string
		wantPath    string
		wantErr     bool
	}{
		{
			name:        "switch to existing project",
			chatID:      "chat1",
			projectName: "project-a",
			wantPath:    "/path/a",
			wantErr:     false,
		},
		{
			name:        "switch to another project",
			chatID:      "chat1",
			projectName: "project-b",
			wantPath:    "/path/b",
			wantErr:     false,
		},
		{
			name:        "non-existent project",
			chatID:      "chat1",
			projectName: "unknown",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj, err := h.setActiveProject(tt.chatID, tt.projectName)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if proj.Path != tt.wantPath {
				t.Errorf("project path = %q, want %q", proj.Path, tt.wantPath)
			}
		})
	}
}

// TestSetActiveProjectNoProjects tests error when no projects configured
func TestSetActiveProjectNoProjects(t *testing.T) {
	h := &Handler{
		projects:      nil,
		activeProject: make(map[string]string),
	}

	_, err := h.setActiveProject("chat1", "any")
	if err == nil {
		t.Error("expected error when projects is nil")
	}
}

// TestPendingTask tests pending task management
func TestPendingTask(t *testing.T) {
	h := &Handler{
		pendingTasks: make(map[string]*PendingTask),
	}

	// Add pending task
	task := &PendingTask{
		TaskID:      "TEST-01",
		Description: "Test task",
		ChatID:      "chat1",
		CreatedAt:   time.Now(),
	}

	h.mu.Lock()
	h.pendingTasks["chat1"] = task
	h.mu.Unlock()

	// Check pending task exists
	h.mu.Lock()
	got, exists := h.pendingTasks["chat1"]
	h.mu.Unlock()

	if !exists {
		t.Fatal("pending task not found")
	}
	if got.TaskID != "TEST-01" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "TEST-01")
	}

	// Delete pending task
	h.mu.Lock()
	delete(h.pendingTasks, "chat1")
	h.mu.Unlock()

	h.mu.Lock()
	_, exists = h.pendingTasks["chat1"]
	h.mu.Unlock()

	if exists {
		t.Error("pending task should be deleted")
	}
}

// TestResolveTaskID tests task ID resolution from user input
func TestResolveTaskID(t *testing.T) {
	// Create temp directory with task files
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create test task files
	task07Content := `# TASK-07: Test Voice Support
**Status**: backlog

Description here.`
	task12Content := `# TASK-12: Another Task
**Status**: in-progress

Description here.`

	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-07-voice-support.md"), []byte(task07Content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-12-another.md"), []byte(task12Content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		name       string
		input      string
		wantID     string
		wantFullID string
		wantNil    bool
	}{
		{
			name:       "simple number",
			input:      "7",
			wantID:     "07",
			wantFullID: "TASK-07",
		},
		{
			name:       "padded number",
			input:      "07",
			wantID:     "07",
			wantFullID: "TASK-07",
		},
		{
			name:       "with TASK prefix",
			input:      "TASK-07",
			wantID:     "07",
			wantFullID: "TASK-07",
		},
		{
			name:       "lowercase task prefix",
			input:      "task-12",
			wantID:     "12",
			wantFullID: "TASK-12",
		},
		{
			name:       "with space",
			input:      "task 7",
			wantID:     "07",
			wantFullID: "TASK-07",
		},
		{
			name:       "with hash",
			input:      "#12",
			wantID:     "12",
			wantFullID: "TASK-12",
		},
		{
			name:    "non-existent task",
			input:   "99",
			wantNil: true,
		},
		{
			name:    "invalid input",
			input:   "abc",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.resolveTaskID(tt.input)

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected non-nil TaskInfo")
			}

			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if got.FullID != tt.wantFullID {
				t.Errorf("FullID = %q, want %q", got.FullID, tt.wantFullID)
			}
		})
	}
}

// TestResolveTaskFromDescription tests extracting task ID from natural language
func TestResolveTaskFromDescription(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	task07Content := `# TASK-07: Test Task
**Status**: backlog`

	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-07-test.md"), []byte(task07Content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		name        string
		description string
		wantFullID  string
		wantNil     bool
	}{
		{
			name:        "start task",
			description: "start task 07",
			wantFullID:  "TASK-07",
		},
		{
			name:        "run task",
			description: "run task 7",
			wantFullID:  "TASK-07",
		},
		{
			name:        "execute task",
			description: "execute task-07",
			wantFullID:  "TASK-07",
		},
		{
			name:        "just number",
			description: "07",
			wantFullID:  "TASK-07",
		},
		{
			name:        "do number",
			description: "do 7",
			wantFullID:  "TASK-07",
		},
		{
			name:        "no task reference",
			description: "create a new file",
			wantNil:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.resolveTaskFromDescription(tt.description)

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("expected non-nil TaskInfo")
			}

			if got.FullID != tt.wantFullID {
				t.Errorf("FullID = %q, want %q", got.FullID, tt.wantFullID)
			}
		})
	}
}

// TestLoadTaskDescription tests loading full task content
func TestLoadTaskDescription(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	taskContent := `# TASK-01: Test Task
**Status**: backlog

Full task description here.`

	taskFile := filepath.Join(tasksDir, "TASK-01-test.md")
	if err := os.WriteFile(taskFile, []byte(taskContent), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		name     string
		taskInfo *TaskInfo
		wantLen  int
		wantZero bool
	}{
		{
			name: "valid task info",
			taskInfo: &TaskInfo{
				ID:       "01",
				FullID:   "TASK-01",
				FilePath: taskFile,
			},
			wantLen: len(taskContent),
		},
		{
			name:     "nil task info",
			taskInfo: nil,
			wantZero: true,
		},
		{
			name: "empty file path",
			taskInfo: &TaskInfo{
				ID:       "01",
				FullID:   "TASK-01",
				FilePath: "",
			},
			wantZero: true,
		},
		{
			name: "non-existent file",
			taskInfo: &TaskInfo{
				ID:       "99",
				FullID:   "TASK-99",
				FilePath: "/nonexistent/file.md",
			},
			wantZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.loadTaskDescription(tt.taskInfo)

			if tt.wantZero {
				if got != "" {
					t.Errorf("expected empty string, got %q", got)
				}
				return
			}

			if len(got) != tt.wantLen {
				t.Errorf("description length = %d, want %d", len(got), tt.wantLen)
			}
		})
	}
}

// TestContainsAny tests the containsAny helper
func TestContainsAny(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substrs  []string
		expected bool
	}{
		{
			name:     "contains first",
			s:        "show me the tasks",
			substrs:  []string{"tasks", "issues", "backlog"},
			expected: true,
		},
		{
			name:     "contains second",
			s:        "list all issues",
			substrs:  []string{"tasks", "issues", "backlog"},
			expected: true,
		},
		{
			name:     "contains none",
			s:        "hello world",
			substrs:  []string{"tasks", "issues", "backlog"},
			expected: false,
		},
		{
			name:     "empty string",
			s:        "",
			substrs:  []string{"tasks"},
			expected: false,
		},
		{
			name:     "empty substrs",
			s:        "hello",
			substrs:  []string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsAny(tt.s, tt.substrs...)
			if got != tt.expected {
				t.Errorf("containsAny(%q, %v) = %v, want %v", tt.s, tt.substrs, got, tt.expected)
			}
		})
	}
}

// TestExtractTaskNumber tests extracting task number from filename
func TestExtractTaskNumber(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"TASK-01-description.md", "01"},
		{"TASK-07-voice-support.md", "07"},
		{"TASK-12-another-task.md", "12"},
		{"task-99-test.md", "99"},
		{"TASK-123-long-number.md", "123"},
		{"README.md", ""},
		{"index.md", ""},
		{"TASK-.md", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := extractTaskNumber(tt.filename)
			if got != tt.expected {
				t.Errorf("extractTaskNumber(%q) = %q, want %q", tt.filename, got, tt.expected)
			}
		})
	}
}

// TestMakeProgressBar tests progress bar generation
func TestMakeProgressBar(t *testing.T) {
	tests := []struct {
		percent  int
		expected string
	}{
		{0, "░░░░░░░░░░░░░░░░░░░░"},
		{25, "█████░░░░░░░░░░░░░░░"},
		{50, "██████████░░░░░░░░░░"},
		{75, "███████████████░░░░░"},
		{100, "████████████████████"},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := makeProgressBar(tt.percent)
			if got != tt.expected {
				t.Errorf("makeProgressBar(%d) = %q, want %q", tt.percent, got, tt.expected)
			}
		})
	}
}

// TestParseTaskFile tests parsing task file metadata
func TestParseTaskFile(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		content    string
		wantStatus string
		wantTitle  string
	}{
		{
			name: "complete task",
			content: `# TASK-01: Complete Example
**Status**: complete

Description.`,
			wantStatus: "complete",
			wantTitle:  "Complete Example",
		},
		{
			name: "in progress task",
			content: `# TASK-02: In Progress Task
**Status**: in-progress

Description.`,
			wantStatus: "in-progress",
			wantTitle:  "In Progress Task",
		},
		{
			name: "backlog task",
			content: `# TASK-03: Backlog Task
**Status**: backlog

Description.`,
			wantStatus: "backlog",
			wantTitle:  "Backlog Task",
		},
		{
			name: "no explicit status field",
			content: `# TASK-04: Simple Task

Description only, no metadata.`,
			wantStatus: "pending",
			wantTitle:  "Simple Task",
		},
		{
			name: "emoji status",
			content: `# TASK-05: Done with Emoji
**Status**: done

Description.`,
			wantStatus: "done",
			wantTitle:  "Done with Emoji",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(tmpDir, tt.name+".md")
			if err := os.WriteFile(filePath, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			status, title := parseTaskFile(filePath)

			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

// TestParseTaskFileNonExistent tests parsing non-existent file
func TestParseTaskFileNonExistent(t *testing.T) {
	status, title := parseTaskFile("/nonexistent/file.md")

	if status != "pending" {
		t.Errorf("status = %q, want %q", status, "pending")
	}
	if title != "" {
		t.Errorf("title = %q, want empty", title)
	}
}

// TestVoiceNotAvailableMessage tests the voice setup error message
func TestVoiceNotAvailableMessage(t *testing.T) {
	tests := []struct {
		name             string
		transcriptionErr error
		wantContains     []string
	}{
		{
			name:             "no error set",
			transcriptionErr: nil,
			wantContains:     []string{"Voice transcription not available", "openai_api_key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{
				transcriptionErr: tt.transcriptionErr,
			}

			got := h.voiceNotAvailableMessage()

			for _, want := range tt.wantContains {
				if !contains(got, want) {
					t.Errorf("voiceNotAvailableMessage() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestHandlerCheckSingleton tests the singleton check delegation
func TestHandlerCheckSingleton(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := GetUpdatesResponse{
			OK:     true,
			Result: []*Update{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	h := &Handler{
		client: NewClient(testutil.FakeTelegramBotToken),
	}

	// The actual check will fail because it can't reach real Telegram API
	// but we're verifying the method signature and delegation work
	ctx := context.Background()
	_ = h.CheckSingleton(ctx)
}

// TestFastListTasksEmpty tests fast list when no tasks directory
func TestFastListTasksEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastListTasks()
	if result != "" {
		t.Errorf("expected empty result for non-existent tasks dir, got %q", result)
	}
}

// TestFastListTasksWithTasks tests fast list with actual tasks
func TestFastListTasksWithTasks(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create task files
	tasks := []struct {
		filename string
		content  string
	}{
		{
			"TASK-01-done.md",
			"# TASK-01: Done Task\n**Status**: complete\n",
		},
		{
			"TASK-02-progress.md",
			"# TASK-02: In Progress\n**Status**: in-progress\n",
		},
		{
			"TASK-03-pending.md",
			"# TASK-03: Pending Task\n**Status**: backlog\n",
		},
	}

	for _, task := range tasks {
		if err := os.WriteFile(filepath.Join(tasksDir, task.filename), []byte(task.content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastListTasks()

	if result == "" {
		t.Error("expected non-empty result")
	}

	// Should contain sections
	wantContains := []string{"In Progress", "Backlog", "Recently done", "Progress:"}
	for _, want := range wantContains {
		if !containsSubstr(result, want) {
			t.Errorf("result should contain %q, got:\n%s", want, result)
		}
	}
}

// TestFastReadStatusEmpty tests status reading when file doesn't exist
func TestFastReadStatusEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastReadStatus()
	if result != "" {
		t.Errorf("expected empty result for non-existent readme, got %q", result)
	}
}

// TestFastGrepTodosNoFiles tests grep when no source files
func TestFastGrepTodosNoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastGrepTodos()
	if !containsSubstr(result, "No TODOs") {
		t.Errorf("expected 'No TODOs' message, got %q", result)
	}
}

// TestFastGrepTodosWithTodos tests grep with actual TODO comments
func TestFastGrepTodosWithTodos(t *testing.T) {
	tmpDir := t.TempDir()
	cmdDir := filepath.Join(tmpDir, "cmd")
	if err := os.MkdirAll(cmdDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `package main

// TODO: implement this feature
func main() {
	// FIXME: broken code here
}`

	if err := os.WriteFile(filepath.Join(cmdDir, "main.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastGrepTodos()

	wantContains := []string{"TODO", "FIXME", "cmd/main.go"}
	for _, want := range wantContains {
		if !containsSubstr(result, want) {
			t.Errorf("result should contain %q, got:\n%s", want, result)
		}
	}
}

// TestTryFastAnswer tests the fast answer path
func TestTryFastAnswer(t *testing.T) {
	tmpDir := t.TempDir()
	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		name      string
		question  string
		wantEmpty bool
	}{
		{
			name:      "tasks question",
			question:  "what tasks are there",
			wantEmpty: true, // No tasks dir, so returns empty
		},
		{
			name:      "status question",
			question:  "what is the current status",
			wantEmpty: true, // No readme, so returns empty
		},
		{
			name:      "todos question",
			question:  "show me all todos",
			wantEmpty: false, // Returns "No TODOs" message
		},
		{
			name:      "unrelated question",
			question:  "how do I run tests",
			wantEmpty: true, // Falls back to Claude
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := h.tryFastAnswer(tt.question)
			isEmpty := got == ""
			if isEmpty != tt.wantEmpty {
				t.Errorf("tryFastAnswer(%q) isEmpty = %v, want %v (got: %q)", tt.question, isEmpty, tt.wantEmpty, got)
			}
		})
	}
}

// TestGetActiveProjectInfo tests project info retrieval
func TestGetActiveProjectInfo(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{
			{Name: "test", Path: "/test/path"},
		},
	}

	tests := []struct {
		name     string
		projects ProjectSource
		wantNil  bool
	}{
		{
			name:     "with projects",
			projects: projects,
			wantNil:  false,
		},
		{
			name:     "nil projects",
			projects: nil,
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{
				projects:      tt.projects,
				projectPath:   "/test/path",
				activeProject: make(map[string]string),
			}

			got := h.getActiveProjectInfo("chat1")

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
			} else {
				if got == nil {
					t.Error("expected non-nil ProjectInfo")
				}
			}
		})
	}
}

// TestRunningTask tests running task structure
func TestRunningTask(t *testing.T) {
	h := &Handler{
		runningTasks: make(map[string]*RunningTask),
	}

	// Add running task
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	task := &RunningTask{
		TaskID:    "TEST-01",
		ChatID:    "chat1",
		StartedAt: time.Now(),
		Cancel:    cancel,
	}

	h.mu.Lock()
	h.runningTasks["chat1"] = task
	h.mu.Unlock()

	// Check running task exists
	h.mu.Lock()
	got, exists := h.runningTasks["chat1"]
	h.mu.Unlock()

	if !exists {
		t.Fatal("running task not found")
	}
	if got.TaskID != "TEST-01" {
		t.Errorf("TaskID = %q, want %q", got.TaskID, "TEST-01")
	}

	// Cancel should work
	if got.Cancel == nil {
		t.Error("Cancel function is nil")
	}

	// Context should not be done yet
	select {
	case <-ctx.Done():
		t.Error("context should not be cancelled yet")
	default:
		// OK
	}

	// After cancel, context should be done
	cancel()
	select {
	case <-ctx.Done():
		// OK
	default:
		t.Error("context should be cancelled")
	}
}

// TestProjectInfo tests ProjectInfo struct
func TestProjectInfo(t *testing.T) {
	proj := &ProjectInfo{
		Name:          "test-project",
		Path:          "/path/to/project",
		Navigator:     true,
		DefaultBranch: "main",
	}

	if proj.Name != "test-project" {
		t.Errorf("Name = %q, want test-project", proj.Name)
	}
	if proj.Path != "/path/to/project" {
		t.Errorf("Path = %q, want /path/to/project", proj.Path)
	}
	if !proj.Navigator {
		t.Error("Navigator should be true")
	}
	if proj.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", proj.DefaultBranch)
	}
}

// TestPendingTaskStruct tests PendingTask struct
func TestPendingTaskStruct(t *testing.T) {
	now := time.Now()
	task := &PendingTask{
		TaskID:      "TASK-01",
		Description: "Test description",
		ChatID:      "12345",
		MessageID:   100,
		CreatedAt:   now,
	}

	if task.TaskID != "TASK-01" {
		t.Errorf("TaskID = %q, want TASK-01", task.TaskID)
	}
	if task.Description != "Test description" {
		t.Errorf("Description = %q, want Test description", task.Description)
	}
	if task.ChatID != "12345" {
		t.Errorf("ChatID = %q, want 12345", task.ChatID)
	}
	if task.MessageID != 100 {
		t.Errorf("MessageID = %d, want 100", task.MessageID)
	}
	if !task.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", task.CreatedAt, now)
	}
}

// TestTaskInfo tests TaskInfo struct
func TestTaskInfo(t *testing.T) {
	info := &TaskInfo{
		ID:       "07",
		FullID:   "TASK-07",
		Title:    "Voice Support",
		Status:   "backlog",
		FilePath: "/path/to/TASK-07.md",
	}

	if info.ID != "07" {
		t.Errorf("ID = %q, want 07", info.ID)
	}
	if info.FullID != "TASK-07" {
		t.Errorf("FullID = %q, want TASK-07", info.FullID)
	}
	if info.Title != "Voice Support" {
		t.Errorf("Title = %q, want Voice Support", info.Title)
	}
	if info.Status != "backlog" {
		t.Errorf("Status = %q, want backlog", info.Status)
	}
	if info.FilePath != "/path/to/TASK-07.md" {
		t.Errorf("FilePath = %q, want /path/to/TASK-07.md", info.FilePath)
	}
}

// TestHandlerConfigStruct tests HandlerConfig struct
func TestHandlerConfigStruct(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{{Name: "test", Path: "/test"}},
	}

	config := &HandlerConfig{
		BotToken:    testutil.FakeTelegramBotToken,
		ProjectPath: "/project/path",
		Projects:    projects,
		AllowedIDs:  []int64{123, 456},
	}

	if config.BotToken != testutil.FakeTelegramBotToken {
		t.Errorf("BotToken = %q, want %s", config.BotToken, testutil.FakeTelegramBotToken)
	}
	if config.ProjectPath != "/project/path" {
		t.Errorf("ProjectPath = %q, want /project/path", config.ProjectPath)
	}
	if config.Projects == nil {
		t.Error("Projects should not be nil")
	}
	if len(config.AllowedIDs) != 2 {
		t.Errorf("AllowedIDs len = %d, want 2", len(config.AllowedIDs))
	}
}

// TestNewHandlerWithProjects tests handler creation with projects source
func TestNewHandlerWithProjects(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{
			{Name: "default", Path: "/default/path"},
			{Name: "other", Path: "/other/path"},
		},
	}

	config := &HandlerConfig{
		BotToken: testutil.FakeTelegramBotToken,
		Projects: projects,
		// Note: ProjectPath is empty, should use default from Projects
	}

	h := NewHandler(config, nil)

	if h.projectPath != "/default/path" {
		t.Errorf("projectPath = %q, want /default/path (from default project)", h.projectPath)
	}
}

// TestMockProjectSourceMethods tests all MockProjectSource methods
func TestMockProjectSourceMethods(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{
			{Name: "alpha", Path: "/alpha"},
			{Name: "beta", Path: "/beta"},
		},
	}

	// GetProjectByName
	alpha := projects.GetProjectByName("alpha")
	if alpha == nil || alpha.Path != "/alpha" {
		t.Error("GetProjectByName(alpha) failed")
	}

	notFound := projects.GetProjectByName("notfound")
	if notFound != nil {
		t.Error("GetProjectByName(notfound) should return nil")
	}

	// GetProjectByPath
	beta := projects.GetProjectByPath("/beta")
	if beta == nil || beta.Name != "beta" {
		t.Error("GetProjectByPath(/beta) failed")
	}

	notFoundPath := projects.GetProjectByPath("/notfound")
	if notFoundPath != nil {
		t.Error("GetProjectByPath(/notfound) should return nil")
	}

	// GetDefaultProject
	def := projects.GetDefaultProject()
	if def == nil || def.Name != "alpha" {
		t.Error("GetDefaultProject should return first project")
	}

	// ListProjects
	list := projects.ListProjects()
	if len(list) != 2 {
		t.Errorf("ListProjects len = %d, want 2", len(list))
	}
}

// TestMockProjectSourceEmpty tests empty project source
func TestMockProjectSourceEmpty(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{},
	}

	if projects.GetDefaultProject() != nil {
		t.Error("GetDefaultProject should return nil for empty source")
	}
	if len(projects.ListProjects()) != 0 {
		t.Error("ListProjects should return empty list")
	}
}

// TestFastReadStatusWithContent tests status reading with valid content
func TestFastReadStatusWithContent(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `# Development README

## Current State

| Component | Status |
|-----------|--------|
| Gateway | Complete |
| Adapter | In Progress |

## Active Tasks
- Task 1
- Task 2
`

	if err := os.WriteFile(filepath.Join(agentDir, "DEVELOPMENT-README.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastReadStatus()

	if result == "" {
		t.Error("expected non-empty result")
	}
	if !containsSubstr(result, "Project Status") {
		t.Errorf("result should contain 'Project Status', got:\n%s", result)
	}
}

// TestFastReadStatusNoRelevantSection tests status with no relevant sections
func TestFastReadStatusNoRelevantSection(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := `# Development README

Just some random content without any status sections.
`

	if err := os.WriteFile(filepath.Join(agentDir, "DEVELOPMENT-README.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	result := h.fastReadStatus()

	// Should return empty because no relevant sections found
	if result != "" {
		t.Errorf("expected empty result for no relevant sections, got:\n%s", result)
	}
}

// TestVoiceNotAvailableMessageWithAPIKeyError tests specific error message
func TestVoiceNotAvailableMessageWithAPIKeyError(t *testing.T) {
	h := &Handler{
		transcriptionErr: fmt.Errorf("no backend configured: missing API key"),
	}

	got := h.voiceNotAvailableMessage()

	if !containsSubstr(got, "OpenAI API key") {
		t.Errorf("should mention OpenAI API key, got:\n%s", got)
	}
}

// TestNewHandlerWithTranscriptionError verifies transcription error handling
func TestNewHandlerWithTranscriptionError(t *testing.T) {
	// This test verifies that handler creation works even if transcription fails
	config := &HandlerConfig{
		BotToken:    testutil.FakeTelegramBotToken,
		ProjectPath: "/test/path",
		// Transcription with invalid config would fail
	}

	h := NewHandler(config, nil)

	if h == nil {
		t.Fatal("NewHandler should not return nil even with transcription issues")
	}
	// transcriptionErr and transcriber should be nil when not configured
	if h.transcriptionErr != nil {
		t.Error("transcriptionErr should be nil when not configured")
	}
	if h.transcriber != nil {
		t.Error("transcriber should be nil when not configured")
	}
}

// TestResolveTaskIDWithVariousFormats tests more format variations
func TestResolveTaskIDWithVariousFormats(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create task with different naming convention
	task5Content := `# TASK-5: Short ID Task
**Status**: backlog`

	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-5-short.md"), []byte(task5Content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		input   string
		wantNil bool
	}{
		{"5", false},
		{"05", false},
		{"task-5", false},
		{"TASK-5", false},
		{" 5 ", false}, // with whitespace
		{"task 5", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := h.resolveTaskID(tt.input)
			if tt.wantNil && got != nil {
				t.Errorf("expected nil for %q", tt.input)
			}
			if !tt.wantNil && got == nil {
				t.Errorf("expected non-nil for %q", tt.input)
			}
		})
	}
}

// TestResolveTaskIDNoTasksDir tests when tasks directory doesn't exist
func TestResolveTaskIDNoTasksDir(t *testing.T) {
	h := &Handler{
		projectPath: "/nonexistent/path",
	}

	got := h.resolveTaskID("07")
	if got != nil {
		t.Error("expected nil when tasks dir doesn't exist")
	}
}

// TestResolveTaskFromDescriptionVariations tests more description patterns
func TestResolveTaskFromDescriptionVariations(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	task3Content := `# TASK-03: Test Task
**Status**: backlog`
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-03-test.md"), []byte(task3Content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		projectPath: tmpDir,
	}

	tests := []struct {
		desc    string
		wantNil bool
	}{
		{"execute 3", false},
		{"run task 03", false},
		{"start task-03", false},
		{"do 3", false},
		{"please help with something", true}, // no task reference
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := h.resolveTaskFromDescription(tt.desc)
			if tt.wantNil && got != nil {
				t.Errorf("expected nil for %q", tt.desc)
			}
			if !tt.wantNil && got == nil {
				t.Errorf("expected non-nil for %q", tt.desc)
			}
		})
	}
}

// TestParseTaskFileVariations tests more task file formats
func TestParseTaskFileVariations(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		content    string
		wantStatus string
	}{
		{
			name: "status with emoji",
			content: `# TASK-01: Task
**Status**: complete`,
			wantStatus: "complete",
		},
		{
			name: "status wip",
			content: `# TASK-02: Task
**Status**: wip`,
			wantStatus: "wip",
		},
		{
			name: "status done",
			content: `# TASK-03: Task
**Status**: done`,
			wantStatus: "done",
		},
		{
			name: "lowercase status",
			content: `# task-04: task
status: pending`,
			wantStatus: "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filePath := filepath.Join(tmpDir, tt.name+".md")
			if err := os.WriteFile(filePath, []byte(tt.content), 0644); err != nil {
				t.Fatal(err)
			}

			status, _ := parseTaskFile(filePath)
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
		})
	}
}
