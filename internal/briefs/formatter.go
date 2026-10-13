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

	fmt.Fprintf(&sb, "PILOT DAILY BRIEF — %s\n", brief.GeneratedAt.Format("Jan 2, 2006"))
	sb.WriteString(strings.Repeat("=", 50) + "\n\n")

	// Completed
	fmt.Fprintf(&sb, "COMPLETED (%d)\n", len(brief.Completed))
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
	fmt.Fprintf(&sb, "IN PROGRESS (%d)\n", len(brief.InProgress))
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	if len(brief.InProgress) == 0 {
		sb.WriteString("  No tasks in progress\n")
	}
	for _, task := range brief.InProgress {
		fmt.Fprintf(&sb, "  • %s — %d%% (%s)\n", task.ID, task.Progress, task.Status)
	}
	sb.WriteString("\n")

	// Blocked
	if len(brief.Blocked) > 0 {
		fmt.Fprintf(&sb, "FAILED (%d)\n", len(brief.Blocked))
		sb.WriteString(strings.Repeat("-", 30) + "\n")
		for _, task := range brief.Blocked {
			fmt.Fprintf(&sb, "  • %s\n", task.ID)
			if task.Error != "" {
				// Truncate error to first line
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 60 {
					errLine = errLine[:60] + "..."
				}
				fmt.Fprintf(&sb, "    Error: %s\n", errLine)
			}
		}
		sb.WriteString("\n")
	}

	// Upcoming
	fmt.Fprintf(&sb, "UPCOMING (%d)\n", len(brief.Upcoming))
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	if len(brief.Upcoming) == 0 {
		sb.WriteString("  No tasks queued\n")
	}
	for _, task := range brief.Upcoming {
		fmt.Fprintf(&sb, "  • %s\n", task.ID)
	}
	sb.WriteString("\n")

	// Metrics
	sb.WriteString("METRICS\n")
	sb.WriteString(strings.Repeat("-", 30) + "\n")
	fmt.Fprintf(&sb, "  Success rate: %.0f%% (%d/%d)\n",
		brief.Metrics.SuccessRate*100,
		brief.Metrics.CompletedCount,
		brief.Metrics.TotalTasks)
	fmt.Fprintf(&sb, "  Avg completion: %s\n", formatDuration(brief.Metrics.AvgDurationMs))
	fmt.Fprintf(&sb, "  PRs created: %d\n", brief.Metrics.PRsCreated)

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
