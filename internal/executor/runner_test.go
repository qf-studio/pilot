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
