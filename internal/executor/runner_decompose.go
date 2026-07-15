package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/webhooks"
)

// executeDecomposedTask runs subtasks sequentially and aggregates results (GH-218).
// Each subtask runs to completion before the next starts. Only the final subtask
// creates a PR (CreatePR is already set by the decomposer). All changes accumulate
// on the same branch, so the final PR contains all subtask work.
//
// GH-1235: executionPath is passed explicitly to handle worktree isolation.
// When worktree mode is active, executionPath differs from parentTask.ProjectPath
// and the branch is already checked out in the worktree, so we skip branch creation.
func (r *Runner) executeDecomposedTask(ctx context.Context, parentTask *Task, subtasks []*Task, executionPath string) (*ExecutionResult, error) {
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

	// GH-1235: Use executionPath for git operations - this is the worktree path when
	// worktree isolation is active, or parentTask.ProjectPath in non-worktree mode.
	git := NewGitOperations(executionPath)

	// GH-1235: Only create branch in non-worktree mode. When worktree mode is active,
	// the worktree was already created with the correct branch checked out, and trying
	// to checkout the branch again fails because it's locked by the active worktree.
	inWorktreeMode := executionPath != parentTask.ProjectPath
	if parentTask.Branch != "" && !inWorktreeMode {
		r.reportProgress(parentTask.ID, "Branching", 1, "Switching to default branch...")

		// GH-279: Always switch to default branch and pull latest before creating new branch.
		// This prevents new branches from forking off previous pilot branches instead of main.
		// GH-836: Hard fail if we can't switch - continuing from wrong branch causes corrupted PRs.
		defaultBranch, err := git.SwitchToDefaultBranchAndPull(ctx)
		if err != nil {
			return nil, fmt.Errorf("branch switch failed, aborting execution: failed to switch to default branch: %w", err)
		}
		r.reportProgress(parentTask.ID, "Branching", 2, fmt.Sprintf("On %s, creating %s...", defaultBranch, parentTask.Branch))

		// GH-1235: Use CreateOrResetBranch (-B flag) instead of CreateBranch (-b flag)
		// because worktree mode may have already created this branch. The -B flag
		// handles both cases: creates if missing, resets if exists.
		if err := git.CreateOrResetBranch(ctx, parentTask.Branch); err != nil {
			return nil, fmt.Errorf("failed to create/reset branch: %w", err)
		}
		r.reportProgress(parentTask.ID, "Branching", 5, fmt.Sprintf("Branch %s ready", parentTask.Branch))
	} else if parentTask.Branch != "" && inWorktreeMode {
		r.reportProgress(parentTask.ID, "Branching", 5, fmt.Sprintf("Branch %s already checked out in worktree", parentTask.Branch))
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

		// GH-1235: Execute subtasks in the worktree when worktree mode is active
		subtask.ProjectPath = executionPath

		// GH-4339: register this subtask's planned title with the monitor
		// before it starts executing. subtask.ID (e.g. "GH-4328-1") has no
		// monitor entry yet at this point — without a Register call here, the
		// first progress callback for that ID (from executeWithOptions below)
		// hits Monitor.UpdateProgress's unknown-taskID fallback, which creates
		// the entry with Title == ID. The dashboard then renders the bare
		// sub-issue ID as its own title instead of the planned subtask
		// summary.
		if r.monitor != nil {
			r.monitor.Register(subtask.ID, subtask.Title, "")
		}

		// Temporarily disable decomposer to prevent recursive decomposition
		savedDecomposer := r.decomposer
		r.decomposer = nil

		var subtaskResult *ExecutionResult
		var err error
		if r.executeFunc != nil {
			// Internal override for testing (mirrors epic.go's sub-issue loop).
			subtaskResult, err = r.executeFunc(ctx, subtask)
		} else {
			subtaskResult, err = r.executeWithOptions(ctx, subtask, false)
		}

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
			// TASK-320 B2: a subtask that produced no commit (analysis, verification,
			// or a change already present on the branch) is NOT a task failure. The
			// ghost-SHA guard flags every no-commit run as !Success, which previously
			// aborted the whole decomposed task on the FIRST such subtask (the
			// "subtask 1/5 failed: no new commit produced" freeze, GH-3470/GH-3228).
			// Aggregate its cost and continue; the task only no-ops if NO subtask
			// delivers a commit, checked after the loop.
			if isNoOpResult(subtaskResult) {
				aggregateSubtaskCost(aggregateResult, subtaskResult)
				r.log.Info("Subtask produced no commit; continuing decomposition (TASK-320 B2)",
					slog.String("subtask_id", subtask.ID),
					slog.Int("index", subtaskNum),
					slog.Int("total", totalSubtasks),
					slog.String("reason", subtaskResult.Error),
				)
				continue
			}
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
		aggregateResult.CacheCreationInputTokens += subtaskResult.CacheCreationInputTokens
		aggregateResult.CacheReadInputTokens += subtaskResult.CacheReadInputTokens
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

	// TASK-320 B2: every subtask ran without a hard error, but none delivered a
	// commit — the decomposed task is a genuine no-op. Escalate the final subtask
	// once with the evidence-backed directive before declaring a terminal no-op,
	// so a model that silently refused an explicit spec gets one firm re-prompt.
	// Never surface an empty error string for this path (acceptance: descriptive).
	if aggregateResult.Success && aggregateResult.CommitSHA == "" && aggregateResult.PRUrl == "" && len(subtasks) > 0 {
		r.escalateDecomposedNoOp(ctx, parentTask, subtasks, executionPath, aggregateResult)
	}

	// GH-4028 / TASK-359 Layer 1: subtasks run with task.Branch cleared (see
	// "subtask.Branch = ..." above), so the direct path's push+PR block
	// (task.CreatePR && task.Branch != "") never fires for any subtask —
	// including the final one, which carries the parent's real CreatePR
	// intent. The decomposed parent must finalize push+PR itself once all
	// subtasks (and any no-op escalation) are done.
	if aggregateResult.Success && parentTask.CreatePR && parentTask.Branch != "" && aggregateResult.PRUrl == "" {
		r.finalizeDecomposedParentPR(ctx, parentTask, git, aggregateResult)
	}

	aggregateResult.Duration = time.Since(start)
	aggregateResult.EstimatedCostUSD = estimateCostWithCache(
		aggregateResult.TokensInput+aggregateResult.ResearchTokens,
		aggregateResult.TokensOutput,
		aggregateResult.CacheCreationInputTokens,
		aggregateResult.CacheReadInputTokens,
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

// isNoOpResult reports whether a subtask result is a no-commit no-op (analysis,
// verification, or a change already present) rather than a real failure. The
// ghost-SHA guard marks every no-commit run as !Success; this distinguishes the
// benign case from a genuine error so a decomposed task is not aborted by an
// early non-delivering subtask. TASK-320 B2.
func isNoOpResult(res *ExecutionResult) bool {
	if res == nil || res.Success {
		return false
	}
	return res.Outcome == "no_op" || containsAny(res.Error, noOpErrorSignatures)
}

// aggregateSubtaskCost folds a subtask's token/research usage into the aggregate.
// Cost accrues even for no-op subtasks (the model still ran); commit/PR/quality
// are aggregated separately, only on delivery.
func aggregateSubtaskCost(agg, sub *ExecutionResult) {
	agg.TokensInput += sub.TokensInput
	agg.TokensOutput += sub.TokensOutput
	agg.TokensTotal += sub.TokensTotal
	agg.CacheCreationInputTokens += sub.CacheCreationInputTokens
	agg.CacheReadInputTokens += sub.CacheReadInputTokens
	agg.ResearchTokens += sub.ResearchTokens
	if sub.ModelName != "" {
		agg.ModelName = sub.ModelName
	}
}

// escalateDecomposedNoOp re-runs the final subtask exactly once with the
// evidence-backed directive when the whole decomposed task delivered no commit.
// On recovery it folds the commit/PR into agg; otherwise it sets a descriptive
// terminal no-op error (never empty). TASK-320 B2.
//
// This lives at the decomposition layer — it re-invokes executeWithOptions rather
// than duplicating the ghost-SHA/SHA-harvest logic inside that ~1700-line function
// (the in-executor variant the task doc flagged as needing a structured refactor).
func (r *Runner) escalateDecomposedNoOp(ctx context.Context, parentTask *Task, subtasks []*Task, executionPath string, agg *ExecutionResult) {
	final := subtasks[len(subtasks)-1]

	escalated := *final // shallow copy; only value fields below are mutated
	escalated.Branch = ""
	escalated.ProjectPath = executionPath
	escalated.Description = final.Description + "\n\n" + EvidenceBackedSpecDirective

	r.log.Warn("Decomposed task produced no commit; escalating final subtask once (TASK-320 B2)",
		slog.String("parent_id", parentTask.ID),
		slog.String("subtask_id", final.ID),
	)

	savedDecomposer := r.decomposer
	r.decomposer = nil
	retryResult, err := r.executeWithOptions(ctx, &escalated, false)
	r.decomposer = savedDecomposer

	if err == nil && retryResult != nil && retryResult.Success && retryResult.CommitSHA != "" {
		aggregateSubtaskCost(agg, retryResult)
		agg.CommitSHA = retryResult.CommitSHA
		agg.PRUrl = retryResult.PRUrl
		agg.FilesChanged += retryResult.FilesChanged
		agg.LinesAdded += retryResult.LinesAdded
		agg.LinesRemoved += retryResult.LinesRemoved
		if retryResult.QualityGates != nil {
			agg.QualityGates = retryResult.QualityGates
		}
		r.log.Info("Escalated retry recovered the no-op (TASK-320 B2)",
			slog.String("parent_id", parentTask.ID),
			slog.String("commit_sha", retryResult.CommitSHA),
		)
		return
	}

	if retryResult != nil {
		aggregateSubtaskCost(agg, retryResult)
	}
	agg.Success = false
	agg.Error = fmt.Sprintf("no new commit produced — all %d subtask(s) were no-ops after one escalated retry (task %s)", len(subtasks), parentTask.ID)
}

// finalizeDecomposedParentPR runs the decomposed-parent's push → PR-create →
// record pr_created sequence with the SAME error contract as the direct path
// (runner.go executeWithOptions ~line 3782) and finalizeEpicBranchPR
// (TASK-359 Layer 1): a push or PR-create failure marks the execution
// failed, never leaving a "completed" row with an empty pr_url.
//
// GH-4028: every subtask executes with task.Branch cleared (so the direct
// path's own `task.CreatePR && task.Branch != ""` push+PR block never runs
// for a subtask), so the parent branch was never pushed and no PR was ever
// opened even though the final subtask carried the parent's CreatePR
// intent. This is called once, after all subtasks (and any no-op
// escalation) have finished, using the parent task's own Branch/CreatePR.
func (r *Runner) finalizeDecomposedParentPR(ctx context.Context, task *Task, git *GitOperations, result *ExecutionResult) {
	log := r.log

	// TASK-359 Shape C / GH-4022: an already-merged branch short-circuits
	// push+CreatePR — check first, since a merged branch's remote copy may
	// already have been deleted by autopilot.
	if r.checkAlreadyMergedBranch(ctx, git, task, result, nil) {
		return
	}

	baseBranch := task.BaseBranch
	if baseBranch == "" {
		baseBranch, _ = git.GetDefaultBranch(ctx)
		if baseBranch == "" {
			baseBranch = "main"
		}
	}

	// GH-2743-style no-commits guard: a decomposed parent that reaches this
	// point with no commits vs base produced nothing on the branch — unlike
	// the epic path (children may have shipped their own PRs), a
	// decomposed-task branch with zero commits is a genuine failure.
	if guardCount, _ := git.CountNewCommits(ctx, baseBranch); guardCount == 0 {
		result.Success = false
		result.Error = "decomposed-parent branch has no commits vs base branch"
		r.reportProgress(task.ID, "PR Failed", 100, result.Error)
		return
	}

	// GH-4286: strip any memory doc a subtask session committed without
	// indexing in graph.json — left in place it trips the Knowledge Graph
	// Drift Gate and can cost this PR to the autopilot CI-fix/size-guard path
	// (see PR #4279).
	if stripped, stripErr := git.StripUnindexedMemoryDocs(ctx, baseBranch); stripErr != nil {
		log.Warn("Failed to strip unindexed memory doc(s) from decomposed-parent branch",
			slog.String("task_id", task.ID),
			slog.Any("error", stripErr),
		)
	} else if len(stripped) > 0 {
		log.Info("Stripped unindexed memory doc(s) from decomposed-parent branch to avoid drift-gate failure",
			slog.String("task_id", task.ID),
			slog.Any("files", stripped),
		)
	}

	r.reportProgress(task.ID, "Creating PR", 96, "Pushing branch...")

	var pushErr error
	for attempt := 1; attempt <= gitPushRetryAttempts; attempt++ {
		pushErr = git.Push(ctx, task.Branch)
		if pushErr == nil {
			break
		}
		// GH-1389: a worktree push may report a chdir error even though the
		// data reached the remote.
		if git.RemoteBranchExists(ctx, task.Branch) {
			log.Warn("Decomposed-parent push reported error but branch exists on remote, continuing",
				slog.String("task_id", task.ID),
				slog.String("branch", task.Branch),
				slog.Any("error", pushErr),
			)
			pushErr = nil
			break
		}
		if attempt < gitPushRetryAttempts {
			log.Warn("Decomposed-parent push failed, retrying",
				slog.String("task_id", task.ID),
				slog.Int("attempt", attempt),
				slog.Int("max_attempts", gitPushRetryAttempts),
				slog.Any("error", pushErr),
			)
			time.Sleep(gitPushRetryDelay)
		}
	}
	if pushErr != nil {
		result.Success = false
		result.Error = formatGitStepFailureWithRecovery(ctx, git, "push", gitPushRetryAttempts, pushErr, task.ID, task.Branch, result.CommitSHA)
		r.reportProgress(task.ID, "PR Failed", 100, result.Error)
		return
	}

	// GH-457: use the actual pushed HEAD as the CommitSHA source of truth.
	if pushedSHA, shaErr := git.GetCurrentCommitSHA(ctx); shaErr == nil && pushedSHA != "" {
		result.CommitSHA = pushedSHA
	}

	// GH-4022: adopt an already-open PR for this branch instead of racing
	// gh CLI into a duplicate. Runs after push so the branch is guaranteed
	// to exist on the remote for the lookup.
	if r.adoptOpenBranchPR(ctx, git, task, result, nil) {
		return
	}

	r.reportProgress(task.ID, "Creating PR", 98, "Creating pull request...")

	issueNum := strings.TrimPrefix(task.ID, "GH-")

	// GH-4220 parity: finalizeDecomposedParentPR built its title the same way
	// finalizeEpicBranchPR (runner.go) did before that fix — raw
	// "<task.ID>: <task.Title>" with no conventional-commit normalization.
	// A decomposed parent's own task.Title is a raw issue title just like the
	// epic parent's, so it hits the same validatePRTitle rejection (git.go:178)
	// deterministically. Route it through the same normalizeTitle machinery.
	decomposedDiffStats, _ := git.GetDiffStats(ctx, baseBranch)
	normalizedDecomposedTitle, titleErr := normalizeTitle(task.Title, task.Labels, decomposedDiffStats)
	if titleErr != nil {
		result.Success = false
		result.Error = fmt.Sprintf("decomposed-parent PR creation failed: %v", titleErr)
		log.Warn("Decomposed-parent PR creation refused: non-conventional title",
			slog.String("task_id", task.ID),
			slog.String("title", task.Title),
			slog.Any("labels", task.Labels),
		)
		// GH-4220 (e): parity with the direct path's GH-2363 escalation — see
		// recordTitleRejection (title_rejection.go) for why epic/decomposed
		// paths need the same stop-retry guard the direct path already has.
		r.recordTitleRejection(ctx, task, result)
		r.reportProgress(task.ID, "PR Failed", 100, result.Error)
		return
	}
	r.clearTitleRejectionState(task)
	prTitle := fmt.Sprintf("%s: %s", task.ID, normalizedDecomposedTitle)

	// Route PR/MR creation through the adapter-specific creator when
	// available, mirroring the direct path's registry → non-GitHub
	// PRCreator → gh CLI fallback order (runner.go ~line 3967).
	var prURL string
	var createErr error
	ghSDKCreator := PRCreator(nil)
	if task.SourceAdapter == "github" && task.SourceRepo != "" {
		ghSDKCreator = r.prCreatorFor("github:" + task.SourceRepo)
	}
	switch {
	case ghSDKCreator != nil:
		prBody := fmt.Sprintf("## Summary\n\nAutomated PR created by Pilot for task %s.\n\nCloses #%s\n\n## Changes\n\n%s", task.ID, issueNum, task.Description)
		for attempt := 1; attempt <= prCreateRetryAttempts; attempt++ {
			prURL, createErr = ghSDKCreator.CreatePR(ctx, task.Branch, baseBranch, prTitle, prBody)
			if createErr == nil {
				break
			}
			if attempt < prCreateRetryAttempts {
				log.Warn("Decomposed-parent PR creation failed (SDK), retrying",
					slog.String("task_id", task.ID),
					slog.Int("attempt", attempt),
					slog.Int("max_attempts", prCreateRetryAttempts),
					slog.Any("error", createErr),
				)
				time.Sleep(prCreateRetryDelay)
			}
		}
	case r.prCreator != nil && task.SourceAdapter != "" && task.SourceAdapter != "github":
		closeKeyword := ""
		if task.SourceIssueID != "" {
			closeKeyword = fmt.Sprintf("\n\nCloses #%s", task.SourceIssueID)
		}
		prBody := fmt.Sprintf("## Summary\n\nAutomated MR created by Pilot for task %s.%s\n\n## Changes\n\n%s", task.ID, closeKeyword, task.Description)
		for attempt := 1; attempt <= prCreateRetryAttempts; attempt++ {
			prURL, createErr = r.prCreator.CreatePR(ctx, task.Branch, baseBranch, prTitle, prBody)
			if createErr == nil {
				break
			}
			if attempt < prCreateRetryAttempts {
				log.Warn("Decomposed-parent MR creation failed, retrying",
					slog.String("task_id", task.ID),
					slog.Int("attempt", attempt),
					slog.Int("max_attempts", prCreateRetryAttempts),
					slog.Any("error", createErr),
				)
				time.Sleep(prCreateRetryDelay)
			}
		}
	default:
		prBody := fmt.Sprintf("## Summary\n\nAutomated PR created by Pilot for task %s.\n\nCloses #%s\n\n## Changes\n\n%s", task.ID, issueNum, task.Description)
		for attempt := 1; attempt <= prCreateRetryAttempts; attempt++ {
			prURL, createErr = git.CreatePR(ctx, prTitle, prBody, baseBranch)
			if createErr == nil {
				break
			}
			if attempt < prCreateRetryAttempts {
				log.Warn("Decomposed-parent PR creation failed, retrying",
					slog.String("task_id", task.ID),
					slog.Int("attempt", attempt),
					slog.Int("max_attempts", prCreateRetryAttempts),
					slog.Any("error", createErr),
				)
				time.Sleep(prCreateRetryDelay)
			}
		}
	}

	if createErr != nil {
		result.Success = false
		result.Error = formatGitStepFailureWithRecovery(ctx, git, "pr-create", prCreateRetryAttempts, createErr, task.ID, task.Branch, result.CommitSHA)
		r.reportProgress(task.ID, "PR Failed", 100, result.Error)
		return
	}

	// TASK-359 Layer 1 invariant: a PR-mode task that finished without a PR
	// URL is NOT a success. Guards against CreatePR returning a non-error
	// empty URL.
	if prURL == "" {
		result.Success = false
		result.Error = "decomposed-parent finalize produced no PR URL"
		r.reportProgress(task.ID, "PR Failed", 100, result.Error)
		return
	}

	result.PRUrl = prURL
	log.Info("Decomposed-parent PR created", slog.String("task_id", task.ID), slog.String("pr_url", prURL))
	r.saveLogEntry(task.LogExecutionID(), "info", "PR created: "+prURL)
	r.recordExecutionEvent(task.LogExecutionID(), memory.StagePRCreated, "pr created: "+prURL)
	r.reportProgress(task.ID, "Completed", 100, fmt.Sprintf("PR created: %s", prURL))
}
