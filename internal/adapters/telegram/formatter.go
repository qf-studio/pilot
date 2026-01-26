package telegram

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
)

// Internal signals to strip from output
var internalSignals = []string{
	"EXIT_SIGNAL: true",
	"EXIT_SIGNAL:true",
	"LOOP COMPLETE",
	"TASK MODE COMPLETE",
	"NAVIGATOR_STATUS",
	"━━━━━━━━━━",
	"Phase:",
	"Iteration:",
	"Progress:",
	"Completion Indicators:",
	"Exit Conditions:",
	"State Hash:",
	"Next Action:",
}

// FormatTaskConfirmation formats a task confirmation message
func FormatTaskConfirmation(taskID, description, projectPath string) string {
	return fmt.Sprintf(
		"📋 *Confirm Task*\n\n"+
			"`%s`\n\n"+
			"*Task:* %s\n"+
			"*Project:* `%s`\n\n"+
			"Execute this task?",
		taskID,
		escapeMarkdown(truncateDescription(description, 200)),
		projectPath,
	)
}

// FormatTaskStarted formats a task started message
func FormatTaskStarted(taskID, description string) string {
	return fmt.Sprintf(
		"🚀 *Executing*\n`%s`\n\n%s",
		taskID,
		escapeMarkdown(truncateDescription(description, 150)),
	)
}

// FormatProgressUpdate formats a progress update message
func FormatProgressUpdate(taskID, phase string, progress int, message string) string {
	// Build progress bar (20 chars)
	filled := progress / 5 // 0-20 filled chars
	if filled > 20 {
		filled = 20
	}
	if filled < 0 {
		filled = 0
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", 20-filled)

	// Phase emoji
	phaseEmoji := "⏳"
	switch phase {
	case "Starting":
		phaseEmoji = "🚀"
	case "Branching":
		phaseEmoji = "🌿"
	case "Exploring":
		phaseEmoji = "🔍"
	case "Installing":
		phaseEmoji = "📦"
	case "Implementing":
		phaseEmoji = "⚙️"
	case "Testing":
		phaseEmoji = "🧪"
	case "Committing":
		phaseEmoji = "💾"
	case "Completed":
		phaseEmoji = "✅"
	case "Navigator":
		phaseEmoji = "🧭"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s *%s* \\(%d%%\\)\n", phaseEmoji, phase, progress))
	sb.WriteString(fmt.Sprintf("`%s`\n\n", bar))
	sb.WriteString(fmt.Sprintf("`%s`", taskID))

	// Add activity message if present
	if message != "" {
		cleanMsg := truncateDescription(message, 60)
		sb.WriteString(fmt.Sprintf("\n\n📝 %s", escapeMarkdown(cleanMsg)))
	}

	return sb.String()
}

// FormatTaskResult formats a task result message with clean output
func FormatTaskResult(result *executor.ExecutionResult) string {
	if result.Success {
		return formatSuccessResult(result)
	}
	return formatFailureResult(result)
}

// formatSuccessResult formats a successful task result
func formatSuccessResult(result *executor.ExecutionResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("✅ *Task completed*\n`%s`\n\n", result.TaskID))
	sb.WriteString(fmt.Sprintf("⏱ Duration: %s\n", result.Duration.Round(time.Second)))

	// Add commit SHA if present
	if result.CommitSHA != "" {
		sb.WriteString(fmt.Sprintf("📝 Commit: `%s`\n", result.CommitSHA[:min(8, len(result.CommitSHA))]))
	}

	// Add PR URL if present
	if result.PRUrl != "" {
		sb.WriteString(fmt.Sprintf("\n🔗 [View PR](%s)\n", result.PRUrl))
	}

	// Clean and add output summary
	cleanOutput := cleanInternalSignals(result.Output)
	if cleanOutput != "" {
		// Extract key information from output
		summary := extractSummary(cleanOutput)
		if summary != "" {
			sb.WriteString(fmt.Sprintf("\n📄 *Summary:*\n%s", summary))
		}
	}

	return sb.String()
}

// formatFailureResult formats a failed task result
func formatFailureResult(result *executor.ExecutionResult) string {
	cleanError := cleanInternalSignals(result.Error)
	if cleanError == "" {
		cleanError = "Unknown error"
	}

	// Truncate error for Telegram
	if len(cleanError) > 400 {
		cleanError = cleanError[:400] + "..."
	}

	return fmt.Sprintf(
		"❌ *Task failed*\n`%s`\n\n⏱ Duration: %s\n\n```\n%s\n```",
		result.TaskID,
		result.Duration.Round(time.Second),
		cleanError,
	)
}

// FormatGreeting formats a greeting response
func FormatGreeting(username string) string {
	name := "there"
	if username != "" {
		name = username
	}
	return fmt.Sprintf(
		"👋 Hey %s! I'm Pilot bot.\n\n"+
			"Send me a task to execute, or ask me a question about the codebase.\n\n"+
			"*Examples:*\n"+
			"• `Create a hello.py file`\n"+
			"• `What files handle auth?`\n"+
			"• `/help` for more info",
		name,
	)
}

// FormatQuestionAck formats acknowledgment for a question
func FormatQuestionAck() string {
	return "🔍 *Looking into that...*"
}

// FormatQuestionAnswer formats an answer to a question
func FormatQuestionAnswer(answer string) string {
	// Clean any internal signals from the answer
	cleanAnswer := cleanInternalSignals(answer)

	// Truncate if too long for Telegram
	if len(cleanAnswer) > 3500 {
		cleanAnswer = cleanAnswer[:3500] + "\n\n_(truncated)_"
	}

	return cleanAnswer
}

// cleanInternalSignals removes internal Navigator signals from output
func cleanInternalSignals(text string) string {
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	var cleanLines []string
	skipBlock := false

	for _, line := range lines {
		// Skip NAVIGATOR_STATUS blocks
		if strings.Contains(line, "NAVIGATOR_STATUS") {
			skipBlock = true
			continue
		}
		if skipBlock {
			// End of block when we see another separator
			if strings.HasPrefix(strings.TrimSpace(line), "━") && len(cleanLines) > 0 {
				skipBlock = false
			}
			continue
		}

		// Skip lines with internal signals
		shouldSkip := false
		for _, signal := range internalSignals {
			if strings.Contains(line, signal) {
				shouldSkip = true
				break
			}
		}
		if shouldSkip {
			continue
		}

		// Skip empty lines at the start
		if len(cleanLines) == 0 && strings.TrimSpace(line) == "" {
			continue
		}

		cleanLines = append(cleanLines, line)
	}

	// Trim trailing empty lines
	for len(cleanLines) > 0 && strings.TrimSpace(cleanLines[len(cleanLines)-1]) == "" {
		cleanLines = cleanLines[:len(cleanLines)-1]
	}

	return strings.Join(cleanLines, "\n")
}

// extractSummary extracts key summary points from output
func extractSummary(output string) string {
	// Look for common summary patterns
	patterns := []struct {
		regex   string
		format  string
	}{
		{`(?i)created?\s+["\x60]?([^"\x60\n]+\.\w+)["\x60]?`, "📁 Created: `%s`"},
		{`(?i)modified?\s+["\x60]?([^"\x60\n]+\.\w+)["\x60]?`, "📝 Modified: `%s`"},
		{`(?i)added?\s+["\x60]?([^"\x60\n]+\.\w+)["\x60]?`, "➕ Added: `%s`"},
		{`(?i)deleted?\s+["\x60]?([^"\x60\n]+\.\w+)["\x60]?`, "🗑 Deleted: `%s`"},
	}

	var summaryItems []string
	seen := make(map[string]bool)

	for _, p := range patterns {
		re := regexp.MustCompile(p.regex)
		matches := re.FindAllStringSubmatch(output, 5) // Max 5 matches per pattern
		for _, match := range matches {
			if len(match) > 1 {
				item := fmt.Sprintf(p.format, match[1])
				if !seen[item] {
					summaryItems = append(summaryItems, item)
					seen[item] = true
				}
			}
		}
	}

	if len(summaryItems) == 0 {
		return ""
	}

	// Limit to 5 items
	if len(summaryItems) > 5 {
		summaryItems = summaryItems[:5]
		summaryItems = append(summaryItems, "_(and more...)_")
	}

	return strings.Join(summaryItems, "\n")
}

// escapeMarkdown escapes Telegram Markdown special characters
func escapeMarkdown(text string) string {
	// Characters that need escaping in Telegram Markdown
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// truncateDescription truncates a string to maxLen
func truncateDescription(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
