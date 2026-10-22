package briefs

import (
	"fmt"
	"strings"
)

// SlackFormatter formats briefs for Slack using mrkdwn
type SlackFormatter struct{}

// NewSlackFormatter creates a new Slack formatter
func NewSlackFormatter() *SlackFormatter {
	return &SlackFormatter{}
}

// Format formats a brief for Slack
func (f *SlackFormatter) Format(brief *Brief) (string, error) {
	var sb strings.Builder

	// Header
	fmt.Fprintf(&sb, ":bar_chart: *%s* — %s\n", briefTitle(brief.Kind), brief.GeneratedAt.Format("Jan 2, 2006"))
	if brief.Weekly != nil {
		fmt.Fprintf(&sb, "*Last 7 days:* %s\n", brief.Weekly.Summary())
	}
	sb.WriteString("\n")

	// Completed
	fmt.Fprintf(&sb, "*:white_check_mark: Completed (%d)*\n", len(brief.Completed))
	if len(brief.Completed) == 0 {
		sb.WriteString("_No tasks completed_\n")
	}
	for _, task := range brief.Completed {
		line := fmt.Sprintf("• `%s`", task.ID)
		if task.PRUrl != "" {
			line += fmt.Sprintf(" — <%s|PR ready>", task.PRUrl)
		}
		duration := formatDuration(task.DurationMs)
		line += fmt.Sprintf(" (%s)", duration)
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n")

	// In Progress
	fmt.Fprintf(&sb, "*:arrows_counterclockwise: In Progress (%d)*\n", len(brief.InProgress))
	if len(brief.InProgress) == 0 {
		sb.WriteString("_No tasks in progress_\n")
	}
	for _, task := range brief.InProgress {
		progressBar := generateSlackProgressBar(task.Progress)
		fmt.Fprintf(&sb, "• `%s` — %s %d%%\n", task.ID, progressBar, task.Progress)
	}
	sb.WriteString("\n")

	// Blocked
	if len(brief.Blocked) > 0 {
		fmt.Fprintf(&sb, "*:no_entry: Blocked (%d)*\n", len(brief.Blocked))
		for _, task := range brief.Blocked {
			fmt.Fprintf(&sb, "• `%s`\n", task.ID)
			if task.Error != "" {
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 80 {
					errLine = errLine[:80] + "..."
				}
				fmt.Fprintf(&sb, "  └ `%s`\n", errLine)
			}
		}
		sb.WriteString("\n")
	}

	// Upcoming
	fmt.Fprintf(&sb, "*:clipboard: Upcoming (%d)*\n", len(brief.Upcoming))
	if len(brief.Upcoming) == 0 {
		sb.WriteString("_No tasks queued_\n")
	}
	for _, task := range brief.Upcoming {
		fmt.Fprintf(&sb, "• `%s`\n", task.ID)
	}
	sb.WriteString("\n")

	// Metrics
	sb.WriteString("*:chart_with_upwards_trend: Metrics*\n")
	fmt.Fprintf(&sb, "• Success rate: *%.0f%%* (%d/%d)\n",
		brief.Metrics.SuccessRate*100,
		brief.Metrics.CompletedCount,
		brief.Metrics.TotalTasks)
	fmt.Fprintf(&sb, "• Avg completion: *%s*\n", formatDuration(brief.Metrics.AvgDurationMs))
	fmt.Fprintf(&sb, "• PRs created: *%d*\n", brief.Metrics.PRsCreated)

	return sb.String(), nil
}

// SlackBlocks returns the brief as Slack Block Kit blocks
func (f *SlackFormatter) SlackBlocks(brief *Brief) []map[string]interface{} {
	blocks := []map[string]interface{}{}

	// Header
	blocks = append(blocks, map[string]interface{}{
		"type": "header",
		"text": map[string]interface{}{
			"type":  "plain_text",
			"text":  fmt.Sprintf("%s — %s", briefTitle(brief.Kind), brief.GeneratedAt.Format("Jan 2, 2006")),
			"emoji": true,
		},
	})
	if brief.Weekly != nil {
		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]interface{}{
				"type": "mrkdwn",
				"text": "*Last 7 days:* " + brief.Weekly.Summary(),
			},
		})
	}

	// Completed section
	completedText := fmt.Sprintf(":white_check_mark: *Completed (%d)*\n", len(brief.Completed))
	if len(brief.Completed) == 0 {
		completedText += "_No tasks completed_"
	} else {
		for _, task := range brief.Completed {
			line := fmt.Sprintf("• `%s`", task.ID)
			if task.PRUrl != "" {
				line += fmt.Sprintf(" — <%s|PR>", task.PRUrl)
			}
			completedText += line + "\n"
		}
	}
	blocks = append(blocks, map[string]interface{}{
		"type": "section",
		"text": map[string]interface{}{
			"type": "mrkdwn",
			"text": completedText,
		},
	})

	// In Progress section
	progressText := fmt.Sprintf(":arrows_counterclockwise: *In Progress (%d)*\n", len(brief.InProgress))
	if len(brief.InProgress) == 0 {
		progressText += "_No tasks in progress_"
	} else {
		for _, task := range brief.InProgress {
			progressBar := generateSlackProgressBar(task.Progress)
			progressText += fmt.Sprintf("• `%s` %s %d%%\n", task.ID, progressBar, task.Progress)
		}
	}
	blocks = append(blocks, map[string]interface{}{
		"type": "section",
		"text": map[string]interface{}{
			"type": "mrkdwn",
			"text": progressText,
		},
	})

	// Blocked section (if any)
	if len(brief.Blocked) > 0 {
		blockedText := fmt.Sprintf(":no_entry: *Blocked (%d)*\n", len(brief.Blocked))
		for _, task := range brief.Blocked {
			blockedText += fmt.Sprintf("• `%s`", task.ID)
			if task.Error != "" {
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 50 {
					errLine = errLine[:50] + "..."
				}
				blockedText += fmt.Sprintf("\n  └ `%s`", errLine)
			}
			blockedText += "\n"
		}
		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]interface{}{
				"type": "mrkdwn",
				"text": blockedText,
			},
		})
	}

	// Upcoming section
	upcomingText := fmt.Sprintf(":clipboard: *Upcoming (%d)*\n", len(brief.Upcoming))
	if len(brief.Upcoming) == 0 {
		upcomingText += "_No tasks queued_"
	} else {
		for _, task := range brief.Upcoming {
			upcomingText += fmt.Sprintf("• `%s`\n", task.ID)
		}
	}
	blocks = append(blocks, map[string]interface{}{
		"type": "section",
		"text": map[string]interface{}{
			"type": "mrkdwn",
			"text": upcomingText,
		},
	})

	// Divider
	blocks = append(blocks, map[string]interface{}{
		"type": "divider",
	})

	// Metrics
	metricsText := fmt.Sprintf(":chart_with_upwards_trend: *Metrics*\n"+
		"Success rate: *%.0f%%* (%d/%d) • Avg: *%s* • PRs: *%d*",
		brief.Metrics.SuccessRate*100,
		brief.Metrics.CompletedCount,
		brief.Metrics.TotalTasks,
		formatDuration(brief.Metrics.AvgDurationMs),
		brief.Metrics.PRsCreated)
	blocks = append(blocks, map[string]interface{}{
		"type": "context",
		"elements": []map[string]interface{}{
			{
				"type": "mrkdwn",
				"text": metricsText,
			},
		},
	})

	return blocks
}

// generateSlackProgressBar creates a text progress bar for Slack
func generateSlackProgressBar(progress int) string {
	filled := progress / 10
	empty := 10 - filled
	bar := ""
	for i := 0; i < filled; i++ {
		bar += "▓"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	return bar
}
