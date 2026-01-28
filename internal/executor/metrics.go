package executor

import (
	"log/slog"
	"time"
)

// ExecutionMetrics captures detailed metrics about a task execution.
// Used for observability, cost tracking, and performance analysis.
type ExecutionMetrics struct {
	// TaskID is the identifier of the executed task
	TaskID string

	// Complexity is the detected task complexity level
	Complexity Complexity

	// Model is the AI model used for execution
	Model string

	// Duration is the total execution time
	Duration time.Duration

	// NavigatorSkipped indicates if Navigator overhead was skipped for trivial tasks
	NavigatorSkipped bool

	// TokensIn is the number of input tokens consumed
	TokensIn int64

	// TokensOut is the number of output tokens generated
	TokensOut int64

	// EstimatedCostUSD is the estimated cost based on model and token usage
	EstimatedCostUSD float64

	// Phase is the final execution phase (Completed, Failed, TimedOut)
	Phase string

	// FilesRead is the number of files read during execution
	FilesRead int

	// FilesWritten is the number of files written during execution
	FilesWritten int

	// CommitSHA is the git commit SHA if a commit was made
	CommitSHA string

	// Timeout is the configured timeout for this execution
	Timeout time.Duration

	// TimedOut indicates if the task was terminated due to timeout
	TimedOut bool
}

// LogAttrs returns slog attributes for structured logging.
func (m *ExecutionMetrics) LogAttrs() []slog.Attr {
	return []slog.Attr{
		slog.String("task_id", m.TaskID),
		slog.String("complexity", m.Complexity.String()),
		slog.String("model", m.Model),
		slog.Duration("duration", m.Duration),
		slog.Bool("navigator_skipped", m.NavigatorSkipped),
		slog.Int64("tokens_in", m.TokensIn),
		slog.Int64("tokens_out", m.TokensOut),
		slog.Float64("cost_usd", m.EstimatedCostUSD),
		slog.String("phase", m.Phase),
		slog.Int("files_read", m.FilesRead),
		slog.Int("files_written", m.FilesWritten),
		slog.String("commit_sha", m.CommitSHA),
		slog.Duration("timeout", m.Timeout),
		slog.Bool("timed_out", m.TimedOut),
	}
}

// LogValue implements slog.LogValuer for structured logging.
func (m *ExecutionMetrics) LogValue() slog.Value {
	return slog.GroupValue(m.LogAttrs()...)
}

// NewExecutionMetrics creates a new ExecutionMetrics from execution state.
func NewExecutionMetrics(
	taskID string,
	complexity Complexity,
	model string,
	duration time.Duration,
	state *progressState,
	timeout time.Duration,
	timedOut bool,
) *ExecutionMetrics {
	return &ExecutionMetrics{
		TaskID:           taskID,
		Complexity:       complexity,
		Model:            model,
		Duration:         duration,
		NavigatorSkipped: complexity.ShouldSkipNavigator(),
		TokensIn:         state.tokensInput,
		TokensOut:        state.tokensOutput,
		EstimatedCostUSD: estimateCost(state.tokensInput, state.tokensOutput, model),
		Phase:            state.phase,
		FilesRead:        state.filesRead,
		FilesWritten:     state.filesWrite,
		CommitSHA:        lastCommitSHA(state.commitSHAs),
		Timeout:          timeout,
		TimedOut:         timedOut,
	}
}

// lastCommitSHA returns the last SHA from a slice, or empty string if none.
func lastCommitSHA(shas []string) string {
	if len(shas) == 0 {
		return ""
	}
	return shas[len(shas)-1]
}
