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
`)
	fmt.Fprintf(&sb, "<meta name=\"pilot-brief-kind\" content=\"%s\">\n", kindOrDaily(brief.Kind))
	sb.WriteString(`<style>
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
	fmt.Fprintf(&sb, "<h1>📊 %s</h1>\n", briefTitle(brief.Kind))
	fmt.Fprintf(&sb, "<p style=\"color: #64748b;\">%s</p>\n", brief.GeneratedAt.Format("Monday, January 2, 2006"))
	writeWeeklyProofHTML(&sb, brief.Weekly)

	// Completed
	sb.WriteString("<div class=\"section\">\n")
	fmt.Fprintf(&sb, "<h2>✅ Completed (%d)</h2>\n", len(brief.Completed))
	if len(brief.Completed) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks completed</p>\n")
	} else {
		for _, task := range brief.Completed {
			sb.WriteString("<div class=\"task\">\n")
			fmt.Fprintf(&sb, "<span class=\"task-id\">%s</span>", html.EscapeString(task.ID))
			if task.PRUrl != "" {
				fmt.Fprintf(&sb, " — <a class=\"pr-link\" href=\"%s\">PR ready for review</a>", html.EscapeString(task.PRUrl))
			}
			fmt.Fprintf(&sb, " <span style=\"color: #64748b;\">(%s)</span>", formatDuration(task.DurationMs))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// In Progress
	sb.WriteString("<div class=\"section\">\n")
	fmt.Fprintf(&sb, "<h2>🔄 In Progress (%d)</h2>\n", len(brief.InProgress))
	if len(brief.InProgress) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks in progress</p>\n")
	} else {
		for _, task := range brief.InProgress {
			sb.WriteString("<div class=\"task\">\n")
			fmt.Fprintf(&sb, "<span class=\"task-id\">%s</span> ", html.EscapeString(task.ID))
			fmt.Fprintf(&sb, "<div class=\"progress-bar\"><div class=\"progress-fill\" style=\"width: %d%%;\"></div></div>", task.Progress)
			fmt.Fprintf(&sb, " %d%%", task.Progress)
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// Blocked
	if len(brief.Blocked) > 0 {
		sb.WriteString("<div class=\"section\">\n")
		fmt.Fprintf(&sb, "<h2>🚫 Blocked (%d)</h2>\n", len(brief.Blocked))
		for _, task := range brief.Blocked {
			sb.WriteString("<div class=\"blocked\">\n")
			fmt.Fprintf(&sb, "<strong class=\"task-id\">%s</strong>", html.EscapeString(task.ID))
			if task.Error != "" {
				errLine := strings.Split(task.Error, "\n")[0]
				if len(errLine) > 100 {
					errLine = errLine[:100] + "..."
				}
				fmt.Fprintf(&sb, "<br><span class=\"error\">%s</span>", html.EscapeString(errLine))
			}
			sb.WriteString("</div>\n")
		}
		sb.WriteString("</div>\n")
	}

	// Upcoming
	sb.WriteString("<div class=\"section\">\n")
	fmt.Fprintf(&sb, "<h2>📋 Upcoming (%d)</h2>\n", len(brief.Upcoming))
	if len(brief.Upcoming) == 0 {
		sb.WriteString("<p class=\"empty\">No tasks queued</p>\n")
	} else {
		for _, task := range brief.Upcoming {
			sb.WriteString("<div class=\"task\">\n")
			fmt.Fprintf(&sb, "<span class=\"task-id\">%s</span>", html.EscapeString(task.ID))
			sb.WriteString("</div>\n")
		}
	}
	sb.WriteString("</div>\n")

	// Metrics
	sb.WriteString("<div class=\"metrics\">\n")
	sb.WriteString("<h2 style=\"margin-top: 0;\">📈 Metrics</h2>\n")
	sb.WriteString("<div class=\"metric\">\n")
	fmt.Fprintf(&sb, "<div class=\"metric-value\">%.0f%%</div>\n", brief.Metrics.SuccessRate*100)
	fmt.Fprintf(&sb, "<div class=\"metric-label\">Success rate (%d/%d)</div>\n", brief.Metrics.CompletedCount, brief.Metrics.TotalTasks)
	sb.WriteString("</div>\n")
	sb.WriteString("<div class=\"metric\">\n")
	fmt.Fprintf(&sb, "<div class=\"metric-value\">%s</div>\n", formatDuration(brief.Metrics.AvgDurationMs))
	sb.WriteString("<div class=\"metric-label\">Avg completion</div>\n")
	sb.WriteString("</div>\n")
	sb.WriteString("<div class=\"metric\">\n")
	fmt.Fprintf(&sb, "<div class=\"metric-value\">%d</div>\n", brief.Metrics.PRsCreated)
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
	return fmt.Sprintf("%s — %s", briefTitle(brief.Kind), brief.GeneratedAt.Format("Jan 2, 2006"))
}

// FormatStructured renders a StructuredBrief as HTML email with the same
// four sections as the console payload, in the same order: Shipped, Review
// needed, Pool, Failed (GH-5791). Cost appears only on shipped runs that
// carry one (own-key executions).
func (f *EmailFormatter) FormatStructured(b *StructuredBrief) (string, error) {
	var sb strings.Builder

	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
`)
	fmt.Fprintf(&sb, "<meta name=\"pilot-brief-kind\" content=\"%s\">\n", kindOrDaily(b.Kind))
	sb.WriteString(`</head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; line-height: 1.6; color: #333; max-width: 600px; margin: 0 auto; padding: 20px;">
`)
	fmt.Fprintf(&sb, "<h1>%s</h1>\n", briefTitle(b.Kind))
	fmt.Fprintf(&sb, "<p style=\"color: #64748b;\">%s</p>\n", b.GeneratedAt.Format("Monday, January 2, 2006"))
	writeWeeklyProofHTML(&sb, b.Weekly)

	// Shipped
	fmt.Fprintf(&sb, "<h2>Shipped (%d)</h2>\n", len(b.Shipped))
	if len(b.Shipped) == 0 {
		sb.WriteString("<p style=\"color: #94a3b8;\">Nothing shipped</p>\n")
	}
	for _, r := range b.Shipped {
		fmt.Fprintf(&sb, "<p><strong>%s</strong> %s", html.EscapeString(r.IssueRef), html.EscapeString(r.Title))
		if r.PRURL != "" {
			fmt.Fprintf(&sb, " — <a href=\"%s\">PR</a>", html.EscapeString(r.PRURL))
		}
		fmt.Fprintf(&sb, "<br>%s · +%d −%d", formatDuration(r.DurationMs), r.LinesAdded, r.LinesRemoved)
		if r.CostUSD != nil {
			fmt.Fprintf(&sb, " · $%.2f", *r.CostUSD)
		}
		sb.WriteString("</p>\n")
	}

	// Review needed
	fmt.Fprintf(&sb, "<h2>Review needed (%d)</h2>\n", len(b.ReviewNeeded))
	if len(b.ReviewNeeded) == 0 {
		sb.WriteString("<p style=\"color: #94a3b8;\">Nothing waiting on you</p>\n")
	}
	for _, r := range b.ReviewNeeded {
		fmt.Fprintf(&sb, "<p><strong>%s</strong> %s", html.EscapeString(r.IssueRef), html.EscapeString(r.Title))
		if r.PRURL != "" {
			fmt.Fprintf(&sb, " — <a href=\"%s\">PR</a>", html.EscapeString(r.PRURL))
		}
		if r.Overdue {
			sb.WriteString(" <em>(waiting over 24h)</em>")
		}
		fmt.Fprintf(&sb, "<br>%s</p>\n", html.EscapeString(r.Reason))
	}

	// Pool
	sb.WriteString("<h2>Pool</h2>\n")
	if b.Pool == nil {
		sb.WriteString("<p style=\"color: #94a3b8;\">Allowance usage unavailable</p>\n")
	} else {
		fmt.Fprintf(&sb, "<p>%.0f%% of allowance used</p>\n", b.Pool.PercentUsed)
	}

	// Failed
	fmt.Fprintf(&sb, "<h2>Failed (%d)</h2>\n", len(b.Failed))
	if len(b.Failed) == 0 {
		sb.WriteString("<p style=\"color: #94a3b8;\">No failures</p>\n")
	}
	for _, r := range b.Failed {
		retry := "not retryable"
		if r.Retryable {
			retry = "retryable"
		}
		fmt.Fprintf(&sb, "<p><strong>%s</strong> — %s (%s)</p>\n",
			html.EscapeString(r.IssueRef), html.EscapeString(r.FailureClass), retry)
	}

	sb.WriteString("<p style=\"margin-top: 32px; color: #94a3b8; font-size: 0.85em;\">This brief was generated automatically by Pilot.</p>\n</body>\n</html>")
	return sb.String(), nil
}

// SubjectStructured returns the email subject line for a structured brief.
func (f *EmailFormatter) SubjectStructured(b *StructuredBrief) string {
	return fmt.Sprintf("%s — %s", briefTitle(b.Kind), b.GeneratedAt.Format("Jan 2, 2006"))
}

// writeWeeklyProofHTML renders the weekly proof numbers and the biggest
// shipped runs. It writes nothing for a daily brief (w == nil).
func writeWeeklyProofHTML(sb *strings.Builder, w *WeeklyProof) {
	if w == nil {
		return
	}
	fmt.Fprintf(sb, "<h2>Last 7 days</h2>\n<p><strong>%d</strong> tickets shipped · <strong>%.0f</strong> min median time to PR · <strong>%d</strong> PRs merged</p>\n",
		w.TicketsShipped, w.MedianTimeToPRMinutes, w.MergedCount)
	if len(w.TopShipped) == 0 {
		return
	}
	sb.WriteString("<h3>Biggest shipped</h3>\n")
	for _, r := range w.TopShipped {
		fmt.Fprintf(sb, "<p><strong>%s</strong> %s", html.EscapeString(r.IssueRef), html.EscapeString(r.Title))
		if r.PRURL != "" {
			fmt.Fprintf(sb, " — <a href=\"%s\">PR</a>", html.EscapeString(r.PRURL))
		}
		fmt.Fprintf(sb, "<br>+%d −%d</p>\n", r.LinesAdded, r.LinesRemoved)
	}
}
