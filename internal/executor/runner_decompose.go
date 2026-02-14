package executor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/webhooks"
)

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
		// GH-836: Hard fail if we can't switch - continuing from wrong branch causes corrupted PRs.
		defaultBranch, err := git.SwitchToDefaultBranchAndPull(ctx)
		if err != nil {
			return nil, fmt.Errorf("branch switch failed, aborting execution: failed to switch to default branch: %w", err)
		}
		r.reportProgress(parentTask.ID, "Branching", 2, fmt.Sprintf("On %s, creating %s...", defaultBranch, parentTask.Branch))

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

		subtaskResult, err := r.executeWithOptions(ctx, subtask, false)

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
