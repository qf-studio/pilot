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

	"github.com/alekspetrov/pilot/internal/adapters/asana"
	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/jira"
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
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

	// Register task with monitor if in dashboard mode
	// Note: monitor.Start() is NOT called here — it's called by runner.executeWithOptions()
	// when execution actually begins, enabling accurate queued→running dashboard transitions.
	if monitor != nil {
		monitor.Register(taskID, issue.Title, issue.HTMLURL)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📥 GitHub Issue #%d: %s", issue.Number, issue.Title))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Title,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check — block task if daily/monthly limits exceeded
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
				slog.String("action", string(checkResult.Action)),
			)

			// Emit budget exceeded alert
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Title,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}

			// Comment on the GitHub issue and label as failed
			parts := strings.Split(sourceRepo, "/")
			if len(parts) == 2 {
				comment := fmt.Sprintf("⛔ **Budget Limit Exceeded**\n\n%s\n\nDaily remaining: $%.2f\nMonthly remaining: $%.2f\n\nThis task has been skipped. Resume execution after adjusting budget limits or waiting for the next billing period.",
					checkResult.Reason, checkResult.DailyLeft, checkResult.MonthlyLeft)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
			}

			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &github.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📥 GitHub Issue #%d: %s\n", issue.Number, issue.Title)

	parts := strings.Split(sourceRepo, "/")
	if len(parts) == 2 {
		if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelInProgress}); err != nil {
			logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
		}
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
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			if monitor != nil {
				monitor.Queue(taskID)
			}
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, task)
	}

	// Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if monitor != nil {
		if execErr != nil {
			monitor.Fail(taskID, execErr.Error())
		} else {
			monitor.Complete(taskID, prURL)
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if execErr != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		}
		if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, duration, "", false)())
	}

	// Build the issue result
	issueResult := &github.IssueResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: branchName,
		Error:      execErr,
	}

	// Extract PR number and head SHA from result if we have one
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
		if result.CommitSHA != "" {
			issueResult.HeadSHA = result.CommitSHA
		}
	}

	// Update issue labels and add comment
	if len(parts) == 2 {
		if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelInProgress); err != nil {
			logGitHubAPIError("RemoveLabel", parts[0], parts[1], issue.Number, err)
		}

		if execErr != nil {
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		} else if result != nil && result.Success {
			// Validate deliverables before marking as done
			if result.CommitSHA == "" && result.PRUrl == "" {
				// No commits and no PR - mark as failed
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
					result.Duration, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
				// Update issueResult to reflect failure
				issueResult.Success = false
			} else {
				// Has deliverables — add pilot-done immediately to close label gap
				// GH-1350: Prevents parallel poller re-dispatch race during the window
				// between execution complete and autopilot merge handler
				// GH-1015: Autopilot also adds pilot-done after merge (idempotent)
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelDone}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}

				// GH-1302: Clean up stale pilot-failed label from prior failed attempt
				if github.HasLabel(issue, github.LabelFailed) {
					if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelFailed); err != nil {
						logGitHubAPIError("RemoveLabel", parts[0], parts[1], issue.Number, err)
					}
				}

					// Close the issue so dependent issues can proceed
				if err := client.UpdateIssueState(ctx, parts[0], parts[1], issue.Number, "closed"); err != nil {
					logGitHubAPIError("UpdateIssueState", parts[0], parts[1], issue.Number, err)
				}

				comment := buildExecutionComment(result, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		} else if result != nil {
			// result exists but Success is false - mark as failed
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := buildFailureComment(result)
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		}
	}

	return issueResult, execErr
}

// handleLinearIssueWithResult processes a Linear issue picked up by the poller (GH-393)
func handleLinearIssueWithResult(ctx context.Context, cfg *config.Config, client *linear.Client, issue *linear.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*linear.IssueResult, error) {
	taskID := issue.Identifier // e.g., "APP-123"

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		issueURL := fmt.Sprintf("https://linear.app/issue/%s", issue.Identifier)
		monitor.Register(taskID, issue.Title, issueURL)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📊 Linear Issue %s: %s", issue.Identifier, issue.Title))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Title,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Title,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &linear.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📊 Linear Issue %s: %s\n", issue.Identifier, issue.Title)

	taskDesc := fmt.Sprintf("Linear Issue %s: %s\n\n%s", issue.Identifier, issue.Title, issue.Description)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// GH-920: Extract acceptance criteria from Linear issue description
	// GH-1472: Set SourceAdapter/SourceIssueID for sub-issue creation via Linear API
	task := &executor.Task{
		ID:                 taskID,
		Title:              issue.Title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(issue.Description),
		SourceAdapter:      "linear",
		SourceIssueID:      issue.ID,
	}

	// GH-1472: Wire Linear client as SubIssueCreator for epic decomposition
	runner.SetSubIssueCreator(client)

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			if monitor != nil {
				monitor.Queue(taskID)
			}
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, task)
	}

	// Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if monitor != nil {
		if execErr != nil {
			monitor.Fail(taskID, execErr.Error())
		} else {
			monitor.Complete(taskID, prURL)
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if execErr != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, duration, "", false)())
	}

	// Build issue result
	issueResult := &linear.IssueResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: branchName, // GH-1361: always set branch for autopilot wiring
	}
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
		issueResult.HeadSHA = result.CommitSHA // GH-1361: for autopilot CI monitoring
	}

	// Add comment to Linear issue
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
		if err := client.AddComment(ctx, issue.ID, comment); err != nil {
			logging.WithComponent("linear").Warn("Failed to add comment",
				slog.String("issue", issue.Identifier),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if err := client.AddComment(ctx, issue.ID, comment); err != nil {
				logging.WithComponent("linear").Warn("Failed to add comment",
					slog.String("issue", issue.Identifier),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			comment := buildExecutionComment(result, branchName)
			if err := client.AddComment(ctx, issue.ID, comment); err != nil {
				logging.WithComponent("linear").Warn("Failed to add comment",
					slog.String("issue", issue.Identifier),
					slog.Any("error", err),
				)
			}

			// GH-1403: Best-effort state transition to Done
			doneStateID, err := client.GetTeamDoneStateID(ctx, issue.Team.Key)
			if err != nil {
				logging.WithComponent("linear").Warn("failed to get done state ID for team",
					slog.String("issue", issue.Identifier),
					slog.String("team", issue.Team.Key),
					slog.Any("error", err),
				)
			} else if err := client.UpdateIssueState(ctx, issue.ID, doneStateID); err != nil {
				logging.WithComponent("linear").Warn("failed to transition issue to done state",
					slog.String("issue", issue.Identifier),
					slog.String("state_id", doneStateID),
					slog.Any("error", err),
				)
			}
		}
	} else if result != nil {
		comment := buildFailureComment(result)
		if err := client.AddComment(ctx, issue.ID, comment); err != nil {
			logging.WithComponent("linear").Warn("Failed to add comment",
				slog.String("issue", issue.Identifier),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// handleJiraIssueWithResult processes a Jira issue picked up by the poller (GH-905)
func handleJiraIssueWithResult(ctx context.Context, cfg *config.Config, client *jira.Client, issue *jira.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*jira.IssueResult, error) {
	taskID := issue.Key // e.g., "PROJ-123"

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		issueURL := fmt.Sprintf("%s/browse/%s", cfg.Adapters.Jira.BaseURL, issue.Key)
		monitor.Register(taskID, issue.Fields.Summary, issueURL)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📊 Jira Issue %s: %s", issue.Key, issue.Fields.Summary))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Fields.Summary,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Fields.Summary,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &jira.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📊 Jira Issue %s: %s\n", issue.Key, issue.Fields.Summary)

	taskDesc := fmt.Sprintf("Jira Issue %s: %s\n\n%s", issue.Key, issue.Fields.Summary, issue.Fields.Description)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	task := &executor.Task{
		ID:          taskID,
		Title:       issue.Fields.Summary,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			if monitor != nil {
				monitor.Queue(taskID)
			}
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, task)
	}

	// Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if monitor != nil {
		if execErr != nil {
			monitor.Fail(taskID, execErr.Error())
		} else {
			monitor.Complete(taskID, prURL)
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if execErr != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Fields.Summary,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: issue.Fields.Summary,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Fields.Summary,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Fields.Summary, status, duration, "", false)())
	}

	// Build issue result
	issueResult := &jira.IssueResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: branchName, // GH-1399: always set branch for autopilot wiring
	}
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
		issueResult.HeadSHA = result.CommitSHA // GH-1399: for autopilot CI monitoring
	}

	// Add comment to Jira issue
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n%s", execErr.Error())
		if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
			logging.WithComponent("jira").Warn("Failed to add comment",
				slog.String("issue", issue.Key),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\nDuration: %s\nBranch: %s\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
				logging.WithComponent("jira").Warn("Failed to add comment",
					slog.String("issue", issue.Key),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			comment := buildJiraExecutionComment(result, branchName)
			if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
				logging.WithComponent("jira").Warn("Failed to add comment",
					slog.String("issue", issue.Key),
					slog.Any("error", err),
				)
			}

			// GH-1403: Best-effort state transition to Done
			// Check config for explicit transition ID, fall back to name-based lookup
			if cfg.Adapters.Jira.Transitions.Done != "" {
				if err := client.TransitionIssue(ctx, issue.Key, cfg.Adapters.Jira.Transitions.Done); err != nil {
					logging.WithComponent("jira").Warn("failed to transition issue to done state (explicit ID)",
						slog.String("issue", issue.Key),
						slog.String("transition_id", cfg.Adapters.Jira.Transitions.Done),
						slog.Any("error", err),
					)
				}
			} else {
				if err := client.TransitionIssueTo(ctx, issue.Key, "Done"); err != nil {
					logging.WithComponent("jira").Warn("failed to transition issue to done state (name lookup)",
						slog.String("issue", issue.Key),
						slog.Any("error", err),
					)
				}
			}
		}
	} else if result != nil {
		comment := buildJiraFailureComment(result)
		if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
			logging.WithComponent("jira").Warn("Failed to add comment",
				slog.String("issue", issue.Key),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// buildJiraExecutionComment creates a comment for successful Jira execution
func buildJiraExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var parts []string
	parts = append(parts, "✅ Pilot execution completed successfully!")
	parts = append(parts, "")

	if result.PRUrl != "" {
		parts = append(parts, fmt.Sprintf("Pull Request: %s", result.PRUrl))
	}
	if result.CommitSHA != "" {
		parts = append(parts, fmt.Sprintf("Commit: %s", result.CommitSHA[:min(8, len(result.CommitSHA))]))
	}
	parts = append(parts, fmt.Sprintf("Branch: %s", branchName))
	parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))

	return strings.Join(parts, "\n")
}

// buildJiraFailureComment creates a comment for failed Jira execution
func buildJiraFailureComment(result *executor.ExecutionResult) string {
	var parts []string
	parts = append(parts, "❌ Pilot execution failed")
	parts = append(parts, "")
	if result.Error != "" {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Error))
	}
	if result.Duration > 0 {
		parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))
	}
	return strings.Join(parts, "\n")
}

// handleAsanaTaskWithResult processes an Asana task picked up by the poller (GH-906)
func handleAsanaTaskWithResult(ctx context.Context, cfg *config.Config, client *asana.Client, task *asana.Task, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*asana.TaskResult, error) {
	taskID := "ASANA-" + task.GID

	// Get task URL
	taskURL := task.Permalink
	if taskURL == "" {
		taskURL = "https://app.asana.com/0/0/" + task.GID
	}

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		monitor.Register(taskID, task.Name, taskURL)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📦 Asana Task %s: %s", task.GID, task.Name))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: task.Name,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: task.Name,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &asana.TaskResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📦 Asana Task %s: %s\n", task.GID, task.Name)

	taskDesc := fmt.Sprintf("Asana Task %s: %s\n\n%s", task.GID, task.Name, task.Notes)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	execTask := &executor.Task{
		ID:          taskID,
		Title:       task.Name,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, execTask)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			if monitor != nil {
				monitor.Queue(taskID)
			}
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    execTask.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, execTask)
	}

	// Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if monitor != nil {
		if execErr != nil {
			monitor.Fail(taskID, execErr.Error())
		} else {
			monitor.Complete(taskID, prURL)
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if execErr != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: task.Name,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: task.Name,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: task.Name,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, task.Name, status, duration, "", false)())
	}

	// Build task result
	taskResult := &asana.TaskResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: branchName, // GH-1399: always set branch for autopilot wiring
	}
	if result != nil {
		if result.PRUrl != "" {
			taskResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				taskResult.PRNumber = prNum
			}
		}
		taskResult.HeadSHA = result.CommitSHA // GH-1399: for autopilot CI monitoring
	}

	// Add comment to Asana task
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n%s", execErr.Error())
		if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
			logging.WithComponent("asana").Warn("Failed to add comment",
				slog.String("task", task.GID),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\nDuration: %s\nBranch: %s\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
				logging.WithComponent("asana").Warn("Failed to add comment",
					slog.String("task", task.GID),
					slog.Any("error", err),
				)
			}
			taskResult.Success = false
		} else {
			comment := buildAsanaExecutionComment(result, branchName)
			if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
				logging.WithComponent("asana").Warn("Failed to add comment",
					slog.String("task", task.GID),
					slog.Any("error", err),
				)
			}

			// GH-1403: Best-effort task completion
			if _, err := client.CompleteTask(ctx, task.GID); err != nil {
				logging.WithComponent("asana").Warn("failed to complete task",
					slog.String("task", task.GID),
					slog.Any("error", err),
				)
			}
		}
	} else if result != nil {
		comment := buildAsanaFailureComment(result)
		if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
			logging.WithComponent("asana").Warn("Failed to add comment",
				slog.String("task", task.GID),
				slog.Any("error", err),
			)
		}
	}

	return taskResult, execErr
}

// buildAsanaExecutionComment creates a comment for successful Asana execution
func buildAsanaExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var parts []string
	parts = append(parts, "✅ Pilot execution completed successfully!")
	parts = append(parts, "")

	if result.PRUrl != "" {
		parts = append(parts, fmt.Sprintf("Pull Request: %s", result.PRUrl))
	}
	if result.CommitSHA != "" {
		parts = append(parts, fmt.Sprintf("Commit: %s", result.CommitSHA[:min(8, len(result.CommitSHA))]))
	}
	parts = append(parts, fmt.Sprintf("Branch: %s", branchName))
	parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))

	return strings.Join(parts, "\n")
}

// buildAsanaFailureComment creates a comment for failed Asana execution
func buildAsanaFailureComment(result *executor.ExecutionResult) string {
	var parts []string
	parts = append(parts, "❌ Pilot execution failed")
	parts = append(parts, "")
	if result.Error != "" {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Error))
	}
	if result.Duration > 0 {
		parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))
	}
	return strings.Join(parts, "\n")
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
