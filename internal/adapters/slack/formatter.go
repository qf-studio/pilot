package slack

import (
	"fmt"
	"strings"
)

// Block Kit formatting helpers for Slack messages.
// These helpers create properly formatted Slack blocks for consistent UI.

// FormatGreeting returns a greeting message for Slack.
func FormatGreeting(username string) string {
	if username != "" {
		return fmt.Sprintf("👋 Hi %s! I'm Pilot, your AI coding assistant. How can I help?", username)
	}
	return "👋 Hi! I'm Pilot, your AI coding assistant. How can I help?"
}

// FormatQuestionAck returns an acknowledgment for question processing.
func FormatQuestionAck() string {
	return "🔍 Looking into that..."
}

// FormatTaskConfirmation returns a task confirmation message.
func FormatTaskConfirmation(taskID, description, projectPath string) string {
	// Truncate description if too long
	desc := description
	if len(desc) > 500 {
		desc = desc[:497] + "..."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 *Task: %s*\n\n", taskID))
	sb.WriteString(desc)
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("_Project: %s_", projectPath))
	return sb.String()
}

// FormatTaskStarted returns a message indicating task execution started.
func FormatTaskStarted(taskID, description string) string {
	desc := truncateText(description, 100)
	return fmt.Sprintf("🚀 Starting task %s\n\n%s", taskID, desc)
}

// FormatProgressUpdate returns a formatted progress update message.
func FormatProgressUpdate(taskID, phase string, progress int, message string) string {
	bar := makeProgressBar(progress)
	return fmt.Sprintf("⚙️ %s\n%s %d%%\n\n_%s_", taskID, bar, progress, message)
}

// FormatTaskResult formats the execution result for Slack.
func FormatTaskResult(output string, success bool, prURL string) string {
	var sb strings.Builder

	if success {
		sb.WriteString("✅ *Task completed*\n\n")
	} else {
		sb.WriteString("❌ *Task failed*\n\n")
	}

	if output != "" {
		// Truncate if too long for Slack
		out := output
		if len(out) > 2500 {
			out = out[:2497] + "..."
		}
		sb.WriteString(out)
		sb.WriteString("\n\n")
	}

	if prURL != "" {
		sb.WriteString(fmt.Sprintf("🔗 *PR:* <%s|View Pull Request>", prURL))
	}

	return sb.String()
}

// FormatResearchOutput formats research findings for Slack.
func FormatResearchOutput(content string) string {
	if content == "" {
		return "🤷 No research findings to report."
	}
	return content
}

// FormatPlanSummary extracts and formats a plan summary for display.
func FormatPlanSummary(plan string) string {
	lines := strings.Split(plan, "\n")
	var summary []string
	lineCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		summary = append(summary, trimmed)
		lineCount++

		if lineCount >= 15 {
			break
		}
	}

	result := strings.Join(summary, "\n")
	if len(result) > 1500 {
		result = result[:1497] + "..."
	}

	return result
}

// makeProgressBar creates a text progress bar.
func makeProgressBar(percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent / 5 // 20 chars total
	empty := 20 - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// truncateText truncates text to maxLen, adding ellipsis if needed.
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}

// ChunkContent splits content into chunks suitable for Slack messages.
// Slack has a 4096 character limit per message.
func ChunkContent(content string, maxLen int) []string {
	if len(content) <= maxLen {
		return []string{content}
	}

	var chunks []string
	remaining := content

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		// Try to break at a newline
		breakPoint := maxLen
		lastNewline := strings.LastIndex(remaining[:maxLen], "\n")
		if lastNewline > maxLen/2 {
			breakPoint = lastNewline + 1
		}

		chunks = append(chunks, remaining[:breakPoint])
		remaining = remaining[breakPoint:]
	}

	return chunks
}

// BuildConfirmationBlocks creates Block Kit blocks for task confirmation.
func BuildConfirmationBlocks(taskID, description string) []interface{} {
	desc := description
	if len(desc) > 500 {
		desc = desc[:497] + "..."
	}

	return []interface{}{
		Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("📋 *Task: %s*\n\n%s", taskID, desc),
			},
		},
		ActionsBlock{
			Type:    "actions",
			BlockID: "task_confirmation",
			Elements: []ButtonElement{
				{
					Type: "button",
					Text: &TextObject{
						Type:  "plain_text",
						Text:  "✅ Execute",
						Emoji: true,
					},
					ActionID: "execute_task",
					Value:    taskID,
					Style:    "primary",
				},
				{
					Type: "button",
					Text: &TextObject{
						Type:  "plain_text",
						Text:  "❌ Cancel",
						Emoji: true,
					},
					ActionID: "cancel_task",
					Value:    taskID,
					Style:    "danger",
				},
			},
		},
	}
}

// BuildProgressBlocks creates Block Kit blocks for progress updates.
func BuildProgressBlocks(taskID, phase string, progress int, message string) []interface{} {
	bar := makeProgressBar(progress)

	return []interface{}{
		Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("⚙️ *%s*\n\n%s %d%%\n\n_%s_", taskID, bar, progress, message),
			},
		},
	}
}

// BuildResultBlocks creates Block Kit blocks for task results.
func BuildResultBlocks(taskID string, success bool, output, prURL string) []interface{} {
	var icon, status string
	if success {
		icon = "✅"
		status = "completed"
	} else {
		icon = "❌"
		status = "failed"
	}

	blocks := []interface{}{
		Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("%s *%s %s*", icon, taskID, status),
			},
		},
	}

	if output != "" {
		out := output
		if len(out) > 2500 {
			out = out[:2497] + "..."
		}
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: out,
			},
		})
	}

	if prURL != "" {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: fmt.Sprintf("🔗 <%s|View Pull Request>", prURL),
			},
		})
	}

	return blocks
}

// planningErrorMessage returns the user-facing message when runner.Execute
// returns a non-nil error during planning. It distinguishes context deadline
// exceeded (timeout) from all other executor errors.
func planningErrorMessage(err error, ctxErr error) string {
	if ctxErr != nil {
		return "⏱ Planning timed out. Try a simpler request."
	}
	return fmt.Sprintf("❌ Planning failed: %s", err.Error())
}

// planEmptyMessage returns the appropriate user-facing message when planning
// produces no output. It differentiates between executor errors, non-success
// (e.g. timeout), and the case where the task is too simple for planning.
func planEmptyMessage(resultError string, resultSuccess bool) string {
	switch {
	case resultError != "":
		return fmt.Sprintf("❌ Planning error: %s", resultError)
	case !resultSuccess:
		return "⏱ Planning timed out. Try a simpler request."
	default:
		return "🤷 The task may be too simple for planning. Try executing it directly."
	}
}

// CleanInternalSignals removes internal Navigator signals from output.
func CleanInternalSignals(output string) string {
	// Remove common internal signals
	signals := []string{
		"[EXIT_SIGNAL]",
		"[NAV_COMPLETE]",
		"[RESEARCH_DONE]",
		"[IMPL_DONE]",
	}

	result := output
	for _, signal := range signals {
		result = strings.ReplaceAll(result, signal, "")
	}

	return strings.TrimSpace(result)
}
