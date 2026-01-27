package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// StreamEvent represents a Claude Code stream-json event
type StreamEvent struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype,omitempty"`
	Message       *AssistantMsg   `json:"message,omitempty"`
	Result        string          `json:"result,omitempty"`
	IsError       bool            `json:"is_error,omitempty"`
	DurationMS    int             `json:"duration_ms,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`
	// Token usage (TASK-13)
	Usage *UsageInfo `json:"usage,omitempty"`
	Model string     `json:"model,omitempty"`
}

// UsageInfo represents token usage in stream events
type UsageInfo struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// AssistantMsg represents the message field in assistant events
type AssistantMsg struct {
	Content []ContentBlock `json:"content"`
}

// ContentBlock represents content in assistant messages
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

// ToolResultContent represents tool result in user events
type ToolResultContent struct {
	ToolUseID string `json:"tool_use_id"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
}

// progressState tracks execution phase for compact progress reporting
type progressState struct {
	phase        string   // Current phase: Exploring, Implementing, Testing, Committing
	filesRead    int      // Count of files read
	filesWrite   int      // Count of files written
	commands     int      // Count of bash commands
	hasNavigator bool     // Project has Navigator
	navPhase     string   // Navigator phase: INIT, RESEARCH, IMPL, VERIFY, COMPLETE
	navIteration int      // Navigator loop iteration
	navProgress  int      // Navigator-reported progress
	exitSignal   bool     // Navigator EXIT_SIGNAL detected
	commitSHAs   []string // Extracted commit SHAs from git output
	// Metrics tracking (TASK-13)
	tokensInput  int64  // Input tokens used
	tokensOutput int64  // Output tokens used
	modelName    string // Model used
	// Note: filesChanged/linesAdded/linesRemoved tracked via git diff at commit time
}

// Task represents a task to be executed
type Task struct {
	ID          string
	Title       string
	Description string
	Priority    int
	ProjectPath string
	Branch      string
	Verbose     bool   // Stream Claude Code output to console
	CreatePR    bool   // Create GitHub PR after successful execution
	BaseBranch  string // Base branch for PR (defaults to main/master)
	ImagePath   string // Path to image file for multimodal analysis
}

// ExecutionResult represents the result of task execution
type ExecutionResult struct {
	TaskID    string
	Success   bool
	Output    string
	Error     string
	Duration  time.Duration
	PRUrl     string
	CommitSHA string
	// Metrics fields (TASK-13)
	TokensInput      int64
	TokensOutput     int64
	TokensTotal      int64
	EstimatedCostUSD float64
	FilesChanged     int
	LinesAdded       int
	LinesRemoved     int
	ModelName        string
}

// ProgressCallback is called during execution with progress updates
type ProgressCallback func(taskID string, phase string, progress int, message string)

// Runner executes tasks using Claude Code
type Runner struct {
	onProgress ProgressCallback
	mu         sync.Mutex
	running    map[string]*exec.Cmd
	log        *slog.Logger
}

// NewRunner creates a new executor runner
func NewRunner() *Runner {
	return &Runner{
		running: make(map[string]*exec.Cmd),
		log:     logging.WithComponent("executor"),
	}
}

// OnProgress sets the progress callback
func (r *Runner) OnProgress(callback ProgressCallback) {
	r.onProgress = callback
}

// Execute runs a task using Claude Code
func (r *Runner) Execute(ctx context.Context, task *Task) (*ExecutionResult, error) {
	start := time.Now()
	log := r.log.With(slog.String("task_id", task.ID))

	log.Info("Starting task execution",
		slog.String("project", task.ProjectPath),
		slog.String("branch", task.Branch),
		slog.Bool("create_pr", task.CreatePR),
	)

	// Initialize git operations
	git := NewGitOperations(task.ProjectPath)

	// Create branch if specified
	if task.Branch != "" {
		r.reportProgress(task.ID, "Branching", 5, fmt.Sprintf("Creating branch %s...", task.Branch))
		if err := git.CreateBranch(ctx, task.Branch); err != nil {
			// Check if branch already exists - try to switch to it
			if switchErr := git.SwitchBranch(ctx, task.Branch); switchErr != nil {
				return nil, fmt.Errorf("failed to create/switch branch: %w", err)
			}
			r.reportProgress(task.ID, "Branching", 8, fmt.Sprintf("Switched to existing branch %s", task.Branch))
		} else {
			r.reportProgress(task.ID, "Branching", 8, fmt.Sprintf("Created branch %s", task.Branch))
		}
	}

	// Build the prompt for Claude Code
	prompt := r.BuildPrompt(task)

	// Create the Claude Code command with stream-json output
	// --verbose is required for stream-json to work
	// --dangerously-skip-permissions allows autonomous execution without approval prompts
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
		"--verbose",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
	)
	cmd.Dir = task.ProjectPath

	// Track the running command
	r.mu.Lock()
	r.running[task.ID] = cmd
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.running, task.ID)
		r.mu.Unlock()
	}()

	// Report start
	r.reportProgress(task.ID, "Starting", 0, "Initializing Claude Code...")

	// Create pipes for output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		log.Error("Failed to start Claude Code", slog.Any("error", err))
		return nil, fmt.Errorf("failed to start Claude Code: %w", err)
	}
	log.Debug("Claude Code process started", slog.Int("pid", cmd.Process.Pid))

	// State for tracking progress and result
	var finalResult string
	var finalError string
	state := &progressState{phase: "Starting"}
	var wg sync.WaitGroup

	// Read stdout (stream-json events)
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		// Increase buffer size for large JSON events
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if task.Verbose {
				fmt.Printf("   %s\n", line)
			}

			// Parse JSON event
			result, errMsg := r.parseStreamEvent(task.ID, line, state)
			if result != "" {
				finalResult = result
			}
			if errMsg != "" {
				finalError = errMsg
			}
		}
	}()

	// Read stderr
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		var errorBuilder strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			errorBuilder.WriteString(line + "\n")
			if task.Verbose {
				fmt.Printf("   [err] %s\n", line)
			}
		}
		if errorBuilder.Len() > 0 && finalError == "" {
			finalError = errorBuilder.String()
		}
	}()

	// Wait for output readers
	wg.Wait()

	// Wait for command to complete
	err = cmd.Wait()
	duration := time.Since(start)

	result := &ExecutionResult{
		TaskID:   task.ID,
		Output:   finalResult,
		Error:    finalError,
		Duration: duration,
	}

	// Extract commit SHA from state
	if len(state.commitSHAs) > 0 {
		result.CommitSHA = state.commitSHAs[len(state.commitSHAs)-1] // Use last commit
	}

	// Populate metrics from state (TASK-13)
	result.TokensInput = state.tokensInput
	result.TokensOutput = state.tokensOutput
	result.TokensTotal = state.tokensInput + state.tokensOutput
	result.FilesChanged = state.filesWrite
	result.ModelName = state.modelName
	if result.ModelName == "" {
		result.ModelName = "claude-sonnet-4-5" // Default model
	}
	// Estimate cost based on token usage
	result.EstimatedCostUSD = estimateCost(state.tokensInput, state.tokensOutput, result.ModelName)

	if err != nil {
		result.Success = false
		if result.Error == "" {
			result.Error = err.Error()
		}
		log.Error("Task execution failed",
			slog.String("error", result.Error),
			slog.Duration("duration", duration),
		)
		r.reportProgress(task.ID, "Failed", 100, result.Error)
	} else {
		result.Success = true
		log.Info("Task execution succeeded",
			slog.Duration("duration", duration),
			slog.Int64("tokens_in", state.tokensInput),
			slog.Int64("tokens_out", state.tokensOutput),
			slog.Int("files_written", state.filesWrite),
		)
		r.reportProgress(task.ID, "Completed", 95, "Execution completed")

		// Create PR if requested and we have commits
		if task.CreatePR && task.Branch != "" {
			r.reportProgress(task.ID, "Creating PR", 96, "Pushing branch...")

			// Push branch
			if err := git.Push(ctx, task.Branch); err != nil {
				result.Success = false
				result.Error = fmt.Sprintf("push failed: %v", err)
				r.reportProgress(task.ID, "PR Failed", 100, result.Error)
				return result, nil
			}

			r.reportProgress(task.ID, "Creating PR", 98, "Creating pull request...")

			// Determine base branch
			baseBranch := task.BaseBranch
			if baseBranch == "" {
				baseBranch, _ = git.GetDefaultBranch(ctx)
				if baseBranch == "" {
					baseBranch = "main"
				}
			}

			// Generate PR body
			prBody := fmt.Sprintf("## Summary\n\nAutomated PR created by Pilot for task %s.\n\n## Changes\n\n%s", task.ID, task.Description)

			// Create PR
			prURL, err := git.CreatePR(ctx, task.Title, prBody, baseBranch)
			if err != nil {
				result.Success = false
				result.Error = fmt.Sprintf("PR creation failed: %v", err)
				r.reportProgress(task.ID, "PR Failed", 100, result.Error)
				return result, nil
			}

			result.PRUrl = prURL
			log.Info("Pull request created", slog.String("pr_url", prURL))
			r.reportProgress(task.ID, "Completed", 100, fmt.Sprintf("PR created: %s", prURL))
		} else {
			r.reportProgress(task.ID, "Completed", 100, "Task completed successfully")
		}
	}

	return result, nil
}

// Cancel cancels a running task
func (r *Runner) Cancel(taskID string) error {
	r.mu.Lock()
	cmd, ok := r.running[taskID]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("task %s is not running", taskID)
	}

	return cmd.Process.Kill()
}

// IsRunning checks if a task is running
func (r *Runner) IsRunning(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[taskID]
	return ok
}

// BuildPrompt builds the prompt for Claude Code (exported for dry-run preview)
func (r *Runner) BuildPrompt(task *Task) string {
	var sb strings.Builder

	// Handle image analysis tasks (no Navigator overhead for simple image questions)
	if task.ImagePath != "" {
		sb.WriteString(fmt.Sprintf("Read and analyze the image at: %s\n\n", task.ImagePath))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))
		sb.WriteString("Respond directly with your analysis. Be concise.\n")
		return sb.String()
	}

	// Check if project has Navigator initialized
	agentDir := filepath.Join(task.ProjectPath, ".agent")
	hasNavigator := false
	if _, err := os.Stat(agentDir); err == nil {
		hasNavigator = true
	}

	// Navigator-aware prompt structure
	if hasNavigator {
		// Navigator handles workflow, autonomous completion, and documentation
		sb.WriteString("Start my Navigator session.\n\n")
		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("Create branch `%s` before starting.\n\n", task.Branch))
		}

		// Navigator will handle: workflow check, complexity detection, autonomous completion
		sb.WriteString("Run until done. Use Navigator's autonomous completion protocol.\n\n")
		sb.WriteString("CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed. Use format: `type(scope): description (TASK-XX)`\n")
	} else {
		// Non-Navigator project: explicit instructions with strict constraints
		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		sb.WriteString("## Constraints\n\n")
		sb.WriteString("- ONLY create files explicitly mentioned in the task\n")
		sb.WriteString("- Do NOT create additional files, tests, configs, or dependencies\n")
		sb.WriteString("- Do NOT modify existing files unless explicitly requested\n")
		sb.WriteString("- If task specifies a file type (e.g., .py), use ONLY that type\n")
		sb.WriteString("- Do NOT add package.json, requirements.txt, or build configs\n")
		sb.WriteString("- Keep implementation minimal and focused\n\n")

		sb.WriteString("## Instructions\n\n")

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("1. Create git branch: `%s`\n", task.Branch))
		} else {
			sb.WriteString("1. Work on current branch (no new branch)\n")
		}

		sb.WriteString("2. Implement EXACTLY what is requested - nothing more, nothing less\n")
		sb.WriteString("3. Commit with format: `type(scope): description`\n")
		sb.WriteString("\nWork autonomously. Do not ask for confirmation.\n")
	}

	return sb.String()
}

// parseStreamEvent parses a stream-json event and reports progress
// Returns (finalResult, errorMessage) - non-empty when task completes
func (r *Runner) parseStreamEvent(taskID, line string, state *progressState) (string, string) {
	var event StreamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		// Not valid JSON, skip
		return "", ""
	}

	switch event.Type {
	case "system":
		if event.Subtype == "init" {
			r.reportProgress(taskID, "🚀 Started", 5, "Claude Code initialized")
		}

	case "assistant":
		if event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case "tool_use":
					r.handleToolUse(taskID, block.Name, block.Input, state)
				case "text":
					// Parse Navigator-specific patterns from text
					r.parseNavigatorPatterns(taskID, block.Text, state)
				}
			}
		}

	case "user":
		// Tool results - parse for commit SHAs
		if event.ToolUseResult != nil {
			var toolResult ToolResultContent
			if err := json.Unmarshal(event.ToolUseResult, &toolResult); err == nil {
				// Extract commit SHA from git commit output
				// Pattern: "[branch abc1234] commit message" or "[main abc1234] message"
				extractCommitSHA(toolResult.Content, state)
			}
		}

	case "result":
		// Capture final usage stats from result event
		if event.Usage != nil {
			state.tokensInput += event.Usage.InputTokens
			state.tokensOutput += event.Usage.OutputTokens
		}
		if event.Model != "" {
			state.modelName = event.Model
		}
		r.log.Debug("Stream result received",
			slog.String("task_id", taskID),
			slog.Bool("is_error", event.IsError),
			slog.String("model", event.Model),
		)
		if event.IsError {
			r.log.Warn("Claude Code returned error", slog.String("task_id", taskID), slog.String("error", event.Result))
			return "", event.Result
		}
		return event.Result, ""
	}

	// Track usage from any event with usage info
	if event.Usage != nil {
		state.tokensInput += event.Usage.InputTokens
		state.tokensOutput += event.Usage.OutputTokens
	}
	if event.Model != "" && state.modelName == "" {
		state.modelName = event.Model
	}

	return "", ""
}

// parseNavigatorPatterns detects Navigator-specific progress signals from text
func (r *Runner) parseNavigatorPatterns(taskID, text string, state *progressState) {
	// Navigator Session Started
	if strings.Contains(text, "Navigator Session Started") {
		state.hasNavigator = true
		r.reportProgress(taskID, "Navigator", 10, "Navigator session started")
		return
	}

	// Navigator Status Block - extract phase and progress
	if strings.Contains(text, "NAVIGATOR_STATUS") {
		state.hasNavigator = true
		r.parseNavigatorStatusBlock(taskID, text, state)
		return
	}

	// Phase transitions
	if strings.Contains(text, "PHASE:") && strings.Contains(text, "→") {
		// Extract phase from "PHASE: X → Y" pattern
		if idx := strings.Index(text, "→"); idx != -1 {
			after := strings.TrimSpace(text[idx+3:]) // Skip "→ "
			if newline := strings.Index(after, "\n"); newline != -1 {
				after = after[:newline]
			}
			phase := strings.TrimSpace(after)
			if phase != "" {
				r.handleNavigatorPhase(taskID, phase, state)
			}
		}
		return
	}

	// Workflow check - indicates task analysis
	if strings.Contains(text, "WORKFLOW CHECK") {
		if state.phase != "Analyzing" {
			state.phase = "Analyzing"
			r.reportProgress(taskID, "Analyzing", 12, "Workflow check...")
		}
		return
	}

	// Task Mode
	if strings.Contains(text, "TASK MODE ACTIVATED") {
		r.reportProgress(taskID, "Task Mode", 15, "Task mode activated")
		return
	}

	// Completion signals
	if strings.Contains(text, "LOOP COMPLETE") || strings.Contains(text, "TASK MODE COMPLETE") {
		state.exitSignal = true
		r.reportProgress(taskID, "Completing", 95, "Task complete signal received")
		return
	}

	// EXIT_SIGNAL detection
	if strings.Contains(text, "EXIT_SIGNAL: true") || strings.Contains(text, "EXIT_SIGNAL:true") {
		state.exitSignal = true
		r.reportProgress(taskID, "Finishing", 92, "Exit signal detected")
		return
	}

	// Stagnation detection
	if strings.Contains(text, "STAGNATION DETECTED") {
		r.reportProgress(taskID, "⚠️ Stalled", 0, "Navigator detected stagnation")
		return
	}
}

// parseNavigatorStatusBlock extracts progress from Navigator status block
func (r *Runner) parseNavigatorStatusBlock(taskID, text string, state *progressState) {
	// Extract Phase: from status block
	if idx := strings.Index(text, "Phase:"); idx != -1 {
		line := text[idx:]
		if newline := strings.Index(line, "\n"); newline != -1 {
			line = line[:newline]
		}
		phase := strings.TrimSpace(strings.TrimPrefix(line, "Phase:"))
		if phase != "" {
			r.handleNavigatorPhase(taskID, phase, state)
		}
	}

	// Extract Progress: percentage
	if idx := strings.Index(text, "Progress:"); idx != -1 {
		line := text[idx:]
		if newline := strings.Index(line, "\n"); newline != -1 {
			line = line[:newline]
		}
		// Parse "Progress: 45%" or similar
		line = strings.TrimPrefix(line, "Progress:")
		line = strings.TrimSpace(line)
		line = strings.TrimSuffix(line, "%")
		if pct, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			state.navProgress = pct
		}
	}

	// Extract Iteration
	if idx := strings.Index(text, "Iteration:"); idx != -1 {
		line := text[idx:]
		if newline := strings.Index(line, "\n"); newline != -1 {
			line = line[:newline]
		}
		// Parse "Iteration: 2/5" format
		line = strings.TrimPrefix(line, "Iteration:")
		if slash := strings.Index(line, "/"); slash != -1 {
			if iter, err := strconv.Atoi(strings.TrimSpace(line[:slash])); err == nil {
				state.navIteration = iter
			}
		}
	}
}

// handleNavigatorPhase maps Navigator phases to progress
func (r *Runner) handleNavigatorPhase(taskID, phase string, state *progressState) {
	phase = strings.ToUpper(strings.TrimSpace(phase))

	// Skip if same phase
	if state.navPhase == phase {
		return
	}
	state.navPhase = phase

	var displayPhase string
	var progress int
	var message string

	switch phase {
	case "INIT":
		displayPhase = "Init"
		progress = 10
		message = "Initializing task..."
	case "RESEARCH":
		displayPhase = "Research"
		progress = 25
		message = "Researching codebase..."
	case "IMPL", "IMPLEMENTATION":
		displayPhase = "Implement"
		progress = 50
		message = "Implementing changes..."
	case "VERIFY", "VERIFICATION":
		displayPhase = "Verify"
		progress = 80
		message = "Verifying changes..."
	case "COMPLETE", "COMPLETED":
		displayPhase = "Complete"
		progress = 95
		message = "Finalizing..."
	default:
		displayPhase = phase
		progress = 50
		message = fmt.Sprintf("Phase: %s", phase)
	}

	// Use Navigator's reported progress if available
	if state.navProgress > 0 {
		progress = state.navProgress
	}

	state.phase = displayPhase
	r.reportProgress(taskID, displayPhase, progress, message)
}

// handleToolUse processes tool usage and updates phase-based progress
func (r *Runner) handleToolUse(taskID, toolName string, input map[string]interface{}, state *progressState) {
	// Log tool usage at debug level
	r.log.Debug("Tool used",
		slog.String("task_id", taskID),
		slog.String("tool", toolName),
	)

	var newPhase string
	var progress int
	var message string

	switch toolName {
	case "Read", "Glob", "Grep":
		state.filesRead++
		if state.phase != "Exploring" {
			newPhase = "Exploring"
			progress = 15
			message = "Analyzing codebase..."
		}

	case "Write", "Edit":
		state.filesWrite++
		if fp, ok := input["file_path"].(string); ok {
			// Check if writing to .agent/ (Navigator activity)
			if strings.Contains(fp, ".agent/") {
				state.hasNavigator = true
				if strings.Contains(fp, ".context-markers/") {
					newPhase = "Checkpoint"
					progress = 88
					message = "Creating context marker..."
				} else if strings.Contains(fp, "/tasks/") {
					newPhase = "Documenting"
					progress = 85
					message = "Updating task docs..."
				}
				// Don't report other .agent/ writes
			} else if state.phase != "Implementing" || state.filesWrite == 1 {
				newPhase = "Implementing"
				progress = 40 + min(state.filesWrite*5, 30)
				message = fmt.Sprintf("Creating %s", filepath.Base(fp))
			}
		} else {
			if state.phase != "Implementing" {
				newPhase = "Implementing"
				progress = 40
				message = "Writing files..."
			}
		}

	case "Bash":
		state.commands++
		if cmd, ok := input["command"].(string); ok {
			cmdLower := strings.ToLower(cmd)

			// Detect phase from command (order matters - check specific patterns first)
			if strings.Contains(cmdLower, "git commit") {
				if state.phase != "Committing" {
					newPhase = "Committing"
					progress = 90
					message = "Committing changes..."
				}
			} else if strings.Contains(cmdLower, "git checkout") || strings.Contains(cmdLower, "git branch") {
				if state.phase != "Branching" {
					newPhase = "Branching"
					progress = 10
					message = "Setting up branch..."
				}
			} else if strings.Contains(cmdLower, "pytest") || strings.Contains(cmdLower, "jest") ||
				strings.Contains(cmdLower, "go test") || strings.Contains(cmdLower, "npm test") ||
				strings.Contains(cmdLower, "make test") {
				if state.phase != "Testing" {
					newPhase = "Testing"
					progress = 75
					message = "Running tests..."
				}
			} else if strings.Contains(cmdLower, "npm install") || strings.Contains(cmdLower, "pip install") ||
				strings.Contains(cmdLower, "go mod") {
				if state.phase != "Installing" {
					newPhase = "Installing"
					progress = 30
					message = "Installing dependencies..."
				}
			}
			// Skip other bash commands - too noisy
		}

	case "Task":
		// Sub-agent spawned
		if state.phase != "Delegating" {
			newPhase = "Delegating"
			progress = 50
			if desc, ok := input["description"].(string); ok {
				message = fmt.Sprintf("Spawning agent: %s", truncateText(desc, 40))
			} else {
				message = "Running sub-task..."
			}
		}

	case "Skill":
		// Navigator skill invocation
		if skill, ok := input["skill"].(string); ok {
			state.hasNavigator = true
			skillLower := strings.ToLower(skill)

			switch {
			case strings.HasPrefix(skillLower, "nav-start"):
				newPhase = "Navigator"
				progress = 10
				message = "Starting Navigator session..."
			case strings.HasPrefix(skillLower, "nav-loop"):
				newPhase = "Loop Mode"
				progress = 20
				message = "Entering loop mode..."
			case strings.HasPrefix(skillLower, "nav-task"):
				newPhase = "Task Mode"
				progress = 15
				message = "Task mode activated..."
			case strings.HasPrefix(skillLower, "nav-compact"):
				newPhase = "Compacting"
				progress = 90
				message = "Compacting context..."
			case strings.HasPrefix(skillLower, "nav-marker"):
				newPhase = "Checkpoint"
				progress = 88
				message = "Creating checkpoint..."
			case strings.HasPrefix(skillLower, "nav-simplify"):
				newPhase = "Simplifying"
				progress = 82
				message = "Simplifying code..."
			default:
				// Other nav skills
				if strings.HasPrefix(skillLower, "nav-") {
					message = fmt.Sprintf("Navigator: %s", skill)
				}
			}
		}
	}

	// Only report if phase changed
	if newPhase != "" && newPhase != state.phase {
		state.phase = newPhase
		r.reportProgress(taskID, newPhase, progress, message)
	}
}

// formatToolMessage creates a human-readable message for tool usage
func formatToolMessage(toolName string, input map[string]interface{}) string {
	switch toolName {
	case "Write":
		if fp, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Writing %s", filepath.Base(fp))
		}
	case "Edit":
		if fp, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Editing %s", filepath.Base(fp))
		}
	case "Read":
		if fp, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Reading %s", filepath.Base(fp))
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return fmt.Sprintf("Running: %s", truncateText(cmd, 40))
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return fmt.Sprintf("Searching: %s", pattern)
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return fmt.Sprintf("Grep: %s", truncateText(pattern, 30))
		}
	case "Task":
		if desc, ok := input["description"].(string); ok {
			return fmt.Sprintf("Spawning: %s", truncateText(desc, 40))
		}
	}
	return fmt.Sprintf("Using %s", toolName)
}

// truncateText truncates text to maxLen and adds ellipsis
func truncateText(text string, maxLen int) string {
	// Remove newlines for display
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}

// min returns the smaller of two ints
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// extractCommitSHA extracts git commit SHA from tool output
// Pattern: "[branch abc1234]" or "[main abc1234]" from git commit output
func extractCommitSHA(content string, state *progressState) {
	// Look for git commit output pattern: [branch sha]
	// Example: "[main abc1234] feat: add feature"
	// Example: "[pilot/TASK-123 def5678] fix: bug"
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}

		// Find closing bracket
		closeBracket := strings.Index(line, "]")
		if closeBracket == -1 {
			continue
		}

		// Extract branch and SHA: "[branch sha]"
		inside := line[1:closeBracket]
		parts := strings.Fields(inside)
		if len(parts) >= 2 {
			sha := parts[len(parts)-1]
			// Validate SHA format (7-40 hex characters)
			if isValidSHA(sha) {
				state.commitSHAs = append(state.commitSHAs, sha)
			}
		}
	}
}

// isValidSHA checks if a string looks like a git SHA (7-40 hex chars)
func isValidSHA(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, c := range s {
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		isUpperHex := c >= 'A' && c <= 'F'
		if !isDigit && !isLowerHex && !isUpperHex {
			return false
		}
	}
	return true
}

// estimateCost calculates estimated cost from token usage (TASK-13)
func estimateCost(inputTokens, outputTokens int64, model string) float64 {
	// Model pricing in USD per 1M tokens
	const (
		sonnetInputPrice  = 3.00
		sonnetOutputPrice = 15.00
		opusInputPrice    = 15.00
		opusOutputPrice   = 75.00
	)

	var inputPrice, outputPrice float64
	if strings.Contains(strings.ToLower(model), "opus") {
		inputPrice = opusInputPrice
		outputPrice = opusOutputPrice
	} else {
		inputPrice = sonnetInputPrice
		outputPrice = sonnetOutputPrice
	}

	inputCost := float64(inputTokens) * inputPrice / 1_000_000
	outputCost := float64(outputTokens) * outputPrice / 1_000_000
	return inputCost + outputCost
}

// reportProgress sends a progress update
func (r *Runner) reportProgress(taskID, phase string, progress int, message string) {
	// Always log progress for terminal visibility
	r.log.Info("Task progress",
		slog.String("task_id", taskID),
		slog.String("phase", phase),
		slog.Int("progress", progress),
		slog.String("message", message),
	)

	// Send to callback (e.g., Telegram) if registered
	if r.onProgress != nil {
		r.onProgress(taskID, phase, progress, message)
	}
}
