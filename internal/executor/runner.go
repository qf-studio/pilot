package executor

import (
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
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/replay"
	"github.com/alekspetrov/pilot/internal/webhooks"
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

// Task represents a task to be executed by the Runner.
// It contains all the information needed to execute a development task
// using Claude Code, including project context, branching options, and PR creation settings.
type Task struct {
	// ID is the unique identifier for this task (e.g., "TASK-123").
	ID string
	// Title is the human-readable title of the task.
	Title string
	// Description contains the full task description and requirements.
	Description string
	// Priority indicates task priority (lower numbers = higher priority).
	Priority int
	// ProjectPath is the absolute path to the project directory.
	ProjectPath string
	// Branch is the git branch name to create for this task (optional).
	Branch string
	// Verbose enables streaming Claude Code output to console when true.
	Verbose bool
	// CreatePR enables automatic GitHub PR creation after successful execution.
	CreatePR bool
	// BaseBranch specifies the base branch for PR creation (defaults to main/master).
	BaseBranch string
	// ImagePath is the path to an image file for multimodal analysis tasks (optional).
	ImagePath string
	// DirectCommit enables pushing directly to main without branches or PRs.
	// Requires executor.direct_commit=true in config AND --direct-commit flag.
	DirectCommit bool
	// SourceRepo is the source repository in "owner/repo" format (GH-386).
	// Used for cross-project execution validation to prevent issues from one repo
	// being executed against a different project.
	SourceRepo string
}

// QualityGateResult represents the result of a single quality gate check.
type QualityGateResult struct {
	// Name is the gate name (e.g., "build", "test", "lint")
	Name string
	// Passed indicates whether the gate passed
	Passed bool
	// Duration is how long the gate took to run
	Duration time.Duration
	// RetryCount is the number of retries attempted (0 if passed first try)
	RetryCount int
	// Error contains the error message if the gate failed
	Error string
}

// QualityGatesResult represents the aggregate quality gate results.
type QualityGatesResult struct {
	// Enabled indicates whether quality gates were configured and run
	Enabled bool
	// AllPassed indicates whether all gates passed
	AllPassed bool
	// Gates contains individual gate results
	Gates []QualityGateResult
	// TotalDuration is the total time spent running all gates
	TotalDuration time.Duration
	// TotalRetries is the sum of all retry attempts across gates
	TotalRetries int
}

// ExecutionResult represents the result of task execution by the Runner.
// It contains the execution outcome, any output or errors, and metrics
// about resource usage including token counts and estimated costs.
type ExecutionResult struct {
	// TaskID is the identifier of the executed task.
	TaskID string
	// Success indicates whether the task completed successfully.
	Success bool
	// Output contains the final output from Claude Code.
	Output string
	// Error contains error details if the execution failed.
	Error string
	// Duration is the total execution time.
	Duration time.Duration
	// PRUrl is the URL of the created pull request (if CreatePR was enabled).
	PRUrl string
	// CommitSHA is the git commit SHA of the last commit made during execution.
	CommitSHA string
	// TokensInput is the number of input tokens consumed.
	TokensInput int64
	// TokensOutput is the number of output tokens generated.
	TokensOutput int64
	// TokensTotal is the total token count (input + output).
	TokensTotal int64
	// ResearchTokens is the number of tokens used by parallel research phase (GH-217).
	ResearchTokens int64
	// EstimatedCostUSD is the estimated cost in USD based on token usage.
	EstimatedCostUSD float64
	// FilesChanged is the number of files modified during execution.
	FilesChanged int
	// LinesAdded is the number of lines added across all changes.
	LinesAdded int
	// LinesRemoved is the number of lines removed across all changes.
	LinesRemoved int
	// ModelName is the Claude model used for execution.
	ModelName string
	// QualityGates contains the results of quality gate checks (if enabled)
	QualityGates *QualityGatesResult
	// IsEpic indicates this result is from epic planning (not execution)
	IsEpic bool
	// EpicPlan contains the planning result for epic tasks (GH-405)
	EpicPlan *EpicPlan
}

// ProgressCallback is a function called during execution with progress updates.
// It receives the task ID, current phase name, progress percentage (0-100),
// and a human-readable message describing the current activity.
type ProgressCallback func(taskID string, phase string, progress int, message string)

// TokenCallback is a function called during execution with token usage updates.
// It receives the task ID, input tokens, and output tokens.
type TokenCallback func(taskID string, inputTokens, outputTokens int64)

// Runner executes development tasks using an AI backend (Claude Code, OpenCode, etc.).
// It manages task lifecycle including branch creation, AI invocation,
// progress tracking, PR creation, and execution recording. Runner is safe for
// concurrent use and tracks all running tasks for cancellation support.
type Runner struct {
	backend               Backend // AI execution backend
	config                *BackendConfig
	onProgress            ProgressCallback
	progressCallbacks     map[string]ProgressCallback // Named callbacks for multi-listener support
	progressMu            sync.RWMutex                // Protects progressCallbacks
	tokenCallbacks        map[string]TokenCallback    // Named callbacks for token usage updates
	tokenMu               sync.RWMutex                // Protects tokenCallbacks
	mu                    sync.Mutex
	running               map[string]*exec.Cmd
	log                   *slog.Logger
	recordingsPath        string                // Path to recordings directory (empty = default)
	enableRecording       bool                  // Whether to record executions
	alertProcessor        AlertEventProcessor   // Optional alert processor for event emission
	webhooks              *webhooks.Manager     // Optional webhook manager for event delivery
	qualityCheckerFactory QualityCheckerFactory // Optional factory for creating quality checkers
	modelRouter           *ModelRouter          // Model and timeout routing based on complexity
	parallelRunner        *ParallelRunner       // Optional parallel research runner (GH-217)
	decomposer            *TaskDecomposer       // Optional task decomposer for complex tasks (GH-218)
	suppressProgressLogs  bool                  // Suppress slog output for progress (use when visual display is active)
}

// NewRunner creates a new Runner instance with Claude Code backend by default.
// The Runner is ready to execute tasks immediately after creation.
func NewRunner() *Runner {
	return &Runner{
		backend:           NewClaudeCodeBackend(nil),
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               logging.WithComponent("executor"),
		enableRecording:   true, // Recording enabled by default
		modelRouter:       NewModelRouter(nil, nil),
	}
}

// NewRunnerWithBackend creates a Runner with a specific backend.
func NewRunnerWithBackend(backend Backend) *Runner {
	if backend == nil {
		backend = NewClaudeCodeBackend(nil)
	}
	return &Runner{
		backend:           backend,
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               logging.WithComponent("executor"),
		enableRecording:   true,
		modelRouter:       NewModelRouter(nil, nil),
	}
}

// NewRunnerWithConfig creates a Runner from backend configuration.
func NewRunnerWithConfig(config *BackendConfig) (*Runner, error) {
	backend, err := NewBackend(config)
	if err != nil {
		return nil, err
	}
	runner := NewRunnerWithBackend(backend)
	runner.config = config

	// Configure model routing and timeouts from config
	if config != nil {
		runner.modelRouter = NewModelRouter(config.ModelRouting, config.Timeout)

		// Configure task decomposition (GH-218)
		if config.Decompose != nil && config.Decompose.Enabled {
			runner.decomposer = NewTaskDecomposer(config.Decompose)
		}
	}

	return runner, nil
}

// Config returns the runner's backend configuration.
func (r *Runner) Config() *BackendConfig {
	return r.config
}

// SetBackend changes the execution backend.
func (r *Runner) SetBackend(backend Backend) {
	r.backend = backend
}

// GetBackend returns the current execution backend.
func (r *Runner) GetBackend() Backend {
	return r.backend
}

// SetRecordingsPath sets a custom directory path for storing execution recordings.
// If not set, recordings are stored in the default location (~/.pilot/recordings).
func (r *Runner) SetRecordingsPath(path string) {
	r.recordingsPath = path
}

// SetRecordingEnabled enables or disables execution recording.
// When enabled, all Claude Code stream events are captured for replay and debugging.
func (r *Runner) SetRecordingEnabled(enabled bool) {
	r.enableRecording = enabled
}

// SetAlertProcessor sets the alert processor for emitting task lifecycle events.
// When set, the runner will emit events for task started, completed, and failed.
// The processor interface is satisfied by alerts.Engine.
func (r *Runner) SetAlertProcessor(processor AlertEventProcessor) {
	r.alertProcessor = processor
}

// SetWebhookManager sets the webhook manager for delivering task lifecycle events.
// When set, the runner can dispatch webhook events for task started, progress,
// completed, failed, and PR created events to configured endpoints.
func (r *Runner) SetWebhookManager(mgr *webhooks.Manager) {
	r.webhooks = mgr
}

// SetQualityCheckerFactory sets the factory for creating quality checkers.
// The factory is called with the task ID and project path to create a checker
// that runs quality gates (build, test, lint) before PR creation.
func (r *Runner) SetQualityCheckerFactory(factory QualityCheckerFactory) {
	r.qualityCheckerFactory = factory
}

// SetModelRouter sets the model router for complexity-based model and timeout selection.
func (r *Runner) SetModelRouter(router *ModelRouter) {
	r.modelRouter = router
}

// SetParallelRunner sets the parallel runner for research phase execution (GH-217).
// When set and enabled, medium/complex tasks run parallel research subagents
// before the main implementation to gather codebase context.
func (r *Runner) SetParallelRunner(runner *ParallelRunner) {
	r.parallelRunner = runner
}

// EnableParallelResearch creates and configures a default parallel runner.
// This is a convenience method to enable parallel research with default settings.
func (r *Runner) EnableParallelResearch() {
	r.parallelRunner = NewParallelRunner(DefaultParallelConfig(), r.modelRouter)
}

// SetDecomposer sets the task decomposer for auto-splitting complex tasks (GH-218).
// When set and enabled, complex tasks are decomposed into subtasks that run sequentially,
// with only the final subtask creating a PR.
func (r *Runner) SetDecomposer(decomposer *TaskDecomposer) {
	r.decomposer = decomposer
}

// EnableDecomposition creates and configures a default task decomposer.
// This is a convenience method to enable decomposition with default settings.
func (r *Runner) EnableDecomposition(config *DecomposeConfig) {
	if config == nil {
		config = DefaultDecomposeConfig()
		config.Enabled = true // Enable by default when called explicitly
	}
	r.decomposer = NewTaskDecomposer(config)
}

// getRecordingsPath returns the recordings path, using default if not set
func (r *Runner) getRecordingsPath() string {
	if r.recordingsPath != "" {
		return r.recordingsPath
	}
	return replay.DefaultRecordingsPath()
}

// OnProgress registers a callback function to receive progress updates during task execution.
// The callback is invoked whenever the execution phase changes or significant events occur.
// Deprecated: Use AddProgressCallback for multi-listener support. This method remains for
// backward compatibility but will overwrite any callback set via OnProgress (not AddProgressCallback).
func (r *Runner) OnProgress(callback ProgressCallback) {
	r.onProgress = callback
}

// AddProgressCallback registers a named callback for progress updates.
// Multiple callbacks can be registered with different names. Use RemoveProgressCallback
// to unregister. This is thread-safe and works alongside the legacy OnProgress callback.
func (r *Runner) AddProgressCallback(name string, callback ProgressCallback) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	if r.progressCallbacks == nil {
		r.progressCallbacks = make(map[string]ProgressCallback)
	}
	r.progressCallbacks[name] = callback
}

// RemoveProgressCallback removes a named callback registered via AddProgressCallback.
func (r *Runner) RemoveProgressCallback(name string) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	delete(r.progressCallbacks, name)
}

// AddTokenCallback registers a named callback for token usage updates.
// Multiple callbacks can be registered with different names. Use RemoveTokenCallback
// to unregister. This is thread-safe.
func (r *Runner) AddTokenCallback(name string, callback TokenCallback) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	if r.tokenCallbacks == nil {
		r.tokenCallbacks = make(map[string]TokenCallback)
	}
	r.tokenCallbacks[name] = callback
}

// RemoveTokenCallback removes a named callback registered via AddTokenCallback.
func (r *Runner) RemoveTokenCallback(name string) {
	r.tokenMu.Lock()
	defer r.tokenMu.Unlock()
	delete(r.tokenCallbacks, name)
}

// reportTokens sends token usage updates to all registered callbacks.
func (r *Runner) reportTokens(taskID string, inputTokens, outputTokens int64) {
	r.tokenMu.RLock()
	defer r.tokenMu.RUnlock()
	for _, cb := range r.tokenCallbacks {
		cb(taskID, inputTokens, outputTokens)
	}
}

// SuppressProgressLogs disables slog output for progress updates.
// Use this when a visual progress display is active to prevent log spam.
func (r *Runner) SuppressProgressLogs(suppress bool) {
	r.suppressProgressLogs = suppress
}

// EmitProgress exposes the progress callback for external callers (e.g., Dispatcher).
// This allows the dispatcher to emit progress events using the runner's registered callback.
func (r *Runner) EmitProgress(taskID, phase string, progress int, message string) {
	r.reportProgress(taskID, phase, progress, message)
}

// Execute runs a task using the configured backend and returns the execution result.
// It handles the complete task lifecycle: branch creation, prompt building,
// backend invocation, progress tracking, and optional PR creation.
// The context can be used to cancel execution. Returns an error only for
// setup failures; execution failures are reported in ExecutionResult.
//
// When a decomposer is configured and enabled, complex tasks are automatically
// split into subtasks that run sequentially (GH-218). Only the final subtask
// creates a PR, accumulating all changes from previous subtasks.
func (r *Runner) Execute(ctx context.Context, task *Task) (*ExecutionResult, error) {
	start := time.Now()

	// GH-386: Validate source repo matches project path to prevent cross-project execution
	if task.SourceRepo != "" && task.ProjectPath != "" {
		if err := ValidateRepoProjectMatch(task.SourceRepo, task.ProjectPath); err != nil {
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Sprintf("cross-project execution blocked: %v", err),
			}, fmt.Errorf("cross-project execution blocked: %w", err)
		}
	}

	// Detect complexity for routing decisions
	complexity := DetectComplexity(task)

	// GH-405: Epic tasks trigger planning mode instead of execution
	if complexity.IsEpic() {
		r.log.Info("Epic task detected, running planning mode",
			slog.String("task_id", task.ID),
			slog.String("title", task.Title),
		)
		r.reportProgress(task.ID, "Planning", 10, "Running epic planning...")

		plan, err := r.PlanEpic(ctx, task)
		if err != nil {
			return &ExecutionResult{
				TaskID:   task.ID,
				Success:  false,
				Error:    fmt.Sprintf("epic planning failed: %v", err),
				Duration: time.Since(start),
				IsEpic:   true,
			}, nil
		}

		r.reportProgress(task.ID, "Planning", 30, fmt.Sprintf("Epic planned: %d subtasks", len(plan.Subtasks)))

		// GH-412: Create sub-issues from the plan
		r.reportProgress(task.ID, "Creating Issues", 40, "Creating GitHub sub-issues...")

		issues, err := r.CreateSubIssues(ctx, plan)
		if err != nil {
			return &ExecutionResult{
				TaskID:   task.ID,
				Success:  false,
				Error:    fmt.Sprintf("failed to create sub-issues: %v", err),
				Duration: time.Since(start),
				IsEpic:   true,
				EpicPlan: plan,
			}, nil
		}

		r.reportProgress(task.ID, "Executing", 50, fmt.Sprintf("Executing %d sub-issues sequentially...", len(issues)))

		// GH-412: Execute sub-issues sequentially
		if err := r.ExecuteSubIssues(ctx, task, issues); err != nil {
			return &ExecutionResult{
				TaskID:   task.ID,
				Success:  false,
				Error:    fmt.Sprintf("sub-issue execution failed: %v", err),
				Duration: time.Since(start),
				IsEpic:   true,
				EpicPlan: plan,
			}, nil
		}

		r.reportProgress(task.ID, "Complete", 100, "Epic completed successfully")

		return &ExecutionResult{
			TaskID:   task.ID,
			Success:  true,
			Output:   fmt.Sprintf("Epic completed: %d sub-issues executed", len(issues)),
			Duration: time.Since(start),
			IsEpic:   true,
			EpicPlan: plan,
		}, nil
	}

	// Check for task decomposition (GH-218)
	// Decomposition happens before timeout setup because subtasks have their own timeouts
	if r.decomposer != nil {
		result := r.decomposer.Decompose(task)
		if result.Decomposed && len(result.Subtasks) > 1 {
			r.log.Info("Task decomposed",
				slog.String("task_id", task.ID),
				slog.Int("subtask_count", len(result.Subtasks)),
				slog.String("reason", result.Reason),
			)
			return r.executeDecomposedTask(ctx, task, result.Subtasks)
		}
	}

	// Apply timeout based on task complexity
	timeout := r.modelRouter.SelectTimeout(task)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	log := r.log.With(
		slog.String("task_id", task.ID),
		slog.String("backend", r.backend.Name()),
		slog.String("complexity", complexity.String()),
		slog.Duration("timeout", timeout),
	)

	// Select model if routing is enabled
	selectedModel := r.modelRouter.SelectModel(task)
	if selectedModel != "" {
		log = log.With(slog.String("routed_model", selectedModel))
	}

	log.Info("Starting task execution",
		slog.String("project", task.ProjectPath),
		slog.String("branch", task.Branch),
		slog.Bool("create_pr", task.CreatePR),
	)

	// Emit task started event
	r.emitAlertEvent(AlertEvent{
		Type:      AlertEventTypeTaskStarted,
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Project:   task.ProjectPath,
		Timestamp: time.Now(),
	})

	// Dispatch webhook for task started
	r.dispatchWebhook(ctx, webhooks.EventTaskStarted, webhooks.TaskStartedData{
		TaskID:      task.ID,
		Title:       task.Title,
		Description: task.Description,
		Project:     task.ProjectPath,
		Source:      "pilot",
	})

	// Initialize git operations
	git := NewGitOperations(task.ProjectPath)

	// Create branch if specified (skip for direct commit mode)
	if task.Branch != "" && !task.DirectCommit {
		r.reportProgress(task.ID, "Branching", 3, "Switching to default branch...")

		// GH-279: Always switch to default branch and pull latest before creating new branch.
		// This prevents new branches from forking off previous pilot branches instead of main.
		defaultBranch, err := git.SwitchToDefaultBranchAndPull(ctx)
		if err != nil {
			log.Warn("Failed to switch to default branch, continuing from current branch",
				slog.String("task_id", task.ID),
				slog.Any("error", err),
			)
		} else {
			r.reportProgress(task.ID, "Branching", 5, fmt.Sprintf("On %s, creating %s...", defaultBranch, task.Branch))
		}

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

	// Run parallel research phase for medium/complex tasks (GH-217)
	var researchResult *ResearchResult
	if r.parallelRunner != nil && complexity.ShouldRunResearch() {
		r.reportProgress(task.ID, "Research", 10, "Running parallel research...")
		var researchErr error
		researchResult, researchErr = r.parallelRunner.ExecuteResearchPhase(ctx, task)
		if researchErr != nil {
			log.Warn("Research phase failed, continuing without research context",
				slog.String("task_id", task.ID),
				slog.Any("error", researchErr),
			)
		} else if researchResult != nil && len(researchResult.Findings) > 0 {
			log.Info("Research phase completed",
				slog.String("task_id", task.ID),
				slog.Int("findings", len(researchResult.Findings)),
				slog.Duration("duration", researchResult.Duration),
				slog.Int64("tokens", researchResult.TotalTokens),
			)
		}
	}

	// Build the prompt
	prompt := r.BuildPrompt(task)

	// Append research context if available (GH-217)
	if researchResult != nil && len(researchResult.Findings) > 0 {
		prompt = r.appendResearchContext(prompt, researchResult)
	}

	// State for tracking progress
	state := &progressState{phase: "Starting"}

	// Initialize recorder if recording is enabled
	var recorder *replay.Recorder
	if r.enableRecording {
		var recErr error
		recorder, recErr = replay.NewRecorder(task.ID, task.ProjectPath, r.getRecordingsPath())
		if recErr != nil {
			log.Warn("Failed to create recorder, continuing without recording", slog.Any("error", recErr))
		} else {
			recorder.SetBranch(task.Branch)
			log.Debug("Recording enabled", slog.String("recording_id", recorder.GetRecordingID()))
		}
	}

	// Report start
	backendName := r.backend.Name()
	r.reportProgress(task.ID, "Starting", 0, fmt.Sprintf("Initializing %s...", backendName))

	// Execute via backend
	backendResult, err := r.backend.Execute(ctx, ExecuteOptions{
		Prompt:      prompt,
		ProjectPath: task.ProjectPath,
		Verbose:     task.Verbose,
		Model:       selectedModel,
		EventHandler: func(event BackendEvent) {
			// Record the event
			if recorder != nil {
				if recErr := recorder.RecordEvent(event.Raw); recErr != nil {
					log.Warn("Failed to record event", slog.Any("error", recErr))
				}
			}

			// Process event for progress tracking
			r.processBackendEvent(task.ID, event, state)
		},
	})

	duration := time.Since(start)

	// Build execution result
	result := &ExecutionResult{
		TaskID:   task.ID,
		Duration: duration,
	}

	if err != nil {
		result.Success = false

		// Check if this was a timeout
		timedOut := ctx.Err() == context.DeadlineExceeded
		if timedOut {
			result.Error = fmt.Sprintf("task timed out after %v", timeout)
			log.Error("Task timed out",
				slog.String("task_id", task.ID),
				slog.String("complexity", complexity.String()),
				slog.Duration("timeout", timeout),
				slog.Duration("duration", duration),
			)
			r.reportProgress(task.ID, "Timeout", 100, result.Error)

			// Emit task timeout event with complexity metadata
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeTaskTimeout,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Project:   task.ProjectPath,
				Error:     result.Error,
				Metadata: map[string]string{
					"complexity":  complexity.String(),
					"timeout":     timeout.String(),
					"duration_ms": fmt.Sprintf("%d", duration.Milliseconds()),
				},
				Timestamp: time.Now(),
			})

			// Dispatch webhook for task timeout
			r.dispatchWebhook(ctx, webhooks.EventTaskTimeout, webhooks.TaskTimeoutData{
				TaskID:     task.ID,
				Title:      task.Title,
				Project:    task.ProjectPath,
				Duration:   duration,
				Timeout:    timeout,
				Complexity: complexity.String(),
				Phase:      state.phase,
			})
		} else {
			result.Error = err.Error()
			log.Error("Backend execution failed",
				slog.String("error", result.Error),
				slog.Duration("duration", duration),
			)
			r.reportProgress(task.ID, "Failed", 100, result.Error)

			// Emit task failed event
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeTaskFailed,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Project:   task.ProjectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})

			// Dispatch webhook for task failed (non-timeout)
			r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
				TaskID:   task.ID,
				Title:    task.Title,
				Project:  task.ProjectPath,
				Duration: duration,
				Error:    result.Error,
				Phase:    state.phase,
			})
		}

		// Finish recording with failed status
		if recorder != nil {
			recorder.SetModel(state.modelName)
			recorder.SetNavigator(state.hasNavigator)
			if finErr := recorder.Finish("failed"); finErr != nil {
				log.Warn("Failed to finish recording", slog.Any("error", finErr))
			}
		}
		return result, nil
	}

	// Copy backend result to execution result
	result.Success = backendResult.Success
	result.Output = backendResult.Output
	result.Error = backendResult.Error
	result.TokensInput = backendResult.TokensInput
	result.TokensOutput = backendResult.TokensOutput
	result.TokensTotal = backendResult.TokensInput + backendResult.TokensOutput
	result.ModelName = backendResult.Model

	// Track research phase tokens (GH-217)
	if researchResult != nil {
		result.ResearchTokens = researchResult.TotalTokens
		result.TokensTotal += researchResult.TotalTokens
	}

	// Extract commit SHA from state
	if len(state.commitSHAs) > 0 {
		result.CommitSHA = state.commitSHAs[len(state.commitSHAs)-1] // Use last commit
	}

	// Fill in additional metrics from state
	result.FilesChanged = state.filesWrite
	if result.ModelName == "" {
		result.ModelName = state.modelName
	}
	if result.ModelName == "" {
		result.ModelName = "claude-sonnet-4-5" // Default model
	}
	// Estimate cost based on token usage (including research tokens)
	result.EstimatedCostUSD = estimateCost(result.TokensInput+result.ResearchTokens, result.TokensOutput, result.ModelName)

	if !result.Success {
		log.Error("Task execution failed",
			slog.String("error", result.Error),
			slog.Duration("duration", duration),
		)
		r.reportProgress(task.ID, "Failed", 100, result.Error)

		// Emit task failed event
		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeTaskFailed,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Error:     result.Error,
			Timestamp: time.Now(),
		})

		// Dispatch webhook for task failed
		r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
			TaskID:   task.ID,
			Title:    task.Title,
			Project:  task.ProjectPath,
			Duration: duration,
			Error:    result.Error,
			Phase:    state.phase,
		})

		// Finish recording with failed status
		if recorder != nil {
			recorder.SetModel(result.ModelName)
			recorder.SetNavigator(state.hasNavigator)
			if finErr := recorder.Finish("failed"); finErr != nil {
				log.Warn("Failed to finish recording", slog.Any("error", finErr))
			}
		}
	} else {
		result.Success = true

		// Log execution metrics for observability (GH-54 speed optimization)
		metrics := NewExecutionMetrics(
			task.ID,
			complexity,
			result.ModelName,
			duration,
			state,
			timeout,
			false, // not timed out
		)
		log.Info("Task completed",
			slog.String("task_id", metrics.TaskID),
			slog.String("complexity", metrics.Complexity.String()),
			slog.String("model", metrics.Model),
			slog.Duration("duration", metrics.Duration),
			slog.Bool("navigator_skipped", metrics.NavigatorSkipped),
			slog.Int64("tokens_in", metrics.TokensIn),
			slog.Int64("tokens_out", metrics.TokensOut),
			slog.Float64("cost_usd", metrics.EstimatedCostUSD),
			slog.Int("files_read", metrics.FilesRead),
			slog.Int("files_written", metrics.FilesWritten),
		)
		r.reportProgress(task.ID, "Completed", 90, "Execution completed")

		// Auto-enable minimal build gate if not configured (GH-363)
		// This ensures broken code never becomes a PR, even without explicit quality config
		if r.qualityCheckerFactory == nil {
			buildCmd := quality.DetectBuildCommand(task.ProjectPath)
			if buildCmd != "" {
				log.Info("Auto-enabling build gate (no quality config)",
					slog.String("command", buildCmd),
				)

				// Create minimal quality checker with auto-detected build command
				minimalConfig := quality.MinimalBuildGate()
				minimalConfig.Gates[0].Command = buildCmd

				r.qualityCheckerFactory = func(taskID, projectPath string) QualityChecker {
					return &simpleQualityChecker{
						config:      minimalConfig,
						projectPath: projectPath,
						taskID:      taskID,
					}
				}
			}
		}

		// Run quality gates if configured
		if r.qualityCheckerFactory != nil {
			const maxAutoRetries = 2 // Circuit breaker to prevent infinite loops

			// Track quality gate results across retries (GH-209)
			var finalOutcome *QualityOutcome
			var totalQualityRetries int

			for retryAttempt := 0; retryAttempt <= maxAutoRetries; retryAttempt++ {
				r.reportProgress(task.ID, "Quality Gates", 91, "Running quality checks...")

				checker := r.qualityCheckerFactory(task.ID, task.ProjectPath)
				outcome, qErr := checker.Check(ctx)
				if qErr != nil {
					log.Error("Quality gate check error", slog.Any("error", qErr))
					result.Success = false
					result.Error = fmt.Sprintf("quality gate error: %v", qErr)
					r.reportProgress(task.ID, "Quality Failed", 100, result.Error)

					// Emit task failed event
					r.emitAlertEvent(AlertEvent{
						Type:      AlertEventTypeTaskFailed,
						TaskID:    task.ID,
						TaskTitle: task.Title,
						Project:   task.ProjectPath,
						Error:     result.Error,
						Timestamp: time.Now(),
					})

					// Dispatch webhook for task failed
					r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
						TaskID:   task.ID,
						Title:    task.Title,
						Project:  task.ProjectPath,
						Duration: time.Since(start),
						Error:    result.Error,
						Phase:    "Quality Gates",
					})

					if recorder != nil {
						recorder.SetModel(result.ModelName)
						recorder.SetNavigator(state.hasNavigator)
						if finErr := recorder.Finish("failed"); finErr != nil {
							log.Warn("Failed to finish recording", slog.Any("error", finErr))
						}
					}
					return result, nil
				}

				// Quality gates passed - exit retry loop
				if outcome.Passed {
					finalOutcome = outcome
					r.reportProgress(task.ID, "Quality Passed", 94, "All quality gates passed")

					// Run self-review phase (GH-364)
					if err := r.runSelfReview(ctx, task, state); err != nil {
						log.Warn("Self-review error", slog.Any("error", err))
						// Continue anyway - self-review is advisory
					}

					break
				}
				// Track this outcome for potential failure reporting
				finalOutcome = outcome

				// Quality gates failed
				log.Warn("Quality gates failed",
					slog.Bool("should_retry", outcome.ShouldRetry),
					slog.Int("attempt", outcome.Attempt),
					slog.Int("retry_attempt", retryAttempt),
				)

				// Check if we should retry with Claude Code
				if outcome.ShouldRetry && retryAttempt < maxAutoRetries {
					totalQualityRetries++ // Track total retries across all gates (GH-209)
					r.reportProgress(task.ID, "Quality Retry", 92,
						fmt.Sprintf("Fixing issues (attempt %d/%d)...", retryAttempt+1, maxAutoRetries))

					// Emit retry event
					r.emitAlertEvent(AlertEvent{
						Type:      AlertEventTypeTaskRetry,
						TaskID:    task.ID,
						TaskTitle: task.Title,
						Project:   task.ProjectPath,
						Metadata: map[string]string{
							"attempt":  strconv.Itoa(retryAttempt + 1),
							"feedback": truncateText(outcome.RetryFeedback, 500),
						},
						Timestamp: time.Now(),
					})

					// Build retry prompt with feedback
					retryPrompt := r.buildRetryPrompt(task, outcome.RetryFeedback, retryAttempt+1)

					log.Info("Re-invoking Claude Code with retry feedback",
						slog.String("task_id", task.ID),
						slog.Int("retry_attempt", retryAttempt+1),
					)

					// Re-invoke backend with retry prompt
					retryResult, retryErr := r.backend.Execute(ctx, ExecuteOptions{
						Prompt:      retryPrompt,
						ProjectPath: task.ProjectPath,
						Verbose:     task.Verbose,
						Model:       selectedModel,
						EventHandler: func(event BackendEvent) {
							if recorder != nil {
								if recErr := recorder.RecordEvent(event.Raw); recErr != nil {
									log.Warn("Failed to record retry event", slog.Any("error", recErr))
								}
							}
							r.processBackendEvent(task.ID, event, state)
						},
					})

					if retryErr != nil {
						log.Error("Retry execution failed", slog.Any("error", retryErr))
						result.Success = false
						result.Error = fmt.Sprintf("retry execution failed: %v", retryErr)
						r.reportProgress(task.ID, "Retry Failed", 100, result.Error)

						r.emitAlertEvent(AlertEvent{
							Type:      AlertEventTypeTaskFailed,
							TaskID:    task.ID,
							TaskTitle: task.Title,
							Project:   task.ProjectPath,
							Error:     result.Error,
							Timestamp: time.Now(),
						})

						// Dispatch webhook for task failed
						r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
							TaskID:   task.ID,
							Title:    task.Title,
							Project:  task.ProjectPath,
							Duration: time.Since(start),
							Error:    result.Error,
							Phase:    "Quality Retry",
						})

						if recorder != nil {
							recorder.SetModel(result.ModelName)
							recorder.SetNavigator(state.hasNavigator)
							if finErr := recorder.Finish("failed"); finErr != nil {
								log.Warn("Failed to finish recording", slog.Any("error", finErr))
							}
						}
						return result, nil
					}

					// Update result with retry execution stats
					result.TokensInput += retryResult.TokensInput
					result.TokensOutput += retryResult.TokensOutput
					result.TokensTotal = result.TokensInput + result.TokensOutput
					if retryResult.Model != "" {
						result.ModelName = retryResult.Model
					}

					// Extract new commit SHA if any
					if len(state.commitSHAs) > 0 {
						result.CommitSHA = state.commitSHAs[len(state.commitSHAs)-1]
					}

					// Continue to next iteration to re-check quality gates
					r.reportProgress(task.ID, "Re-testing", 93, "Re-running quality gates...")
					continue
				}

				// No more retries allowed - fail the task
				result.Success = false
				if retryAttempt >= maxAutoRetries {
					result.Error = fmt.Sprintf("quality gates failed after %d auto-retries", maxAutoRetries)
				} else {
					result.Error = "quality gates failed, max retries exhausted"
				}

				r.reportProgress(task.ID, "Quality Failed", 100, "Quality gates did not pass")

				// Emit task failed event
				r.emitAlertEvent(AlertEvent{
					Type:      AlertEventTypeTaskFailed,
					TaskID:    task.ID,
					TaskTitle: task.Title,
					Project:   task.ProjectPath,
					Error:     result.Error,
					Timestamp: time.Now(),
				})

				// Dispatch webhook for task failed
				r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
					TaskID:   task.ID,
					Title:    task.Title,
					Project:  task.ProjectPath,
					Duration: time.Since(start),
					Error:    result.Error,
					Phase:    "Quality Gates",
				})

				if recorder != nil {
					recorder.SetModel(result.ModelName)
					recorder.SetNavigator(state.hasNavigator)
					if finErr := recorder.Finish("failed"); finErr != nil {
						log.Warn("Failed to finish recording", slog.Any("error", finErr))
					}
				}
				return result, nil
			}

			// Populate quality gate results in ExecutionResult (GH-209)
			if finalOutcome != nil {
				result.QualityGates = r.buildQualityGatesResult(finalOutcome, totalQualityRetries)
			}
		}

		r.reportProgress(task.ID, "Finalizing", 95, "Preparing for completion")

		// Warn if PR creation requested but quality gates not configured (GH-248)
		if task.CreatePR && r.qualityCheckerFactory == nil {
			log.Warn("quality gates not configured - PR created without local validation",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
			)

			// Run self-review even without quality gates (GH-364)
			if err := r.runSelfReview(ctx, task, state); err != nil {
				log.Warn("Self-review error", slog.Any("error", err))
				// Continue anyway - self-review is advisory
			}
		}

		// Handle direct commit mode: push directly to main
		if task.DirectCommit {
			r.reportProgress(task.ID, "Pushing", 96, "Pushing to main...")

			if err := git.PushToMain(ctx); err != nil {
				result.Success = false
				result.Error = fmt.Sprintf("push to main failed: %v", err)
				r.reportProgress(task.ID, "Push Failed", 100, result.Error)
				return result, nil
			}

			// Get commit SHA for result
			sha, _ := git.GetCurrentCommitSHA(ctx)
			if sha != "" {
				result.CommitSHA = sha
			}

			log.Info("Direct commit pushed to main",
				slog.String("task_id", task.ID),
				slog.String("commit_sha", result.CommitSHA),
			)
			r.reportProgress(task.ID, "Completed", 100, "Pushed directly to main")
		} else if task.CreatePR && task.Branch != "" {
			// Create PR if requested and we have commits
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

			// Update recording with PR info
			if recorder != nil {
				recorder.SetPRUrl(prURL)
			}
		} else {
			r.reportProgress(task.ID, "Completed", 100, "Task completed successfully")
		}

		// Emit task completed event
		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeTaskCompleted,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Metadata: map[string]string{
				"duration_ms": fmt.Sprintf("%d", duration.Milliseconds()),
				"pr_url":      result.PRUrl,
			},
			Timestamp: time.Now(),
		})

		// Dispatch webhook for task completed
		r.dispatchWebhook(ctx, webhooks.EventTaskCompleted, webhooks.TaskCompletedData{
			TaskID:    task.ID,
			Title:     task.Title,
			Project:   task.ProjectPath,
			Duration:  duration,
			PRCreated: result.PRUrl != "",
			PRURL:     result.PRUrl,
		})

		// Finish recording with completed status
		if recorder != nil {
			recorder.SetCommitSHA(result.CommitSHA)
			recorder.SetModel(result.ModelName)
			recorder.SetNavigator(state.hasNavigator)
			if finErr := recorder.Finish("completed"); finErr != nil {
				log.Warn("Failed to finish recording", slog.Any("error", finErr))
			} else {
				log.Info("Recording saved", slog.String("recording_id", recorder.GetRecordingID()))
			}
		}

		// Sync Navigator index (GH-57) - update DEVELOPMENT-README.md
		if state.hasNavigator {
			if syncErr := r.syncNavigatorIndex(task, "completed"); syncErr != nil {
				log.Warn("Failed to sync Navigator index", slog.Any("error", syncErr))
			}
		}
	}

	return result, nil
}

// executeDecomposedTask runs subtasks sequentially and aggregates results (GH-218).
// Each subtask runs to completion before the next starts. Only the final subtask
// creates a PR (CreatePR is already set by the decomposer). All changes accumulate
// on the same branch, so the final PR contains all subtask work.
func (r *Runner) executeDecomposedTask(ctx context.Context, parentTask *Task, subtasks []*Task) (*ExecutionResult, error) {
	start := time.Now()
	totalSubtasks := len(subtasks)

	r.log.Info("Starting decomposed task execution",
		slog.String("parent_id", parentTask.ID),
		slog.Int("subtask_count", totalSubtasks),
	)

	// Emit parent task started event
	r.emitAlertEvent(AlertEvent{
		Type:      AlertEventTypeTaskStarted,
		TaskID:    parentTask.ID,
		TaskTitle: parentTask.Title,
		Project:   parentTask.ProjectPath,
		Metadata: map[string]string{
			"decomposed":    "true",
			"subtask_count": fmt.Sprintf("%d", totalSubtasks),
		},
		Timestamp: time.Now(),
	})

	// Dispatch webhook for decomposed task started
	r.dispatchWebhook(ctx, webhooks.EventTaskStarted, webhooks.TaskStartedData{
		TaskID:      parentTask.ID,
		Title:       parentTask.Title,
		Description: fmt.Sprintf("Decomposed into %d subtasks: %s", totalSubtasks, parentTask.Description),
		Project:     parentTask.ProjectPath,
		Source:      "pilot",
	})

	// Initialize git and create branch ONCE for all subtasks
	git := NewGitOperations(parentTask.ProjectPath)
	if parentTask.Branch != "" {
		r.reportProgress(parentTask.ID, "Branching", 1, "Switching to default branch...")

		// GH-279: Always switch to default branch and pull latest before creating new branch.
		// This prevents new branches from forking off previous pilot branches instead of main.
		defaultBranch, err := git.SwitchToDefaultBranchAndPull(ctx)
		if err != nil {
			r.log.Warn("Failed to switch to default branch, continuing from current branch",
				slog.String("task_id", parentTask.ID),
				slog.Any("error", err),
			)
		} else {
			r.reportProgress(parentTask.ID, "Branching", 2, fmt.Sprintf("On %s, creating %s...", defaultBranch, parentTask.Branch))
		}

		if err := git.CreateBranch(ctx, parentTask.Branch); err != nil {
			if switchErr := git.SwitchBranch(ctx, parentTask.Branch); switchErr != nil {
				return nil, fmt.Errorf("failed to create/switch branch: %w", err)
			}
		}
		r.reportProgress(parentTask.ID, "Branching", 5, fmt.Sprintf("Branch %s ready", parentTask.Branch))
	}

	// Aggregate result
	aggregateResult := &ExecutionResult{
		TaskID:  parentTask.ID,
		Success: true,
	}

	// Execute each subtask sequentially
	for i, subtask := range subtasks {
		subtaskNum := i + 1

		// Report progress with subtask counter
		progressPct := 5 + (85 * subtaskNum / totalSubtasks)
		r.reportProgress(parentTask.ID, "Decomposed", progressPct,
			fmt.Sprintf("Subtask %d/%d: %s", subtaskNum, totalSubtasks, truncateText(subtask.Title, 40)))

		r.log.Info("Executing subtask",
			slog.String("parent_id", parentTask.ID),
			slog.String("subtask_id", subtask.ID),
			slog.Int("index", subtaskNum),
			slog.Int("total", totalSubtasks),
		)

		// Execute subtask (recursively calls Execute, but subtasks won't decompose further)
		// Clear the branch since we already created it
		subtask.Branch = ""

		// Temporarily disable decomposer to prevent recursive decomposition
		savedDecomposer := r.decomposer
		r.decomposer = nil

		subtaskResult, err := r.Execute(ctx, subtask)

		// Restore decomposer
		r.decomposer = savedDecomposer

		if err != nil {
			r.log.Error("Subtask execution error",
				slog.String("subtask_id", subtask.ID),
				slog.Any("error", err),
			)
			aggregateResult.Success = false
			aggregateResult.Error = fmt.Sprintf("subtask %d/%d failed: %v", subtaskNum, totalSubtasks, err)
			break
		}

		if !subtaskResult.Success {
			r.log.Warn("Subtask failed",
				slog.String("subtask_id", subtask.ID),
				slog.String("error", subtaskResult.Error),
			)
			aggregateResult.Success = false
			aggregateResult.Error = fmt.Sprintf("subtask %d/%d failed: %s", subtaskNum, totalSubtasks, subtaskResult.Error)
			break
		}

		// Aggregate metrics
		aggregateResult.TokensInput += subtaskResult.TokensInput
		aggregateResult.TokensOutput += subtaskResult.TokensOutput
		aggregateResult.TokensTotal += subtaskResult.TokensTotal
		aggregateResult.ResearchTokens += subtaskResult.ResearchTokens
		aggregateResult.FilesChanged += subtaskResult.FilesChanged
		aggregateResult.LinesAdded += subtaskResult.LinesAdded
		aggregateResult.LinesRemoved += subtaskResult.LinesRemoved

		// Keep last commit SHA and PR URL
		if subtaskResult.CommitSHA != "" {
			aggregateResult.CommitSHA = subtaskResult.CommitSHA
		}
		if subtaskResult.PRUrl != "" {
			aggregateResult.PRUrl = subtaskResult.PRUrl
		}
		if subtaskResult.ModelName != "" {
			aggregateResult.ModelName = subtaskResult.ModelName
		}

		// Track quality gates from final subtask
		if subtask.CreatePR && subtaskResult.QualityGates != nil {
			aggregateResult.QualityGates = subtaskResult.QualityGates
		}

		r.log.Info("Subtask completed",
			slog.String("subtask_id", subtask.ID),
			slog.Int("index", subtaskNum),
			slog.Int("total", totalSubtasks),
		)
	}

	aggregateResult.Duration = time.Since(start)
	aggregateResult.EstimatedCostUSD = estimateCost(
		aggregateResult.TokensInput+aggregateResult.ResearchTokens,
		aggregateResult.TokensOutput,
		aggregateResult.ModelName,
	)

	// Emit completion event
	if aggregateResult.Success {
		r.reportProgress(parentTask.ID, "Completed", 100,
			fmt.Sprintf("All %d subtasks completed", totalSubtasks))

		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeTaskCompleted,
			TaskID:    parentTask.ID,
			TaskTitle: parentTask.Title,
			Project:   parentTask.ProjectPath,
			Metadata: map[string]string{
				"duration_ms":   fmt.Sprintf("%d", aggregateResult.Duration.Milliseconds()),
				"pr_url":        aggregateResult.PRUrl,
				"subtask_count": fmt.Sprintf("%d", totalSubtasks),
			},
			Timestamp: time.Now(),
		})

		r.dispatchWebhook(ctx, webhooks.EventTaskCompleted, webhooks.TaskCompletedData{
			TaskID:    parentTask.ID,
			Title:     parentTask.Title,
			Project:   parentTask.ProjectPath,
			Duration:  aggregateResult.Duration,
			PRCreated: aggregateResult.PRUrl != "",
			PRURL:     aggregateResult.PRUrl,
		})
	} else {
		r.reportProgress(parentTask.ID, "Failed", 100, aggregateResult.Error)

		r.emitAlertEvent(AlertEvent{
			Type:      AlertEventTypeTaskFailed,
			TaskID:    parentTask.ID,
			TaskTitle: parentTask.Title,
			Project:   parentTask.ProjectPath,
			Error:     aggregateResult.Error,
			Timestamp: time.Now(),
		})

		r.dispatchWebhook(ctx, webhooks.EventTaskFailed, webhooks.TaskFailedData{
			TaskID:   parentTask.ID,
			Title:    parentTask.Title,
			Project:  parentTask.ProjectPath,
			Duration: aggregateResult.Duration,
			Error:    aggregateResult.Error,
			Phase:    "Decomposed",
		})
	}

	return aggregateResult, nil
}

// Cancel terminates a running task by killing its Claude Code process.
// Returns an error if the task is not currently running.
func (r *Runner) Cancel(taskID string) error {
	r.mu.Lock()
	cmd, ok := r.running[taskID]
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("task %s is not running", taskID)
	}

	return cmd.Process.Kill()
}

// IsRunning returns true if the specified task is currently being executed.
func (r *Runner) IsRunning(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[taskID]
	return ok
}

// BuildPrompt constructs the prompt string sent to Claude Code for a task.
// It adapts the prompt based on whether the project uses Navigator, adding
// appropriate workflow instructions. Trivial tasks (typos, logs, comments)
// skip Navigator overhead for faster execution. Exported for dry-run preview.
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

	// Detect task complexity for routing decisions (GH-216)
	complexity := DetectComplexity(task)

	// Skip Navigator for trivial tasks even if .agent/ exists (GH-216)
	// This reduces overhead for typos, logging, comments, renames, etc.
	useNavigator := hasNavigator && !complexity.ShouldSkipNavigator()

	// Navigator-aware prompt structure for medium/complex tasks
	if useNavigator {
		// Navigator handles workflow, autonomous completion, and documentation
		sb.WriteString("Start my Navigator session.\n\n")

		// CRITICAL: Override CLAUDE.md rules meant for human sessions (GH-265)
		// Project CLAUDE.md may contain "DO NOT write code" rules for human Navigator
		// sessions. Pilot IS the execution bot - it MUST write code and commit.
		sb.WriteString("## PILOT EXECUTION MODE\n\n")
		sb.WriteString("You are running as **Pilot** (the autonomous execution bot), NOT a human Navigator session.\n")
		sb.WriteString("IGNORE any CLAUDE.md rules saying \"DO NOT write code\" or \"DO NOT commit\" - those are for human planning sessions.\n")
		sb.WriteString("Your job is to IMPLEMENT, COMMIT, and optionally CREATE PRs.\n\n")

		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("Create branch `%s` before starting.\n\n", task.Branch))
		}

		// Navigator will handle: workflow check, complexity detection, autonomous completion
		sb.WriteString("Run until done. Use Navigator's autonomous completion protocol.\n\n")

		// Pre-commit verification checklist (GH-359)
		sb.WriteString("## Pre-Commit Verification\n\n")
		sb.WriteString("BEFORE committing, verify:\n")
		sb.WriteString("1. **Build passes**: Run `go build ./...` (or equivalent for the project)\n")
		sb.WriteString("2. **Config wiring**: Any new config struct fields must flow from yaml → main.go → handler\n")
		sb.WriteString("3. **Methods exist**: Any method calls you added must have implementations\n")
		sb.WriteString("4. **Tests pass**: Run `go test ./...` for changed packages\n\n")
		sb.WriteString("If any verification fails, fix it before committing.\n\n")

		sb.WriteString("CRITICAL: You MUST commit all changes before completing. A task is NOT complete until changes are committed. Use format: `type(scope): description (TASK-XX)`\n")
	} else if hasNavigator && complexity.ShouldSkipNavigator() {
		// Trivial task in Navigator project - minimal prompt without Navigator overhead (GH-216)
		// Still need Pilot execution mode notice since CLAUDE.md may have "don't write code" rules
		sb.WriteString("## PILOT EXECUTION MODE (Trivial Task)\n\n")
		sb.WriteString("You are **Pilot** (execution bot). IGNORE any CLAUDE.md \"DO NOT write code\" rules.\n\n")

		sb.WriteString(fmt.Sprintf("## Task: %s\n\n", task.ID))
		sb.WriteString(fmt.Sprintf("%s\n\n", task.Description))
		sb.WriteString("## Instructions\n\n")
		sb.WriteString("This is a trivial change. Execute quickly without Navigator workflow.\n\n")

		if task.Branch != "" {
			sb.WriteString(fmt.Sprintf("1. Create git branch: `%s`\n", task.Branch))
		} else {
			sb.WriteString("1. Work on current branch\n")
		}

		sb.WriteString("2. Make the minimal change required\n")
		sb.WriteString("3. Verify build passes before committing\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n\n")
		sb.WriteString("Work autonomously. Do not ask for confirmation.\n")
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
		sb.WriteString("3. Before committing, verify: build passes, tests pass, no undefined methods\n")
		sb.WriteString("4. Commit with format: `type(scope): description`\n")
		sb.WriteString("\nWork autonomously. Do not ask for confirmation.\n")
	}

	return sb.String()
}

// buildRetryPrompt constructs a prompt for Claude Code to fix quality gate failures.
// It includes the original task context and the specific error feedback to address.
func (r *Runner) buildRetryPrompt(task *Task, feedback string, attempt int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Quality Gate Retry (Attempt %d)\n\n", attempt))
	sb.WriteString("The previous implementation attempt failed quality gates. Please fix the issues below.\n\n")
	sb.WriteString(feedback)
	sb.WriteString("\n\n")
	sb.WriteString("## Original Task Context\n\n")
	sb.WriteString(fmt.Sprintf("Task: %s\n", task.ID))
	sb.WriteString(fmt.Sprintf("Title: %s\n\n", task.Title))
	sb.WriteString("## Instructions\n\n")
	sb.WriteString("1. Review the error output above carefully\n")
	sb.WriteString("2. Fix the issues in the affected files\n")
	sb.WriteString("3. Ensure all tests pass\n")
	sb.WriteString("4. Commit your fixes with a descriptive message\n\n")
	sb.WriteString("Work autonomously. Do not ask for confirmation.\n")

	return sb.String()
}

// runSelfReview executes a self-review phase where Claude examines its changes.
// This catches issues like unwired config, undefined methods, or incomplete implementations.
// Returns nil if review passes or is skipped, error only for critical failures.
func (r *Runner) runSelfReview(ctx context.Context, task *Task, state *progressState) error {
	// Skip self-review if disabled in config
	if r.config != nil && r.config.SkipSelfReview {
		r.log.Debug("Self-review skipped (disabled in config)", slog.String("task_id", task.ID))
		return nil
	}

	// Skip for trivial tasks - they don't need self-review
	complexity := DetectComplexity(task)
	if complexity.ShouldSkipNavigator() {
		r.log.Debug("Self-review skipped (trivial task)", slog.String("task_id", task.ID))
		return nil
	}

	r.log.Info("Running self-review phase", slog.String("task_id", task.ID))
	r.reportProgress(task.ID, "Self-Review", 95, "Reviewing changes...")

	reviewPrompt := r.buildSelfReviewPrompt(task)

	// Execute self-review with shorter timeout (2 minutes)
	reviewCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// Select model (use same routing as main execution)
	selectedModel := r.modelRouter.SelectModel(task)

	result, err := r.backend.Execute(reviewCtx, ExecuteOptions{
		Prompt:      reviewPrompt,
		ProjectPath: task.ProjectPath,
		Verbose:     task.Verbose,
		Model:       selectedModel,
		EventHandler: func(event BackendEvent) {
			// Track tokens from self-review
			state.tokensInput += event.TokensInput
			state.tokensOutput += event.TokensOutput
			// Extract any new commit SHAs from self-review fixes
			if event.Type == EventTypeToolResult && event.ToolResult != "" {
				extractCommitSHA(event.ToolResult, state)
			}
		},
	})

	if err != nil {
		// Self-review failure is not fatal - log and continue
		r.log.Warn("Self-review execution failed",
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return nil
	}

	// Check if review found and fixed issues
	if strings.Contains(result.Output, "REVIEW_FIXED:") {
		r.log.Info("Self-review fixed issues",
			slog.String("task_id", task.ID),
		)
		r.reportProgress(task.ID, "Self-Review", 97, "Issues fixed during review")
	} else if strings.Contains(result.Output, "REVIEW_PASSED") {
		r.log.Info("Self-review passed",
			slog.String("task_id", task.ID),
		)
		r.reportProgress(task.ID, "Self-Review", 97, "Review passed")
	} else {
		r.log.Debug("Self-review completed (no explicit signal)",
			slog.String("task_id", task.ID),
		)
	}

	return nil
}

// buildSelfReviewPrompt constructs the prompt for self-review phase.
// The prompt instructs Claude to examine its changes for common issues
// and fix them before PR creation.
func (r *Runner) buildSelfReviewPrompt(task *Task) string {
	var sb strings.Builder

	sb.WriteString("## Self-Review Phase\n\n")
	sb.WriteString("Review the changes you just made for completeness. Run these checks:\n\n")

	sb.WriteString("### 1. Diff Analysis\n")
	sb.WriteString("```bash\ngit diff --cached\n```\n")
	sb.WriteString("Examine your staged changes. Look for:\n")
	sb.WriteString("- Methods called that don't exist\n")
	sb.WriteString("- Struct fields added but never used\n")
	sb.WriteString("- Config fields that aren't wired through\n")
	sb.WriteString("- Import statements for unused packages\n\n")

	sb.WriteString("### 2. Build Verification\n")
	sb.WriteString("```bash\ngo build ./...\n```\n")
	sb.WriteString("If build fails, fix the errors.\n\n")

	sb.WriteString("### 3. Wiring Check\n")
	sb.WriteString("For any NEW struct fields you added:\n")
	sb.WriteString("- Search for the field name in the codebase\n")
	sb.WriteString("- Verify the field is assigned when creating the struct\n")
	sb.WriteString("- Verify the field is used somewhere\n\n")

	sb.WriteString("### 4. Method Existence Check\n")
	sb.WriteString("For any NEW method calls you added:\n")
	sb.WriteString("- Search for `func.*methodName` to verify the method exists\n")
	sb.WriteString("- If method doesn't exist, implement it\n\n")

	sb.WriteString("### Actions\n")
	sb.WriteString("- If you find issues: FIX them and commit the fix\n")
	sb.WriteString("- Output `REVIEW_FIXED: <description>` if you fixed something\n")
	sb.WriteString("- Output `REVIEW_PASSED` if everything looks good\n\n")

	sb.WriteString("Work autonomously. Fix any issues you find.\n")

	return sb.String()
}

// appendResearchContext adds research findings to the prompt (GH-217).
// Research context is inserted before the task instructions to provide
// codebase context gathered by parallel research subagents.
func (r *Runner) appendResearchContext(prompt string, research *ResearchResult) string {
	if research == nil || len(research.Findings) == 0 {
		return prompt
	}

	var sb strings.Builder

	// Insert research context after the task header but before instructions
	sb.WriteString(prompt)
	sb.WriteString("\n\n")
	sb.WriteString("## Pre-Research Context\n\n")
	sb.WriteString("The following context was gathered by parallel research subagents:\n\n")

	for i, finding := range research.Findings {
		// Limit individual findings to prevent prompt bloat
		trimmed := finding
		if len(trimmed) > 2000 {
			trimmed = trimmed[:2000] + "\n... (truncated)"
		}
		sb.WriteString(fmt.Sprintf("### Research Finding %d\n\n%s\n\n", i+1, trimmed))
	}

	sb.WriteString("Use this context to inform your implementation. Do not repeat the research.\n\n")

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
		// Report token usage to callbacks (e.g., dashboard)
		r.reportTokens(taskID, state.tokensInput, state.tokensOutput)
	}
	if event.Model != "" && state.modelName == "" {
		state.modelName = event.Model
	}

	return "", ""
}

// processBackendEvent handles events from any backend and updates progress state.
// This is the unified event handler that works with both Claude Code and OpenCode.
func (r *Runner) processBackendEvent(taskID string, event BackendEvent, state *progressState) {
	// Track token usage
	state.tokensInput += event.TokensInput
	state.tokensOutput += event.TokensOutput
	if event.Model != "" {
		state.modelName = event.Model
	}

	// Report token usage to callbacks (e.g., dashboard)
	if event.TokensInput > 0 || event.TokensOutput > 0 {
		r.reportTokens(taskID, state.tokensInput, state.tokensOutput)
	}

	switch event.Type {
	case EventTypeInit:
		r.reportProgress(taskID, "🚀 Started", 5, event.Message)

	case EventTypeText:
		// Parse Navigator-specific patterns from text
		if event.Message != "" {
			r.parseNavigatorPatterns(taskID, event.Message, state)
		}

	case EventTypeToolUse:
		r.handleToolUse(taskID, event.ToolName, event.ToolInput, state)

	case EventTypeToolResult:
		// Extract commit SHA from tool output
		if event.ToolResult != "" {
			extractCommitSHA(event.ToolResult, state)
		}

	case EventTypeResult:
		r.log.Debug("Backend result received",
			slog.String("task_id", taskID),
			slog.Bool("is_error", event.IsError),
		)

	case EventTypeError:
		r.log.Warn("Backend error", slog.String("task_id", taskID), slog.String("error", event.Message))

	case EventTypeProgress:
		// Progress events may contain phase information
		if event.Phase != "" {
			r.handleNavigatorPhase(taskID, event.Phase, state)
		}
	}
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

// emitAlertEvent sends an event to the alert processor if configured
func (r *Runner) emitAlertEvent(event AlertEvent) {
	if r.alertProcessor == nil {
		return
	}
	r.alertProcessor.ProcessEvent(event)
}

// dispatchWebhook sends a webhook event if webhook manager is configured
func (r *Runner) dispatchWebhook(ctx context.Context, eventType webhooks.EventType, data any) {
	if r.webhooks == nil {
		return
	}
	event := webhooks.NewEvent(eventType, data)
	r.webhooks.Dispatch(ctx, event)
}

// reportProgress sends a progress update to all registered callbacks
func (r *Runner) reportProgress(taskID, phase string, progress int, message string) {
	// Log progress unless suppressed (e.g., when visual progress display is active)
	if !r.suppressProgressLogs {
		r.log.Info("Task progress",
			slog.String("task_id", taskID),
			slog.String("phase", phase),
			slog.Int("progress", progress),
			slog.String("message", message),
		)
	}

	// Send to legacy callback (e.g., Telegram) if registered
	if r.onProgress != nil {
		r.onProgress(taskID, phase, progress, message)
	}

	// Send to all named callbacks (e.g., dashboard, monitors)
	r.progressMu.RLock()
	callbacks := make([]ProgressCallback, 0, len(r.progressCallbacks))
	for _, cb := range r.progressCallbacks {
		callbacks = append(callbacks, cb)
	}
	r.progressMu.RUnlock()

	for _, cb := range callbacks {
		cb(taskID, phase, progress, message)
	}
}

// syncNavigatorIndex updates DEVELOPMENT-README.md after task completion.
// It moves completed tasks from "In Progress" to "Completed" section.
// Supports both TASK-XX and GH-XX formats.
func (r *Runner) syncNavigatorIndex(task *Task, status string) error {
	indexPath := filepath.Join(task.ProjectPath, ".agent", "DEVELOPMENT-README.md")

	// Check if Navigator index exists
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		r.log.Debug("Navigator index not found, skipping sync",
			slog.String("task_id", task.ID),
			slog.String("path", indexPath),
		)
		return nil
	}

	// Read current index
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("failed to read navigator index: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var result []string
	var taskEntry string
	var taskTitle string
	inProgressSection := false
	completedSection := false
	completedInsertIdx := -1

	// Extract task number for matching (handles GH-XX and TASK-XX)
	taskNum := extractTaskNumber(task.ID)

	for i, line := range lines {
		// Track sections
		if strings.Contains(line, "### In Progress") {
			inProgressSection = true
			completedSection = false
			result = append(result, line)
			continue
		}
		if strings.Contains(line, "### Backlog") || strings.Contains(line, "## Completed") {
			inProgressSection = false
		}
		if strings.Contains(line, "## Completed") {
			completedSection = true
			result = append(result, line)
			// Find where to insert (after the header and any date line)
			completedInsertIdx = len(result)
			continue
		}
		if completedSection && strings.HasPrefix(strings.TrimSpace(line), "| Item") {
			// After table header in completed section
			result = append(result, line)
			completedInsertIdx = len(result)
			continue
		}
		if completedSection && strings.HasPrefix(strings.TrimSpace(line), "|---") {
			result = append(result, line)
			completedInsertIdx = len(result)
			continue
		}

		// Check if this line contains our task in the In Progress table
		if inProgressSection && strings.Contains(line, "|") {
			// Table row format: | GH# | Title | Status |
			// or: | 54 | Speed Optimization ... | 🔄 Pilot executing |
			if strings.Contains(line, task.ID) || (taskNum != "" && containsTaskNumber(line, taskNum)) {
				// Extract title from the row
				parts := strings.Split(line, "|")
				if len(parts) >= 3 {
					taskTitle = strings.TrimSpace(parts[2]) // Title is second column after GH#
				}
				taskEntry = line
				// Skip this line (don't add to result) - we'll move it to completed
				continue
			}
		}

		result = append(result, line)
		_ = i // suppress unused warning
	}

	// If we found a task entry to move
	if taskEntry != "" && completedInsertIdx >= 0 {
		// Create completed entry
		completedEntry := fmt.Sprintf("| %s | %s |", task.ID, taskTitle)

		// Insert at the right position
		newResult := make([]string, 0, len(result)+1)
		newResult = append(newResult, result[:completedInsertIdx]...)
		newResult = append(newResult, completedEntry)
		newResult = append(newResult, result[completedInsertIdx:]...)
		result = newResult

		// Write updated index
		if err := os.WriteFile(indexPath, []byte(strings.Join(result, "\n")), 0644); err != nil {
			return fmt.Errorf("failed to write navigator index: %w", err)
		}

		r.log.Info("Updated Navigator index",
			slog.String("task_id", task.ID),
			slog.String("status", status),
			slog.String("moved_to", "Completed"),
		)
	} else if taskEntry != "" {
		r.log.Debug("Task found but no Completed section to move to",
			slog.String("task_id", task.ID),
		)
	} else {
		r.log.Debug("Task not found in Navigator index In Progress section",
			slog.String("task_id", task.ID),
		)
	}

	return nil
}

// extractTaskNumber extracts the numeric part from task IDs like "GH-57" or "TASK-123"
func extractTaskNumber(taskID string) string {
	// Handle GH-XX format
	if strings.HasPrefix(taskID, "GH-") {
		return strings.TrimPrefix(taskID, "GH-")
	}
	// Handle TASK-XX format
	if strings.HasPrefix(taskID, "TASK-") {
		return strings.TrimPrefix(taskID, "TASK-")
	}
	return taskID
}

// containsTaskNumber checks if a line contains a task number in various formats
func containsTaskNumber(line, taskNum string) bool {
	// Check for "| 57 |" or "| GH-57 |" or "| TASK-57 |" patterns
	patterns := []string{
		fmt.Sprintf("| %s ", taskNum),
		fmt.Sprintf("|%s ", taskNum),
		fmt.Sprintf("| %s|", taskNum),
		fmt.Sprintf("|%s|", taskNum),
		fmt.Sprintf("GH-%s", taskNum),
		fmt.Sprintf("TASK-%s", taskNum),
	}
	for _, p := range patterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// buildQualityGatesResult converts QualityOutcome to QualityGatesResult for ExecutionResult (GH-209)
func (r *Runner) buildQualityGatesResult(outcome *QualityOutcome, totalRetries int) *QualityGatesResult {
	if outcome == nil {
		return nil
	}

	qgResult := &QualityGatesResult{
		Enabled:       true,
		AllPassed:     outcome.Passed,
		TotalDuration: outcome.TotalDuration,
		TotalRetries:  totalRetries,
		Gates:         make([]QualityGateResult, len(outcome.GateDetails)),
	}

	for i, detail := range outcome.GateDetails {
		qgResult.Gates[i] = QualityGateResult(detail)
	}

	return qgResult
}

// simpleQualityChecker is a minimal quality checker for auto-enabled build gates (GH-363).
// Used when quality gates aren't explicitly configured but we still want basic build verification.
type simpleQualityChecker struct {
	config      *quality.Config
	projectPath string
	taskID      string
}

// Check runs the build gate and returns the outcome.
func (c *simpleQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	runner := quality.NewRunner(c.config, c.projectPath)

	results, err := runner.RunAll(ctx, c.taskID)
	if err != nil {
		return nil, err
	}

	// Convert to QualityOutcome
	outcome := &QualityOutcome{
		Passed:        results.AllPassed,
		ShouldRetry:   !results.AllPassed && c.config.OnFailure.Action == quality.ActionRetry,
		TotalDuration: results.TotalTime,
		GateDetails:   make([]QualityGateDetail, 0, len(results.Results)),
	}

	// Build retry feedback if failed
	if !results.AllPassed {
		outcome.RetryFeedback = quality.FormatErrorFeedback(results)
	}

	for _, r := range results.Results {
		outcome.GateDetails = append(outcome.GateDetails, QualityGateDetail{
			Name:       r.GateName,
			Passed:     r.Status == quality.StatusPassed,
			Duration:   r.Duration,
			RetryCount: r.RetryCount,
			Error:      r.Error,
		})
	}

	return outcome, nil
}

// ExtractRepoName extracts the repository name from "owner/repo" format.
// Returns just the repo part (e.g., "pilot" from "alekspetrov/pilot").
func ExtractRepoName(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repo
}

// ValidateRepoProjectMatch validates that a source repo matches the project path.
// This is a defense against cross-project execution (GH-386).
// Returns an error if there's a mismatch between repo name and project directory name.
func ValidateRepoProjectMatch(sourceRepo, projectPath string) error {
	if sourceRepo == "" || projectPath == "" {
		return nil // Nothing to validate
	}

	repoName := ExtractRepoName(sourceRepo)
	projectDir := filepath.Base(projectPath)

	// Normalize for comparison (case-insensitive)
	repoName = strings.ToLower(repoName)
	projectDir = strings.ToLower(projectDir)

	if repoName != projectDir {
		return fmt.Errorf(
			"repo/project mismatch: issue from '%s' but executing in '%s' (expected project directory '%s')",
			sourceRepo, projectPath, repoName,
		)
	}

	return nil
}
