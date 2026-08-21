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
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/ghissue"
	"github.com/qf-studio/pilot/internal/logging"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

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

// resolveGitHubMemberIDByLogin resolves a GitHub login/email pair to a team member ID
// (GH-634). Uses the global teamAdapter (set at startup); returns "" if no adapter is
// configured or no matching member is found — callers treat "" as "skip RBAC".
func resolveGitHubMemberIDByLogin(login, email string) string {
	if teamAdapter == nil {
		return ""
	}
	memberID, err := teamAdapter.ResolveGitHubIdentity(login, email)
	if err != nil {
		logging.WithComponent("teams").Warn("failed to resolve GitHub identity",
			slog.String("github_user", login),
			slog.Any("error", err),
		)
		return ""
	}
	if memberID != "" {
		logging.WithComponent("teams").Info("resolved GitHub user to team member",
			slog.String("github_user", login),
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
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
	} else if hr.Result != nil && !hr.IsTerminalByDesign() {
		// GH-4794/GH-4801: a superseded/canceled execution is the success path
		// for "this work is no longer wanted" — don't post a failure comment
		// on a closed/canceled work item for it (mirrors GitLab's gate below).
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
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
			// GH-4817 (TASK-459 Phase 3): only demote to failure when the
			// absent commit/PR is anomalous. A no_op/terminal-by-design
			// outcome deliberately produced no deliverable — consult the
			// recorded classification instead of inferring failure from
			// artifact absence alone (the exact GH-4794 mechanism, which
			// re-arms the vendored SDK poller's unmark-for-retry branch).
			if !hr.IsTerminalByDesign() && !executor.IsNoArtifactExplainedOutcome(hr.Result.Outcome) {
				issueResult.Success = false
			}
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
	} else if hr.Result != nil && !hr.IsTerminalByDesign() {
		// GH-4794: a superseded/canceled execution is the success path for
		// "this work is no longer wanted" — don't post a failure note for it.
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
		// GH-4801: azuredevops previously omitted the GH-4587 IsDispatchGated()
		// term (PR#4800 only added IsTerminalByDesign() here) — git history
		// shows GH-4587 was scoped to "GitHub/GitLab SDK translation sites"
		// only, never a deliberate azuredevops exclusion, so unifying onto the
		// shared helper is a genuine bug fix, not a behavior regression.
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
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
func handleGithubIssueEventSDK(ctx context.Context, cfg *config.Config, ev sdkcore.IssueEvent, projectPath string, repoFullName string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer, metrics *autopilot.Metrics) (*sdkcore.IssueResult, error) {
	taskID := ev.SequenceID // "GH-42"; already prefixed by the SDK adapter — do NOT re-prefix
	title := ev.Title

	// GH-5056: admission backstop — an issue carrying pilot-needs-human must
	// never be (re-)admitted through this chokepoint, no matter what
	// unmarked it. The SDK poller's own candidate filter (studio-sdk
	// poller.go:742-815) checks pilot-in-progress/done/blocked/needs-
	// clarification/failed/retry-ready but never pilot-needs-human — and a
	// park (escalateBasePresenceHold, autopilot's escalateAndHold) strips
	// pilot-in-progress while leaving the pilot trigger label standing, so
	// once isTaskStillQueued goes false the poller unmarks and re-dispatches
	// after its 5-min grace: re-hold, re-escalate, on a slow loop that
	// re-blocks this project's queue head each cycle. Checked before any
	// other side effect (spec-guard, pilot-in-progress label/comment, task
	// construction, dispatch) so a re-admitted needs-human issue costs
	// nothing beyond this one check.
	if githubEventHasNeedsHumanLabel(ev.Labels) {
		logging.WithComponent("github").Info("SDK-dispatch: skipping admission, issue carries pilot-needs-human",
			slog.String("task_id", taskID),
			slog.String("reason", labelPilotNeedsHumanSDK),
		)
		return &sdkcore.IssueResult{Success: false, Skipped: true, SkipReason: "needs_human"}, nil
	}

	taskDesc := fmt.Sprintf("GitHub Issue %s: %s\n\n%s", taskID, title, ev.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// GH-489/GH-1267: For autopilot-fix issues, reuse the original branch so the fix
	// lands on the same branch as the failed PR, and extract the PR number for
	// --from-pr session resumption. Mirrors handleGitHubIssueWithResult (GH-4050).
	var fromPR int
	for _, label := range ev.Labels {
		if label == "autopilot-fix" {
			if parsed := parseAutopilotBranch(ev.Body); parsed != "" {
				branchName = parsed
			}
			if pr := parseAutopilotPR(ev.Body); pr > 0 {
				fromPR = pr
			}
			break
		}
	}

	// Resolve owner/repo. M7 4d.2c: the per-repo SDK poller passes its repo
	// explicitly (repoFullName) because resolveGithubRepo matches by repo NAME only
	// and is ambiguous when two projects share a repo name under different owners
	// (repo_resolver.go documents this limitation). Event-based resolution remains
	// the fallback for the webhook / rate-limit-retry paths that don't carry it.
	var repoOwner, repoName string
	if repoFullName != "" {
		if parts := strings.SplitN(repoFullName, "/", 2); len(parts) == 2 {
			repoOwner, repoName = parts[0], parts[1]
		}
	}
	if repoOwner == "" || repoName == "" {
		_, ro, rn, resolveErr := sdkshim.ResolveRepoForEvent(cfg, "github", ev)
		if resolveErr != nil && resolveErr.Error() != sdkshim.ErrRepoNotResolved.Error() {
			logging.WithComponent("github").Warn("Unexpected repo resolution error",
				slog.String("task_id", taskID),
				slog.Any("error", resolveErr),
			)
		}
		repoOwner, repoName = ro, rn
	}

	// GH-4050: Fetch the real issue so State (and author, for MemberID) flow into the
	// task the same way the legacy in-tree path sets them. sdkcore.IssueEvent doesn't
	// surface issue state — without it, an empty State + empty Labels combo bypasses
	// the epic.go parent-done gate and permits spurious sub-issue spawning on
	// re-dispatch of a closed/merged parent (GH-201 OAuth dispatch loop).
	issueNum, _ := strconv.Atoi(ev.IssueID)
	var issueState, memberID string
	// GH-4631: default to the poll-tick snapshot; overwritten below with the
	// fresh GET body when the fetch succeeds, so the spec-quality gate
	// validates the current issue body rather than a stale list-snapshot.
	specBody := ev.Body
	var specClient *githubSDK.Client
	if repoOwner != "" && repoName != "" {
		if ghToken, _ := resolveGitHubToken(cfg); ghToken != "" {
			// TASK-461 Leg 2: built via newGitHubSDKClient for uniformity with
			// the daemon-lifetime sites — this per-event client already
			// re-resolved ghToken fresh above, so the swap is low-risk, not a
			// behavior fix.
			specClient = newGitHubSDKClient(cfg)
			if realIssue := fetchGithubIssueForSDKTask(ctx, specClient, repoOwner, repoName, issueNum, taskID); realIssue != nil {
				issueState = realIssue.State
				memberID = resolveGitHubMemberIDByLogin(realIssue.User.Login, realIssue.User.Email)
				specBody = realIssue.Body
			}
		}
	}

	// M7 4d.3: pre-dispatch spec quality gate on the SDK path (GH-2619 parity —
	// previously exclusive to the legacy in-tree handler). The issue is
	// reconstructed from the event; labels ride along so the
	// pilot-skip-spec-check opt-out works.
	if specClient != nil {
		specLabels := make([]githubSDK.Label, 0, len(ev.Labels))
		for _, l := range ev.Labels {
			specLabels = append(specLabels, githubSDK.Label{Name: l})
		}
		specIssue := &githubSDK.Issue{Number: issueNum, Title: title, Body: specBody, Labels: specLabels}
		parentResolver := func(parentNum int) (*githubSDK.Issue, error) {
			return specClient.GetIssue(ctx, repoOwner, repoName, parentNum)
		}
		if specResult := ghissue.ValidateSpec(specIssue, parentResolver); !specResult.Valid && specResult.SkipReason == "" {
			applySpecGuardSDK(ctx, specClient, repoOwner, repoName, specIssue, specResult.FailureReasons)
			return &sdkcore.IssueResult{Success: false, Skipped: true, SkipReason: "spec_incomplete"}, nil
		}
	}

	// GH-4687: apply pilot-in-progress at the start of work on the SDK-dispatch
	// path. Since the 2026-07-16 SDK-poller cutover this was the only dispatch
	// path with zero label operations — the legacy webhook-only handler
	// (internal/pilot/pilot.go:1191) was the sole producer — which permanently
	// disabled recoverOrphanedIssues (studio-sdk poller.go:350-380, cannot find
	// in-flight issues by label after a restart) and made the pilot-in-progress
	// removal at controller.go:3014 a silent no-op on every poller-dispatched
	// issue. studio-sdk's Notifier.NotifyTaskStarted is already tested and
	// auto-creates the label via AddLabels if the repo doesn't have it yet.
	// Mirrors the non-fatal (WARN-only) pattern at pilot.go:1191-1195 and
	// controller.go:3011-3015 — labeling must never block dispatch.
	// TASK-459 Phase 3 Task 5b: skip the pilot-in-progress label/comment write
	// only on positive evidence the issue is already closed. issueState is ""
	// on fetch failure or when specClient is nil (fail-open default set above,
	// GH-4050) — treat that as unknown, not closed, so the label still fires
	// when state can't be determined. Mirrors the surfaceStalledIssue guard
	// (dispatcher.go).
	// notifyAttempted and labelTracker are captured here (rather than kept
	// local to the block below) so the post-dispatch unwind further down —
	// GH-4961 — can reuse the exact same client/label/tracker that applied
	// pilot-in-progress, without re-deriving them.
	var notifyAttempted bool
	var pilotLabel string
	var labelTracker *alerts.DeadManTracker
	if specClient != nil && issueState != githubSDK.StateClosed {
		if cfg.Adapters != nil && cfg.Adapters.GitHub != nil {
			pilotLabel = cfg.Adapters.GitHub.PilotLabel
		}
		if pilotLabel == "" {
			pilotLabel = "pilot"
		}
		// TASK-441 L2 (GH-4709): dead-man tracker for this path — the exact
		// seam that went silent for 19 days (GH-4687) with zero errors
		// surfaced, because nothing called it at all. RecordAttempt fires
		// regardless of outcome, so a future "wired to nothing" regression
		// (zero attempts despite issues flowing) is detectable the same way
		// zero attempts already are (Stale) instead of hiding behind an
		// error-only counter.
		labelTracker = alertsEngine.RegisterDeadManTracker(
			labelLifecycleDeadManTrackerName(repoOwner+"/"+repoName),
			alerts.AlertTypeLabelLifecycleFailureStreak,
			alerts.DefaultDeadManFailureThreshold,
			alerts.DefaultDeadManWindow,
		)
		notifyAttempted = true
		labelTracker.RecordAttempt()
		if err := notifyTaskStartedSDK(ctx, specClient, pilotLabel, repoOwner, repoName, issueNum, taskID); err != nil {
			labelTracker.RecordFailure(map[string]string{"repo": repoOwner + "/" + repoName})
			logging.WithComponent("github").Warn("Failed to notify task started (SDK path)",
				slog.String("task_id", taskID),
				slog.Int("issue", issueNum),
				slog.Any("error", err))
		} else {
			labelTracker.RecordSuccess()
		}
	} else if specClient != nil {
		logging.WithComponent("github").Info("SDK-dispatch: issue already closed, skipping pilot-in-progress label and comment",
			slog.String("task_id", taskID),
			slog.Int("issue", issueNum))
	}

	task := &executor.Task{
		ID:                 taskID,
		Title:              title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		SourceAdapter:      "github",
		SourceIssueID:      ev.IssueID,
		Priority:           sdkshim.PriorityFromSDK(ev.Priority),
		BaseBranch:         resolveProjectBaseBranch(cfg, projectPath), // GH-2290
		MemberID:           memberID,                                   // GH-634: RBAC lookup
		Labels:             ev.Labels,                                  // GH-727: flow labels for complexity/no-decompose classifier
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(ev.Body),  // GH-920: acceptance criteria in prompts
		FromPR:             fromPR,                                     // GH-1267: session resumption from PR context
		// Propagate parent state so isParentDone() can refuse sub-issue creation
		// when the daemon re-dispatches a closed/merged parent (GH-201 gate parity).
		State: issueState,
	}
	if repoOwner != "" && repoName != "" {
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
		Metrics:      metrics,
	}
	if repoOwner != "" && repoName != "" {
		// GH-4833: pass the already-resolved repo match through so
		// handleIssueGeneric's canary-stamping prefers it over the
		// (possibly default-project-colliding) projectPath.
		deps.ProjectRepo = repoOwner + "/" + repoName
	}
	info := IssueInfo{
		TaskID:  taskID,
		Title:   title,
		URL:     githubIssueURL(cfg, repoOwner, repoName, ev.IssueID),
		Adapter: "github",
		LogMark: "▸",
	}

	hr, execErr := handleIssueGeneric(ctx, deps, info, task)

	// GH-4961: notifyTaskStartedSDK above applies pilot-in-progress BEFORE
	// the dispatcher has actually claimed the task (preserving the GH-4687
	// pre-claim ordering for the happy path). When the dispatcher instead
	// drops this pickup — repick backoff, claim lost, or any other
	// admission-gate decline surfaced as hr.IsDispatchGated() — and no other
	// execution genuinely owns the task, the label just applied is the only
	// evidence of "in progress" left behind with nothing running to clear it
	// later. Unwind it here so the next poll tick isn't permanently skipped.
	//
	// dispatcher.IsActive is the same live-ownership check
	// handleIssueGeneric's own admission gates consult (GH-4008): if it
	// reports true, a *different*, genuinely active execution owns this
	// task (e.g. a legitimate concurrent dispatch) and the label correctly
	// reflects that — must not strip it. hr.IsDispatchGated() can only be
	// true when dispatcher != nil (every ErrDispatchGated-setting branch in
	// handleIssueGeneric requires it), but dispatcherActive is still guarded
	// explicitly here since it's evaluated as a plain argument before
	// shouldUnwindGithubInProgressLabel's own short-circuit ever runs — a
	// bare dispatcher.IsActive(...) call would otherwise risk a nil-pointer
	// dereference if that invariant is ever violated.
	dispatcherActive := dispatcher != nil && dispatcher.IsActive(taskID, projectPath)
	if shouldUnwindGithubInProgressLabel(notifyAttempted, hr, dispatcherActive) {
		labelTracker.RecordAttempt()
		if err := unwindGithubStartedLabel(ctx, specClient, repoOwner, repoName, issueNum); err != nil {
			// A failed unwind is a genuine label-lifecycle failure (the
			// label is now stranded), unlike the unwind itself — which is a
			// deliberate correction, not evidence the original apply/notify
			// path is broken.
			labelTracker.RecordFailure(map[string]string{"repo": repoOwner + "/" + repoName})
			logging.WithComponent("github").Warn("Failed to unwind pilot-in-progress after dropped dispatch pickup",
				slog.String("task_id", taskID),
				slog.Int("issue", issueNum),
				slog.Any("error", err))
		} else {
			labelTracker.RecordSuccess()
			logging.WithComponent("github").Info("Unwound pilot-in-progress label after dropped dispatch pickup",
				slog.String("task_id", taskID),
				slog.Int("issue", issueNum))
		}
	}

	issueResult := &sdkcore.IssueResult{
		Success:    hr.EffectiveSuccess(), // GH-4587/GH-4794: admission-gate declines and superseded/canceled executions are not genuine failures
		BranchName: hr.BranchName,
		PRNumber:   hr.PRNumber,
		PRURL:      hr.PRURL,
		HeadSHA:    hr.HeadSHA,
		Error:      hr.Error,
	}

	return issueResult, execErr
}

// labelPilotNeedsHumanSDK mirrors internal/executor's labelPilotNeedsHuman
// and internal/autopilot's labelNeedsHuman, defined locally here for the
// same import-cycle-avoidance reason both of those document — cmd/pilot is
// the one place all three can't simply share one package-level constant
// without introducing a dependency between the other two.
const labelPilotNeedsHumanSDK = "pilot-needs-human"

// githubEventHasNeedsHumanLabel reports whether labels carries
// pilot-needs-human (GH-5056). See handleGithubIssueEventSDK's call site
// for the re-admission loop this check exists to close.
func githubEventHasNeedsHumanLabel(labels []string) bool {
	for _, l := range labels {
		if l == labelPilotNeedsHumanSDK {
			return true
		}
	}
	return false
}

// fetchGithubIssueForSDKTask fetches the real issue via the studio-sdk GitHub client so
// State (and author, for MemberID resolution) can flow into the executor.Task the SDK-poller
// path constructs (GH-4050). sdkcore.IssueEvent does not surface issue state itself, so
// without this fetch the epic.go parent-done gate silently no-ops on every SDK-dispatched
// task. Returns nil on any fetch error — best-effort, matching the existing spec-guard
// fetch tolerance elsewhere in this file.
func fetchGithubIssueForSDKTask(ctx context.Context, client *githubSDK.Client, owner, repo string, issueNum int, taskID string) *githubSDK.Issue {
	issue, err := client.GetIssue(ctx, owner, repo, issueNum)
	if err != nil {
		logging.WithComponent("github").Warn("failed to fetch issue for state/member resolution",
			slog.String("task_id", taskID),
			slog.Any("error", err),
		)
		return nil
	}
	return issue
}

// labelLifecycleDeadManTrackerPrefix is the alerts.DeadManTracker
// registration name prefix (TASK-441 L2, GH-4709) for the
// notifyTaskStartedSDK call site above. GH-4866: per-repo, not global — a
// global shared counter let one repo's real, sustained label-lifecycle
// failures get diluted by every other (healthy) repo's RecordSuccess calls
// on the same tracker, so a single broken repo's streak could take far
// longer to reach threshold (or never reach it at all, on a large
// multi-repo fleet) than the DefaultDeadManFailureThreshold consecutive
// failures the tracker is supposed to guarantee. Mirrors
// sdkPreFlightJudge's per-repo "intent_judge:"+repoFullName keying
// (poller_github.go) for the same reason.
const labelLifecycleDeadManTrackerPrefix = "label_lifecycle:"

// labelLifecycleDeadManTrackerName returns the per-repo tracker name for
// repoFullName ("owner/repo"). Registered once per repo poller at startup
// (startGithubSDKPollerForRepo, poller_github.go — mirroring the self-review
// tracker registered there) and resolved again here by the same name on
// every event, relying on RegisterDeadManTracker's memoize-by-name so both
// call sites share one set of counters per repo instead of the startup
// registration and the event-time registration racing to create two.
func labelLifecycleDeadManTrackerName(repoFullName string) string {
	return labelLifecycleDeadManTrackerPrefix + repoFullName
}

// shouldUnwindGithubInProgressLabel reports whether a pilot-in-progress label
// applied earlier in handleGithubIssueEventSDK (before the dispatch attempt,
// per GH-4687's pre-claim ordering) must be removed again because the
// dispatch that followed never actually claimed the task (GH-4961).
//
// notifyAttempted is true only when notifyTaskStartedSDK was actually called
// for this event (skipped entirely for a closed issue, or when specClient
// couldn't be built) — nothing to unwind if the label was never touched.
//
// hr.IsDispatchGated() is true for every admission-gate decline
// handleIssueGeneric can return with no execution having been claimed:
// already-active/backoff/terminal-completion pre-checks, and the dispatcher's
// own repick-backoff/claim-lost drops (QueueTask returning "", nil) — see
// executor.ErrDispatchGated and handler_common.go's gatedDrop paths.
//
// dispatcherActive distinguishes the two ways a gated decline can arise:
//   - false: nothing is running for this task — the label just applied is
//     now the sole (stale) evidence of "in progress" and must be removed, or
//     every future poll tick will skip the issue forever (the wedge this
//     bug fixes).
//   - true: a *different*, genuinely active execution owns the task (e.g. a
//     legitimate concurrent dispatch race) — the label correctly describes
//     reality and must be left alone.
//
// A successful (non-gated) dispatch must never reach here with a true
// result — see the call site — so the happy path never performs the extra
// label round-trip this function exists to avoid.
func shouldUnwindGithubInProgressLabel(notifyAttempted bool, hr *HandlerResult, dispatcherActive bool) bool {
	return notifyAttempted && hr.IsDispatchGated() && !dispatcherActive
}

// unwindGithubStartedLabel removes exactly the label notifyTaskStartedSDK
// applied — githubSDK.LabelInProgress ("pilot-in-progress") — never the
// poller's own trigger label ("pilot", cfg.Adapters.GitHub.PilotLabel).
//
// GH-5028: the unwind call at this call site used to remove pilotLabel (the
// trigger label) instead of the label NotifyTaskStarted actually applies
// (studio-sdk's Notifier.NotifyTaskStarted only ever adds the constant
// LabelInProgress — the triggerLabel it's constructed with is never read).
// A dispatch pickup dropped after the label went on (repick backoff, claim
// lost) then had its "correction" strip the wrong label: the issue was left
// with pilot-in-progress still on it but pilot gone, invisible to the next
// poll's `Labels: []string{p.label}` query, with zero execution ever having
// started — exactly the live incident (issue queued 16:35Z, pilot label
// removed, no PR, no ledger progress, sat invisible until an operator
// manually restored it). Extracted so the exact label targeted by the
// unwind is unit-testable without standing up the full SDK dispatch path.
func unwindGithubStartedLabel(ctx context.Context, client *githubSDK.Client, owner, repo string, issueNum int) error {
	return client.RemoveLabel(ctx, owner, repo, issueNum, githubSDK.LabelInProgress)
}

// notifyTaskStartedSDK applies the pilot-in-progress label and posts the
// task-started comment via studio-sdk's tested Notifier (GH-4687). Extracted
// so the SDK-dispatch label wiring can be unit tested directly against a
// mock GitHub server — handleGithubIssueEventSDK itself resolves a real
// token/repo and can't be exercised end-to-end in a unit test.
func notifyTaskStartedSDK(ctx context.Context, client *githubSDK.Client, pilotLabel, owner, repo string, issueNum int, taskID string) error {
	return githubSDK.NewNotifier(client, pilotLabel).NotifyTaskStarted(ctx, owner, repo, issueNum, taskID)
}

// githubIssueURL builds the HTML URL for a GitHub issue. It prefers the resolved
// owner/repo (M7 4d.2c: under the per-repo poller fan-out an event may belong to a
// project repo, not the default adapter repo) and falls back to the default adapter
// repo. Returns "" when neither is available.
func githubIssueURL(cfg *config.Config, owner, repo, issueID string) string {
	if owner != "" && repo != "" {
		return fmt.Sprintf("https://github.com/%s/%s/issues/%s", owner, repo, issueID)
	}
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
		return fmt.Sprintf("https://github.com/%s/issues/%s", cfg.Adapters.GitHub.Repo, issueID)
	}
	return ""
}
