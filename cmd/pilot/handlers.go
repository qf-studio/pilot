package main

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	gitlabSDK "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/ghissue"
	"github.com/qf-studio/pilot/internal/logging"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// syncBoardStatus updates a GitHub Projects V2 board column for an issue.
// It is a no-op when boardSync is nil or status is empty. Errors are logged, never propagated.
func syncBoardStatus(ctx context.Context, boardSync *github.ProjectBoardSync, nodeID string, status string) {
	if boardSync == nil || status == "" {
		return
	}
	if err := boardSync.UpdateProjectItemStatus(ctx, nodeID, status); err != nil {
		slog.Warn("board sync failed", "status", status, "error", err)
	}
}

// logGitHubAPIError logs a warning when a GitHub API call fails.
func logGitHubAPIError(operation string, owner, repo string, issueNum int, err error) {
	if err != nil {
		logging.WithComponent("github").Warn("GitHub API call failed",
			slog.String("operation", operation),
			slog.String("repo", owner+"/"+repo),
			slog.Int("issue", issueNum),
			slog.Any("error", err),
		)
	}
}

// noOpErrorMarker is the shared prefix of the executor's ghost-SHA guard errors
// ("no new commit produced — …", both the worktree-HEAD and post-push variants).
// TASK-321: used to recognize an ambiguous no-op that may actually be already-merged work.
const noOpErrorMarker = "no new commit produced"

// issueAlreadyMerged reports whether a merged PR already exists for the issue,
// using the same Search + branch-lookup strategy as the poller's pre-dispatch
// guard (Search API has ~30s indexing lag, so we supplement with a strongly-
// consistent branch lookup). Read-only — labeling/closing is the caller's job.
// TASK-321: distinguishes a re-dispatch of shipped work from a genuine no-op.
func issueAlreadyMerged(ctx context.Context, client *github.Client, owner, repo string, issueNumber int) bool {
	if found, err := client.SearchMergedPRsForIssue(ctx, owner, repo, issueNumber); err == nil && found {
		return true
	}
	branch := fmt.Sprintf("pilot/GH-%d", issueNumber)
	found, err := client.FindMergedPRByBranch(ctx, owner, repo, branch)
	return err == nil && found
}

// issueHasOpenPR reports whether an OPEN pilot PR already exists for the issue.
// Counterpart to issueAlreadyMerged for the awaiting-merge window: pilot-done +
// issue close are deferred to merge time (GH-3139/TASK-301), so between PR
// creation and merge a re-dispatch finds the work already on the pilot/GH-N
// branch and produces a "no new commit produced" no-op even though a healthy PR
// is open. TASK-341: used to classify that no-op as awaiting-merge rather than
// pilot-blocked. Branch lookup is strongly consistent (no Search API lag); the
// Search fallback catches PRs whose head deviates from the pilot/GH-N convention.
// Read-only.
func issueHasOpenPR(ctx context.Context, client *github.Client, owner, repo string, issueNumber int) bool {
	branch := fmt.Sprintf("pilot/GH-%d", issueNumber)
	if found, err := client.FindOpenPRByBranch(ctx, owner, repo, branch); err == nil && found {
		return true
	}
	prs, err := client.SearchPRsForIssue(ctx, owner, repo, issueNumber)
	if err != nil {
		return false
	}
	for _, pr := range prs {
		if pr.State == "open" && !pr.Merged {
			return true
		}
	}
	return false
}

// issueHasOpenChildren reports whether the issue is a decomposed parent with
// open sub-issues, using the same two-tier strategy as the autopilot's
// count-verified close path: native sub-issue links first, text search for
// "Parent: GH-N" bodies as fallback/cross-check. A native count of 0 is never
// trusted alone (LinkSubIssue is non-fatal, so native links can cover only a
// subset of children — the GH-3513 incident). Fail-open returns false so leaf
// issues keep their existing treatment on transient API errors. Read-only.
func issueHasOpenChildren(ctx context.Context, client *github.Client, owner, repo string, issueNumber int) bool {
	native, hasNativeLinks, err := client.GetOpenSubIssueCount(ctx, owner, repo, issueNumber)
	if err == nil && hasNativeLinks && native > 0 {
		return true
	}
	text, terr := client.SearchOpenSubIssues(ctx, owner, repo, issueNumber)
	return terr == nil && text > 0
}

// requestReviewersFromConfig looks up the project config for the given sourceRepo
// and requests PR reviewers if configured. Errors are logged but not propagated.
func requestReviewersFromConfig(ctx context.Context, cfg *config.Config, client *github.Client, sourceRepo, owner, repo string, prNumber int) {
	proj := cfg.FindProjectByRepo(sourceRepo)
	if proj == nil {
		return
	}
	if len(proj.Reviewers) == 0 && len(proj.TeamReviewers) == 0 {
		return
	}
	if err := client.RequestReviewers(ctx, owner, repo, prNumber, proj.Reviewers, proj.TeamReviewers); err != nil {
		logging.WithComponent("github").Warn("Failed to request PR reviewers",
			slog.String("repo", sourceRepo),
			slog.Int("pr", prNumber),
			slog.Any("reviewers", proj.Reviewers),
			slog.Any("team_reviewers", proj.TeamReviewers),
			slog.Any("error", err),
		)
	} else {
		slog.Info("PR reviewers requested",
			slog.String("repo", sourceRepo),
			slog.Int("pr", prNumber),
			slog.Any("reviewers", proj.Reviewers),
			slog.Any("team_reviewers", proj.TeamReviewers),
		)
	}
}

// resolveProjectBaseBranch returns the configured default/branch_from for the given
// project path, or "" when no project matches. Used by adapter handlers to honor
// `default_branch` / `branch_from` overrides (GH-2290).
func resolveProjectBaseBranch(cfg *config.Config, projectPath string) string {
	if cfg == nil {
		return ""
	}
	return cfg.FindProjectByPath(projectPath).ResolveBaseBranch()
}

// parseAutopilotBranch extracts the target branch from an autopilot-fix issue's metadata comment.
// Returns empty string if no metadata found.
// Supports both old format (branch:X) and new format (branch:X pr:N).
func parseAutopilotBranch(body string) string {
	re := regexp.MustCompile(`<!-- autopilot-meta branch:(\S+).*?-->`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	return ""
}

// parseAutopilotPR extracts the PR number from an autopilot-fix issue's metadata comment.
// Returns 0 if no PR metadata found. Used for --from-pr session resumption (GH-1267).
func parseAutopilotPR(body string) int {
	re := regexp.MustCompile(`<!-- autopilot-meta.*?pr:(\d+).*?-->`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// parseAutopilotIteration extracts the CI fix iteration counter from an issue's metadata comment.
// Returns 0 if no iteration metadata found (GH-1566).
func parseAutopilotIteration(body string) int {
	re := regexp.MustCompile(`<!-- autopilot-meta.*?iteration:(\d+).*?-->`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// resolveGitHubMemberID maps a GitHub issue author to a team member ID (GH-634).
// Uses the global teamAdapter (set at startup). Returns "" if no adapter is configured
// or no matching member is found — callers treat "" as "skip RBAC".
func resolveGitHubMemberID(issue *github.Issue) string {
	if teamAdapter == nil {
		return ""
	}
	memberID, err := teamAdapter.ResolveGitHubIdentity(issue.User.Login, issue.User.Email)
	if err != nil {
		logging.WithComponent("teams").Warn("failed to resolve GitHub identity",
			slog.String("github_user", issue.User.Login),
			slog.Any("error", err),
		)
		return ""
	}
	if memberID != "" {
		logging.WithComponent("teams").Info("resolved GitHub user to team member",
			slog.String("github_user", issue.User.Login),
			slog.String("member_id", memberID),
		)
	}
	return memberID
}

// extractGitHubLabelNames returns label name strings from a GitHub issue (GH-727).
// Used to flow labels into executor.Task for decomposition/complexity decisions.
func extractGitHubLabelNames(issue *github.Issue) []string {
	if issue == nil || len(issue.Labels) == 0 {
		return nil
	}
	names := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		names[i] = l.Name
	}
	return names
}

// handleGitHubIssueWithResult processes a GitHub issue and returns result with PR info
// Used in sequential mode to enable PR merge waiting
// sourceRepo is the "owner/repo" string that the issue came from (GH-929)
func handleGitHubIssueWithResult(ctx context.Context, cfg *config.Config, client *github.Client, issue *github.Issue, projectPath string, sourceRepo string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*github.IssueResult, error) {
	taskID := fmt.Sprintf("GH-%d", issue.Number)

	// GH-1853: Construct board sync for GitHub Projects V2 status transitions.
	// boardSync is nil when project_board config is missing or disabled — syncBoardStatus handles nil safely.
	var boardSync *github.ProjectBoardSync
	if cfg.Adapters.GitHub.ProjectBoard != nil && cfg.Adapters.GitHub.ProjectBoard.Enabled {
		parts := strings.Split(cfg.Adapters.GitHub.Repo, "/")
		if len(parts) == 2 && parts[0] != "" {
			boardSync = github.NewProjectBoardSync(client, cfg.Adapters.GitHub.ProjectBoard, parts[0])
		} else {
			slog.Warn("board sync disabled: invalid repo format, expected owner/repo", "repo", cfg.Adapters.GitHub.Repo)
		}
	}

	// GH-386: Pre-execution validation - fail fast if repo doesn't match project
	if err := executor.ValidateRepoProjectMatch(sourceRepo, projectPath); err != nil {
		logging.WithComponent("github").Error("cross-project execution blocked",
			slog.Any("error", err),
			slog.Int("issue_number", issue.Number),
			slog.String("repo", sourceRepo),
			slog.String("project_path", projectPath),
		)
		wrappedErr := fmt.Errorf("cross-project execution blocked: %w", err)
		return &github.IssueResult{
			Success: false,
			Error:   wrappedErr,
		}, wrappedErr
	}

	taskDesc := fmt.Sprintf("GitHub Issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// GH-489: For autopilot-fix issues, reuse the original branch so the fix
	// lands on the same branch as the failed PR (not a new branch).
	// GH-1267: Also extract PR number for --from-pr session resumption.
	var fromPR int
	for _, label := range issue.Labels {
		if label.Name == "autopilot-fix" {
			if parsed := parseAutopilotBranch(issue.Body); parsed != "" {
				branchName = parsed
				slog.Info("using original branch from autopilot-fix metadata",
					slog.String("branch", branchName),
					slog.Int("issue", issue.Number),
				)
			}
			// GH-1267: Extract PR number for session resumption
			if pr := parseAutopilotPR(issue.Body); pr > 0 {
				fromPR = pr
				slog.Info("extracted PR number from autopilot-fix metadata",
					slog.Int("pr", fromPR),
					slog.Int("issue", issue.Number),
				)
			}
			break
		}
	}

	// Always create branches and PRs - required for autopilot workflow
	// GH-386: Include SourceRepo for cross-project validation in executor
	// GH-920: Extract acceptance criteria for prompt inclusion
	// GH-1267: Include FromPR for --from-pr session resumption
	labels := extractGitHubLabelNames(issue)

	slog.Info("Task labels extracted",
		slog.String("task_id", taskID),
		slog.Any("labels", labels),
		slog.Int("label_count", len(issue.Labels)),
	)

	task := &executor.Task{
		ID:                 taskID,
		Title:              issue.Title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		SourceRepo:         sourceRepo,
		MemberID:           resolveGitHubMemberID(issue),                 // GH-634: RBAC lookup
		Labels:             labels,                                       // GH-727: flow labels for complexity classifier
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(issue.Body), // GH-920: acceptance criteria in prompts
		FromPR:             fromPR,                                       // GH-1267: session resumption from PR context
		// GH-2290: honor project.default_branch / branch_from so branching and PR target
		// follow the configured integration branch (e.g. `dev` in main → dev → feature).
		BaseBranch: cfg.FindProjectByRepo(sourceRepo).ResolveBaseBranch(),
		// Propagate parent state so isParentDone() can refuse sub-issue creation
		// when the daemon re-dispatches a closed/merged parent. Without this, an
		// empty State + empty Labels combo bypasses the gate at epic.go and
		// permits spurious sub-issue spawning (GH-201 OAuth dispatch loop).
		State: issue.State,
	}

	parts := strings.Split(sourceRepo, "/")

	// GH-2619: Pre-dispatch spec quality gate — block issues whose bodies are too thin to execute.
	if len(parts) == 2 {
		parentResolver := func(parentNum int) (*github.Issue, error) {
			return client.GetIssue(ctx, parts[0], parts[1], parentNum)
		}
		specResult := github.ValidateSpec(issue, parentResolver)
		if !specResult.Valid && specResult.SkipReason == "" {
			var specBoardSync projectBoardSyncer
			if boardSync != nil {
				specBoardSync = boardSync
			}
			applySpecGuard(ctx, client, parts[0], parts[1], issue, specResult.FailureReasons, specBoardSync, cfg.Adapters.GitHub.ProjectBoard.GetStatuses().Failed)
			return &github.IssueResult{Success: false}, nil
		}
	}

	// Add pilot-in-progress label before execution begins
	if len(parts) == 2 {
		if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelInProgress}); err != nil {
			logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
		}
	}

	// GH-1853: Move issue to "In Progress" column on project board
	syncBoardStatus(ctx, boardSync, issue.NodeID, cfg.Adapters.GitHub.ProjectBoard.GetStatuses().InProgress)

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   issue.Title,
		URL:     issue.HTMLURL,
		Adapter: "github",
		LogMark: "▸",
	}

	// Note: monitor.Start() is NOT called here — it's called by runner.executeWithOptions()
	// when execution actually begins, enabling accurate queued→running dashboard transitions.
	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	// Build the issue result. GH-3270: when the outer error is nil but the executor
	// recorded a failure string (e.g. "no new commit produced"), surface it as the
	// IssueResult.Error so the poller can call IsPermanentFailure on it.
	issueErr := hr.Error
	if issueErr == nil && hr.Result != nil && hr.Result.Error != "" {
		issueErr = fmt.Errorf("%s", hr.Result.Error)
	}
	issueResult := &github.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      issueErr,
	}

	// Post-execution: label management, close issue, add rich execution comment
	if len(parts) == 2 {
		if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelInProgress); err != nil {
			logGitHubAPIError("RemoveLabel", parts[0], parts[1], issue.Number, err)
		}

		// GH-3513 wave 2: a re-dispatch of an already-completed parent hits the
		// ErrParentDone guard. That is a benign skip, not a failure — the parent
		// is already closed + pilot-done. Stacking pilot-failed on top (and
		// posting a ❌ comment) produced contradictory issue states on #3513 and
		// #3546. Success=true so the poller marks it processed instead of
		// re-dispatch-looping.
		if (execErr != nil && executor.IsParentDoneSkip(execErr.Error())) ||
			(hr.Result != nil && executor.IsParentDoneSkip(hr.Result.Error)) {
			slog.Info("re-dispatch of completed parent hit ErrParentDone — benign skip, no labels/comments",
				slog.Int("issue", issue.Number))
			issueResult.Success = true
			return issueResult, nil
		}

		// GH-1853: Resolve board statuses once for all paths (nil-safe via GetStatuses)
		boardStatuses := cfg.Adapters.GitHub.ProjectBoard.GetStatuses()

		if execErr != nil {
			// GH-3787: restart-reap / rate-limit / infra / startup-preflight noise is
			// not a genuine execution failure. Skip labeling entirely — no pilot-failed,
			// no retry-counter escalation — so the poller silently re-picks the issue
			// on its next cycle without burning failed-retry budget on work that may
			// already be shipped.
			if executor.IsInfraNoise(execErr.Error()) {
				slog.Info("execution failure classified as infra noise — skipping pilot-failed label, will retry quietly",
					slog.Int("issue", issue.Number),
					slog.String("error", execErr.Error()),
				)
				return issueResult, nil
			}
			// GH-2402: Classify deterministic failures (e.g. non-conventional title)
			// as pilot-blocked instead of pilot-failed so the poller stops retrying.
			failureLabel := github.LabelFailed
			if executor.IsPermanentFailure(execErr.Error()) {
				failureLabel = github.LabelBlocked
			}
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{failureLabel}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Failed) // GH-1853
			comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
			if failureLabel == github.LabelBlocked {
				comment += "\n\nThis is a deterministic failure (`pilot-blocked`). Retries are paused — fix the underlying issue (e.g. rename to a conventional commit title) and remove the `pilot-blocked` label to resume."
			}
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		} else if hr.Result != nil && hr.Result.Success {
			// Validate deliverables before marking as done.
			// GH-3053: skip for epic-parent results — sub-issues handle the work.
			if !hr.Result.IsEpic && hr.Result.CommitSHA == "" && hr.Result.PRUrl == "" {
				// No commits and no PR - mark as failed
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Failed) // GH-1853
				comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
					hr.Result.Duration, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
				// Update issueResult to reflect failure
				issueResult.Success = false
			} else {
				// Has deliverables — PR created, awaiting merge.
				// GH-3139/TASK-301: pilot-done + issue close are deferred to merge
				// (handleMerging in autopilot) so a later merge conflict cannot
				// ghost-close the issue before any code reaches main.
				// GH-1869: Move to Review column when PR is created
				if hr.PRNumber > 0 {
					syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Review)

					// GH-2099: Auto-assign PR reviewers from project config
					requestReviewersFromConfig(ctx, cfg, client, sourceRepo, parts[0], parts[1], hr.PRNumber)
				}

				// GH-1302/GH-2402: Clean up stale pilot-failed label from prior failed attempt.
				// Removed unconditionally — the in-memory `issue` object is the snapshot
				// from dispatch and won't reflect labels added during execution
				// (e.g. by an earlier retry on the same poll cycle).
				if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelFailed); err != nil {
					// 404 is expected if label doesn't exist — log at debug level
					slog.Debug("pilot-failed label cleanup", "issue", issue.Number, "error", err)
				}

				comment := buildExecutionComment(hr.Result, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		} else if hr.Result != nil {
			// GH-2363: Title-guard escalation already posted its own structured
			// comment and added pilot-failed + pilot-title-rejected. Skip the
			// generic failure-comment path to avoid duplicate noise.
			if hr.Result.TitleRejected {
				syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Failed)
			} else if hr.Result.Declined {
				// GH-2777: Claude explicitly declined the task as unactionable.
				// Add pilot-needs-clarification instead of pilot-failed so the poller
				// blocks without incrementing the retry counter.
				if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelInProgress); err != nil {
					slog.Debug("pilot-in-progress removal (declined)", "issue", issue.Number, "error", err)
				}
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelNeedsClarification}); err != nil {
					logGitHubAPIError("AddLabels(needs-clarification)", parts[0], parts[1], issue.Number, err)
				}
				syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Failed)
				reason := hr.Result.DeclinedReason
				if reason == "" {
					reason = "task could not be implemented as specified"
				}
				comment := fmt.Sprintf("🤔 **Pilot needs clarification before implementing this task**\n\n**Reason**: %s\n\nTo resume, clarify the requirements and remove the `%s` label.", reason, github.LabelNeedsClarification)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment(declined)", parts[0], parts[1], issue.Number, err)
				}
			} else if hr.Result.Error != "" && strings.Contains(hr.Result.Error, noOpErrorMarker) &&
				issueHasOpenChildren(ctx, client, parts[0], parts[1], issue.Number) {
				// GH-3513 wave 2: a no-op on a DECOMPOSED PARENT with open children
				// must not be classified by the already-merged branch below — the
				// parent's own epic PR (or a child's PR matching the search) makes
				// issueAlreadyMerged true while sibling slices are still unshipped,
				// which closed parents prematurely. Leave the parent open; the
				// autopilot's count-verified path closes it when the last child
				// ships. No labels, no comment (re-dispatches can repeat).
				slog.Info("no-op re-dispatch of decomposed parent with open children — deferring close to count-verified path",
					slog.Int("issue", issue.Number))
				if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelBlocked); err != nil {
					slog.Debug("pilot-blocked cleanup (open-children)", "issue", issue.Number, "error", err)
				}
			} else if hr.Result.Error != "" && strings.Contains(hr.Result.Error, noOpErrorMarker) &&
				issueAlreadyMerged(ctx, client, parts[0], parts[1], issue.Number) {
				// TASK-321: a "no new commit produced" no-op is ambiguous — it can mean
				// the work was ALREADY merged to main (a re-dispatch of a shipped issue),
				// not a genuine failure. When a merged PR already exists for this issue,
				// the correct outcome is done+closed, NOT pilot-blocked. (A genuine
				// no-op with no merged PR falls through to the blocked path below.)
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelDone}); err != nil {
					logGitHubAPIError("AddLabels(done)", parts[0], parts[1], issue.Number, err)
				}
				if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelBlocked); err != nil {
					slog.Debug("pilot-blocked cleanup (already-merged)", "issue", issue.Number, "error", err)
				}
				syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Done)
				if err := client.UpdateIssueState(ctx, parts[0], parts[1], issue.Number, "closed"); err != nil {
					logGitHubAPIError("UpdateIssueState(closed)", parts[0], parts[1], issue.Number, err)
				}
				comment := "✅ No new commit was produced because the work for this issue is **already merged to `main`**. This was a re-dispatch of completed work, not a failure — closing as done. (TASK-321)"
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment(already-merged)", parts[0], parts[1], issue.Number, err)
				}
				issueResult.Success = true
			} else if hr.Result.Error != "" && strings.Contains(hr.Result.Error, noOpErrorMarker) &&
				issueHasOpenPR(ctx, client, parts[0], parts[1], issue.Number) {
				// TASK-341: a "no new commit produced" no-op with an OPEN pilot PR is
				// the awaiting-merge window — pilot-done + close are deferred to merge
				// time (GH-3139/TASK-301), so a re-dispatch finds the work already on
				// the branch and produces this no-op even though a healthy PR is open.
				// This is a redundant re-dispatch of shipped work, NOT a failure: leave
				// it for the autopilot merge flow and do NOT add pilot-blocked. (The
				// already-merged case is handled by the branch above; a genuine no-op
				// with neither a merged nor an open PR falls through to the blocked
				// path below.) No comment is posted — the run can repeat before merge
				// and we must not spam the issue.
				slog.Info("no-op re-dispatch with open PR — awaiting merge, not blocked (TASK-341)",
					slog.Int("issue", issue.Number),
				)
				// Defense-in-depth: clear a stale pilot-blocked from a prior phantom
				// classification so the open PR is not held out of the merge flow.
				if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelBlocked); err != nil {
					slog.Debug("pilot-blocked cleanup (awaiting-merge)", "issue", issue.Number, "error", err)
				}
				// issueResult.Success stays false (no new deliverable this run), but no
				// failure/blocked label is applied — the open PR is the deliverable.
			} else if hr.Result.Error != "" && executor.IsInfraNoise(hr.Result.Error) {
				// GH-3787: same infra-noise skip as the execErr branch above — no
				// label, no comment, quiet re-dispatch next cycle.
				slog.Info("execution result classified as infra noise — skipping pilot-failed label, will retry quietly",
					slog.Int("issue", issue.Number),
					slog.String("error", hr.Result.Error),
				)
			} else {
				// result exists but Success is false - mark as failed
				// GH-2402: Use pilot-blocked for deterministic failures so we don't retry.
				failureLabel := github.LabelFailed
				if hr.Result.Error != "" && executor.IsPermanentFailure(hr.Result.Error) {
					failureLabel = github.LabelBlocked
				}
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{failureLabel}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				syncBoardStatus(ctx, boardSync, issue.NodeID, boardStatuses.Failed) // GH-1853
				comment := buildFailureComment(hr.Result)
				if failureLabel == github.LabelBlocked {
					comment += "\n\nThis is a deterministic failure (`pilot-blocked`). Retries are paused — fix the underlying issue and remove the `pilot-blocked` label to resume."
				}
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		}
	}

	return issueResult, execErr
}

// handleLinearIssueWithResult processes a Linear issue delivered as a SDK core.IssueEvent.
// ev.SequenceID is already prefixed (e.g. "APP-123") by the SDK adapter — use it directly.
func handleLinearIssueWithResult(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // e.g., "APP-123"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("Linear Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "linear", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("linear").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:                 taskID,
		Title:              title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(ev.Body),
		SourceAdapter:      "linear",
		SourceIssueID:      ev.IssueID,
		Priority:           sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:         resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	// GH-1472: Wire Linear client as SubIssueCreator for epic decomposition.
	// Uses internal linear.Client which implements executor.SubIssueCreator.CreateIssue.
	if wss := cfg.Adapters.Linear.GetWorkspaces(); len(wss) > 0 {
		runner.SetSubIssueCreator(linear.NewClient(wss[0].APIKey))
	}

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("https://linear.app/issue/%s", taskID),
		Adapter: "linear",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	return &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}, execErr
}

// handleJiraSDKIssueWithResult processes a Jira issue delivered as a SDK core.IssueEvent
// from the poll path. ev.SequenceID is already "JIRA-PROJ-42" (prefixed by the SDK adapter) —
// use it directly as the task ID to avoid double-prefixing.
func handleJiraSDKIssueWithResult(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "JIRA-PROJ-42"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("Jira Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "jira", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("jira").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "jira",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("%s/browse/%s", cfg.Adapters.Jira.BaseURL, ev.IssueID),
		Adapter: "jira",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	return issueResult, execErr
}

// handleAsanaIssueWithResult processes an Asana task delivered as a SDK core.IssueEvent.
// ev.SequenceID is already prefixed ("ASANA-<GID>") by the SDK adapter — use it directly.
func handleAsanaIssueWithResult(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "ASANA-<GID>"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("Asana Task %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "asana", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("asana").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "asana",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("https://app.asana.com/0/0/%s", ev.IssueID),
		Adapter: "asana",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	return issueResult, execErr
}

// buildExecutionComment formats a comment for successful executions.
func buildExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var sb strings.Builder

	sb.WriteString("✅ Pilot completed!\n\n")
	sb.WriteString("| Metric | Value |\n")
	sb.WriteString("|--------|-------|\n")

	// Duration (always present)
	sb.WriteString(fmt.Sprintf("| Duration | %s |\n", result.Duration.Round(time.Second)))

	// Model
	if result.ModelName != "" {
		sb.WriteString(fmt.Sprintf("| Model | `%s` |\n", result.ModelName))
	}

	// Tokens
	if result.TokensTotal > 0 {
		sb.WriteString(fmt.Sprintf("| Tokens | %s (↑%s ↓%s) |\n",
			formatTokenCountComment(result.TokensTotal),
			formatTokenCountComment(result.TokensInput),
			formatTokenCountComment(result.TokensOutput),
		))
	}

	// Cost
	if result.EstimatedCostUSD > 0 {
		sb.WriteString(fmt.Sprintf("| Cost | ~$%.2f |\n", result.EstimatedCostUSD))
	}

	// Files changed
	if result.FilesChanged > 0 || result.LinesAdded > 0 || result.LinesRemoved > 0 {
		sb.WriteString(fmt.Sprintf("| Files | %d changed (+%d -%d) |\n",
			result.FilesChanged, result.LinesAdded, result.LinesRemoved))
	}

	// Branch
	if branchName != "" {
		sb.WriteString(fmt.Sprintf("| Branch | `%s` |\n", branchName))
	}

	// PR
	if result.PRUrl != "" {
		sb.WriteString(fmt.Sprintf("| PR | %s |\n", result.PRUrl))
	}

	// Intent warning (from intent judge, GH-624)
	if result.IntentWarning != "" {
		sb.WriteString(fmt.Sprintf("\n⚠️ **Intent Warning:** %s\n", result.IntentWarning))
	}

	return sb.String()
}

// buildFailureComment formats a comment for failed executions.
func buildFailureComment(result *executor.ExecutionResult) string {
	var sb strings.Builder
	sb.WriteString("❌ Pilot execution failed\n\n")
	if result != nil && result.Error != "" {
		sb.WriteString("<details>\n<summary>Error details</summary>\n\n")
		sb.WriteString(fmt.Sprintf("```\n%s\n```\n", result.Error))
		sb.WriteString("</details>\n")
	}
	if result != nil {
		if result.Duration > 0 {
			sb.WriteString(fmt.Sprintf("\n**Duration:** %s", result.Duration.Round(time.Second)))
		}
		if result.ModelName != "" {
			sb.WriteString(fmt.Sprintf(" | **Model:** `%s`", result.ModelName))
		}
		if result.EstimatedCostUSD > 0 {
			sb.WriteString(fmt.Sprintf(" | **Cost:** ~$%.2f", result.EstimatedCostUSD))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatTokenCountComment formats a token count for display in comments.
func formatTokenCountComment(tokens int64) string {
	if tokens >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}

// handlePlaneIssueWithResult processes a Plane.so work item delivered as a SDK core.IssueEvent.
// ev.SequenceID is already prefixed ("PLANE-42") by the SDK adapter — use it directly.
func handlePlaneIssueWithResult(ctx context.Context, cfg *config.Config, client *planeSDK.Client, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "PLANE-42"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("Plane Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "plane", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("plane").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "plane",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	// Wire SDK client as SubIssueCreator for epic decomposition (GH-1833)
	subCreatorClient := planeSDK.NewClient(
		cfg.Adapters.Plane.BaseURL,
		cfg.Adapters.Plane.APIKey,
		planeSDK.WithWorkspaceSlug(cfg.Adapters.Plane.WorkspaceSlug),
		planeSDK.WithDefaultProjectID(ev.ProjectID),
	)
	runner.SetSubIssueCreator(subCreatorClient)

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("%s/workspaces/%s/projects/%s/work-items/%s", cfg.Adapters.Plane.BaseURL, cfg.Adapters.Plane.WorkspaceSlug, ev.ProjectID, ev.IssueID),
		Adapter: "plane",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	// Post-execution: add HTML comment
	workspaceSlug := cfg.Adapters.Plane.WorkspaceSlug
	projectID := ev.ProjectID
	issueID := ev.IssueID
	if execErr != nil {
		comment := fmt.Sprintf("<p>❌ Pilot execution failed:</p><pre>%s</pre>", execErr.Error())
		if err := client.AddComment(ctx, workspaceSlug, projectID, issueID, comment); err != nil {
			logging.WithComponent("plane").Warn("Failed to add failure comment",
				slog.String("issue_id", issueID),
				slog.Any("error", err),
			)
		}
	} else if hr.Result != nil && hr.Result.Success {
		if !hr.Result.IsEpic && hr.Result.CommitSHA == "" && hr.Result.PRUrl == "" { // GH-3053
			comment := fmt.Sprintf("<p>⚠️ Pilot execution completed but no changes were made.</p><p>Duration: %s<br>Branch: <code>%s</code></p><p>No commits or PR were created. The task may need clarification or manual intervention.</p>",
				hr.Result.Duration, branchName)
			if err := client.AddComment(ctx, workspaceSlug, projectID, issueID, comment); err != nil {
				logging.WithComponent("plane").Warn("Failed to add comment",
					slog.String("issue_id", issueID),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			comment := buildPlaneExecutionComment(hr.Result, branchName)
			if err := client.AddComment(ctx, workspaceSlug, projectID, issueID, comment); err != nil {
				logging.WithComponent("plane").Warn("Failed to add success comment",
					slog.String("issue_id", issueID),
					slog.Any("error", err),
				)
			}
		}
	} else if hr.Result != nil {
		comment := fmt.Sprintf("<p>❌ Pilot execution failed:</p><pre>%s</pre>", hr.Result.Error)
		if err := client.AddComment(ctx, workspaceSlug, projectID, issueID, comment); err != nil {
			logging.WithComponent("plane").Warn("Failed to add failure comment",
				slog.String("issue_id", issueID),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// buildPlaneExecutionComment creates an HTML comment for a successful Plane.so execution.
func buildPlaneExecutionComment(result *executor.ExecutionResult, branchName string) string {
	comment := "<p>✅ Pilot execution completed successfully.</p>"
	if result.PRUrl != "" {
		comment += fmt.Sprintf("<p>🔗 <a href=\"%s\">View Pull Request</a></p>", result.PRUrl)
	}
	comment += fmt.Sprintf("<p>🌿 Branch: <code>%s</code></p>", branchName)
	if result.Duration > 0 {
		comment += fmt.Sprintf("<p>⏱ Duration: %s</p>", result.Duration)
	}
	return comment
}

// handleGitlabIssueWithResult processes a GitLab issue delivered as a SDK core.IssueEvent.
// ev.SequenceID is already prefixed ("GL-42") by the SDK adapter — use it directly.
func handleGitlabIssueWithResult(ctx context.Context, cfg *config.Config, client *gitlabSDK.Client, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "GL-42"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("GitLab Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "gitlab", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("gitlab").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "gitlab",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	// Wire SDK client directly as PRCreator so the runner creates MRs via
	// the GitLab API instead of the gh CLI.
	runner.SetPRCreator(client)

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("%s/%s/-/issues/%s", cfg.Adapters.GitLab.BaseURL, cfg.Adapters.GitLab.Project, ev.IssueID),
		Adapter: "gitlab",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	// Post-execution: add issue note via SDK client.
	issueIID, _ := strconv.Atoi(ev.IssueID)
	if execErr != nil {
		note := fmt.Sprintf("❌ Pilot execution failed:\n\n%s", execErr.Error())
		if _, err := client.AddIssueNote(ctx, issueIID, note); err != nil {
			logging.WithComponent("gitlab").Warn("Failed to add failure note",
				slog.String("task_id", taskID),
				slog.Any("error", err),
			)
		}
	} else if hr.Result != nil && hr.Result.Success {
		if !hr.Result.IsEpic && hr.Result.CommitSHA == "" && hr.Result.PRUrl == "" { // GH-3053
			note := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\nDuration: %s\nBranch: %s\n\nNo commits or MR were created. The task may need clarification or manual intervention.",
				hr.Result.Duration, branchName)
			if _, err := client.AddIssueNote(ctx, issueIID, note); err != nil {
				logging.WithComponent("gitlab").Warn("Failed to add note",
					slog.String("task_id", taskID),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			var parts []string
			parts = append(parts, "✅ Pilot execution completed successfully!")
			parts = append(parts, "")
			if hr.Result.PRUrl != "" {
				parts = append(parts, fmt.Sprintf("Merge Request: %s", hr.Result.PRUrl))
			}
			if hr.Result.CommitSHA != "" {
				parts = append(parts, fmt.Sprintf("Commit: %s", hr.Result.CommitSHA[:min(8, len(hr.Result.CommitSHA))]))
			}
			parts = append(parts, fmt.Sprintf("Branch: %s", branchName))
			parts = append(parts, fmt.Sprintf("Duration: %s", hr.Result.Duration))
			note := strings.Join(parts, "\n")
			if _, err := client.AddIssueNote(ctx, issueIID, note); err != nil {
				logging.WithComponent("gitlab").Warn("Failed to add success note",
					slog.String("task_id", taskID),
					slog.Any("error", err),
				)
			}
		}
	} else if hr.Result != nil {
		note := fmt.Sprintf("❌ Pilot execution failed\n\nError: %s\nDuration: %s", hr.Result.Error, hr.Result.Duration)
		if _, err := client.AddIssueNote(ctx, issueIID, note); err != nil {
			logging.WithComponent("gitlab").Warn("Failed to add failure note",
				slog.String("task_id", taskID),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// handleAzureDevOpsIssueWithResult processes an Azure DevOps work item delivered as a SDK core.IssueEvent
// from the poll path. ev.SequenceID is already "AZDO-42" (prefixed by the SDK adapter) — use directly.
func handleAzureDevOpsIssueWithResult(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "AZDO-42"; already prefixed by the SDK adapter
	title := ev.Title

	taskDesc := fmt.Sprintf("Azure DevOps Work Item %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is Phase-0 stub; ErrRepoNotResolved is expected — log and continue.
	if _, _, _, err := sdkshim.ResolveRepoForEvent(cfg, "azuredevops", ev); err != nil && err.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("azuredevops").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "azuredevops",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     fmt.Sprintf("https://dev.azure.com/_workitems/edit/%s", ev.IssueID),
		Adapter: "azuredevops",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	return issueResult, execErr
}

// handleGithubIssueEventSDK processes a GitHub issue delivered as a studio-sdk core.IssueEvent
// (M7 Phase 4a). It runs ALONGSIDE the legacy in-tree handleGitHubIssueWithResult (which takes a
// *github.Issue) and is exercised only when the dormant SDK poller is enabled
// (adapters.github.use_sdk_poller — see cmd/pilot/poller_github.go).
//
// ev.SequenceID is already "GH-42" (prefixed by the SDK adapter) — used verbatim as the task ID to
// avoid the GH-GH-42 double-prefix the legacy handler's fmt.Sprintf("GH-%d", ...) would create.
// Board sync is handled at the SDK-poller level (config-driven); the spec-guard gate runs below
// (M7 4d.3). Sub-issue handling remains exclusive to the legacy in-tree handler until later phases.
func handleGithubIssueEventSDK(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "GH-42"; already prefixed by the SDK adapter — do NOT re-prefix
	title := ev.Title

	taskDesc := fmt.Sprintf("GitHub Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// ResolveRepoForEvent is tolerated like the other SDK handlers; ErrRepoNotResolved is non-fatal here.
	_, repoOwner, repoName, resolveErr := sdkshim.ResolveRepoForEvent(cfg, "github", ev)
	if resolveErr != nil && resolveErr.Error() != sdkshim.ErrRepoNotResolved.Error() {
		logging.WithComponent("github").Warn("Unexpected repo resolution error",
			slog.String("task_id", taskID),
			slog.Any("error", resolveErr),
		)
	}

	// M7 4d.3: pre-dispatch spec quality gate on the SDK path (GH-2619 parity —
	// previously exclusive to the legacy in-tree handler). The issue is
	// reconstructed from the event; labels ride along so the
	// pilot-skip-spec-check opt-out works.
	if resolveErr == nil && repoOwner != "" && repoName != "" {
		if ghToken, _ := resolveGitHubToken(cfg); ghToken != "" {
			specClient := githubSDK.NewClient(ghToken)
			issueNum, _ := strconv.Atoi(ev.IssueID)
			specLabels := make([]githubSDK.Label, 0, len(ev.Labels))
			for _, l := range ev.Labels {
				specLabels = append(specLabels, githubSDK.Label{Name: l})
			}
			specIssue := &githubSDK.Issue{Number: issueNum, Title: title, Body: ev.Body, Labels: specLabels}
			parentResolver := func(parentNum int) (*githubSDK.Issue, error) {
				return specClient.GetIssue(ctx, repoOwner, repoName, parentNum)
			}
			if specResult := ghissue.ValidateSpec(specIssue, parentResolver); !specResult.Valid && specResult.SkipReason == "" {
				applySpecGuardSDK(ctx, specClient, repoOwner, repoName, specIssue, specResult.FailureReasons)
				return &sdkcore.IssueResult{Success: false, Skipped: true, SkipReason: "spec_incomplete"}, nil
			}
		}
	}

	task := &executor.Task{
		ID:            taskID,
		Title:         title,
		Description:   taskDesc,
		ProjectPath:   projectPath,
		Branch:        branchName,
		CreatePR:      true,
		SourceAdapter: "github",
		SourceIssueID: ev.IssueID,
		Priority:      sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:    resolveProjectBaseBranch(cfg, projectPath), // GH-2290
	}
	if resolveErr == nil && repoOwner != "" && repoName != "" {
		// M7 4d.4: lets the runner select the startup-registered SDK PR creator
		// ("github:owner/repo"); tasks without it keep the gh-CLI path.
		task.SourceRepo = repoOwner + "/" + repoName
	}

	deps := HandlerDeps{
		Cfg:          cfg,
		Dispatcher:   dispatcher,
		Runner:       runner,
		Monitor:      monitor,
		Program:      program,
		AlertsEngine: alertsEngine,
		Enforcer:     enforcer,
		ProjectPath:  projectPath,
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     githubIssueURL(cfg, ev.IssueID),
		Adapter: "github",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	issueResult := &sdkcore.IssueResult{
		Success:    hr.Success,
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	return issueResult, execErr
}

// githubIssueURL builds the HTML URL for a GitHub issue from the default adapter repo
// ("owner/repo"). Returns "" when no default repo is configured.
func githubIssueURL(cfg *config.Config, issueID string) string {
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
		return fmt.Sprintf("https://github.com/%s/issues/%s", cfg.Adapters.GitHub.Repo, issueID)
	}
	return ""
}
