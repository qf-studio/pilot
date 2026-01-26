package briefs

import (
	"fmt"
	"html"
	"strings"
)

// EmailFormatter formats briefs as HTML email
type EmailFormatter struct{}

// NewEmailFormatter creates a new email formatter
func NewEmailFormatter() *EmailFormatter {
	return &EmailFormatter{}
}

// Format formats a brief as HTML email
func (f *EmailFormatter) Format(brief *Brief) (string, error) {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #333; max-width: 600px; margin: 0 auto; padding: 20px; }
h1 { color: #1a1a2e; border-bottom: 2px solid #6366f1; padding-bottom: 10px; }
h2 { color: #4a4a68; margin-top: 24px; }
.section { margin-bottom: 24px; }
.task { padding: 8px 0; border-bottom: 1px solid #eee; }
.task:last-child { border-bottom: none; }
.task-id { font-family: monospace; background: #f4f4f5; padding: 2px 6px; border-radius: 3px; }
.pr-link { color: #6366f1; text-decoration: none; }
.pr-link:hover { text-decoration: underline; }
.error { color: #dc2626; font-family: monospace; font-size: 0.9em; }
.progress-bar { display: inline-block; width: 100px; height: 8px; background: #e5e7eb; border-radius: 4px; overflow: hidden; vertical-align: middle; }
.progress-fill { height: 100%; background: #6366f1; }
.metrics { background: #f8fafc; padding: 16px; border-radius: 8px; }
.metric { display: inline-block; margin-right: 24px; }
.metric-value { font-size: 1.5em; font-weight: bold; color: #6366f1; }
.metric-label { font-size: 0.85em; color: #64748b; }
.empty { color: #94a3b8; font-style: italic; }
.blocked { background: #fef2f2; padding: 12px; border-radius: 6px; border-left: 3px solid #dc2626; }
</style>
</head>
<body>
`)

	// Header
	sb.WriteString("<h1>📊 Pilot Daily Brief</h1>\n")
	sb.WriteString(fmt.Sprintf("<p style=\"color: #64748b;\">%s</p>\n", brief.GeneratedAt.Format("Monday, January 2, 2006")))

	// Completed
	sb.WriteString("<div class=\"section\">\n")
	sb.WriteString(fmt.Sprintf("<h2>✅ Completed (%d)</h2>\n", len(brief.Completed)))
	if len(brief.Completed) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks completed</p>\n")
	} else {
		for _, task := range brief.Completed {
			sb.WriteString("<div class=\"task\">\n")
			sb.WriteString(fmt.Sprintf("<span class=\"task-id\">%s</span>", html.EscapeString(task.ID)))
			if task.PRUrl != "" {
				sb.WriteString(fmt.Sprintf(" — <a class=\"pr-link\" href=\"%s\">PR ready for review</a>", html.EscapeString(task.PRUrl)))
			}
			sb.WriteString(fmt.Sprintf(" <span style=\"color: #64748b;\">(%s)</span>", formatDuration(task.DurationMs)))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// In Progress
	sb.WriteString("<div class=\"section\">\n")
	sb.WriteString(fmt.Sprintf("<h2>🔄 In Progress (%d)</h2>\n", len(brief.InProgress)))
	if len(brief.InProgress) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks in progress</p>\n")
	} else {
		for _, task := range brief.InProgress {
			sb.WriteString("<div class=\"task\">\n")
			sb.WriteString(fmt.Sprintf("<span class=\"task-id\">%s</span> ", html.EscapeString(task.ID)))
			sb.WriteString(fmt.Sprintf("<div class=\"progress-bar\"><div class=\"progress-fill\" style=\"width: %d%%;\"></div></div>", task.Progress))
			sb.WriteString(fmt.Sprintf(" %d%%", task.Progress))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// Blocked
	if len(brief.Blocked) > 0 {
		sb.WriteString("<div class=\"section\">\n")
		sb.WriteString(fmt.Sprintf("<h2>🚫 Blocked (%d)</h2>\n", len(brief.Blocked)))
		for _, task := range brief.Blocked {
			sb.WriteString("<div class=\"blocked\">\n")
			sb.WriteString(fmt.Sprintf("<strong class=\"task-id\">%s</strong>", html.EscapeString(task.ID)))
			if task.Error != "" {
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 100 {
					errLine = errLine[:100] + "..."
				}
				sb.WriteString(fmt.Sprintf("<br><span class=\"error\">%s</span>", html.EscapeString(errLine)))
			}
			sb.WriteString("</div>\n")
		}
		sb.WriteString("</div>\n")
	}

	// Upcoming
	sb.WriteString("<div class=\"section\">\n")
	sb.WriteString(fmt.Sprintf("<h2>📋 Upcoming (%d)</h2>\n", len(brief.Upcoming)))
	if len(brief.Upcoming) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks queued</p>\n")
	} else {
		for _, task := range brief.Upcoming {
			sb.WriteString("<div class=\"task\">\n")
			sb.WriteString(fmt.Sprintf("<span class=\"task-id\">%s</span>", html.EscapeString(task.ID)))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// Metrics
	sb.WriteString("<div class=\"metrics\">\n")
	sb.WriteString("<h2 style=\"margin-top: 0;\">📈 Metrics</h2>\n")
	sb.WriteString("<div class=\"metric\">\n")
	sb.WriteString(fmt.Sprintf("<div class=\"metric-value\">%.0f%%</div>\n", brief.Metrics.SuccessRate*100))
	sb.WriteString(fmt.Sprintf("<div class=\"metric-label\">Success rate (%d/%d)</div>\n", brief.Metrics.CompletedCount, brief.Metrics.TotalTasks))
	sb.WriteString("</div>\n")
	sb.WriteString("<div class=\"metric\">\n")
	sb.WriteString(fmt.Sprintf("<div class=\"metric-value\">%s</div>\n", formatDuration(brief.Metrics.AvgDurationMs)))
	sb.WriteString("<div class=\"metric-label\">Avg completion</div>\n")
	sb.WriteString("</div>\n")
	sb.WriteString("<div class=\"metric\">\n")
	sb.WriteString(fmt.Sprintf("<div class=\"metric-value\">%d</div>\n", brief.Metrics.PRsCreated))
	sb.WriteString("<div class=\"metric-label\">PRs created</div>\n")
	sb.WriteString("</div>\n")
	sb.WriteString("</div>\n")

	sb.WriteString(`
<p style="margin-top: 32px; padding-top: 16px; border-top: 1px solid #e5e7eb; color: #94a3b8; font-size: 0.85em;">
This brief was generated automatically by Pilot.
</p>
</body>
</html>`)

	return sb.String(), nil
}

// Subject returns the email subject line
func (f *EmailFormatter) Subject(brief *Brief) string {
	return fmt.Sprintf("Pilot Daily Brief — %s", brief.GeneratedAt.Format("Jan 2, 2006"))
}
