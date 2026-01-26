package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Executor manages sandboxed task executions
type Executor struct {
	store         *Store
	containerCfg  ContainerConfig
	limits        ResourceLimits
	maxConcurrent int
	activeMu      sync.RWMutex
	active        map[uuid.UUID]*runningExecution
	progressChan  chan ProgressUpdate
}

type runningExecution struct {
	execution   *Execution
	cancel      context.CancelFunc
	containerID string
}

// NewExecutor creates a new sandboxed executor
func NewExecutor(store *Store, containerCfg ContainerConfig, limits ResourceLimits, maxConcurrent int) *Executor {
	return &Executor{
		store:         store,
		containerCfg:  containerCfg,
		limits:        limits,
		maxConcurrent: maxConcurrent,
		active:        make(map[uuid.UUID]*runningExecution),
		progressChan:  make(chan ProgressUpdate, 100),
	}
}

// ProgressUpdates returns a channel for receiving progress updates
func (e *Executor) ProgressUpdates() <-chan ProgressUpdate {
	return e.progressChan
}

// Submit queues an execution request
func (e *Executor) Submit(ctx context.Context, req ExecutionRequest) (*Execution, error) {
	now := time.Now()
	execution := &Execution{
		ID:             uuid.New(),
		OrgID:          req.OrgID,
		ProjectID:      req.ProjectID,
		ExternalTaskID: req.ExternalTaskID,
		Status:         StatusQueued,
		Phase:          PhaseStarting,
		Progress:       0,
		CreatedAt:      now,
	}

	if err := e.store.CreateExecution(ctx, execution); err != nil {
		return nil, fmt.Errorf("failed to create execution: %w", err)
	}

	// Try to start immediately if capacity available
	go e.tryStart(ctx, execution, req.Prompt, req.Branch)

	return execution, nil
}

// tryStart attempts to start an execution if capacity is available
func (e *Executor) tryStart(ctx context.Context, execution *Execution, prompt, branch string) {
	e.activeMu.Lock()
	if len(e.active) >= e.maxConcurrent {
		e.activeMu.Unlock()
		return // Will be picked up by queue processor
	}

	execCtx, cancel := context.WithTimeout(ctx, e.limits.MaxDuration)
	e.active[execution.ID] = &runningExecution{
		execution: execution,
		cancel:    cancel,
	}
	e.activeMu.Unlock()

	go e.run(execCtx, execution, prompt, branch)
}

// run executes the task in a sandboxed container
func (e *Executor) run(ctx context.Context, execution *Execution, prompt, branch string) {
	defer func() {
		e.activeMu.Lock()
		delete(e.active, execution.ID)
		e.activeMu.Unlock()
	}()

	startTime := time.Now()

	// Update status to running
	execution.Status = StatusRunning
	execution.StartedAt = &startTime
	_ = e.store.UpdateExecution(ctx, execution)

	e.emitProgress(execution.ID, PhaseStarting, 5, "Starting execution")

	// Build the execution command
	result, err := e.executeInContainer(ctx, execution, prompt, branch)

	endTime := time.Now()
	execution.DurationMs = endTime.Sub(startTime).Milliseconds()
	execution.CompletedAt = &endTime

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			execution.Status = StatusTimeout
			execution.Error = "execution timed out"
		} else if ctx.Err() == context.Canceled {
			execution.Status = StatusCancelled
			execution.Error = "execution cancelled"
		} else {
			execution.Status = StatusFailed
			execution.Error = err.Error()
		}
	} else {
		execution.Status = StatusCompleted
		execution.Output = result.Output
		execution.PRUrl = result.PRUrl
		execution.CommitSHA = result.CommitSHA
		execution.TokensUsed = result.TokensUsed
	}

	_ = e.store.UpdateExecution(ctx, execution)
	e.emitProgress(execution.ID, PhaseCompleted, 100, "Execution complete")
}

// executeInContainer runs the task in a sandboxed environment
func (e *Executor) executeInContainer(ctx context.Context, execution *Execution, prompt, branch string) (*ExecutionResult, error) {
	// Get project info from store
	project, err := e.store.GetProject(ctx, execution.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	// Create container with resource limits
	containerID, err := e.createContainer(ctx, execution.ID, project.RepoURL, branch)
	if err != nil {
		return nil, fmt.Errorf("failed to create container: %w", err)
	}
	defer e.destroyContainer(ctx, containerID)

	// Update execution with container ID
	e.activeMu.Lock()
	if running, ok := e.active[execution.ID]; ok {
		running.containerID = containerID
	}
	e.activeMu.Unlock()

	e.emitProgress(execution.ID, PhaseBranching, 10, "Setting up workspace")

	// Clone repo and setup Navigator
	if err := e.setupWorkspace(ctx, containerID, project.RepoURL, branch); err != nil {
		return nil, fmt.Errorf("failed to setup workspace: %w", err)
	}

	e.emitProgress(execution.ID, PhaseExploring, 20, "Running Claude Code")

	// Run Claude Code in container
	result, err := e.runClaudeCode(ctx, containerID, prompt, project.Settings.NavigatorEnabled)
	if err != nil {
		return nil, fmt.Errorf("claude code failed: %w", err)
	}

	return result, nil
}

// createContainer creates a sandboxed container for execution
func (e *Executor) createContainer(ctx context.Context, executionID uuid.UUID, repoURL, branch string) (string, error) {
	// Using Fly.io Machines API for fast container creation
	// In production, this would call the Fly.io API
	// For now, we simulate with Docker

	args := []string{
		"run", "-d",
		"--name", fmt.Sprintf("pilot-exec-%s", executionID.String()[:8]),
		"--memory", e.containerCfg.Memory,
		"--cpus", e.containerCfg.CPU,
		"--network", "pilot-sandbox",
		"-e", fmt.Sprintf("EXECUTION_ID=%s", executionID),
		"-e", fmt.Sprintf("REPO_URL=%s", repoURL),
		"-e", fmt.Sprintf("BRANCH=%s", branch),
		e.containerCfg.Image,
		"sleep", "infinity", // Keep alive for exec commands
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	containerID := strings.TrimSpace(string(output))
	return containerID, nil
}

// destroyContainer removes the container
func (e *Executor) destroyContainer(ctx context.Context, containerID string) {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", containerID)
	_ = cmd.Run()
}

// setupWorkspace clones the repo and prepares the environment
func (e *Executor) setupWorkspace(ctx context.Context, containerID, repoURL, branch string) error {
	// Clone the repository
	cloneCmd := fmt.Sprintf("git clone --depth 1 %s /workspace", repoURL)
	if branch != "" {
		cloneCmd = fmt.Sprintf("git clone --depth 1 -b %s %s /workspace", branch, repoURL)
	}

	if err := e.execInContainer(ctx, containerID, cloneCmd); err != nil {
		return fmt.Errorf("failed to clone repo: %w", err)
	}

	// Install dependencies if package.json exists
	checkCmd := "test -f /workspace/package.json && cd /workspace && npm install || true"
	_ = e.execInContainer(ctx, containerID, checkCmd)

	return nil
}

// runClaudeCode executes Claude Code in the container
func (e *Executor) runClaudeCode(ctx context.Context, containerID, prompt string, navigatorEnabled bool) (*ExecutionResult, error) {
	// Build Claude Code command
	claudePrompt := prompt
	if navigatorEnabled {
		claudePrompt = "Start my Navigator session.\n\n" + prompt + "\n\nRun until done. Use Navigator's autonomous completion protocol.\n\nCRITICAL: You MUST commit all changes before completing."
	}

	// Escape the prompt for shell
	escapedPrompt := strings.ReplaceAll(claudePrompt, "'", "'\\''")

	cmd := fmt.Sprintf(`cd /workspace && claude -p '%s' --verbose --output-format stream-json --dangerously-skip-permissions 2>&1`, escapedPrompt)

	// Execute and capture output
	execCmd := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", cmd)
	output, err := execCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude code execution failed: %w", err)
	}

	// Parse the stream-json output
	result := &ExecutionResult{
		Status: StatusCompleted,
	}

	lines := strings.Split(string(output), "\n")
	var lastResultEvent map[string]interface{}

	for _, line := range lines {
		if !strings.HasPrefix(line, "{") {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Check for result event
		if eventType, ok := event["type"].(string); ok && eventType == "result" {
			lastResultEvent = event
		}

		// Extract PR URL if present
		if msg, ok := event["message"].(string); ok {
			if strings.Contains(msg, "github.com") && strings.Contains(msg, "/pull/") {
				// Extract PR URL
				start := strings.Index(msg, "https://github.com")
				if start != -1 {
					end := strings.IndexAny(msg[start:], " \n\t")
					if end == -1 {
						result.PRUrl = msg[start:]
					} else {
						result.PRUrl = msg[start : start+end]
					}
				}
			}
		}
	}

	if lastResultEvent != nil {
		if output, ok := lastResultEvent["result"].(string); ok {
			result.Output = output
		}
		if tokensUsed, ok := lastResultEvent["total_cost_usd"].(float64); ok {
			result.TokensUsed = int64(tokensUsed * 1000000) // Convert to micro-dollars
		}
	}

	// Get commit SHA
	sha, _ := e.getLastCommitSHA(ctx, containerID)
	result.CommitSHA = sha

	return result, nil
}

// execInContainer runs a command in the container
func (e *Executor) execInContainer(ctx context.Context, containerID, cmd string) error {
	execCmd := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", cmd)
	return execCmd.Run()
}

// getLastCommitSHA gets the last commit SHA from the workspace
func (e *Executor) getLastCommitSHA(ctx context.Context, containerID string) (string, error) {
	execCmd := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", "cd /workspace && git rev-parse HEAD")
	output, err := execCmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// emitProgress sends a progress update
func (e *Executor) emitProgress(executionID uuid.UUID, phase ExecutionPhase, progress int, message string) {
	select {
	case e.progressChan <- ProgressUpdate{
		ExecutionID: executionID,
		Phase:       phase,
		Progress:    progress,
		Message:     message,
		Timestamp:   time.Now(),
	}:
	default:
		// Channel full, skip
	}
}

// Cancel cancels a running execution
func (e *Executor) Cancel(ctx context.Context, executionID uuid.UUID) error {
	e.activeMu.RLock()
	running, ok := e.active[executionID]
	e.activeMu.RUnlock()

	if !ok {
		return fmt.Errorf("execution not running")
	}

	running.cancel()

	if running.containerID != "" {
		e.destroyContainer(ctx, running.containerID)
	}

	return nil
}

// GetExecution retrieves an execution by ID
func (e *Executor) GetExecution(ctx context.Context, id uuid.UUID) (*Execution, error) {
	return e.store.GetExecution(ctx, id)
}

// ListExecutions returns executions for an organization
func (e *Executor) ListExecutions(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*Execution, error) {
	return e.store.ListExecutions(ctx, orgID, limit, offset)
}

// GetQueueStats returns queue statistics
func (e *Executor) GetQueueStats(ctx context.Context) (*QueueStats, error) {
	e.activeMu.RLock()
	running := len(e.active)
	e.activeMu.RUnlock()

	pending, err := e.store.CountExecutionsByStatus(ctx, StatusPending)
	if err != nil {
		return nil, err
	}

	queued, err := e.store.CountExecutionsByStatus(ctx, StatusQueued)
	if err != nil {
		return nil, err
	}

	return &QueueStats{
		Running: running,
		Pending: pending,
		Queued:  queued,
	}, nil
}
