package discord

import (
	"fmt"
	"strings"
)

// FormatTaskConfirmation formats a task confirmation message for Discord.
func FormatTaskConfirmation(taskID, description, projectPath string) string {
	desc := description
	if len(desc) > 500 {
		desc = desc[:497] + "..."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("**📋 Task: %s**\n\n", taskID))
	sb.WriteString(desc)
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("*Project: %s*", projectPath))
	return sb.String()
}

// FormatProgressUpdate formats a progress message for Discord.
func FormatProgressUpdate(taskID, phase string, progress int, detail string) string {
	bar := makeProgressBar(progress)
	return fmt.Sprintf("⚙️ %s\n%s %d%%\n\n*%s*", taskID, bar, progress, detail)
}

// FormatTaskResult formats the execution result for Discord.
func FormatTaskResult(output string, success bool, prURL string) string {
	var sb strings.Builder

	if success {
		sb.WriteString("✅ **Task completed**\n\n")
	} else {
		sb.WriteString("❌ **Task failed**\n\n")
	}

	if output != "" {
		out := output
		if len(out) > 1500 {
			out = out[:1497] + "..."
		}
		sb.WriteString(out)
		sb.WriteString("\n\n")
	}

	if prURL != "" {
		sb.WriteString(fmt.Sprintf("🔗 **PR:** [View Pull Request](%s)", prURL))
	}

	return sb.String()
}

// FormatGreeting formats a greeting message for Discord.
func FormatGreeting() string {
	return "👋 Hi! I'm Pilot, your AI coding assistant. How can I help?"
}

// FormatErrorMessage formats an error message for Discord.
func FormatErrorMessage(err string) string {
	return fmt.Sprintf("⚠️ Error: %s", err)
}

// BuildConfirmationButtons creates button components for task confirmation.
func BuildConfirmationButtons() []Component {
	return []Component{
		{
			Type: 1, // ACTION_ROW
			Components: []Button{
				{
					Type:     2, // BUTTON
					Style:    1, // PRIMARY (green)
					Label:    "✅ Execute",
					CustomID: "execute_task",
				},
				{
					Type:     2, // BUTTON
					Style:    4, // DANGER (red)
					Label:    "❌ Cancel",
					CustomID: "cancel_task",
				},
			},
		},
	}
}

// makeProgressBar creates a visual progress bar.
func makeProgressBar(progress int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}

	filled := progress / 10
	empty := 10 - filled

	bar := "["
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	bar += "]"

	return bar
}

// ChunkContent splits long content into Discord message-sized chunks.
func ChunkContent(content string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = MaxMessageLength
	}

	if len(content) <= maxLen {
		return []string{content}
	}

	var chunks []string
	for len(content) > maxLen {
		chunks = append(chunks, content[:maxLen])
		content = content[maxLen:]
	}

	if len(content) > 0 {
		chunks = append(chunks, content)
	}

	return chunks
}

// CleanInternalSignals removes internal markers from output.
func CleanInternalSignals(output string) string {
	// Remove common internal markers
	output = strings.ReplaceAll(output, "<!-- INTERNAL: ", "")
	output = strings.ReplaceAll(output, "<!-- /INTERNAL -->", "")
	output = strings.ReplaceAll(output, "-->", "")
	output = strings.TrimSpace(output)
	return output
}

// TruncateText truncates text to a maximum length.
func TruncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}
