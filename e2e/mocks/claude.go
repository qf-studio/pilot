// Package mocks provides mock implementations for E2E testing.
package mocks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ClaudeCodeMock provides a mock Claude Code executable for E2E testing.
// It creates a temporary script that simulates Claude Code behavior.
type ClaudeCodeMock struct {
	binPath     string
	tmpDir      string
	Response    string
	ShouldFail  bool
	SimulateGit bool   // If true, creates a commit in the repo
	CommitSHA   string // SHA to use for commit
	BranchName  string // Branch to create/use
}

// NewClaudeCodeMock creates a new mock Claude Code executable.
func NewClaudeCodeMock() (*ClaudeCodeMock, error) {
	tmpDir, err := os.MkdirTemp("", "pilot-e2e-claude-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	m := &ClaudeCodeMock{
		tmpDir:     tmpDir,
		Response:   "Task completed successfully",
		CommitSHA:  "e2e123abc456",
		BranchName: "pilot/GH-1",
	}

	if err := m.createMockBinary(); err != nil {
		_ = os.RemoveAll(tmpDir) // Best effort cleanup
		return nil, err
	}

	return m, nil
}

// BinPath returns the path to the mock Claude Code executable.
func (m *ClaudeCodeMock) BinPath() string {
	return m.binPath
}

// Close cleans up temporary files.
func (m *ClaudeCodeMock) Close() {
	_ = os.RemoveAll(m.tmpDir) // Best effort cleanup
}

// SetResponse sets the mock response text.
func (m *ClaudeCodeMock) SetResponse(response string) {
	m.Response = response
	_ = m.createMockBinary() // Regenerate with new response; ignore error as SetResponse is called post-init
}

// SetFailure configures the mock to return an error.
func (m *ClaudeCodeMock) SetFailure(shouldFail bool) {
	m.ShouldFail = shouldFail
	_ = m.createMockBinary() // Regenerate with new failure mode; ignore error as SetFailure is called post-init
}

// createMockBinary creates a shell script that simulates Claude Code.
func (m *ClaudeCodeMock) createMockBinary() error {
	m.binPath = filepath.Join(m.tmpDir, "claude")

	// Create a shell script that outputs JSON events like real Claude Code
	var scriptBuilder strings.Builder
	scriptBuilder.WriteString("#!/bin/bash\n")
	scriptBuilder.WriteString("# Mock Claude Code for E2E testing\n\n")

	// Parse arguments to find project path
	scriptBuilder.WriteString(`
PROJECT_PATH=""
while [[ $# -gt 0 ]]; do
    case $1 in
        --output-format)
            shift
            ;;
        --print)
            ;;
        -p|--prompt)
            shift
            ;;
        *)
            if [[ -d "$1" ]]; then
                PROJECT_PATH="$1"
            fi
            ;;
    esac
    shift
done

`)

	if m.ShouldFail {
		// Output error event
		scriptBuilder.WriteString(`cat << 'EVENTS'
{"type":"result","subtype":"error","is_error":true,"result":"Execution failed"}
EVENTS
exit 1
`)
	} else {
		// Check if we should simulate git operations
		if m.SimulateGit {
			scriptBuilder.WriteString(fmt.Sprintf(`
# Simulate git operations
if [[ -n "$PROJECT_PATH" && -d "$PROJECT_PATH/.git" ]]; then
    cd "$PROJECT_PATH"

    # Create branch if needed
    git checkout -b %s 2>/dev/null || git checkout %s 2>/dev/null || true

    # Create a test file
    echo "// E2E test file" > e2e_test_file.go

    # Stage and commit
    git add .
    git commit -m "feat(e2e): test commit from mock Claude Code" --allow-empty 2>/dev/null || true
fi
`, m.BranchName, m.BranchName))
		}

		// Output success events in JSON stream format
		resultJSON, _ := json.Marshal(m.Response)
		scriptBuilder.WriteString(fmt.Sprintf(`
cat << 'EVENTS'
{"type":"assistant","subtype":"text","message":{"content":[{"type":"text","text":"Starting task execution..."}]}}
{"type":"assistant","subtype":"tool_use","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"."}}]}}
{"type":"user","subtype":"tool_result","tool_use_result":{"content":"Directory listing..."}}
{"type":"assistant","subtype":"text","message":{"content":[{"type":"text","text":"Implementing changes..."}]}}
{"type":"result","subtype":"success","result":%s,"is_error":false,"duration_ms":1000,"num_turns":3,"usage":{"input_tokens":500,"output_tokens":200},"model":"claude-opus-4-5"}
EVENTS
exit 0
`, string(resultJSON)))
	}

	if err := os.WriteFile(m.binPath, []byte(scriptBuilder.String()), 0755); err != nil {
		return fmt.Errorf("failed to write mock binary: %w", err)
	}

	return nil
}

// Exec runs the mock Claude Code with given arguments.
// This is useful for direct testing of the mock.
func (m *ClaudeCodeMock) Exec(args ...string) (string, error) {
	cmd := exec.Command(m.binPath, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// MockExecutionResult represents the result of a mock execution.
type MockExecutionResult struct {
	Success      bool
	Output       string
	CommitSHA    string
	BranchName   string
	TokensInput  int64
	TokensOutput int64
}

// SimulateExecution simulates what Claude Code would do for a task.
// This can be used by tests to directly get results without invoking shell.
func (m *ClaudeCodeMock) SimulateExecution(projectPath string) (*MockExecutionResult, error) {
	if m.ShouldFail {
		return &MockExecutionResult{
			Success: false,
			Output:  "Execution failed",
		}, nil
	}

	result := &MockExecutionResult{
		Success:      true,
		Output:       m.Response,
		CommitSHA:    m.CommitSHA,
		BranchName:   m.BranchName,
		TokensInput:  500,
		TokensOutput: 200,
	}

	if m.SimulateGit && projectPath != "" {
		// Actually create a commit if requested
		gitDir := filepath.Join(projectPath, ".git")
		if _, err := os.Stat(gitDir); err == nil {
			// Create a test file
			testFile := filepath.Join(projectPath, "e2e_test_file.txt")
			if err := os.WriteFile(testFile, []byte("E2E test content\n"), 0644); err != nil {
				return nil, err
			}

			// Git add and commit
			cmd := exec.Command("git", "-C", projectPath, "add", ".")
			if err := cmd.Run(); err != nil {
				return nil, fmt.Errorf("git add failed: %w", err)
			}

			cmd = exec.Command("git", "-C", projectPath, "commit", "-m", "feat(e2e): test commit")
			if err := cmd.Run(); err != nil {
				// Might fail if nothing to commit, that's ok
			}

			// Get actual SHA
			cmd = exec.Command("git", "-C", projectPath, "rev-parse", "HEAD")
			output, err := cmd.Output()
			if err == nil {
				result.CommitSHA = strings.TrimSpace(string(output))
			}
		}
	}

	return result, nil
}
