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
	"syscall"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
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
	// Session ID for resume support (GH-1265)
	SessionID string `json:"session_id,omitempty"`
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

// ContentBlock represents content in assistant messages.
// Also used for tool_result blocks in user messages (Qwen Code sends these
// in message.content[] instead of Claude Code's flat tool_use_result field).
type ContentBlock struct {
	Type    string                 `json:"type"`
	Text    string                 `json:"text,omitempty"`
	Content string                 `json:"content,omitempty"` // For tool_result blocks
	Name    string                 `json:"name,omitempty"`
	Input   map[string]interface{} `json:"input,omitempty"`
	IsError bool                   `json:"is_error,omitempty"` // For tool_result blocks
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
	// Intent judge retry tracking (GH-624)
	intentRetried bool // Set after first intent retry to prevent infinite loops
	// Budget enforcement (GH-539)
	budgetExceeded bool               // Set when per-task token/duration limit is exceeded
	budgetReason   string             // Human-readable reason for budget cancellation
	budgetCancel   context.CancelFunc // Cancel function to terminate execution on budget breach
	// Smart retry tracking (GH-920)
	smartRetryAttempt int // Current retry attempt for error-based retries
	// Session resume support (GH-1265)
	sessionID string // Claude Code session ID for resume in self-review
	// Modified files tracking (GH-1388)
	modifiedFiles []string // List of actually modified files from Write/Edit tool events
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
	// MemberID is the team member ID for permission checks (GH-634).
	// When set and a TeamChecker is configured, the runner enforces RBAC before execution.
	MemberID string
	// Labels contains issue labels (e.g., "no-decompose", "pilot").
	// Flows from GitHub/Linear adapter → executor for decomposition decisions (GH-727).
	Labels []string
	// AcceptanceCriteria contains extracted acceptance criteria from the issue body (GH-920).
	// When present, included in the prompt and verified before commit.
	AcceptanceCriteria []string
	// FromPR is the PR number to resume session context from (GH-1267).
	// When set and UseFromPR is enabled, uses --from-pr <N> to resume the session
	// linked to the original PR, giving Claude full context of previous changes.
	// Typically set for autopilot-fix issues to continue from the failed PR's session.
	FromPR int
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
	// IntentWarning contains the reason if the intent judge flagged a mismatch.
	// When set, the PR was created despite intent misalignment (after retry failed).
	IntentWarning string
}

// ProgressCallback is a function called during execution with progress updates.
// It receives the task ID, current phase name, progress percentage (0-100),
// and a human-readable message describing the current activity.
type ProgressCallback func(taskID string, phase string, progress int, message string)

// TokenCallback is a function called during execution with token usage updates.
// It receives the task ID, input tokens, and output tokens.
type TokenCallback func(taskID string, inputTokens, outputTokens int64)

// TokenLimitCallback is called during execution with per-event token deltas.
// It returns true if execution should continue, false if the per-task token/duration
// limit has been exceeded and execution should be cancelled.
type TokenLimitCallback func(taskID string, deltaInput, deltaOutput int64) bool

// SubIssuePRCallback is called when a sub-issue PR is created during epic execution.
// Signature matches Controller.OnPRCreated so it can be wired directly.
type SubIssuePRCallback func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string)

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
	recordingsPath        string                                                          // Path to recordings directory (empty = default)
	enableRecording       bool                                                            // Whether to record executions
	alertProcessor        AlertEventProcessor                                             // Optional alert processor for event emission
	webhooks              *webhooks.Manager                                               // Optional webhook manager for event delivery
	qualityCheckerFactory QualityCheckerFactory                                           // Optional factory for creating quality checkers
	modelRouter           *ModelRouter                                                    // Model and timeout routing based on complexity
	parallelRunner        *ParallelRunner                                                 // Optional parallel research runner (GH-217)
	decomposer            *TaskDecomposer                                                 // Optional task decomposer for complex tasks (GH-218)
	subtaskParser         *SubtaskParser                                                  // Haiku-based subtask parser; nil falls back to regex (GH-501)
	suppressProgressLogs  bool                                                            // Suppress slog output for progress (use when visual display is active)
	tokenLimitCheck       TokenLimitCallback                                              // Optional per-task token/duration limit check (GH-539)
	onSubIssuePRCreated   SubIssuePRCallback                                              // Optional callback when a sub-issue PR is created (GH-596)
	intentJudge           *IntentJudge                                                    // Optional intent judge for diff-vs-ticket alignment (GH-624)
	teamChecker           TeamChecker                                                     // Optional team RBAC checker (GH-633)
	executeFunc           func(ctx context.Context, task *Task) (*ExecutionResult, error) // Internal override for testing
	skipPreflightChecks   bool                                                            // Skip preflight checks (for testing with mock backends)
	retrier               *Retrier                                                        // Optional smart retry handler (GH-920)
	signalParser          *SignalParser                                                   // Structured signal parser v2 for progress extraction (GH-960)
	knowledge             *memory.KnowledgeStore                                          // Optional knowledge store for experiential memories (GH-994)
	profileManager        *memory.ProfileManager                                          // Optional profile manager for user preferences (GH-994)
	driftDetector         *DriftDetector                                                  // Optional drift detector for collaboration drift (GH-997)
	monitor               *Monitor                                                        // Optional monitor for state transitions (queued→running)
	taskProgress          map[string]int                                                  // Per-task progress high-water mark (monotonic enforcement)
	taskProgressMu        sync.RWMutex                                                    // Protects taskProgress
	// GH-1077: AGENTS.md caching
	agentsContent         string // Cached AGENTS.md content, loaded once per Runner
	agentsProjectPath     string // Project path for agents cache (invalidate on change)
	agentsMu              sync.RWMutex // Protects agents cache
	// GH-1078: Worktree pooling
	worktreeManager       *WorktreeManager // Optional worktree manager with pool support
}

// NewRunner creates a new Runner instance with Claude Code backend by default.
// The Runner is ready to execute tasks immediately after creation.
func NewRunner() *Runner {
	log := logging.WithComponent("executor")
	return &Runner{
		backend:           NewClaudeCodeBackend(nil),
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		taskProgress:      make(map[string]int),
		log:               log,
		enableRecording:   true, // Recording enabled by default
		modelRouter:       NewModelRouter(nil, nil),
		signalParser:      NewSignalParser(log),
	}
}

// NewRunnerWithBackend creates a Runner with a specific backend.
func NewRunnerWithBackend(backend Backend) *Runner {
	if backend == nil {
		backend = NewClaudeCodeBackend(nil)
	}
	log := logging.WithComponent("executor")
	return &Runner{
		backend:           backend,
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		taskProgress:      make(map[string]int),
		log:               log,
		enableRecording:   true,
		modelRouter:       NewModelRouter(nil, nil),
		signalParser:      NewSignalParser(log),
	}
}

// NewRunnerWithConfig creates a Runner from backend configuration.
func NewRunnerWithConfig(config *BackendConfig) (*Runner, error) {
	// Ensure we have a valid config (GH-956: nil config breaks worktree)
	if config == nil {
		slog.Warn("NewRunnerWithConfig called with nil config, using defaults")
		config = DefaultBackendConfig()
	} else {
		slog.Info("NewRunnerWithConfig",
			slog.Bool("use_worktree", config.UseWorktree),
			slog.String("type", config.Type),
		)
	}
	backend, err := NewBackend(config)
	if err != nil {
		return nil, err
	}
	runner := NewRunnerWithBackend(backend)
	runner.config = config

	// Configure model routing, timeouts, and effort from config
	if config != nil {
		runner.modelRouter = NewModelRouterWithEffort(config.ModelRouting, config.Timeout, config.EffortRouting)

		// GH-727: Attach LLM effort classifier if enabled
		// Uses Claude Code subprocess with Haiku - no ANTHROPIC_API_KEY needed
		if config.EffortClassifier != nil && config.EffortClassifier.Enabled {
			classifier := NewEffortClassifier()
			if config.EffortClassifier.Model != "" {
				classifier.model = config.EffortClassifier.Model
			}
			if config.EffortClassifier.Timeout != "" {
				if d, err := time.ParseDuration(config.EffortClassifier.Timeout); err == nil {
					classifier.timeout = d
				}
			}
			if config.ClaudeCode != nil {
				classifier.SetUseStructuredOutput(config.ClaudeCode.UseStructuredOutput)
			}
			runner.modelRouter.SetEffortClassifier(classifier)
			runner.log.Info("LLM effort classifier initialized",
				slog.String("model", classifier.model),
				slog.Duration("timeout", classifier.timeout),
			)
		}

		// Configure task decomposition (GH-218)
		if config.Decompose != nil && config.Decompose.Enabled {
			runner.decomposer = NewTaskDecomposer(config.Decompose)

			// GH-727, GH-868: Attach LLM complexity classifier using Claude Code subprocess
			// No ANTHROPIC_API_KEY needed - uses existing Claude Code subscription
			complexityClassifier := NewComplexityClassifier()
			if config.ClaudeCode != nil {
				complexityClassifier.SetUseStructuredOutput(config.ClaudeCode.UseStructuredOutput)
			}
			runner.decomposer.SetClassifier(complexityClassifier)
		}
	}

	// Initialize Haiku subtask parser; nil if ANTHROPIC_API_KEY unset (GH-501)
	runner.subtaskParser = NewSubtaskParser(runner.log)

	// Initialize intent judge for diff-vs-ticket alignment (GH-624)
	if config != nil && config.IntentJudge != nil && (config.IntentJudge.Enabled == nil || *config.IntentJudge.Enabled) {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey != "" {
			runner.intentJudge = NewIntentJudge(apiKey)
			if config.IntentJudge.Model != "" {
				runner.intentJudge.model = config.IntentJudge.Model
			}
			runner.log.Info("Intent judge initialized", slog.String("model", runner.intentJudge.model))
		} else {
			runner.log.Warn("Intent judge disabled: ANTHROPIC_API_KEY not set")
		}
	} else if config != nil && config.IntentJudge == nil {
		runner.log.Debug("Intent judge disabled: no config")
	}

	// Initialize smart retrier (GH-920)
	if config != nil && config.Retry != nil {
		runner.retrier = NewRetrier(config.Retry)
	}

	// Initialize profile manager and drift detector (GH-1027)
	// Global profile: ~/.pilot/profile.json, Project profile: .agent/.user-profile.json
	homeDir, _ := os.UserHomeDir()
	globalProfilePath := filepath.Join(homeDir, ".pilot", "profile.json")
	// Note: project path will be resolved per-task; using empty default here
	runner.profileManager = memory.NewProfileManager(globalProfilePath, "")

	// Drift detector uses default threshold of 3 corrections within 30-minute window
	runner.driftDetector = NewDriftDetector(3, runner.profileManager)
	runner.log.Debug("Profile manager and drift detector initialized")

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

// SetSkipPreflightChecks disables preflight checks (for testing with mock backends).
func (r *Runner) SetSkipPreflightChecks(skip bool) {
	r.skipPreflightChecks = skip
}

// InitWorktreePool initializes the worktree pool for a given repository.
// Should be called before executing tasks when worktree pooling is enabled.
// GH-1078: Saves 500ms-2s per task by reusing pre-created worktrees.
func (r *Runner) InitWorktreePool(ctx context.Context, repoPath string) error {
	if r.config == nil || r.config.WorktreePoolSize <= 0 {
		return nil // Pooling disabled
	}

	r.worktreeManager = NewWorktreeManagerWithPool(repoPath, r.config.WorktreePoolSize)
	return r.worktreeManager.WarmPool(ctx)
}

// CloseWorktreePool drains and closes the worktree pool.
// Should be called during graceful shutdown.
// GH-1078: Ensures clean shutdown without leaving orphaned worktrees.
func (r *Runner) CloseWorktreePool() {
	if r.worktreeManager != nil {
		r.worktreeManager.Close()
	}
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

// SetTeamChecker sets the team permission checker for RBAC enforcement (GH-634).
// When set, Execute() validates that Task.MemberID has the required permissions
// before proceeding. If not set, all tasks are allowed (backward compatible).
func (r *Runner) SetTeamChecker(tc TeamChecker) {
	r.teamChecker = tc
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

// SetTokenLimitCheck sets the per-task token/duration limit callback (GH-539).
// When set, the callback is invoked on each stream event with cumulative token counts.
// If the callback returns false, the execution context is cancelled and the task
// terminates with a budget-exceeded error.
func (r *Runner) SetTokenLimitCheck(cb TokenLimitCallback) {
	r.tokenLimitCheck = cb
}

// SetOnSubIssuePRCreated sets the callback invoked when a sub-issue PR is created
// during epic execution (GH-588). This allows the autopilot controller to track
// each sub-issue PR individually for CI monitoring and auto-merge.
func (r *Runner) SetOnSubIssuePRCreated(fn SubIssuePRCallback) {
	r.onSubIssuePRCreated = fn
}

// SetIntentJudge sets the intent judge for diff-vs-ticket alignment verification (GH-624).
func (r *Runner) SetIntentJudge(judge *IntentJudge) {
	r.intentJudge = judge
}

// SetKnowledgeStore sets the knowledge store for experiential memories (GH-994).
// When set, relevant memories are surfaced in the prompt and decisions are captured post-task.
func (r *Runner) SetKnowledgeStore(k *memory.KnowledgeStore) {
	r.knowledge = k
}

// SetProfileManager sets the profile manager for user preferences (GH-994).
// When set, user preferences (verbosity, code patterns) are applied to prompts.
func (r *Runner) SetProfileManager(pm *memory.ProfileManager) {
	r.profileManager = pm
}

// SetDriftDetector sets the drift detector for collaboration drift (GH-997).
// When set, prompts may include re-anchoring instructions if drift is detected.
func (r *Runner) SetDriftDetector(dd *DriftDetector) {
	r.driftDetector = dd
}

// SetMonitor sets the task monitor for state transitions.
// When set, Runner signals monitor.Start() when execution actually begins,
// enabling accurate queued→running transitions in the dashboard.
func (r *Runner) SetMonitor(m *Monitor) {
	r.monitor = m
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
	return r.executeWithOptions(ctx, task, true)
}

// executeWithOptions is the internal implementation that allows controlling worktree creation.
// When allowWorktree is false, it skips worktree creation even if configured.
// This prevents recursive worktree creation in sub-issues and decomposed tasks.
func (r *Runner) executeWithOptions(ctx context.Context, task *Task, allowWorktree bool) (*ExecutionResult, error) {
	start := time.Now()

	// Signal monitor that execution is actually starting (queued→running transition)
	if r.monitor != nil {
		r.monitor.Start(task.ID)
	}

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

	// GH-634: Enforce team permissions before execution
	if r.teamChecker != nil && task.MemberID != "" {
		if err := r.teamChecker.CheckProjectAccess(task.MemberID, task.ProjectPath, "execute_tasks"); err != nil {
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Sprintf("permission denied: %v", err),
			}, fmt.Errorf("permission check failed: %w", err)
		}
	}

	// GH-936: Create isolated worktree if configured
	// This allows execution even when user has uncommitted changes in their working directory
	executionPath := task.ProjectPath
	var cleanupWorktree func()

	// Debug: log worktree condition state
	r.log.Info("Worktree condition check",
		slog.Bool("allowWorktree", allowWorktree),
		slog.Bool("configNotNil", r.config != nil),
		slog.Bool("useWorktree", r.config != nil && r.config.UseWorktree),
		slog.String("branch", task.Branch),
		slog.Bool("directCommit", task.DirectCommit),
	)

	if allowWorktree && r.config != nil && r.config.UseWorktree && task.Branch != "" && !task.DirectCommit {
		r.log.Info("Creating isolated worktree for execution",
			slog.String("task_id", task.ID),
			slog.String("branch", task.Branch),
		)
		r.reportProgress(task.ID, "Worktree", 1, "Creating isolated worktree...")

		var worktreePath string
		var cleanup func()
		var err error

		// GH-1078: Use pool if available, otherwise fall back to direct creation
		if r.worktreeManager != nil && r.worktreeManager.PoolSize() > 0 {
			r.log.Debug("Using worktree pool",
				slog.Int("pool_available", r.worktreeManager.PoolAvailable()),
			)
			var result *WorktreeResult
			result, err = r.worktreeManager.Acquire(ctx, task.ID, task.Branch, "")
			if err == nil {
				worktreePath = result.Path
				cleanup = result.Cleanup
			}
		} else {
			worktreePath, cleanup, err = CreateWorktreeWithBranch(
				ctx, task.ProjectPath, task.ID, task.Branch, "")
		}

		if err != nil {
			r.log.Error("Failed to create worktree",
				slog.String("task_id", task.ID),
				slog.Any("error", err),
			)
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Sprintf("failed to create worktree: %v", err),
			}, fmt.Errorf("worktree creation failed: %w", err)
		}
		cleanupWorktree = cleanup
		executionPath = worktreePath

		// Copy Navigator config to worktree (handles untracked .agent/ content)
		if err := EnsureNavigatorInWorktree(task.ProjectPath, worktreePath); err != nil {
			cleanup()
			r.log.Error("Failed to copy Navigator to worktree",
				slog.String("task_id", task.ID),
				slog.Any("error", err),
			)
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Sprintf("failed to setup navigator in worktree: %v", err),
			}, fmt.Errorf("navigator worktree setup failed: %w", err)
		}

		r.log.Info("Using isolated worktree",
			slog.String("task_id", task.ID),
			slog.String("worktree", worktreePath),
		)
		r.reportProgress(task.ID, "Worktree", 2, "Worktree ready")
	}

	// Ensure worktree cleanup on exit (handles panic, early return, success)
	if cleanupWorktree != nil {
		defer cleanupWorktree()
	}

	// GH-915: Run pre-flight checks to catch environmental issues early
	// Skip when using mock backends in tests (skipPreflightChecks flag)
	// GH-1002: Skip git_clean check when worktree isolation is enabled
	if !r.skipPreflightChecks {
		preflightOpts := PreflightOptions{
			SkipGitClean: r.config != nil && r.config.UseWorktree,
		}
		if err := RunPreflightChecksWithOptions(ctx, executionPath, preflightOpts); err != nil {
			r.log.Warn("Pre-flight check failed",
				slog.String("task_id", task.ID),
				slog.Any("error", err),
			)
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   fmt.Sprintf("pre-flight check failed: %v", err),
			}, fmt.Errorf("pre-flight check failed: %w", err)
		}
	}

	// Auto-init Navigator if configured and missing
	// Use executionPath to check/init in worktree if worktree isolation is active
	if r.config != nil && r.config.Navigator != nil && r.config.Navigator.AutoInit {
		if err := r.maybeInitNavigator(executionPath); err != nil {
			r.log.Warn("Navigator auto-init failed", slog.Any("error", err))
			// Continue without Navigator - graceful degradation
		}
	}

	// Detect complexity for routing decisions
	complexity := DetectComplexity(task)

	// GH-664: Skip epic mode if task has no-decompose label
	hasNoDecompose := false
	for _, label := range task.Labels {
		if strings.EqualFold(label, NoDecomposeLabel) {
			hasNoDecompose = true
			break
		}
	}

	// GH-405: Epic tasks trigger planning mode instead of execution
	if complexity.IsEpic() && !hasNoDecompose {
		r.log.Info("Epic task detected, running planning mode",
			slog.String("task_id", task.ID),
			slog.String("title", task.Title),
		)
		r.reportProgress(task.ID, "Planning", 10, "Running epic planning...")

		plan, err := r.PlanEpic(ctx, task, executionPath)
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

		// GH-1265: Detect single-package scope — if all subtasks target the same
		// directory/package, consolidate into a single task instead of creating
		// separate GitHub issues. Creating N sub-issues that all touch the same
		// package causes merge conflicts because each sub-issue branches from main
		// independently and redeclares shared types (e.g., the "pilot onboard" cascade).
		if isSinglePackageScope(plan.Subtasks, task.Description) {
			r.log.Info("Single-package scope detected, skipping epic decomposition — executing as single task",
				slog.String("task_id", task.ID),
				slog.Int("planned_subtasks", len(plan.Subtasks)),
			)
			r.reportProgress(task.ID, "Planning", 35, "Single-package scope detected, running as single task...")

			// Enrich the task description with the planned steps so the executor
			// has the full implementation plan but executes it as one unit.
			task.Description = consolidateEpicPlan(task.Description, plan.Subtasks)

			// Fall through to normal execution below (past epic and decomposer blocks)
		} else {
			// Multi-package epic: safe to create separate GitHub issues

			// GH-412: Create sub-issues from the plan
			r.reportProgress(task.ID, "Creating Issues", 40, "Creating GitHub sub-issues...")

			issues, err := r.CreateSubIssues(ctx, plan, executionPath)
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
			if err := r.ExecuteSubIssues(ctx, task, issues, executionPath); err != nil {
				return &ExecutionResult{
					TaskID:   task.ID,
					Success:  false,
					Error:    fmt.Sprintf("sub-issue execution failed: %v", err),
					Duration: time.Since(start),
					IsEpic:   true,
					EpicPlan: plan,
				}, nil
			}

			// GH-539: Epic sub-executions may have created commits on the branch.
			// Push branch and create PR to propagate deliverables.
			epicResult := &ExecutionResult{
				TaskID:   task.ID,
				Success:  true,
				Output:   fmt.Sprintf("Epic completed: %d sub-issues executed", len(issues)),
				Duration: time.Since(start),
				IsEpic:   true,
				EpicPlan: plan,
			}

			if task.CreatePR && task.Branch != "" {
				epicGit := NewGitOperations(executionPath)

				r.reportProgress(task.ID, "Creating PR", 96, "Pushing epic branch...")

				if err := epicGit.Push(ctx, task.Branch); err != nil {
					r.log.Warn("Epic branch push failed",
						slog.String("task_id", task.ID),
						slog.String("branch", task.Branch),
						slog.Any("error", err),
					)
					// Don't fail the epic — sub-issues may have their own PRs
				} else {
					// Get commit SHA
					if sha, shaErr := epicGit.GetCurrentCommitSHA(ctx); shaErr == nil && sha != "" {
						epicResult.CommitSHA = sha
					}

					// Determine base branch
					baseBranch := task.BaseBranch
					if baseBranch == "" {
						baseBranch, _ = epicGit.GetDefaultBranch(ctx)
						if baseBranch == "" {
							baseBranch = "main"
						}
					}

					// Create PR with GitHub auto-close keyword
					epicIssueNum := strings.TrimPrefix(task.ID, "GH-")
					prBody := fmt.Sprintf("## Summary\n\nAutomated PR created by Pilot for epic task %s.\n\nCloses #%s\n\n## Changes\n\n%s", task.ID, epicIssueNum, task.Description)
					prURL, prErr := epicGit.CreatePR(ctx, task.Title, prBody, baseBranch)
					if prErr != nil {
						r.log.Warn("Epic PR creation failed",
							slog.String("task_id", task.ID),
							slog.Any("error", prErr),
						)
					} else {
						epicResult.PRUrl = prURL
						r.log.Info("Epic PR created", slog.String("pr_url", prURL))
					}
				}
			}

			r.reportProgress(task.ID, "Complete", 100, "Epic completed successfully")
			return epicResult, nil
		}
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
			return r.executeDecomposedTask(ctx, task, result.Subtasks, executionPath)
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

	// Select effort if routing is enabled
	selectedEffort := r.modelRouter.SelectEffort(task)
	if selectedEffort != "" {
		log = log.With(slog.String("routed_effort", selectedEffort))
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

	// Initialize git operations in execution path (worktree or original)
	git := NewGitOperations(executionPath)

	// Create branch if specified (skip for direct commit mode and worktree mode)
	// When using worktree, CreateWorktreeWithBranch already created the branch
	useWorktree := r.config != nil && r.config.UseWorktree && task.Branch != "" && !task.DirectCommit
	if task.Branch != "" && !task.DirectCommit && !useWorktree {
		r.reportProgress(task.ID, "Branching", 3, "Switching to default branch...")

		// GH-279: Always switch to default branch and pull latest before creating new branch.
		// This prevents new branches from forking off previous pilot branches instead of main.
		// GH-836: Hard fail if we can't switch - continuing from wrong branch causes corrupted PRs.
		defaultBranch, err := git.SwitchToDefaultBranchAndPull(ctx)
		if err != nil {
			return nil, fmt.Errorf("branch switch failed, aborting execution: failed to switch to default branch: %w", err)
		}
		r.reportProgress(task.ID, "Branching", 5, fmt.Sprintf("On %s, creating %s...", defaultBranch, task.Branch))

		if err := git.CreateBranch(ctx, task.Branch); err != nil {
			// Branch already exists - check if it's stale (GH-912)
			behindCount, behindErr := git.CommitsBehindMain(ctx, task.Branch)
			if behindErr != nil {
				log.Warn("Failed to check if branch is behind main",
					slog.String("branch", task.Branch),
					slog.Any("error", behindErr),
				)
			}

			if behindCount > 0 {
				// Branch is stale - delete and recreate from main
				log.Info("Stale branch detected, recreating from main",
					slog.String("branch", task.Branch),
					slog.Int("commits_behind", behindCount),
				)
				r.reportProgress(task.ID, "Branching", 6, fmt.Sprintf("Stale branch %s (%d behind), recreating...", task.Branch, behindCount))

				if delErr := git.DeleteBranch(ctx, task.Branch); delErr != nil {
					log.Warn("Failed to delete stale branch",
						slog.String("branch", task.Branch),
						slog.Any("error", delErr),
					)
				}
				// Create fresh branch from main
				if createErr := git.CreateBranch(ctx, task.Branch); createErr != nil {
					return nil, fmt.Errorf("failed to recreate branch after stale detection: %w", createErr)
				}
				r.reportProgress(task.ID, "Branching", 8, fmt.Sprintf("Recreated fresh branch %s", task.Branch))
			} else {
				// Branch exists and is not stale - switch to it
				if switchErr := git.SwitchBranch(ctx, task.Branch); switchErr != nil {
					return nil, fmt.Errorf("failed to create/switch branch: %w", err)
				}
				r.reportProgress(task.ID, "Branching", 8, fmt.Sprintf("Switched to existing branch %s", task.Branch))
			}
		} else {
			r.reportProgress(task.ID, "Branching", 8, fmt.Sprintf("Created branch %s", task.Branch))
		}
	}

	// GH-994: Create task documentation if Navigator is present
	agentPath := filepath.Join(executionPath, ".agent")
	if _, err := os.Stat(agentPath); err == nil {
		if err := CreateTaskDoc(agentPath, task); err != nil {
			log.Warn("Failed to create task doc", slog.Any("error", err))
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
	prompt := r.BuildPrompt(task, executionPath)

	// Append research context if available (GH-217)
	if researchResult != nil && len(researchResult.Findings) > 0 {
		prompt = r.appendResearchContext(prompt, researchResult)
	}

	// State for tracking progress
	state := &progressState{phase: "Starting", budgetCancel: cancel}

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

	// Setup Claude Code hooks if enabled (GH-1266)
	var hookRestoreFunc func() error
	if r.config != nil && r.config.Hooks != nil && r.config.Hooks.Enabled {
		log.Debug("Setting up Claude Code hooks", slog.String("task_id", task.ID))

		// Create temporary directory for hook scripts
		scriptDir, err := os.MkdirTemp("", "pilot-hooks-")
		if err != nil {
			log.Error("Failed to create hooks script directory", slog.Any("error", err))
		} else {
			// Write embedded scripts
			if err := WriteEmbeddedScripts(scriptDir); err != nil {
				log.Error("Failed to write embedded hook scripts", slog.Any("error", err))
			} else {
				// Generate Claude settings
				hookSettings := GenerateClaudeSettings(r.config.Hooks, scriptDir)

				// Merge with existing settings.json (worktree-safe path)
				settingsPath := filepath.Join(executionPath, ".claude", "settings.json")
				restoreFunc, mergeErr := MergeWithExisting(settingsPath, hookSettings)
				if mergeErr != nil {
					log.Error("Failed to setup Claude hooks", slog.Any("error", mergeErr))
					// Clean up script directory
					if rmErr := os.RemoveAll(scriptDir); rmErr != nil {
						log.Warn("Failed to clean up hook scripts after merge error", slog.Any("error", rmErr))
					}
				} else {
					hookRestoreFunc = func() error {
						// Restore original settings.json
						if restoreErr := restoreFunc(); restoreErr != nil {
							log.Warn("Failed to restore original Claude settings", slog.Any("error", restoreErr))
						}
						// Clean up script directory
						if rmErr := os.RemoveAll(scriptDir); rmErr != nil {
							log.Warn("Failed to clean up hook scripts", slog.Any("error", rmErr))
						}
						return nil
					}
					log.Debug("Claude Code hooks configured",
						slog.String("settings_path", settingsPath),
						slog.String("script_dir", scriptDir))
				}
			}
		}
	}

	// Ensure cleanup happens regardless of execution outcome
	defer func() {
		if hookRestoreFunc != nil {
			_ = hookRestoreFunc() // Error already logged inside hookRestoreFunc
		}
	}()

	// Execute via backend with watchdog (GH-882)
	// Watchdog kills subprocess after 2x timeout as a safety net for processes
	// that ignore context cancellation.
	watchdogTimeout := 2 * timeout
	backendResult, err := r.backend.Execute(ctx, ExecuteOptions{
		Prompt:          prompt,
		ProjectPath:     executionPath, // Use worktree path if active
		Verbose:         task.Verbose,
		Model:           selectedModel,
		Effort:          selectedEffort,
		FromPR:          task.FromPR, // GH-1267: session resumption from PR context
		WatchdogTimeout: watchdogTimeout,
		WatchdogCallback: func(pid int, watchdogDuration time.Duration) {
			log.Warn("Watchdog killed subprocess",
				slog.Int("pid", pid),
				slog.Duration("watchdog_timeout", watchdogDuration),
				slog.Duration("configured_timeout", timeout),
			)
			r.reportProgress(task.ID, "Watchdog Kill", 100, fmt.Sprintf("Process killed by watchdog after %v (2x timeout)", watchdogDuration))

			// Emit watchdog kill alert
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeWatchdogKill,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Project:   task.ProjectPath,
				Error:     fmt.Sprintf("subprocess killed by watchdog after %v", watchdogDuration),
				Metadata: map[string]string{
					"pid":                fmt.Sprintf("%d", pid),
					"watchdog_timeout":   watchdogDuration.String(),
					"configured_timeout": timeout.String(),
					"complexity":         complexity.String(),
				},
				Timestamp: time.Now(),
			})
		},
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

		// GH-539: Check if this was a per-task budget limit breach
		if state.budgetExceeded {
			result.Error = fmt.Sprintf("per-task budget limit exceeded: %s", state.budgetReason)
			result.TokensInput = state.tokensInput
			result.TokensOutput = state.tokensOutput
			result.TokensTotal = state.tokensInput + state.tokensOutput
			result.ModelName = state.modelName
			if result.ModelName == "" {
				result.ModelName = "claude-opus-4-5"
			}
			result.EstimatedCostUSD = estimateCost(result.TokensInput, result.TokensOutput, result.ModelName)
			log.Warn("Task cancelled due to per-task budget limit",
				slog.String("task_id", task.ID),
				slog.String("reason", state.budgetReason),
				slog.Int64("input_tokens", state.tokensInput),
				slog.Int64("output_tokens", state.tokensOutput),
				slog.Duration("duration", duration),
			)
			r.reportProgress(task.ID, "Budget Exceeded", 100, result.Error)

			// Emit budget exceeded alert event
			r.emitAlertEvent(AlertEvent{
				Type:      AlertEventTypeTaskFailed,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Project:   task.ProjectPath,
				Error:     result.Error,
				Metadata: map[string]string{
					"reason":        "budget_exceeded",
					"input_tokens":  fmt.Sprintf("%d", state.tokensInput),
					"output_tokens": fmt.Sprintf("%d", state.tokensOutput),
				},
				Timestamp: time.Now(),
			})

			if recorder != nil {
				recorder.SetModel(state.modelName)
				recorder.SetNavigator(state.hasNavigator)
				if finErr := recorder.Finish("budget_exceeded"); finErr != nil {
					log.Warn("Failed to finish recording", slog.Any("error", finErr))
				}
			}
			return result, nil
		}

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
			// GH-917: Check for classified Claude Code error types
			alertType := AlertEventTypeTaskFailed
			errorCategory := "unknown"
			var stderrOutput string // GH-917-5: Always capture stderr for logging

			if beErr, ok := err.(BackendError); ok {
				result.Error = beErr.Error()
				stderrOutput = beErr.ErrorStderr() // Capture stderr from classified error

				// Map error type to alert event type and category
				switch beErr.ErrorType() {
				case "rate_limit":
					alertType = AlertEventTypeRateLimit
					errorCategory = "rate_limit"
					log.Warn("Backend hit rate limit",
						slog.String("task_id", task.ID),
						slog.String("stderr", beErr.ErrorStderr()),
						slog.Duration("duration", duration),
					)
					r.reportProgress(task.ID, "Rate Limited", 100, "Backend hit rate limit - retry later")

				case "invalid_config":
					alertType = AlertEventTypeConfigError
					errorCategory = "invalid_config"
					log.Error("Invalid backend configuration",
						slog.String("task_id", task.ID),
						slog.String("message", beErr.ErrorMessage()),
						slog.String("stderr", beErr.ErrorStderr()),
					)
					r.reportProgress(task.ID, "Config Error", 100, beErr.ErrorMessage())

				case "api_error":
					alertType = AlertEventTypeAPIError
					errorCategory = "api_error"
					log.Error("Backend API error",
						slog.String("task_id", task.ID),
						slog.String("message", beErr.ErrorMessage()),
						slog.String("stderr", beErr.ErrorStderr()),
					)
					r.reportProgress(task.ID, "API Error", 100, beErr.ErrorMessage())

				default:
					// GH-917-5: Log stderr for process errors and unknown errors too
					log.Error("Backend execution failed",
						slog.String("error", result.Error),
						slog.String("error_type", beErr.ErrorType()),
						slog.String("stderr", beErr.ErrorStderr()),
						slog.Duration("duration", duration),
					)
					r.reportProgress(task.ID, "Failed", 100, result.Error)
				}
			} else {
				result.Error = err.Error()
				// GH-917-5: Log even when error is not a classified backend error
				log.Error("Backend execution failed",
					slog.String("error", result.Error),
					slog.String("error_type", "unclassified"),
					slog.Duration("duration", duration),
				)
				r.reportProgress(task.ID, "Failed", 100, result.Error)
			}

			// GH-920: Check for smart retry before emitting alerts
			// Note: state.smartRetryAttempt tracks retry attempts for this error path
			if r.retrier != nil {
				decision := r.retrier.Evaluate(err, state.smartRetryAttempt, timeout)
				if decision.ShouldRetry {
					// GH-1030: Record correction for drift detection
					if r.driftDetector != nil {
						r.driftDetector.RecordCorrection("retry_triggered", fmt.Sprintf("Error: %s, Retry attempt: %d", err.Error(), state.smartRetryAttempt+1))
					}
					state.smartRetryAttempt++
					log.Info("Smart retry triggered",
						slog.String("task_id", task.ID),
						slog.String("error_category", errorCategory),
						slog.Int("attempt", state.smartRetryAttempt),
						slog.Duration("backoff", decision.BackoffDuration),
					)
					r.reportProgress(task.ID, "Retrying", 50, fmt.Sprintf("Waiting %v before retry (attempt %d)...", decision.BackoffDuration, state.smartRetryAttempt))

					// Sleep for backoff duration
					if sleepErr := r.retrier.Sleep(ctx, decision.BackoffDuration); sleepErr != nil {
						log.Warn("Retry sleep interrupted", slog.Any("error", sleepErr))
						// Fall through to emit alerts
					} else {
						// Re-execute with potentially extended timeout
						retryTimeout := timeout
						if decision.ExtendedTimeout > 0 {
							retryTimeout = decision.ExtendedTimeout
						}
						retryCtx, retryCancel := context.WithTimeout(context.Background(), retryTimeout)

						r.reportProgress(task.ID, "Re-executing", 55, fmt.Sprintf("Retry attempt %d with %v timeout...", state.smartRetryAttempt, retryTimeout))

						retryResult, retryErr := r.backend.Execute(retryCtx, ExecuteOptions{
							Prompt:          prompt,
							ProjectPath:     task.ProjectPath,
							Verbose:         task.Verbose,
							Model:           selectedModel,
							Effort:          selectedEffort,
							WatchdogTimeout: 2 * retryTimeout,
							EventHandler: func(event BackendEvent) {
								if recorder != nil {
									_ = recorder.RecordEvent(event.Raw)
								}
								r.processBackendEvent(task.ID, event, state)
							},
						})
						retryCancel()

						if retryErr == nil && retryResult != nil && retryResult.Success {
							// Retry succeeded! Update backendResult and continue
							log.Info("Smart retry succeeded",
								slog.String("task_id", task.ID),
								slog.Int("attempt", state.smartRetryAttempt),
							)
							r.reportProgress(task.ID, "Retry Success", 90, "Retry completed successfully")

							// Update results from retry
							backendResult = retryResult
							err = nil
							goto retrySucceeded
						}
						// Retry failed, continue to emit alerts
						log.Warn("Smart retry failed",
							slog.String("task_id", task.ID),
							slog.Int("attempt", state.smartRetryAttempt),
							slog.Any("error", retryErr),
						)
					}
				}
			}

			// GH-917-5: Include stderr in alert metadata for debugging
			metadata := map[string]string{
				"error_category": errorCategory,
			}
			if stderrOutput != "" {
				metadata["stderr"] = stderrOutput
			}

			// Emit alert event with error category metadata
			r.emitAlertEvent(AlertEvent{
				Type:      alertType,
				TaskID:    task.ID,
				TaskTitle: task.Title,
				Project:   task.ProjectPath,
				Error:     result.Error,
				Metadata:  metadata,
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

retrySucceeded:
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

	// Extract commit SHA from state (parsed from Claude Code output)
	if len(state.commitSHAs) > 0 {
		result.CommitSHA = state.commitSHAs[len(state.commitSHAs)-1] // Use last commit
	}

	// Post-execution summary via structured output (GH-1264)
	// This replaces brittle regex parsing with reliable --json-schema output
	if result.CommitSHA == "" && result.Success && r.config != nil && r.config.ClaudeCode != nil && r.config.ClaudeCode.UseStructuredOutput {
		if summary, summaryErr := r.getPostExecutionSummary(ctx); summaryErr == nil {
			if summary.CommitSHA != "" {
				result.CommitSHA = summary.CommitSHA
				log.Info("CommitSHA extracted via post-execution summary",
					slog.String("task_id", task.ID),
					slog.String("sha", summary.CommitSHA[:min(7, len(summary.CommitSHA))]),
					slog.String("branch", summary.BranchName),
				)
			}
		} else {
			log.Debug("post-execution summary failed, falling back to git",
				slog.String("task_id", task.ID),
				slog.Any("error", summaryErr),
			)
		}
	}

	// Fallback: if output parsing missed the commit SHA, ask git directly.
	// This handles cases where Claude's git commit output format doesn't match
	// the extractCommitSHA() pattern (e.g. different flags, localized output).
	if result.CommitSHA == "" && task.Branch != "" && result.Success {
		baseBranch := task.BaseBranch
		if baseBranch == "" {
			baseBranch, _ = git.GetDefaultBranch(ctx)
			if baseBranch == "" {
				baseBranch = "main"
			}
		}
		if commitCount, countErr := git.CountNewCommits(ctx, baseBranch); countErr == nil && commitCount > 0 {
			if sha, shaErr := git.GetCurrentCommitSHA(ctx); shaErr == nil && sha != "" {
				log.Info("CommitSHA recovered via git (output parsing missed it)",
					slog.String("task_id", task.ID),
					slog.String("sha", sha[:min(7, len(sha))]),
					slog.Int("new_commits", commitCount),
				)
				result.CommitSHA = sha
			}
		}
	}

	// Fill in additional metrics from state
	result.FilesChanged = state.filesWrite
	if result.ModelName == "" {
		result.ModelName = state.modelName
	}
	if result.ModelName == "" {
		result.ModelName = "claude-opus-4-5" // Default model
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

		// No-commit detection and retry (GH-916)
		// ~10% of failures are "No commits between main and pilot/GH-XXX"
		// Claude runs successfully but makes no actual changes, then PR creation fails.
		if task.CreatePR && !task.DirectCommit && task.Branch != "" {
			baseBranch := task.BaseBranch
			if baseBranch == "" {
				baseBranch, _ = git.GetDefaultBranch(ctx)
				if baseBranch == "" {
					baseBranch = "main"
				}
			}

			commitCount, countErr := git.CountNewCommits(ctx, baseBranch)
			if countErr != nil {
				log.Warn("Failed to count commits for no-commit check",
					slog.String("task_id", task.ID),
					slog.Any("error", countErr),
				)
			} else if commitCount == 0 {
				log.Warn("Claude made no commits, retrying with explicit instruction",
					slog.String("task_id", task.ID),
					slog.String("branch", task.Branch),
				)
				r.reportProgress(task.ID, "Retry", 91, "No commits detected, retrying...")

				// Build retry prompt with explicit instruction
				retryPrompt := fmt.Sprintf(`## Retry: No Changes Detected

The previous execution completed but made no code changes. This task requires actual implementation.

**Original Task:** %s

**Instructions:**
1. Read the task requirements carefully
2. Implement the required changes
3. Create at least one commit with your changes
4. Do NOT just analyze or plan - actually write and commit code

%s`, task.Title, task.Description)

				// Execute retry
				retryResult, retryErr := r.backend.Execute(ctx, ExecuteOptions{
					Prompt:          retryPrompt,
					ProjectPath:     task.ProjectPath,
					Verbose:         task.Verbose,
					Model:           selectedModel,
					Effort:          selectedEffort,
					WatchdogTimeout: watchdogTimeout,
					EventHandler: func(event BackendEvent) {
						// Track tokens from retry
						state.tokensInput += event.TokensInput
						state.tokensOutput += event.TokensOutput
						// Extract any commit SHAs from retry
						if event.Type == EventTypeToolResult && event.ToolResult != "" {
							extractCommitSHA(event.ToolResult, state)
						}
						if recorder != nil {
							if recErr := recorder.RecordEvent(event.Raw); recErr != nil {
								log.Warn("Failed to record retry event", slog.Any("error", recErr))
							}
						}
					},
				})

				// Update result with retry tokens
				if retryResult != nil {
					result.TokensInput += retryResult.TokensInput
					result.TokensOutput += retryResult.TokensOutput
					result.TokensTotal = result.TokensInput + result.TokensOutput
				}

				// Check again after retry
				commitCount, _ = git.CountNewCommits(ctx, baseBranch)
				if commitCount == 0 {
					result.Success = false
					result.Error = "Claude completed but made no code changes after retry"
					log.Error("No commits after retry",
						slog.String("task_id", task.ID),
					)
					r.reportProgress(task.ID, "Failed", 100, result.Error)

					// Emit task failed event
					r.emitAlertEvent(AlertEvent{
						Type:      AlertEventTypeTaskFailed,
						TaskID:    task.ID,
						TaskTitle: task.Title,
						Project:   task.ProjectPath,
						Error:     result.Error,
						Metadata: map[string]string{
							"reason": "no_commits_after_retry",
						},
						Timestamp: time.Now(),
					})

					// Finish recording with failed status
					if recorder != nil {
						recorder.SetModel(result.ModelName)
						recorder.SetNavigator(state.hasNavigator)
						if finErr := recorder.Finish("no_commits"); finErr != nil {
							log.Warn("Failed to finish recording", slog.Any("error", finErr))
						}
					}
					return result, nil
				} else if retryErr != nil {
					log.Warn("Retry execution error (but commits exist)",
						slog.String("task_id", task.ID),
						slog.Any("error", retryErr),
						slog.Int("commit_count", commitCount),
					)
				}

				log.Info("Retry successful - commits detected",
					slog.String("task_id", task.ID),
					slog.Int("commit_count", commitCount),
				)
				r.reportProgress(task.ID, "Retry Success", 92, fmt.Sprintf("Retry successful: %d commits", commitCount))

				// Update commit SHA from retry if state captured it
				if len(state.commitSHAs) > 0 {
					result.CommitSHA = state.commitSHAs[len(state.commitSHAs)-1]
				} else if sha, shaErr := git.GetCurrentCommitSHA(ctx); shaErr == nil {
					result.CommitSHA = sha
				}
			}
		}

		// Auto-enable minimal build gate if not configured (GH-363)
		// This ensures broken code never becomes a PR, even without explicit quality config
		if r.qualityCheckerFactory == nil {
			buildCmd := quality.DetectBuildCommand(executionPath)
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

		// Track if quality gates passed for self-review decision (GH-1079)
		qualityGatesPassed := false

		// Run quality gates if configured
		if r.qualityCheckerFactory != nil {
			const maxAutoRetries = 2 // Circuit breaker to prevent infinite loops

			// Track quality gate results across retries (GH-209)
			var finalOutcome *QualityOutcome
			var totalQualityRetries int

			for retryAttempt := 0; retryAttempt <= maxAutoRetries; retryAttempt++ {
				r.reportProgress(task.ID, "Quality Gates", 91, "Running quality checks...")

				checker := r.qualityCheckerFactory(task.ID, executionPath)
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
					qualityGatesPassed = true
					r.reportProgress(task.ID, "Quality Passed", 94, "All quality gates passed")

					// Run simplification phase if enabled (GH-995)
					if r.config != nil && r.config.Simplification != nil && r.config.Simplification.Enabled {
						r.reportProgress(task.ID, "Simplifying", 95, "Simplifying code...")
						simplified, simplifyErr := SimplifyModifiedFiles(executionPath, r.config.Simplification)
						if simplifyErr != nil {
							log.Warn("Simplification error", slog.Any("error", simplifyErr))
							// Continue anyway - simplification is advisory
						} else if len(simplified) > 0 {
							log.Info("Simplified files", slog.Int("count", len(simplified)), slog.Any("files", simplified))
						}
					}

					// Note: Self-review now runs in parallel with intent judge after quality gates (GH-1079)

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

					// GH-1066: Record correction for drift detection
					if r.driftDetector != nil {
						r.driftDetector.RecordCorrection("quality_gate_retry", fmt.Sprintf("Quality gate failure: %s, Retry attempt: %d", outcome.RetryFeedback, retryAttempt+1))
					}

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
						Effort:      selectedEffort,
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
						result.Success = false

						// GH-917: Check for classified backend error types in retry
						alertType := AlertEventTypeTaskFailed
						errorCategory := "unknown"

						if beErr, ok := retryErr.(BackendError); ok {
							result.Error = fmt.Sprintf("retry execution failed: %v", beErr)

							switch beErr.ErrorType() {
							case "rate_limit":
								alertType = AlertEventTypeRateLimit
								errorCategory = "rate_limit"
								log.Warn("Retry hit rate limit",
									slog.String("task_id", task.ID),
									slog.Int("retry_attempt", retryAttempt+1),
								)
								r.reportProgress(task.ID, "Rate Limited", 100, "Retry hit rate limit")
							case "invalid_config":
								alertType = AlertEventTypeConfigError
								errorCategory = "invalid_config"
								log.Error("Retry failed: invalid config", slog.String("message", beErr.ErrorMessage()))
								r.reportProgress(task.ID, "Config Error", 100, beErr.ErrorMessage())
							case "api_error":
								alertType = AlertEventTypeAPIError
								errorCategory = "api_error"
								log.Error("Retry failed: API error", slog.String("message", beErr.ErrorMessage()))
								r.reportProgress(task.ID, "API Error", 100, beErr.ErrorMessage())
							default:
								log.Error("Retry execution failed", slog.Any("error", retryErr))
								r.reportProgress(task.ID, "Retry Failed", 100, result.Error)
							}
						} else {
							result.Error = fmt.Sprintf("retry execution failed: %v", retryErr)
							log.Error("Retry execution failed", slog.Any("error", retryErr))
							r.reportProgress(task.ID, "Retry Failed", 100, result.Error)
						}

						r.emitAlertEvent(AlertEvent{
							Type:      alertType,
							TaskID:    task.ID,
							TaskTitle: task.Title,
							Project:   task.ProjectPath,
							Error:     result.Error,
							Metadata: map[string]string{
								"error_category": errorCategory,
								"phase":          "quality_retry",
							},
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
		}

		// GH-1079: Run self-review and intent judge in parallel (saves 2-5 min per task)
		// Both are independent read-only operations:
		// - Self-review checks code quality (syntax, wiring, style)
		// - Intent judge verifies diff matches issue intent
		var intentVerdict *JudgeVerdict
		var intentErr error
		var intentDiff string
		var intentBaseBranch string

		// Determine if intent judge should run
		runIntentJudge := r.intentJudge != nil && task.CreatePR && !task.DirectCommit && task.Branch != ""

		// Log skip reasons for intent judge
		if r.intentJudge == nil {
			log.Debug("Intent judge skipped: not initialized")
		} else if !task.CreatePR {
			log.Debug("Intent judge skipped: CreatePR=false")
		} else if task.DirectCommit {
			log.Debug("Intent judge skipped: DirectCommit=true")
		} else if task.Branch == "" {
			log.Debug("Intent judge skipped: no branch")
		}

		// Get diff before parallel execution (needed for intent judge)
		if runIntentJudge {
			intentBaseBranch = task.BaseBranch
			if intentBaseBranch == "" {
				intentBaseBranch, _ = git.GetDefaultBranch(ctx)
				if intentBaseBranch == "" {
					intentBaseBranch = "main"
				}
			}
			intentDiff, intentErr = git.GetDiff(ctx, intentBaseBranch)
			if intentErr != nil {
				log.Warn("Intent judge skipped: failed to get diff",
					slog.String("task_id", task.ID),
					slog.Any("error", intentErr),
				)
				runIntentJudge = false
			} else if intentDiff == "" {
				runIntentJudge = false
			}
		}

		// Determine if self-review should run:
		// 1. Quality gates configured AND passed
		// 2. OR quality gates not configured AND CreatePR=true (GH-364)
		runSelfReview := qualityGatesPassed || (r.qualityCheckerFactory == nil && task.CreatePR)

		// Run self-review and intent judge in parallel
		var wg sync.WaitGroup
		var selfReviewErr error

		if runSelfReview {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := r.runSelfReview(ctx, task, state); err != nil {
					selfReviewErr = err
				}
			}()
		}

		if runIntentJudge {
			wg.Add(1)
			go func() {
				defer wg.Done()
				log.Info("Intent judge running",
					slog.String("task_id", task.ID),
					slog.Int("diff_len", len(intentDiff)),
				)
				r.reportProgress(task.ID, "Intent Check", 96, "Verifying diff matches intent...")
				intentVerdict, intentErr = r.intentJudge.Judge(ctx, task.Title, task.Description, intentDiff)
			}()
		}

		wg.Wait()

		// Handle self-review result
		if runSelfReview && selfReviewErr != nil {
			log.Warn("Self-review error", slog.Any("error", selfReviewErr))
			// Continue anyway - self-review is advisory
		}

		// Handle intent judge result
		if runIntentJudge {
			if intentErr != nil {
				log.Warn("Intent judge error (continuing to PR)",
					slog.String("task_id", task.ID),
					slog.Any("error", intentErr),
				)
			} else if intentVerdict != nil && !intentVerdict.Passed {
				log.Warn("Intent judge vetoed diff",
					slog.String("task_id", task.ID),
					slog.String("reason", intentVerdict.Reason),
					slog.Float64("confidence", intentVerdict.Confidence),
				)

				if !state.intentRetried {
					state.intentRetried = true
					r.reportProgress(task.ID, "Intent Retry", 80, "Retrying with intent feedback...")

					retryPrompt := fmt.Sprintf(
						"## Intent Alignment Retry\n\nThe intent judge flagged the previous implementation:\n\n**Reason:** %s\n\nPlease fix the issues above. Focus on implementing exactly what the issue asks for.\n\n## Original Task: %s\n\n%s",
						intentVerdict.Reason, task.Title, task.Description,
					)

					_, retryErr := r.backend.Execute(ctx, ExecuteOptions{
						Prompt:      retryPrompt,
						ProjectPath: task.ProjectPath,
						Verbose:     task.Verbose,
						Model:       selectedModel,
						Effort:      selectedEffort,
						EventHandler: func(event BackendEvent) {
							state.tokensInput += event.TokensInput
							state.tokensOutput += event.TokensOutput
							if event.Type == EventTypeToolResult && event.ToolResult != "" {
								extractCommitSHA(event.ToolResult, state)
							}
						},
					})

					if retryErr == nil {
						// Update result tokens
						result.TokensInput = state.tokensInput
						result.TokensOutput = state.tokensOutput
						result.TokensTotal = state.tokensInput + state.tokensOutput

						// Re-judge the new diff
						newDiff, _ := git.GetDiff(ctx, intentBaseBranch)
						if newDiff != "" {
							v2, _ := r.intentJudge.Judge(ctx, task.Title, task.Description, newDiff)
							if v2 != nil && !v2.Passed {
								result.IntentWarning = v2.Reason
							}
						}
					} else {
						result.IntentWarning = intentVerdict.Reason
					}
				} else {
					result.IntentWarning = intentVerdict.Reason
				}
			}
		}

		// Handle direct commit mode: push directly to main

		// Pre-push lint gate (GH-1376)
		if r.config != nil && r.config.PrePushLint != nil && *r.config.PrePushLint {
			r.reportProgress(task.ID, "Linting", 95, "Running pre-push lint check...")
			lintResult := git.autoFixLint(ctx)
			if !lintResult.Clean && !lintResult.FixedAll {
				// Include unfixable lint issues in execution result for self-review
				if len(lintResult.Issues) > 0 {
					result.IntentWarning = "Lint issues detected but not auto-fixable:\n" + strings.Join(lintResult.Issues, "\n")
				}
			}
		}
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


			// Pre-push lint gate (GH-1376)
			if r.config != nil && r.config.PrePushLint != nil && *r.config.PrePushLint {
				r.reportProgress(task.ID, "Linting", 95, "Running pre-push lint check...")
				lintResult := git.autoFixLint(ctx)
				if !lintResult.Clean && !lintResult.FixedAll {
					// Include unfixable lint issues in execution result for self-review
					if len(lintResult.Issues) > 0 {
						result.IntentWarning = "Lint issues detected but not auto-fixable:\n" + strings.Join(lintResult.Issues, "\n")
					}
				}
			}
			// Push branch
			if err := git.Push(ctx, task.Branch); err != nil {
				// GH-1389: Worktree push may fail with chdir error even if data was already pushed.
				// Check if branch exists on remote before declaring failure.
				if git.RemoteBranchExists(ctx, task.Branch) {
					log.Warn("Push reported error but branch exists on remote, continuing",
						slog.Any("error", err),
						slog.String("branch", task.Branch),
					)
				} else {
					result.Success = false
					result.Error = fmt.Sprintf("push failed: %v", err)
					r.reportProgress(task.ID, "PR Failed", 100, result.Error)
					return result, nil
				}
			}

			// GH-457: Use actual pushed HEAD as CommitSHA source of truth.
			// Self-review or quality retries may push new commits after
			// result.CommitSHA was captured, causing autopilot to check CI
			// against a stale SHA.
			if pushedSHA, pushErr := git.GetCurrentCommitSHA(ctx); pushErr == nil && pushedSHA != "" {
				if result.CommitSHA != "" && result.CommitSHA != pushedSHA {
					log.Info("CommitSHA updated after push (post-execution commits detected)",
						slog.String("task_id", task.ID),
						slog.String("old_sha", result.CommitSHA[:min(7, len(result.CommitSHA))]),
						slog.String("pushed_sha", pushedSHA[:min(7, len(pushedSHA))]),
					)
				}
				result.CommitSHA = pushedSHA
			} else if pushErr != nil {
				log.Warn("Failed to get pushed HEAD SHA, using tracked SHA",
					slog.String("task_id", task.ID),
					slog.Any("error", pushErr),
				)
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

			// Generate PR body with GitHub auto-close keyword
			issueNum := strings.TrimPrefix(task.ID, "GH-")
			prBody := fmt.Sprintf("## Summary\n\nAutomated PR created by Pilot for task %s.\n\nCloses #%s\n\n## Changes\n\n%s", task.ID, issueNum, task.Description)

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
			if syncErr := r.syncNavigatorIndex(task, "completed", executionPath); syncErr != nil {
				log.Warn("Failed to sync Navigator index", slog.Any("error", syncErr))
			}

			// GH-1063: Archive completed task documentation
			agentPath := filepath.Join(executionPath, ".agent")
			if archiveErr := ArchiveTaskDoc(agentPath, task.ID); archiveErr != nil {
				log.Warn("Failed to archive task documentation", slog.Any("error", archiveErr))
			}

			// GH-1388: Update feature matrix for feature tasks
			if strings.HasPrefix(task.Title, "feat(") {
				if fmErr := UpdateFeatureMatrix(agentPath, task, "v1.0.0"); fmErr != nil {
					log.Warn("Failed to update feature matrix", slog.Any("error", fmErr))
				}
			}

			// GH-1064: Create context marker for completed task
			marker := &ContextMarker{
				Name:        fmt.Sprintf("task-completed-%s", task.ID),
				Description: fmt.Sprintf("Task completed: %s", task.Title),
				TaskID:      task.ID,
				CurrentFocus: fmt.Sprintf("Completed %s. %d files changed, %d lines added, %d removed. Cost: $%.2f.",
					task.Title, result.FilesChanged, result.LinesAdded, result.LinesRemoved,
					result.EstimatedCostUSD),
			}

			// Add modified files list (GH-1388)
			if len(state.modifiedFiles) > 0 {
				marker.CurrentFocus += fmt.Sprintf(" Modified: %s.", strings.Join(state.modifiedFiles, ", "))
			}

			// Add commit SHA and PR info if available
			if result.CommitSHA != "" {
				marker.Commits = append(marker.Commits, result.CommitSHA)
			}
			if result.PRUrl != "" {
				marker.CurrentFocus += fmt.Sprintf(" PR: %s", result.PRUrl)
			}

			if createMarkerErr := CreateMarker(agentPath, marker); createMarkerErr != nil {
				log.Warn("Failed to create completion marker", slog.Any("error", createMarkerErr))
			} else {
				log.Debug("Created completion context marker", slog.String("marker_path", marker.FilePath))
			}
		}

		// GH-1018: Sync main branch with origin after task completion
		// This prevents local/remote divergence over time
		if r.config != nil && r.config.SyncMainAfterTask {
			if syncErr := r.syncMainBranch(ctx, task.ProjectPath); syncErr != nil {
				log.Warn("Failed to sync main branch", slog.Any("error", syncErr))
			}
		}

		// GH-1065: Store experiential memory after successful task completion
		if r.knowledge != nil {
			projectID := "pilot" // Default fallback
			if task.ProjectPath != "" {
				projectID = filepath.Base(task.ProjectPath)
			}

			// Enrich content with execution metrics
			content := fmt.Sprintf(
				"Completed %s: %s. Modified %d files. Duration: %v. Model: %s.",
				task.ID, task.Title, result.FilesChanged, duration, result.ModelName,
			)
			if result.IntentWarning != "" {
				content += fmt.Sprintf(" Intent warning: %s.", result.IntentWarning)
			}

			// Enrich context with branch, PR URL, and cost
			contextStr := fmt.Sprintf("Branch: %s, Cost: $%.2f",
				task.Branch, result.EstimatedCostUSD)
			if result.PRUrl != "" {
				contextStr += fmt.Sprintf(", PR: %s", result.PRUrl)
			}

			memory := &memory.Memory{
				Type:       memory.MemoryTypeLearning,
				Content:    content,
				Context:    contextStr,
				Confidence: 1.0,
				ProjectID:  projectID,
			}

			if addErr := r.knowledge.AddMemory(memory); addErr != nil {
				log.Warn("Failed to store task completion memory", slog.Any("error", addErr))
			} else {
				log.Debug("Stored task completion memory", slog.String("task_id", task.ID))
			}
		}
	}

	return result, nil
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

// CancelAll terminates all running subprocesses gracefully.
// It sends SIGTERM to allow processes to clean up, then forcefully kills
// any remaining processes after a 10-second grace period.
// This is called during graceful shutdown to prevent orphaned Claude Code processes.
func (r *Runner) CancelAll() {
	r.mu.Lock()
	// Copy running map to avoid holding lock during signals
	toCancel := make(map[string]*exec.Cmd, len(r.running))
	for id, cmd := range r.running {
		toCancel[id] = cmd
	}
	r.mu.Unlock()

	if len(toCancel) == 0 {
		return
	}

	r.log.Info("Cancelling all running tasks", slog.Int("count", len(toCancel)))

	// Send SIGTERM to all processes for graceful shutdown
	for id, cmd := range toCancel {
		if cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				r.log.Debug("Failed to send SIGTERM", slog.String("task_id", id), slog.Any("error", err))
			} else {
				r.log.Debug("Sent SIGTERM to process", slog.String("task_id", id), slog.Int("pid", cmd.Process.Pid))
			}
		}
	}

	// Wait 10s, then SIGKILL any remaining
	time.AfterFunc(10*time.Second, func() {
		r.mu.Lock()
		remaining := make(map[string]*exec.Cmd, len(r.running))
		for id, cmd := range r.running {
			remaining[id] = cmd
		}
		r.mu.Unlock()

		for id, cmd := range remaining {
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil {
					r.log.Debug("Failed to kill process", slog.String("task_id", id), slog.Any("error", err))
				} else {
					r.log.Info("Force killed process after grace period", slog.String("task_id", id), slog.Int("pid", cmd.Process.Pid))
				}
			}
		}
	})
}

// IsRunning returns true if the specified task is currently being executed.
func (r *Runner) IsRunning(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[taskID]
	return ok
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

	// Select model and effort (use same routing as main execution)
	selectedModel := r.modelRouter.SelectModel(task)
	selectedEffort := r.modelRouter.SelectEffort(task)

	// GH-1265: Determine if session resume is enabled and session ID is available
	var resumeSessionID string
	if r.config != nil && r.config.ClaudeCode != nil && r.config.ClaudeCode.UseSessionResume {
		if state.sessionID != "" {
			resumeSessionID = state.sessionID
			r.log.Debug("Using session resume for self-review",
				slog.String("task_id", task.ID),
				slog.String("session_id", resumeSessionID),
			)
		}
	}

	result, err := r.backend.Execute(reviewCtx, ExecuteOptions{
		Prompt:          reviewPrompt,
		ProjectPath:     task.ProjectPath,
		Verbose:         task.Verbose,
		Model:           selectedModel,
		Effort:          selectedEffort,
		ResumeSessionID: resumeSessionID,
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

	// GH-539: Check per-task token/duration limit on each event
	if r.tokenLimitCheck != nil && !state.budgetExceeded {
		if !r.tokenLimitCheck(taskID, event.TokensInput, event.TokensOutput) {
			state.budgetExceeded = true
			state.budgetReason = fmt.Sprintf("per-task limit exceeded at %d input + %d output tokens",
				state.tokensInput, state.tokensOutput)
			r.log.Warn("Per-task budget limit exceeded, cancelling execution",
				slog.String("task_id", taskID),
				slog.Int64("input_tokens", state.tokensInput),
				slog.Int64("output_tokens", state.tokensOutput),
			)
			if state.budgetCancel != nil {
				state.budgetCancel()
			}
			return // Skip further event processing
		}
	}

	switch event.Type {
	case EventTypeInit:
		// GH-1265: Capture session ID for resume in self-review
		if event.SessionID != "" {
			state.sessionID = event.SessionID
		}
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
	// Try structured signal parser v2 first (GH-960)
	if r.signalParser != nil {
		signals := r.signalParser.ParseSignals(text)
		if len(signals) > 0 {
			r.handleStructuredSignals(taskID, signals, state)
			return
		}
	}

	// Fall back to legacy string-based parsing for backward compatibility
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
			// Clamp progress to valid range (GH-941)
			if pct < 0 {
				r.log.Warn("Legacy parser: clamping negative progress",
					slog.String("task_id", taskID),
					slog.Int("original", pct),
				)
				pct = 0
			}
			if pct > 100 {
				r.log.Warn("Legacy parser: clamping progress > 100",
					slog.String("task_id", taskID),
					slog.Int("original", pct),
				)
				pct = 100
			}
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

// handleStructuredSignals processes v2 structured pilot signals (GH-960)
func (r *Runner) handleStructuredSignals(taskID string, signals []PilotSignal, state *progressState) {
	if len(signals) == 0 {
		return
	}

	// Mark as having Navigator
	state.hasNavigator = true

	// Process signals in order
	for _, signal := range signals {
		r.log.Debug("Processing structured signal",
			slog.String("task_id", taskID),
			slog.String("type", signal.Type),
			slog.String("phase", signal.Phase),
			slog.Int("progress", signal.Progress),
		)

		switch signal.Type {
		case SignalTypeStatus:
			// Update phase if provided
			if signal.Phase != "" {
				r.handleNavigatorPhase(taskID, signal.Phase, state)
			}
			// Update progress if provided
			if signal.Progress > 0 {
				state.navProgress = signal.Progress
			}
			// Update iteration if provided
			if signal.Iteration > 0 {
				state.navIteration = signal.Iteration
			}

		case SignalTypePhase:
			if signal.Phase != "" {
				r.handleNavigatorPhase(taskID, signal.Phase, state)
			}

		case SignalTypeExit:
			state.exitSignal = true
			r.reportProgress(taskID, "Finishing", 95, signal.Message)

		case SignalTypeStagnation:
			r.reportProgress(taskID, "⚠️ Stalled", 0, "Navigator detected stagnation")
		}

		// Check for exit signal from any signal type
		if signal.ExitSignal {
			state.exitSignal = true
			message := signal.Message
			if message == "" {
				message = "Exit signal detected"
			}
			r.reportProgress(taskID, "Finishing", 92, message)
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
			// Track actual modified files (GH-1388)
			if !strings.Contains(fp, ".agent/") {
				state.modifiedFiles = append(state.modifiedFiles, fp)
			}
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
// Pricing source: https://platform.claude.com/docs/en/about-claude/pricing
func estimateCost(inputTokens, outputTokens int64, model string) float64 {
	// Model pricing in USD per 1M tokens
	const (
		// Sonnet 4.5/4
		sonnetInputPrice  = 3.00
		sonnetOutputPrice = 15.00
		// Opus 4.6/4.5 (same pricing)
		opusInputPrice  = 5.00
		opusOutputPrice = 25.00
		// Opus 4.1/4.0 (legacy)
		opus41InputPrice  = 15.00
		opus41OutputPrice = 75.00
		// Haiku 4.5
		haikuInputPrice  = 1.00
		haikuOutputPrice = 5.00
	)

	var inputPrice, outputPrice float64
	modelLower := strings.ToLower(model)
	switch {
	case strings.Contains(modelLower, "opus-4-1") || strings.Contains(modelLower, "opus-4-0") || model == "claude-opus-4":
		// Legacy Opus 4.1/4.0
		inputPrice = opus41InputPrice
		outputPrice = opus41OutputPrice
	case strings.Contains(modelLower, "opus"):
		// Opus 4.6/4.5 ($5/$25)
		inputPrice = opusInputPrice
		outputPrice = opusOutputPrice
	case strings.Contains(modelLower, "haiku"):
		inputPrice = haikuInputPrice
		outputPrice = haikuOutputPrice
	case strings.Contains(modelLower, "qwen"):
		// Qwen3-Coder pricing (per 1M tokens)
		switch {
		case strings.Contains(modelLower, "480b") || strings.Contains(modelLower, "plus"):
			inputPrice = 1.00  // Qwen3-Coder-Plus (International, 0-32K)
			outputPrice = 5.00
		case strings.Contains(modelLower, "flash"):
			inputPrice = 0.30
			outputPrice = 1.50
		default:
			inputPrice = 0.07  // Qwen3-Coder-Next (default)
			outputPrice = 0.30
		}
	default:
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

// reportProgress sends a progress update to all registered callbacks.
// Progress is monotonic — values lower than the current high-water mark
// for a task are clamped upward to prevent dashboard progress regression.
func (r *Runner) reportProgress(taskID, phase string, progress int, message string) {
	// Enforce monotonic progress per task (never go backwards)
	if progress < 100 { // Allow 100 from any state (completion/failure)
		r.taskProgressMu.Lock()
		if r.taskProgress == nil {
			r.taskProgress = make(map[string]int)
		}
		if prev, ok := r.taskProgress[taskID]; ok && progress < prev {
			progress = prev // Clamp to high-water mark
		}
		r.taskProgress[taskID] = progress
		r.taskProgressMu.Unlock()
	} else {
		// Task done — clean up tracking
		r.taskProgressMu.Lock()
		delete(r.taskProgress, taskID)
		r.taskProgressMu.Unlock()
	}

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

// PostExecutionSummary contains git state information extracted via structured output
type PostExecutionSummary struct {
	BranchName   string   `json:"branch_name"`
	CommitSHA    string   `json:"commit_sha"`
	FilesChanged []string `json:"files_changed"`
	Summary      string   `json:"summary"`
}

// getPostExecutionSummary runs a structured output query to extract git state information.
// This replaces brittle regex parsing of git output with reliable --json-schema extraction.
func (r *Runner) getPostExecutionSummary(ctx context.Context) (*PostExecutionSummary, error) {
	if r.config == nil || r.config.ClaudeCode == nil {
		return nil, fmt.Errorf("claude code backend not configured")
	}

	prompt := "Report git state: run 'git log --oneline -1' and 'git branch --show-current' and 'git diff --name-only HEAD~1'. Return branch name, latest commit SHA, and changed files."

	// Use fast Haiku model for this simple task
	claudeCmd := "claude"
	if r.config.ClaudeCode.Command != "" {
		claudeCmd = r.config.ClaudeCode.Command
	}
	cmd := exec.CommandContext(ctx, claudeCmd,
		"--print",
		"-p", prompt,
		"--model", "claude-haiku-4-5-20251001",
		"--output-format", "json",
		"--json-schema", PostExecutionSummarySchema,
	)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude command failed: %w", err)
	}

	structuredOutput, err := extractStructuredOutput(output)
	if err != nil {
		return nil, fmt.Errorf("extract structured output: %w", err)
	}

	var summary PostExecutionSummary
	if err := json.Unmarshal(structuredOutput, &summary); err != nil {
		return nil, fmt.Errorf("parse post-execution summary: %w", err)
	}

	return &summary, nil
}
