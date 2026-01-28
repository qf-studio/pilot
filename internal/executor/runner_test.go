package executor

import (
	"testing"
)

func TestNewRunner(t *testing.T) {
	runner := NewRunner()

	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
	if runner.running == nil {
		t.Error("running map not initialized")
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

	prompt := runner.BuildPrompt(task)

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

	prompt := runner.BuildPrompt(task)

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
			name:         "opus 1M input tokens",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "claude-opus-4-5",
			minCost:      14.9,
			maxCost:      15.1,
		},
		{
			name:         "opus 1M output tokens",
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
			name:         "case insensitive opus",
			inputTokens:  1000000,
			outputTokens: 0,
			model:        "Claude-OPUS-4-5",
			minCost:      14.9,
			maxCost:      15.1,
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

	prompt := runner.BuildPrompt(task)

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
