package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewRunner(t *testing.T) {
	runner := NewRunner()

	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
	if runner.running == nil {
		t.Error("running map not initialized")
	}
	if runner.backend == nil {
		t.Error("backend not initialized")
	}
	if runner.backend.Name() != BackendTypeClaudeCode {
		t.Errorf("default backend = %q, want %q", runner.backend.Name(), BackendTypeClaudeCode)
	}
}

func TestNewRunnerWithBackend(t *testing.T) {
	backend := NewOpenCodeBackend(nil)
	runner := NewRunnerWithBackend(backend)

	if runner == nil {
		t.Fatal("NewRunnerWithBackend returned nil")
	}
	if runner.backend.Name() != BackendTypeOpenCode {
		t.Errorf("backend = %q, want %q", runner.backend.Name(), BackendTypeOpenCode)
	}
}

func TestNewRunnerWithBackendNil(t *testing.T) {
	runner := NewRunnerWithBackend(nil)

	if runner == nil {
		t.Fatal("NewRunnerWithBackend returned nil")
	}
	// Should default to Claude Code
	if runner.backend.Name() != BackendTypeClaudeCode {
		t.Errorf("backend = %q, want %q", runner.backend.Name(), BackendTypeClaudeCode)
	}
}

func TestNewRunnerWithConfig(t *testing.T) {
	config := &BackendConfig{
		Type: BackendTypeOpenCode,
		OpenCode: &OpenCodeConfig{
			ServerURL: "http://localhost:5000",
		},
	}

	runner, err := NewRunnerWithConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner == nil {
		t.Fatal("NewRunnerWithConfig returned nil")
	}
	if runner.backend.Name() != BackendTypeOpenCode {
		t.Errorf("backend = %q, want %q", runner.backend.Name(), BackendTypeOpenCode)
	}
}

func TestNewRunnerWithConfigInvalid(t *testing.T) {
	config := &BackendConfig{
		Type: "invalid-backend",
	}

	_, err := NewRunnerWithConfig(config)
	if err == nil {
		t.Error("expected error for invalid backend type")
	}
}

func TestRunnerSetBackend(t *testing.T) {
	runner := NewRunner()
	if runner.backend.Name() != BackendTypeClaudeCode {
		t.Errorf("initial backend = %q, want %q", runner.backend.Name(), BackendTypeClaudeCode)
	}

	opencode := NewOpenCodeBackend(nil)
	runner.SetBackend(opencode)

	if runner.backend.Name() != BackendTypeOpenCode {
		t.Errorf("backend after set = %q, want %q", runner.backend.Name(), BackendTypeOpenCode)
	}
}

func TestRunnerGetBackend(t *testing.T) {
	runner := NewRunner()
	backend := runner.GetBackend()

	if backend == nil {
		t.Fatal("GetBackend returned nil")
	}
	if backend.Name() != BackendTypeClaudeCode {
		t.Errorf("backend = %q, want %q", backend.Name(), BackendTypeClaudeCode)
	}
}

func TestBuildPrompt(t *testing.T) {
	runner := NewRunner()

	task := &Task{
		ID:          "TASK-123",
		Title:       "Add authentication",
		Description: "Implement user authentication flow",
		ProjectPath: "/path/to/project",
		Branch:      "pilot/TASK-123",
	}

	prompt := runner.BuildPrompt(task, task.ProjectPath)

	if prompt == "" {
		t.Error("buildPrompt returned empty string")
	}

	// Check that key elements are in the prompt
	tests := []string{
		"TASK-123",
		"Implement user authentication flow",
		"pilot/TASK-123",
		"Commit",
	}

	for _, expected := range tests {
		if !contains(prompt, expected) {
			t.Errorf("Prompt missing expected content: %s", expected)
		}
	}
}

func TestBuildPromptNoBranch(t *testing.T) {
	runner := NewRunner()

	task := &Task{
		ID:          "TASK-456",
		Description: "Fix a bug",
		ProjectPath: "/path/to/project",
		Branch:      "", // No branch
	}

	prompt := runner.BuildPrompt(task, task.ProjectPath)

	if !contains(prompt, "current branch") {
		t.Error("Prompt should mention current branch when Branch is empty")
	}
	if contains(prompt, "Create a new git branch") {
		t.Error("Prompt should not mention creating branch when Branch is empty")
	}
}

func TestIsRunning(t *testing.T) {
	runner := NewRunner()

	if runner.IsRunning("nonexistent") {
		t.Error("IsRunning returned true for nonexistent task")
	}
}

func TestParseStreamEvent(t *testing.T) {
	runner := NewRunner()

	// Track progress calls
	var progressCalls []struct {
		phase   string
		message string
	}
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		progressCalls = append(progressCalls, struct {
			phase   string
			message string
		}{phase, message})
	})

	tests := []struct {
		name          string
		json          string
		wantResult    string
		wantError     string
		wantProgress  bool
		expectedPhase string
	}{
		{
			name:          "system init",
			json:          `{"type":"system","subtype":"init","session_id":"abc"}`,
			wantProgress:  true,
			expectedPhase: "🚀 Started",
		},
		{
			name:          "tool use Write triggers Implementing phase",
			json:          `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/tmp/test.go"}}]}}`,
			wantProgress:  true,
			expectedPhase: "Implementing",
		},
		{
			name:          "tool use Read triggers Exploring phase",
			json:          `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}`,
			wantProgress:  true,
			expectedPhase: "Exploring",
		},
		{
			name:          "git commit triggers Committing phase",
			json:          `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git commit -m 'test'"}}]}}`,
			wantProgress:  true,
			expectedPhase: "Committing",
		},
		{
			name:       "result success",
			json:       `{"type":"result","subtype":"success","result":"Done!","is_error":false}`,
			wantResult: "Done!",
		},
		{
			name:      "result error",
			json:      `{"type":"result","subtype":"error","result":"Failed","is_error":true}`,
			wantError: "Failed",
		},
		{
			name:         "invalid json",
			json:         `not valid json`,
			wantProgress: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			progressCalls = nil
			state := &progressState{phase: "Starting"}

			result, errMsg := runner.parseStreamEvent("TASK-1", tt.json, state)

			if result != tt.wantResult {
				t.Errorf("result = %q, want %q", result, tt.wantResult)
			}
			if errMsg != tt.wantError {
				t.Errorf("error = %q, want %q", errMsg, tt.wantError)
			}
			if tt.wantProgress && len(progressCalls) == 0 {
				t.Error("expected progress call, got none")
			}
			if tt.expectedPhase != "" && len(progressCalls) > 0 {
				if progressCalls[0].phase != tt.expectedPhase {
					t.Errorf("phase = %q, want %q", progressCalls[0].phase, tt.expectedPhase)
				}
			}
		})
	}
}

func TestFormatToolMessage(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		want     string
	}{
		{
			name:     "Write tool",
			toolName: "Write",
			input:    map[string]interface{}{"file_path": "/path/to/file.go"},
			want:     "Writing file.go",
		},
		{
			name:     "Bash tool",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "go test ./..."},
			want:     "Running: go test ./...",
		},
		{
			name:     "Read tool",
			toolName: "Read",
			input:    map[string]interface{}{"file_path": "/src/main.go"},
			want:     "Reading main.go",
		},
		{
			name:     "Unknown tool",
			toolName: "CustomTool",
			input:    map[string]interface{}{},
			want:     "Using CustomTool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolMessage(tt.toolName, tt.input)
			if got != tt.want {
				t.Errorf("formatToolMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		text   string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"this is a longer text", 10, "this is..."},
		{"with\nnewlines", 20, "with newlines"},
	}

	for _, tt := range tests {
		got := truncateText(tt.text, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateText(%q, %d) = %q, want %q", tt.text, tt.maxLen, got, tt.want)
		}
	}
}

func TestNavigatorPatternParsing(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	var lastMessage string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
		lastMessage = message
	})

	tests := []struct {
		name          string
		text          string
		expectedPhase string
		expectedMsg   string
	}{
		{
			name:          "Navigator session started",
			text:          "Navigator Session Started\n━━━━━━━━━",
			expectedPhase: "Navigator",
			expectedMsg:   "Navigator session started",
		},
		{
			name:          "Phase transition IMPL",
			text:          "PHASE: RESEARCH → IMPL\n━━━━━━━━━",
			expectedPhase: "Implement",
			expectedMsg:   "Implementing changes...",
		},
		{
			name:          "Phase transition VERIFY",
			text:          "PHASE: IMPL → VERIFY\n━━━━━━━━━",
			expectedPhase: "Verify",
			expectedMsg:   "Verifying changes...",
		},
		{
			name:          "Loop complete",
			text:          "━━━━━━━━━\nLOOP COMPLETE\n━━━━━━━━━",
			expectedPhase: "Completing",
			expectedMsg:   "Task complete signal received",
		},
		{
			name:          "Exit signal",
			text:          "EXIT_SIGNAL: true",
			expectedPhase: "Finishing",
			expectedMsg:   "Exit signal detected",
		},
		{
			name:          "Task mode complete",
			text:          "TASK MODE COMPLETE\n━━━━━━━━━",
			expectedPhase: "Completing",
			expectedMsg:   "Task complete signal received",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastPhase = ""
			lastMessage = ""
			state := &progressState{phase: "Starting"}

			runner.parseNavigatorPatterns("TASK-1", tt.text, state)

			if lastPhase != tt.expectedPhase {
				t.Errorf("phase = %q, want %q", lastPhase, tt.expectedPhase)
			}
			if lastMessage != tt.expectedMsg {
				t.Errorf("message = %q, want %q", lastMessage, tt.expectedMsg)
			}
		})
	}
}

func TestNavigatorStatusBlockParsing(t *testing.T) {
	runner := NewRunner()
	state := &progressState{phase: "Starting"}

	statusBlock := `NAVIGATOR_STATUS
==================================================
Phase: IMPL
Iteration: 2/5
Progress: 45%

Completion Indicators:
  [x] Code changes committed
==================================================`

	runner.parseNavigatorStatusBlock("TASK-1", statusBlock, state)

	if state.navPhase != "IMPL" {
		t.Errorf("navPhase = %q, want IMPL", state.navPhase)
	}
	if state.navIteration != 2 {
		t.Errorf("navIteration = %d, want 2", state.navIteration)
	}
	if state.navProgress != 45 {
		t.Errorf("navProgress = %d, want 45", state.navProgress)
	}
}

func TestNavigatorSkillDetection(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	tests := []struct {
		skill         string
		expectedPhase string
	}{
		{"nav-start", "Navigator"},
		{"nav-loop", "Loop Mode"},
		{"nav-task", "Task Mode"},
		{"nav-compact", "Compacting"},
		{"nav-marker", "Checkpoint"},
	}

	for _, tt := range tests {
		t.Run(tt.skill, func(t *testing.T) {
			lastPhase = ""
			state := &progressState{phase: "Starting"}

			runner.handleToolUse("TASK-1", "Skill", map[string]interface{}{
				"skill": tt.skill,
			}, state)

			if lastPhase != tt.expectedPhase {
				t.Errorf("phase = %q, want %q", lastPhase, tt.expectedPhase)
			}
			if !state.hasNavigator {
				t.Error("hasNavigator should be true")
			}
		})
	}
}

func TestExtractCommitSHA(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "standard commit output",
			content:  "[main abc1234] feat: add feature",
			expected: []string{"abc1234"},
		},
		{
			name:     "branch with slash",
			content:  "[pilot/TASK-123 def5678] fix: bug fix",
			expected: []string{"def5678"},
		},
		{
			name:     "full SHA",
			content:  "[main abc1234567890abcdef1234567890abcdef12] commit msg",
			expected: []string{"abc1234567890abcdef1234567890abcdef12"},
		},
		{
			name:     "multiline with commit",
			content:  "Some output\n[feature/test 1234567] test commit\nMore output",
			expected: []string{"1234567"},
		},
		{
			name:     "no commit",
			content:  "Just some random output",
			expected: nil,
		},
		{
			name:     "invalid SHA format",
			content:  "[main not-a-sha] message",
			expected: nil,
		},
		{
			name:     "multiple commits",
			content:  "[main abc1234] first\n[main def5678] second",
			expected: []string{"abc1234", "def5678"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &progressState{}
			extractCommitSHA(tt.content, state)

			if len(tt.expected) == 0 && len(state.commitSHAs) > 0 {
				t.Errorf("expected no SHAs, got %v", state.commitSHAs)
			}
			if len(tt.expected) > 0 {
				if len(state.commitSHAs) != len(tt.expected) {
					t.Errorf("expected %d SHAs, got %d: %v", len(tt.expected), len(state.commitSHAs), state.commitSHAs)
				}
				for i, sha := range tt.expected {
					if i < len(state.commitSHAs) && state.commitSHAs[i] != sha {
						t.Errorf("SHA[%d] = %q, want %q", i, state.commitSHAs[i], sha)
					}
				}
			}
		})
	}
}

func TestIsValidSHA(t *testing.T) {
	tests := []struct {
		sha   string
		valid bool
	}{
		{"abc1234", true},
		{"ABC1234", true},
		{"1234567890abcdef1234567890abcdef12345678", true},
		{"abc123", false},  // too short
		{"not-sha", false}, // invalid chars
		{"", false},
		{"abc1234567890abcdef1234567890abcdef123456789", false}, // too long (41 chars)
	}

	for _, tt := range tests {
		t.Run(tt.sha, func(t *testing.T) {
			if got := isValidSHA(tt.sha); got != tt.valid {
				t.Errorf("isValidSHA(%q) = %v, want %v", tt.sha, got, tt.valid)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}

func TestTaskStruct(t *testing.T) {
	tests := []struct {
		name string
		task *Task
	}{
		{
			name: "full task",
			task: &Task{
				ID:          "TASK-123",
				Title:       "Add authentication",
				Description: "Implement OAuth2 flow",
				Priority:    1,
				ProjectPath: "/path/to/project",
				Branch:      "pilot/TASK-123",
				Verbose:     true,
				CreatePR:    true,
				BaseBranch:  "main",
				ImagePath:   "",
			},
		},
		{
			name: "minimal task",
			task: &Task{
				ID:          "T-1",
				Description: "Fix bug",
				ProjectPath: "/tmp/proj",
			},
		},
		{
			name: "image task",
			task: &Task{
				ID:          "IMG-1",
				Description: "Analyze screenshot",
				ProjectPath: "/tmp/proj",
				ImagePath:   "/tmp/screenshot.png",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.task.ID == "" {
				t.Error("Task ID should not be empty")
			}
			if tt.task.ProjectPath == "" {
				t.Error("ProjectPath should not be empty")
			}
		})
	}
}

func TestExecutionResultStruct(t *testing.T) {
	result := &ExecutionResult{
		TaskID:           "TASK-123",
		Success:          true,
		Output:           "Task completed successfully",
		Error:            "",
		Duration:         5000000000, // 5 seconds
		PRUrl:            "https://github.com/org/repo/pull/42",
		CommitSHA:        "abc1234",
		TokensInput:      1000,
		TokensOutput:     500,
		TokensTotal:      1500,
		EstimatedCostUSD: 0.015,
		FilesChanged:     3,
		LinesAdded:       100,
		LinesRemoved:     20,
		ModelName:        "claude-sonnet-4-5",
	}

	if result.TaskID != "TASK-123" {
		t.Errorf("TaskID = %q, want TASK-123", result.TaskID)
	}
	if !result.Success {
		t.Error("Success should be true")
	}
	if result.TokensTotal != 1500 {
		t.Errorf("TokensTotal = %d, want 1500", result.TokensTotal)
	}
	if result.CommitSHA != "abc1234" {
		t.Errorf("CommitSHA = %q, want abc1234", result.CommitSHA)
	}
}

func TestEstimateCost(t *testing.T) {
	tests := []struct {
		name         string
		inputTokens  int64
		outputTokens int64
		model        string
		minCost      float64
		maxCost      float64
	}{
		{
			name:         "sonnet zero tokens",
			inputTokens:  0,
			outputTokens: 0,
			model:        "claude-sonnet-4-5",
			minCost:      0,
			maxCost:      0,
		},
		{
			name:         "sonnet 1M input tokens",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "claude-sonnet-4-5",
			minCost:      2.9,
			maxCost:      3.1,
		},
		{
			name:         "sonnet 1M output tokens",
			inputTokens:  0,
			outputTokens: 1000000,
			model:        "claude-sonnet-4-5",
			minCost:      14.9,
			maxCost:      15.1,
		},
		{
			name:         "opus 4.6 1M input tokens",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "claude-opus-4-6",
			minCost:      4.9,
			maxCost:      5.1,
		},
		{
			name:         "opus 4.6 1M output tokens",
			inputTokens:  0,
			outputTokens: 1000000,
			model:        "claude-opus-4-6",
			minCost:      24.9,
			maxCost:      25.1,
		},
		{
			name:         "legacy opus 4.5 1M input tokens",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "claude-opus-4-5",
			minCost:      14.9,
			maxCost:      15.1,
		},
		{
			name:         "legacy opus 4.5 1M output tokens",
			inputTokens:  0,
			outputTokens: 1000000,
			model:        "claude-opus-4-5",
			minCost:      74.9,
			maxCost:      75.1,
		},
		{
			name:         "mixed usage sonnet",
			inputTokens:  100000,
			outputTokens: 50000,
			model:        "claude-sonnet-4-5",
			minCost:      1.0,
			maxCost:      1.1, // 0.3 + 0.75
		},
		{
			name:         "case insensitive opus uses 4.6 pricing",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "Claude-OPUS-4-6",
			minCost:      4.9,
			maxCost:      5.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := estimateCost(tt.inputTokens, tt.outputTokens, tt.model)
			if cost < tt.minCost || cost > tt.maxCost {
				t.Errorf("estimateCost() = %f, want between %f and %f", cost, tt.minCost, tt.maxCost)
			}
		})
	}
}

func TestMinFunction(t *testing.T) {
	tests := []struct {
		a, b     int
		expected int
	}{
		{1, 2, 1},
		{2, 1, 1},
		{0, 0, 0},
		{-1, 1, -1},
		{100, 50, 50},
		{-10, -20, -20},
	}

	for _, tt := range tests {
		result := min(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("min(%d, %d) = %d, want %d", tt.a, tt.b, result, tt.expected)
		}
	}
}

func TestBuildPromptImageTask(t *testing.T) {
	runner := NewRunner()

	task := &Task{
		ID:          "IMG-1",
		Description: "What is shown in this image?",
		ProjectPath: "/path/to/project",
		ImagePath:   "/path/to/screenshot.png",
	}

	prompt := runner.BuildPrompt(task, task.ProjectPath)

	if !contains(prompt, "/path/to/screenshot.png") {
		t.Error("Image task prompt should contain image path")
	}
	if !contains(prompt, "What is shown in this image?") {
		t.Error("Image task prompt should contain description")
	}
	if contains(prompt, "Navigator") {
		t.Error("Image task should not include Navigator workflow")
	}
}

func TestBuildPromptSkipsNavigatorForTrivialTasks(t *testing.T) {
	// Create a temp directory with .agent/ to simulate Navigator project
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("Failed to create .agent dir: %v", err)
	}

	runner := NewRunner()

	tests := []struct {
		name            string
		description     string
		expectNavigator bool
	}{
		{
			name:            "trivial task - fix typo",
			description:     "Fix typo in README",
			expectNavigator: false,
		},
		{
			name:            "trivial task - add logging",
			description:     "Add log statement to debug function",
			expectNavigator: false,
		},
		{
			name:            "trivial task - update comment",
			description:     "Update comment in handler.go",
			expectNavigator: false,
		},
		{
			name:            "trivial task - rename variable",
			description:     "Rename variable from foo to bar",
			expectNavigator: false,
		},
		{
			name:            "medium task - add feature",
			description:     "Add user authentication with JWT tokens and session management",
			expectNavigator: true,
		},
		{
			name:            "complex task - refactor",
			description:     "Refactor the authentication module to use OAuth2",
			expectNavigator: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &Task{
				ID:          "TEST-1",
				Title:       tc.name,
				Description: tc.description,
				ProjectPath: tmpDir,
				Branch:      "test-branch",
			}

			prompt := runner.BuildPrompt(task, task.ProjectPath)

			hasNavigator := contains(prompt, "## Autonomous Execution Workflow")
			hasTrivialMarker := contains(prompt, "trivial change")

			if tc.expectNavigator {
				if !hasNavigator {
					t.Errorf("Expected Navigator session for non-trivial task, got prompt: %s", prompt)
				}
				if hasTrivialMarker {
					t.Errorf("Non-trivial task should not have trivial marker")
				}
			} else {
				if hasNavigator {
					t.Errorf("Trivial task should skip Navigator, got prompt: %s", prompt)
				}
				if !hasTrivialMarker {
					t.Errorf("Trivial task should have trivial marker, got prompt: %s", prompt)
				}
			}
		})
	}
}

func TestProgressStateStruct(t *testing.T) {
	state := &progressState{
		phase:        "Implementing",
		filesRead:    5,
		filesWrite:   3,
		commands:     10,
		hasNavigator: true,
		navPhase:     "IMPL",
		navIteration: 2,
		navProgress:  45,
		exitSignal:   false,
		commitSHAs:   []string{"abc1234", "def5678"},
		tokensInput:  1000,
		tokensOutput: 500,
		modelName:    "claude-sonnet-4-5",
	}

	if state.phase != "Implementing" {
		t.Errorf("phase = %q, want Implementing", state.phase)
	}
	if len(state.commitSHAs) != 2 {
		t.Errorf("commitSHAs count = %d, want 2", len(state.commitSHAs))
	}
	if state.tokensInput+state.tokensOutput != 1500 {
		t.Error("Token sum calculation incorrect")
	}
}

func TestHandleToolUseGlob(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.handleToolUse("TASK-1", "Glob", map[string]interface{}{
		"pattern": "**/*.go",
	}, state)

	if state.filesRead != 1 {
		t.Errorf("filesRead = %d, want 1", state.filesRead)
	}
	if lastPhase != "Exploring" {
		t.Errorf("phase = %q, want Exploring", lastPhase)
	}
}

func TestHandleToolUseGrep(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.handleToolUse("TASK-1", "Grep", map[string]interface{}{
		"pattern": "func main",
	}, state)

	if state.filesRead != 1 {
		t.Errorf("filesRead = %d, want 1", state.filesRead)
	}
	if lastPhase != "Exploring" {
		t.Errorf("phase = %q, want Exploring", lastPhase)
	}
}

func TestHandleToolUseEdit(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	var lastMessage string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
		lastMessage = message
	})

	state := &progressState{phase: "Starting"}
	runner.handleToolUse("TASK-1", "Edit", map[string]interface{}{
		"file_path": "/path/to/file.go",
	}, state)

	if state.filesWrite != 1 {
		t.Errorf("filesWrite = %d, want 1", state.filesWrite)
	}
	if lastPhase != "Implementing" {
		t.Errorf("phase = %q, want Implementing", lastPhase)
	}
	if !contains(lastMessage, "file.go") {
		t.Errorf("message should mention file name, got %q", lastMessage)
	}
}

func TestHandleToolUseBashTests(t *testing.T) {
	tests := []struct {
		name          string
		command       string
		expectedPhase string
	}{
		{"pytest", "pytest tests/", "Testing"},
		{"jest", "npm run jest", "Testing"},
		{"go test", "go test ./...", "Testing"},
		{"npm test", "npm test", "Testing"},
		{"make test", "make test", "Testing"},
		{"npm install", "npm install", "Installing"},
		{"pip install", "pip install -r requirements.txt", "Installing"},
		{"go mod", "go mod tidy", "Installing"},
		{"git checkout", "git checkout -b feature", "Branching"},
		{"git branch", "git branch new-branch", "Branching"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewRunner()

			var lastPhase string
			runner.OnProgress(func(taskID, phase string, progress int, message string) {
				lastPhase = phase
			})

			state := &progressState{phase: "Starting"}
			runner.handleToolUse("TASK-1", "Bash", map[string]interface{}{
				"command": tt.command,
			}, state)

			if lastPhase != tt.expectedPhase {
				t.Errorf("phase = %q, want %q for command %q", lastPhase, tt.expectedPhase, tt.command)
			}
		})
	}
}

func TestHandleToolUseAgentWrite(t *testing.T) {
	runner := NewRunner()

	var progressCalls int
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		progressCalls++
	})

	state := &progressState{phase: "Starting"}

	// Writing to .agent directory should set hasNavigator
	runner.handleToolUse("TASK-1", "Write", map[string]interface{}{
		"file_path": "/project/.agent/tasks/TASK-1.md",
	}, state)

	if !state.hasNavigator {
		t.Error("hasNavigator should be true after writing to .agent/")
	}
}

func TestHandleToolUseContextMarker(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.handleToolUse("TASK-1", "Write", map[string]interface{}{
		"file_path": "/project/.agent/.context-markers/marker-123.md",
	}, state)

	if lastPhase != "Checkpoint" {
		t.Errorf("phase = %q, want Checkpoint", lastPhase)
	}
	if !state.hasNavigator {
		t.Error("hasNavigator should be true")
	}
}

func TestHandleToolUseTask(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	var lastMessage string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
		lastMessage = message
	})

	state := &progressState{phase: "Starting"}
	runner.handleToolUse("TASK-1", "Task", map[string]interface{}{
		"description": "Run unit tests and verify",
	}, state)

	if lastPhase != "Delegating" {
		t.Errorf("phase = %q, want Delegating", lastPhase)
	}
	if !contains(lastMessage, "Spawning") {
		t.Errorf("message should contain Spawning, got %q", lastMessage)
	}
}

func TestHandleNavigatorPhaseCases(t *testing.T) {
	tests := []struct {
		phase         string
		expectedPhase string
	}{
		{"INIT", "Init"},
		{"RESEARCH", "Research"},
		{"IMPL", "Implement"},
		{"IMPLEMENTATION", "Implement"},
		{"VERIFY", "Verify"},
		{"VERIFICATION", "Verify"},
		{"COMPLETE", "Complete"},
		{"COMPLETED", "Complete"},
		{"UNKNOWN_PHASE", "UNKNOWN_PHASE"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			runner := NewRunner()

			var lastPhase string
			runner.OnProgress(func(taskID, phase string, progress int, message string) {
				lastPhase = phase
			})

			state := &progressState{phase: "Starting", navPhase: ""}
			runner.handleNavigatorPhase("TASK-1", tt.phase, state)

			if lastPhase != tt.expectedPhase {
				t.Errorf("phase = %q, want %q", lastPhase, tt.expectedPhase)
			}
		})
	}
}

func TestHandleNavigatorPhaseSkipsSame(t *testing.T) {
	runner := NewRunner()

	var progressCalls int
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		progressCalls++
	})

	state := &progressState{phase: "Starting", navPhase: "IMPL"}

	// Calling with same phase should not trigger progress
	runner.handleNavigatorPhase("TASK-1", "IMPL", state)

	if progressCalls != 0 {
		t.Errorf("progressCalls = %d, want 0 when phase unchanged", progressCalls)
	}
}

func TestParseNavigatorPatternsWorkflowCheck(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.parseNavigatorPatterns("TASK-1", "WORKFLOW CHECK - analyzing task", state)

	if lastPhase != "Analyzing" {
		t.Errorf("phase = %q, want Analyzing", lastPhase)
	}
}

func TestParseNavigatorPatternsTaskModeActivated(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.parseNavigatorPatterns("TASK-1", "TASK MODE ACTIVATED\n━━━━━━━━━", state)

	if lastPhase != "Task Mode" {
		t.Errorf("phase = %q, want Task Mode", lastPhase)
	}
}

func TestParseNavigatorPatternsStagnation(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}
	runner.parseNavigatorPatterns("TASK-1", "STAGNATION DETECTED - retrying", state)

	if lastPhase != "⚠️ Stalled" {
		t.Errorf("phase = %q, want ⚠️ Stalled", lastPhase)
	}
}

func TestFormatToolMessageAdditional(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		input    map[string]interface{}
		want     string
	}{
		{
			name:     "Edit tool",
			toolName: "Edit",
			input:    map[string]interface{}{"file_path": "/src/main.go"},
			want:     "Editing main.go",
		},
		{
			name:     "Glob tool",
			toolName: "Glob",
			input:    map[string]interface{}{"pattern": "**/*.ts"},
			want:     "Searching: **/*.ts",
		},
		{
			name:     "Grep tool",
			toolName: "Grep",
			input:    map[string]interface{}{"pattern": "TODO"},
			want:     "Grep: TODO",
		},
		{
			name:     "Task tool",
			toolName: "Task",
			input:    map[string]interface{}{"description": "Run linter"},
			want:     "Spawning: Run linter",
		},
		{
			name:     "Bash long command",
			toolName: "Bash",
			input:    map[string]interface{}{"command": "this is a very long command that should be truncated"},
			want:     "Running: this is a very long command that shou...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolMessage(tt.toolName, tt.input)
			if got != tt.want {
				t.Errorf("formatToolMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseStreamEventUsageTracking(t *testing.T) {
	runner := NewRunner()
	state := &progressState{phase: "Starting"}

	// Event with usage info
	jsonEvent := `{"type":"assistant","message":{"content":[]},"usage":{"input_tokens":100,"output_tokens":50},"model":"claude-sonnet-4-5"}`

	runner.parseStreamEvent("TASK-1", jsonEvent, state)

	if state.tokensInput != 100 {
		t.Errorf("tokensInput = %d, want 100", state.tokensInput)
	}
	if state.tokensOutput != 50 {
		t.Errorf("tokensOutput = %d, want 50", state.tokensOutput)
	}
	if state.modelName != "claude-sonnet-4-5" {
		t.Errorf("modelName = %q, want claude-sonnet-4-5", state.modelName)
	}
}

func TestParseStreamEventEmptyJSON(t *testing.T) {
	runner := NewRunner()
	state := &progressState{phase: "Starting"}

	// Empty object should not panic
	result, errMsg := runner.parseStreamEvent("TASK-1", "{}", state)

	if result != "" {
		t.Errorf("result should be empty, got %q", result)
	}
	if errMsg != "" {
		t.Errorf("errMsg should be empty, got %q", errMsg)
	}
}

func TestRunnerSetRecordingsPath(t *testing.T) {
	runner := NewRunner()

	runner.SetRecordingsPath("/custom/recordings")

	if runner.recordingsPath != "/custom/recordings" {
		t.Errorf("recordingsPath = %q, want /custom/recordings", runner.recordingsPath)
	}
}

func TestRunnerSetRecordingEnabled(t *testing.T) {
	runner := NewRunner()

	// Default should be true
	if !runner.enableRecording {
		t.Error("enableRecording should default to true")
	}

	runner.SetRecordingEnabled(false)

	if runner.enableRecording {
		t.Error("enableRecording should be false after SetRecordingEnabled(false)")
	}
}

func TestStreamEventStructs(t *testing.T) {
	event := StreamEvent{
		Type:    "assistant",
		Subtype: "message",
		Message: &AssistantMsg{
			Content: []ContentBlock{
				{Type: "text", Text: "Hello"},
				{Type: "tool_use", Name: "Read", Input: map[string]interface{}{"file_path": "/test.go"}},
			},
		},
		Result:  "",
		IsError: false,
		Usage: &UsageInfo{
			InputTokens:  100,
			OutputTokens: 50,
		},
		Model: "claude-sonnet-4-5",
	}

	if event.Type != "assistant" {
		t.Errorf("Type = %q, want assistant", event.Type)
	}
	if len(event.Message.Content) != 2 {
		t.Errorf("Content length = %d, want 2", len(event.Message.Content))
	}
	if event.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", event.Usage.InputTokens)
	}
}

func TestToolResultContentStruct(t *testing.T) {
	result := ToolResultContent{
		ToolUseID: "tool-123",
		Type:      "tool_result",
		Content:   "[main abc1234] feat: add feature",
		IsError:   false,
	}

	if result.ToolUseID != "tool-123" {
		t.Errorf("ToolUseID = %q, want tool-123", result.ToolUseID)
	}
	if result.IsError {
		t.Error("IsError should be false")
	}
}

func TestProcessBackendEvent(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	var lastMessage string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
		lastMessage = message
	})

	tests := []struct {
		name          string
		event         BackendEvent
		expectedPhase string
		expectedMsg   string
	}{
		{
			name: "init event",
			event: BackendEvent{
				Type:    EventTypeInit,
				Message: "Backend initialized",
			},
			expectedPhase: "🚀 Started",
			expectedMsg:   "Backend initialized",
		},
		{
			name: "tool use Read",
			event: BackendEvent{
				Type:      EventTypeToolUse,
				ToolName:  "Read",
				ToolInput: map[string]interface{}{"file_path": "/test.go"},
			},
			expectedPhase: "Exploring",
		},
		{
			name: "tool use Write",
			event: BackendEvent{
				Type:      EventTypeToolUse,
				ToolName:  "Write",
				ToolInput: map[string]interface{}{"file_path": "/output.go"},
			},
			expectedPhase: "Implementing",
		},
		{
			name: "text with Navigator session",
			event: BackendEvent{
				Type:    EventTypeText,
				Message: "Navigator Session Started\n━━━━━━━━━",
			},
			expectedPhase: "Navigator",
		},
		{
			name: "text with EXIT_SIGNAL",
			event: BackendEvent{
				Type:    EventTypeText,
				Message: "EXIT_SIGNAL: true",
			},
			expectedPhase: "Finishing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastPhase = ""
			lastMessage = ""
			state := &progressState{phase: "Starting"}

			runner.processBackendEvent("TASK-1", tt.event, state)

			if tt.expectedPhase != "" && lastPhase != tt.expectedPhase {
				t.Errorf("phase = %q, want %q", lastPhase, tt.expectedPhase)
			}
			if tt.expectedMsg != "" && lastMessage != tt.expectedMsg {
				t.Errorf("message = %q, want %q", lastMessage, tt.expectedMsg)
			}
		})
	}
}

func TestProcessBackendEventTokenTracking(t *testing.T) {
	runner := NewRunner()
	state := &progressState{}

	// Process multiple events with token usage
	events := []BackendEvent{
		{Type: EventTypeText, TokensInput: 100, TokensOutput: 50},
		{Type: EventTypeText, TokensInput: 200, TokensOutput: 100},
		{Type: EventTypeResult, TokensInput: 50, TokensOutput: 25, Model: "claude-sonnet-4-5"},
	}

	for _, event := range events {
		runner.processBackendEvent("TASK-1", event, state)
	}

	expectedInput := int64(350)
	expectedOutput := int64(175)

	if state.tokensInput != expectedInput {
		t.Errorf("tokensInput = %d, want %d", state.tokensInput, expectedInput)
	}
	if state.tokensOutput != expectedOutput {
		t.Errorf("tokensOutput = %d, want %d", state.tokensOutput, expectedOutput)
	}
	if state.modelName != "claude-sonnet-4-5" {
		t.Errorf("modelName = %q, want claude-sonnet-4-5", state.modelName)
	}
}

func TestProcessBackendEventToolResult(t *testing.T) {
	runner := NewRunner()
	state := &progressState{}

	// Tool result with commit SHA
	event := BackendEvent{
		Type:       EventTypeToolResult,
		ToolResult: "[main abc1234] feat: add feature",
	}

	runner.processBackendEvent("TASK-1", event, state)

	if len(state.commitSHAs) != 1 {
		t.Fatalf("commitSHAs length = %d, want 1", len(state.commitSHAs))
	}
	if state.commitSHAs[0] != "abc1234" {
		t.Errorf("commitSHA = %q, want abc1234", state.commitSHAs[0])
	}
}

func TestProcessBackendEventProgressPhase(t *testing.T) {
	runner := NewRunner()

	var lastPhase string
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		lastPhase = phase
	})

	state := &progressState{phase: "Starting"}

	// Progress event with phase
	event := BackendEvent{
		Type:  EventTypeProgress,
		Phase: "IMPL",
	}

	runner.processBackendEvent("TASK-1", event, state)

	if lastPhase != "Implement" {
		t.Errorf("phase = %q, want Implement", lastPhase)
	}
}

func TestExtractTaskNumber(t *testing.T) {
	tests := []struct {
		taskID   string
		expected string
	}{
		{"GH-57", "57"},
		{"GH-123", "123"},
		{"TASK-42", "42"},
		{"TASK-999", "999"},
		{"OTHER-1", "OTHER-1"},
		{"57", "57"},
	}

	for _, tt := range tests {
		t.Run(tt.taskID, func(t *testing.T) {
			result := extractTaskNumber(tt.taskID)
			if result != tt.expected {
				t.Errorf("extractTaskNumber(%q) = %q, want %q", tt.taskID, result, tt.expected)
			}
		})
	}
}

func TestContainsTaskNumber(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		taskNum  string
		expected bool
	}{
		{"pipe spaced", "| 57 | Title | Status |", "57", true},
		{"no space", "|57 | Title |", "57", true},
		{"GH format", "| GH-57 | Title |", "57", true},
		{"TASK format", "| TASK-57 | Title |", "57", true},
		{"different number", "| 58 | Title |", "57", false},
		{"partial match", "| 157 | Title |", "57", false},
		{"in text", "Task GH-57 is done", "57", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsTaskNumber(tt.line, tt.taskNum)
			if result != tt.expected {
				t.Errorf("containsTaskNumber(%q, %q) = %v, want %v", tt.line, tt.taskNum, result, tt.expected)
			}
		})
	}
}

func TestSyncNavigatorIndex(t *testing.T) {
	runner := NewRunner()

	// Create temp directory with Navigator structure
	tmpDir := t.TempDir()
	agentDir := tmpDir + "/.agent"
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("failed to create .agent dir: %v", err)
	}

	// Create sample DEVELOPMENT-README.md
	indexContent := `# Development Navigator

## Active Work

### In Progress

| GH# | Title | Status |
|-----|-------|--------|
| 57 | Navigator Index Auto-Sync | 🔄 Pilot executing |
| 58 | Other Task | 🔄 Pilot executing |

### Backlog

| Priority | Topic |
|----------|-------|
| P1 | Future work |

## Completed (2026-01-28)

| Item | What |
|------|------|
| GH-52 | Previous task |
`

	indexPath := agentDir + "/DEVELOPMENT-README.md"
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index: %v", err)
	}

	// Test sync
	task := &Task{
		ID:          "GH-57",
		Title:       "Navigator Index Auto-Sync",
		ProjectPath: tmpDir,
	}

	err := runner.syncNavigatorIndex(task, "completed", task.ProjectPath)
	if err != nil {
		t.Fatalf("syncNavigatorIndex failed: %v", err)
	}

	// Read updated index
	updatedContent, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("failed to read updated index: %v", err)
	}

	contentStr := string(updatedContent)

	// Task should be removed from In Progress
	if strings.Contains(contentStr, "| 57 | Navigator Index Auto-Sync | 🔄 Pilot executing |") {
		t.Error("Task should be removed from In Progress section")
	}

	// Task should be in Completed section
	if !strings.Contains(contentStr, "| GH-57 | Navigator Index Auto-Sync |") {
		t.Error("Task should be added to Completed section")
	}

	// Other task should still be in progress
	if !strings.Contains(contentStr, "| 58 | Other Task | 🔄 Pilot executing |") {
		t.Error("Other tasks should remain in In Progress")
	}
}

func TestSyncNavigatorIndexNoIndex(t *testing.T) {
	runner := NewRunner()

	// Test with non-existent index
	task := &Task{
		ID:          "GH-99",
		ProjectPath: t.TempDir(),
	}

	err := runner.syncNavigatorIndex(task, "completed", task.ProjectPath)
	if err != nil {
		t.Errorf("syncNavigatorIndex should not error for missing index: %v", err)
	}
}

func TestSyncNavigatorIndexTaskNotInProgress(t *testing.T) {
	runner := NewRunner()

	// Create temp directory with Navigator structure
	tmpDir := t.TempDir()
	agentDir := tmpDir + "/.agent"
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("failed to create .agent dir: %v", err)
	}

	// Create index without our task
	indexContent := `# Development Navigator

### In Progress

| GH# | Title | Status |
|-----|-------|--------|
| 58 | Other Task | 🔄 Pilot executing |

## Completed

| Item | What |
|------|------|
`

	indexPath := agentDir + "/DEVELOPMENT-README.md"
	if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
		t.Fatalf("failed to write index: %v", err)
	}

	// Test sync for task not in progress
	task := &Task{
		ID:          "GH-99",
		ProjectPath: tmpDir,
	}

	err := runner.syncNavigatorIndex(task, "completed", task.ProjectPath)
	if err != nil {
		t.Fatalf("syncNavigatorIndex failed: %v", err)
	}

	// Index should be unchanged
	updatedContent, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("failed to read updated index: %v", err)
	}

	if !strings.Contains(string(updatedContent), "| 58 | Other Task | 🔄 Pilot executing |") {
		t.Error("Index should remain unchanged when task not found")
	}
}

// TestAddProgressCallback verifies that multiple named callbacks can be registered
// and all receive progress updates (GH-149 fix)
func TestAddProgressCallback(t *testing.T) {
	runner := NewRunner()

	var callback1Received, callback2Received, legacyReceived bool
	var callback1TaskID, callback2TaskID, legacyTaskID string

	// Register legacy callback (simulates Telegram handler)
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		legacyReceived = true
		legacyTaskID = taskID
	})

	// Register named callbacks (simulates dashboard)
	runner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
		callback1Received = true
		callback1TaskID = taskID
	})

	runner.AddProgressCallback("monitor", func(taskID, phase string, progress int, message string) {
		callback2Received = true
		callback2TaskID = taskID
	})

	// Emit progress
	runner.EmitProgress("TASK-TEST", "Testing", 50, "Test message")

	// All callbacks should receive the progress
	if !legacyReceived {
		t.Error("Legacy OnProgress callback should be called")
	}
	if !callback1Received {
		t.Error("Named callback 'dashboard' should be called")
	}
	if !callback2Received {
		t.Error("Named callback 'monitor' should be called")
	}

	// Task IDs should match
	if legacyTaskID != "TASK-TEST" {
		t.Errorf("Legacy taskID = %q, want TASK-TEST", legacyTaskID)
	}
	if callback1TaskID != "TASK-TEST" {
		t.Errorf("Dashboard taskID = %q, want TASK-TEST", callback1TaskID)
	}
	if callback2TaskID != "TASK-TEST" {
		t.Errorf("Monitor taskID = %q, want TASK-TEST", callback2TaskID)
	}
}

// TestRemoveProgressCallback verifies that named callbacks can be removed
func TestRemoveProgressCallback(t *testing.T) {
	runner := NewRunner()

	var received bool

	runner.AddProgressCallback("test", func(taskID, phase string, progress int, message string) {
		received = true
	})

	// Remove the callback
	runner.RemoveProgressCallback("test")

	// Emit progress - callback should NOT be called
	runner.EmitProgress("TASK-TEST", "Testing", 50, "Test message")

	if received {
		t.Error("Removed callback should not be called")
	}
}

// TestProgressCallbackIsolation verifies that OnProgress(nil) doesn't affect named callbacks
// This is the core fix for GH-149
func TestProgressCallbackIsolation(t *testing.T) {
	runner := NewRunner()

	var namedReceived bool

	// Register named callback (simulates dashboard)
	runner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
		namedReceived = true
	})

	// Set and clear legacy callback (simulates Telegram handler behavior)
	runner.OnProgress(func(taskID, phase string, progress int, message string) {})
	runner.OnProgress(nil) // This is what Telegram handler does after execution

	// Emit progress - named callback should still work
	runner.EmitProgress("TASK-TEST", "Testing", 50, "Test message")

	if !namedReceived {
		t.Error("Named callback should still be called after OnProgress(nil)")
	}
}

// TestSuppressProgressLogs verifies that slog output can be suppressed
// This is the fix for GH-152: show visual progress instead of log spam
func TestSuppressProgressLogs(t *testing.T) {
	runner := NewRunner()

	var callbackReceived bool
	runner.OnProgress(func(taskID, phase string, progress int, message string) {
		callbackReceived = true
	})

	// Suppress logs (simulates visual progress mode)
	runner.SuppressProgressLogs(true)

	// Emit progress - callback should still be called even when logs suppressed
	runner.EmitProgress("TASK-TEST", "Testing", 50, "Test message")

	if !callbackReceived {
		t.Error("Callback should be called even when progress logs are suppressed")
	}

	// Verify the flag is set correctly
	if !runner.suppressProgressLogs {
		t.Error("suppressProgressLogs should be true after SuppressProgressLogs(true)")
	}

	// Reset suppression
	runner.SuppressProgressLogs(false)
	if runner.suppressProgressLogs {
		t.Error("suppressProgressLogs should be false after SuppressProgressLogs(false)")
	}
}

// Test decomposition wiring in runner (GH-218)
func TestRunner_SetDecomposer(t *testing.T) {
	runner := NewRunner()

	// Initially no decomposer
	if runner.decomposer != nil {
		t.Error("Expected decomposer to be nil initially")
	}

	// Set decomposer
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 50,
	}
	decomposer := NewTaskDecomposer(config)
	runner.SetDecomposer(decomposer)

	if runner.decomposer == nil {
		t.Error("Expected decomposer to be set")
	}
	if runner.decomposer != decomposer {
		t.Error("Expected decomposer to be the one we set")
	}
}

func TestRunner_EnableDecomposition(t *testing.T) {
	runner := NewRunner()

	// Enable with nil config - should use defaults with enabled=true
	runner.EnableDecomposition(nil)

	if runner.decomposer == nil {
		t.Error("Expected decomposer to be created")
	}

	// Enable with custom config
	runner2 := NewRunner()
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "medium",
		MaxSubtasks:         3,
		MinDescriptionWords: 20,
	}
	runner2.EnableDecomposition(config)

	if runner2.decomposer == nil {
		t.Error("Expected decomposer to be created with custom config")
	}
}

func TestNewRunnerWithConfig_Decompose(t *testing.T) {
	// Test that NewRunnerWithConfig wires decomposer from config
	config := &BackendConfig{
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			Command: "claude",
		},
		Decompose: &DecomposeConfig{
			Enabled:             true,
			MinComplexity:       "complex",
			MaxSubtasks:         5,
			MinDescriptionWords: 50,
		},
	}

	runner, err := NewRunnerWithConfig(config)
	if err != nil {
		t.Fatalf("NewRunnerWithConfig failed: %v", err)
	}

	if runner.decomposer == nil {
		t.Error("Expected decomposer to be wired from config")
	}
}

func TestNewRunnerWithConfig_DecomposeDisabled(t *testing.T) {
	// Test that disabled decompose config doesn't create decomposer
	config := &BackendConfig{
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			Command: "claude",
		},
		Decompose: &DecomposeConfig{
			Enabled: false, // Disabled
		},
	}

	runner, err := NewRunnerWithConfig(config)
	if err != nil {
		t.Fatalf("NewRunnerWithConfig failed: %v", err)
	}

	if runner.decomposer != nil {
		t.Error("Expected decomposer to be nil when disabled in config")
	}
}

// Test self-review prompt generation (GH-364)
func TestBuildSelfReviewPrompt(t *testing.T) {
	runner := NewRunner()

	task := &Task{
		ID:          "TEST-001",
		Title:       "Test task",
		Description: "Test description",
		ProjectPath: "/tmp/test",
	}

	prompt := runner.buildSelfReviewPrompt(task)

	// Verify key elements
	if !strings.Contains(prompt, "Self-Review Phase") {
		t.Error("Prompt should contain Self-Review Phase header")
	}
	if !strings.Contains(prompt, "git diff") {
		t.Error("Prompt should include diff analysis")
	}
	if !strings.Contains(prompt, "go build") {
		t.Error("Prompt should include build verification")
	}
	if !strings.Contains(prompt, "REVIEW_PASSED") {
		t.Error("Prompt should include success signal")
	}
	if !strings.Contains(prompt, "REVIEW_FIXED") {
		t.Error("Prompt should include fixed signal")
	}
	if !strings.Contains(prompt, "Wiring Check") {
		t.Error("Prompt should include wiring check")
	}
	if !strings.Contains(prompt, "Method Existence Check") {
		t.Error("Prompt should include method existence check")
	}
}

// Test self-review config option (GH-364)
func TestBackendConfig_SkipSelfReview(t *testing.T) {
	// Default config should have SkipSelfReview as false
	config := DefaultBackendConfig()
	if config.SkipSelfReview {
		t.Error("Default config should not skip self-review")
	}

	// Test with skip enabled
	config.SkipSelfReview = true
	if !config.SkipSelfReview {
		t.Error("SkipSelfReview should be true when set")
	}
}

// Test that self-review is skipped for trivial tasks (GH-364)
func TestSelfReviewSkipsTrivialTasks(t *testing.T) {
	// Trivial task should be skipped
	trivialTask := &Task{
		ID:          "TRIV-001",
		Title:       "Fix typo in README",
		Description: "Fix typo in README",
		ProjectPath: "/tmp/test",
	}

	complexity := DetectComplexity(trivialTask)
	if !complexity.ShouldSkipNavigator() {
		t.Error("Expected trivial task to be detected as trivial")
	}

	// The self-review should skip trivial tasks without needing backend execution
	// We can't fully test runSelfReview without mocking, but we verify the complexity check
	if complexity != ComplexityTrivial {
		t.Errorf("Expected complexity to be trivial, got %s", complexity)
	}

	// Non-trivial task should NOT be skipped
	mediumTask := &Task{
		ID:          "MED-001",
		Title:       "Add user authentication with JWT tokens",
		Description: "Implement user authentication flow with JWT tokens and session management",
		ProjectPath: "/tmp/test",
	}

	mediumComplexity := DetectComplexity(mediumTask)
	if mediumComplexity.ShouldSkipNavigator() {
		t.Error("Expected medium complexity task to NOT skip Navigator/self-review")
	}
}

// Test runner with SkipSelfReview config (GH-364)
func TestNewRunnerWithConfig_SkipSelfReview(t *testing.T) {
	config := &BackendConfig{
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			Command: "claude",
		},
		SkipSelfReview: true,
	}

	runner, err := NewRunnerWithConfig(config)
	if err != nil {
		t.Fatalf("NewRunnerWithConfig failed: %v", err)
	}

	if runner.config == nil {
		t.Fatal("Expected runner.config to be set")
	}
	if !runner.config.SkipSelfReview {
		t.Error("Expected SkipSelfReview to be true in runner config")
	}
}

// Test ExtractRepoName function (GH-386)
func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		repo     string
		expected string
	}{
		{"alekspetrov/pilot", "pilot"},
		{"org/my-repo", "my-repo"},
		{"company/complex.repo.name", "complex.repo.name"},
		{"pilot", "pilot"}, // Already just repo name
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.repo, func(t *testing.T) {
			result := ExtractRepoName(tt.repo)
			if result != tt.expected {
				t.Errorf("ExtractRepoName(%q) = %q, want %q", tt.repo, result, tt.expected)
			}
		})
	}
}

// Test ValidateRepoProjectMatch function (GH-386)
func TestValidateRepoProjectMatch(t *testing.T) {
	tests := []struct {
		name        string
		sourceRepo  string
		projectPath string
		wantErr     bool
	}{
		{
			name:        "matching repo and project",
			sourceRepo:  "alekspetrov/pilot",
			projectPath: "/Users/test/Projects/pilot",
			wantErr:     false,
		},
		{
			name:        "matching with different case",
			sourceRepo:  "alekspetrov/Pilot",
			projectPath: "/Users/test/Projects/pilot",
			wantErr:     false,
		},
		{
			name:        "mismatched repo and project",
			sourceRepo:  "alekspetrov/pilot",
			projectPath: "/Users/test/Projects/bostonteamgroup",
			wantErr:     true,
		},
		{
			name:        "empty source repo",
			sourceRepo:  "",
			projectPath: "/Users/test/Projects/pilot",
			wantErr:     false, // No validation needed
		},
		{
			name:        "empty project path",
			sourceRepo:  "alekspetrov/pilot",
			projectPath: "",
			wantErr:     false, // No validation needed
		},
		{
			name:        "both empty",
			sourceRepo:  "",
			projectPath: "",
			wantErr:     false,
		},
		{
			name:        "similar but not matching",
			sourceRepo:  "org/pilot-dev",
			projectPath: "/Projects/pilot",
			wantErr:     true,
		},
		{
			name:        "repo name with special chars",
			sourceRepo:  "org/my-awesome-project",
			projectPath: "/home/user/my-awesome-project",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRepoProjectMatch(tt.sourceRepo, tt.projectPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRepoProjectMatch(%q, %q) error = %v, wantErr %v",
					tt.sourceRepo, tt.projectPath, err, tt.wantErr)
			}
		})
	}
}

// Test that Task struct has SourceRepo field (GH-386)
func TestTaskStructSourceRepo(t *testing.T) {
	task := &Task{
		ID:          "GH-386",
		Title:       "Cross-project defense",
		Description: "Prevent cross-project execution",
		ProjectPath: "/Users/test/Projects/pilot",
		Branch:      "pilot/GH-386",
		CreatePR:    true,
		SourceRepo:  "alekspetrov/pilot",
	}

	if task.SourceRepo != "alekspetrov/pilot" {
		t.Errorf("SourceRepo = %q, want alekspetrov/pilot", task.SourceRepo)
	}
}

// GH-539: Test per-task budget limit enforcement in processBackendEvent
func TestProcessBackendEvent_TokenLimitExceeded(t *testing.T) {
	runner := NewRunner()

	cancelCalled := false
	state := &progressState{
		phase:        "Starting",
		budgetCancel: func() { cancelCalled = true },
	}

	// Set a token limit callback that triggers on > 1000 total tokens
	var totalTokens int64
	runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
		totalTokens += deltaInput + deltaOutput
		return totalTokens <= 1000
	})

	// First event: 500 tokens — should be allowed
	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  300,
		TokensOutput: 200,
	}, state)

	if state.budgetExceeded {
		t.Error("budget should not be exceeded after 500 tokens")
	}
	if cancelCalled {
		t.Error("cancel should not be called yet")
	}

	// Second event: 600 more tokens — total 1100, should exceed
	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  400,
		TokensOutput: 200,
	}, state)

	if !state.budgetExceeded {
		t.Error("budget should be exceeded after 1100 tokens")
	}
	if !cancelCalled {
		t.Error("cancel function should have been called")
	}
	if state.budgetReason == "" {
		t.Error("budget reason should be set")
	}
}

func TestProcessBackendEvent_NoTokenLimitCallback(t *testing.T) {
	runner := NewRunner()
	// No tokenLimitCheck set — budget enforcement disabled

	state := &progressState{phase: "Starting"}

	// Send a large number of tokens — should not trigger any budget breach
	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  1000000,
		TokensOutput: 500000,
	}, state)

	if state.budgetExceeded {
		t.Error("budget should not be exceeded when no callback is set")
	}
}

func TestProcessBackendEvent_BudgetExceededSkipsFurtherChecks(t *testing.T) {
	runner := NewRunner()

	callCount := 0
	runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
		callCount++
		return false // Always exceeds
	})

	state := &progressState{
		phase:        "Starting",
		budgetCancel: func() {},
	}

	// First event triggers budget exceeded
	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  100,
		TokensOutput: 50,
	}, state)

	if !state.budgetExceeded {
		t.Error("budget should be exceeded")
	}
	if callCount != 1 {
		t.Errorf("expected 1 callback call, got %d", callCount)
	}

	// Second event should NOT trigger callback again (already exceeded)
	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  100,
		TokensOutput: 50,
	}, state)

	if callCount != 1 {
		t.Errorf("expected callback not called again after exceeded, got %d calls", callCount)
	}
}

func TestProcessBackendEvent_TokensStillTrackedWithBudget(t *testing.T) {
	runner := NewRunner()

	runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
		return true // Always allow
	})

	state := &progressState{
		phase:        "Starting",
		budgetCancel: func() {},
	}

	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  300,
		TokensOutput: 200,
	}, state)

	runner.processBackendEvent("TASK-1", BackendEvent{
		Type:         EventTypeText,
		TokensInput:  400,
		TokensOutput: 100,
	}, state)

	if state.tokensInput != 700 {
		t.Errorf("expected 700 input tokens, got %d", state.tokensInput)
	}
	if state.tokensOutput != 300 {
		t.Errorf("expected 300 output tokens, got %d", state.tokensOutput)
	}
}

func TestSetTokenLimitCheck(t *testing.T) {
	runner := NewRunner()

	if runner.tokenLimitCheck != nil {
		t.Error("expected nil tokenLimitCheck by default")
	}

	runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
		return true
	})

	if runner.tokenLimitCheck == nil {
		t.Error("expected non-nil tokenLimitCheck after setting")
	}
}

// Test mismatch error message format (GH-386)
func TestValidateRepoProjectMatchErrorMessage(t *testing.T) {
	err := ValidateRepoProjectMatch("alekspetrov/pilot", "/Projects/wrong-project")
	if err == nil {
		t.Fatal("Expected error for mismatched repo/project")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "alekspetrov/pilot") {
		t.Error("Error message should contain source repo")
	}
	if !strings.Contains(errMsg, "wrong-project") {
		t.Error("Error message should contain project path")
	}
	if !strings.Contains(errMsg, "pilot") {
		t.Error("Error message should contain expected project name")
	}
}

// =============================================================================
// TeamChecker Permission Tests (GH-634)
// =============================================================================

// mockTeamChecker implements TeamChecker for testing
type mockTeamChecker struct {
	permErr    error  // Error to return from CheckPermission
	accessErr  error  // Error to return from CheckProjectAccess
	lastPerm   string // Last permission checked
	lastMember string // Last member ID checked
}

func (m *mockTeamChecker) CheckPermission(memberID string, perm string) error {
	m.lastMember = memberID
	m.lastPerm = perm
	return m.permErr
}

func (m *mockTeamChecker) CheckProjectAccess(memberID, projectPath string, requiredPerm string) error {
	m.lastMember = memberID
	m.lastPerm = requiredPerm
	return m.accessErr
}

func TestRunner_SetTeamChecker(t *testing.T) {
	runner := NewRunner()
	if runner.teamChecker != nil {
		t.Error("teamChecker should be nil by default")
	}

	checker := &mockTeamChecker{}
	runner.SetTeamChecker(checker)

	if runner.teamChecker == nil {
		t.Error("teamChecker should be set after SetTeamChecker")
	}
}

func TestRunner_Execute_NoTeamChecker_Allowed(t *testing.T) {
	// Without TeamChecker, execution should proceed past permission check.
	// It will fail later (no project dir, etc.) but NOT on permission.
	runner := NewRunner()

	task := &Task{
		ID:          "test-1",
		Title:       "Test task",
		Description: "Test",
		ProjectPath: "/tmp/test-no-checker",
		MemberID:    "member-123", // MemberID set but no checker
	}

	_, err := runner.Execute(t.Context(), task)
	if err != nil && strings.Contains(err.Error(), "permission") {
		t.Errorf("should not fail on permission without TeamChecker: %v", err)
	}
}

func TestRunner_Execute_TeamChecker_Denied(t *testing.T) {
	runner := NewRunner()
	checker := &mockTeamChecker{
		accessErr: os.ErrPermission,
	}
	runner.SetTeamChecker(checker)

	task := &Task{
		ID:          "test-denied",
		Title:       "Test task",
		Description: "Test",
		ProjectPath: "/tmp/test",
		MemberID:    "viewer-member",
	}

	result, err := runner.Execute(t.Context(), task)

	// Should return permission error
	if err == nil {
		t.Fatal("expected error for denied permission")
	}
	if !strings.Contains(err.Error(), "permission check failed") {
		t.Errorf("error should mention permission check: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result even on permission failure")
	}
	if result.Success {
		t.Error("result should not be successful on permission denial")
	}
	if !strings.Contains(result.Error, "permission denied") {
		t.Errorf("result error should mention permission denied: %s", result.Error)
	}

	// Verify the checker was called with correct args
	if checker.lastMember != "viewer-member" {
		t.Errorf("checker got memberID %q, want %q", checker.lastMember, "viewer-member")
	}
	if checker.lastPerm != "execute_tasks" {
		t.Errorf("checker got perm %q, want %q", checker.lastPerm, "execute_tasks")
	}
}

func TestRunner_Execute_TeamChecker_NoMemberID_Skipped(t *testing.T) {
	// When MemberID is empty, permission check should be skipped
	runner := NewRunner()
	checker := &mockTeamChecker{
		accessErr: os.ErrPermission, // Would fail if called
	}
	runner.SetTeamChecker(checker)

	task := &Task{
		ID:          "test-no-member",
		Title:       "Test task",
		Description: "Test",
		ProjectPath: "/tmp/test",
		MemberID:    "", // Empty MemberID
	}

	// Should NOT fail on permission (checker should not be called)
	_, err := runner.Execute(t.Context(), task)
	if err != nil && strings.Contains(err.Error(), "permission") {
		t.Errorf("should skip permission check when MemberID is empty: %v", err)
	}

	// Verify checker was NOT called
	if checker.lastMember != "" {
		t.Errorf("checker should not have been called, but got memberID %q", checker.lastMember)
	}
}

func TestTask_MemberID_Field(t *testing.T) {
	task := &Task{
		ID:       "test-1",
		MemberID: "member-abc",
	}

	if task.MemberID != "member-abc" {
		t.Errorf("got MemberID %q, want %q", task.MemberID, "member-abc")
	}
}

// =============================================================================
// CancelAll Tests (GH-883)
// =============================================================================

func TestRunner_CancelAll_Empty(t *testing.T) {
	runner := NewRunner()

	// CancelAll on empty running map should not panic
	runner.CancelAll()

	// Verify no tasks are running
	if len(runner.running) != 0 {
		t.Errorf("expected empty running map, got %d entries", len(runner.running))
	}
}

func TestRunner_CancelAll_WithProcesses(t *testing.T) {
	runner := NewRunner()

	// Create a long-running process (sleep)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start test process: %v", err)
	}

	// Add it to the running map
	runner.mu.Lock()
	runner.running["test-task-1"] = cmd
	runner.mu.Unlock()

	// Verify process is running
	if cmd.Process == nil {
		t.Fatal("expected process to be started")
	}

	// CancelAll should signal the process
	runner.CancelAll()

	// Wait a bit for SIGTERM to take effect
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// Process should have been terminated
		if err == nil {
			t.Error("expected process to be killed with error")
		}
	case <-time.After(2 * time.Second):
		// Force kill if still running (shouldn't happen)
		_ = cmd.Process.Kill()
		t.Error("process did not terminate after SIGTERM within timeout")
	}
}

func TestRunner_CancelAll_MultipleTasks(t *testing.T) {
	runner := NewRunner()

	// Start multiple sleep processes
	var cmds []*exec.Cmd
	for i := 0; i < 3; i++ {
		cmd := exec.Command("sleep", "60")
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start test process %d: %v", i, err)
		}
		cmds = append(cmds, cmd)

		runner.mu.Lock()
		runner.running[fmt.Sprintf("test-task-%d", i)] = cmd
		runner.mu.Unlock()
	}

	// Verify all are in the map
	runner.mu.Lock()
	count := len(runner.running)
	runner.mu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 running tasks, got %d", count)
	}

	// CancelAll should signal all processes
	runner.CancelAll()

	// Wait for all processes to terminate
	for i, cmd := range cmds {
		done := make(chan error, 1)
		go func(c *exec.Cmd) {
			done <- c.Wait()
		}(cmd)

		select {
		case err := <-done:
			if err == nil {
				t.Errorf("expected process %d to be killed with error", i)
			}
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("process %d did not terminate after SIGTERM within timeout", i)
		}
	}
}
