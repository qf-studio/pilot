package briefs

import (
	"fmt"
	"strings"
	"time"
)

// Formatter formats briefs for delivery
type Formatter interface {
	Format(brief *Brief) (string, error)
}

// PlainTextFormatter formats briefs as plain text
type PlainTextFormatter struct{}

// NewPlainTextFormatter creates a new plain text formatter
func NewPlainTextFormatter() *PlainTextFormatter {
	return &PlainTextFormatter{}
}

// Format formats a brief as plain text
func (f *PlainTextFormatter) Format(brief *Brief) (string, error) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("PILOT DAILY BRIEF — %s\n", brief.GeneratedAt.Format("Jan 2, 2006")))
	sb.WriteString(strings.Repeat("=", 50) + "\n\n")

	// Completed
	sb.WriteString(fmt.Sprintf("COMPLETED (%d)\n", len(brief.Completed)))
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	if len(brief.Completed) == 0 {
		sb.WriteString("  No tasks completed\n")
	}
	for _, task := range brief.Completed {
		duration := formatDuration(task.DurationMs)
		line := fmt.Sprintf("  • %s", task.ID)
		if task.PRUrl != "" {
			line += fmt.Sprintf(" — PR: %s", task.PRUrl)
		}
		line += fmt.Sprintf(" (%s)", duration)
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n")

	// In Progress
	sb.WriteString(fmt.Sprintf("IN PROGRESS (%d)\n", len(brief.InProgress)))
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	if len(brief.InProgress) == 0 {
		sb.WriteString("  No tasks in progress\n")
	}
	for _, task := range brief.InProgress {
		sb.WriteString(fmt.Sprintf("  • %s — %d%% (%s)\n", task.ID, task.Progress, task.Status))
	}
	sb.WriteString("\n")

	// Blocked
	if len(brief.Blocked) > 0 {
		sb.WriteString(fmt.Sprintf("BLOCKED (%d)\n", len(brief.Blocked)))
		sb.WriteString(strings.Repeat("-", 30) + "\n")
		for _, task := range brief.Blocked {
			sb.WriteString(fmt.Sprintf("  • %s\n", task.ID))
			if task.Error != "" {
				// Truncate error to first line
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 60 {
					errLine = errLine[:60] + "..."
				}
				sb.WriteString(fmt.Sprintf("    Error: %s\n", errLine))
			}
		}
		sb.WriteString("\n")
	}

	// Upcoming
	sb.WriteString(fmt.Sprintf("UPCOMING (%d)\n", len(brief.Upcoming)))
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	if len(brief.Upcoming) == 0 {
		sb.WriteString("  No tasks queued\n")
	}
	for _, task := range brief.Upcoming {
		sb.WriteString(fmt.Sprintf("  • %s\n", task.ID))
	}
	sb.WriteString("\n")

	// Metrics
	sb.WriteString("METRICS\n")
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	sb.WriteString(fmt.Sprintf("  Success rate: %.0f%% (%d/%d)\n",
		brief.Metrics.SuccessRate*100,
		brief.Metrics.CompletedCount,
		brief.Metrics.TotalTasks))
	sb.WriteString(fmt.Sprintf("  Avg completion: %s\n", formatDuration(brief.Metrics.AvgDurationMs)))
	sb.WriteString(fmt.Sprintf("  PRs created: %d\n", brief.Metrics.PRsCreated))

	return sb.String(), nil
}

// formatDuration formats milliseconds as human readable duration
func formatDuration(ms int64) string {
	if ms == 0 {
		return "N/A"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}
