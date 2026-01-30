package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Player replays execution recordings
type Player struct {
	recording *Recording
	events    []*StreamEvent
	options   *ReplayOptions
	callback  ReplayCallback
}

// ReplayCallback is called for each event during replay
type ReplayCallback func(event *StreamEvent, index int, total int) error

// NewPlayer creates a new replay player
func NewPlayer(recording *Recording, options *ReplayOptions) (*Player, error) {
	if options == nil {
		options = DefaultReplayOptions()
	}

	events, err := LoadStreamEvents(recording)
	if err != nil {
		return nil, fmt.Errorf("failed to load events: %w", err)
	}

	return &Player{
		recording: recording,
		events:    events,
		options:   options,
	}, nil
}

// OnEvent sets the replay callback
func (p *Player) OnEvent(callback ReplayCallback) {
	p.callback = callback
}

// Play replays the recording
func (p *Player) Play(ctx context.Context) error {
	total := len(p.events)
	if total == 0 {
		return nil
	}

	startIdx := p.options.StartAt
	endIdx := total
	if p.options.StopAt > 0 && p.options.StopAt < total {
		endIdx = p.options.StopAt
	}

	var prevTime time.Time
	for i := startIdx; i < endIdx; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		event := p.events[i]

		// Apply real-time delay if speed > 0
		if p.options.Speed > 0 && !prevTime.IsZero() {
			delay := event.Timestamp.Sub(prevTime)
			scaledDelay := time.Duration(float64(delay) / p.options.Speed)
			if scaledDelay > 0 {
				time.Sleep(scaledDelay)
			}
		}
		prevTime = event.Timestamp

		// Call callback
		if p.callback != nil {
			if err := p.callback(event, i, total); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetEvent returns an event by index
func (p *Player) GetEvent(index int) *StreamEvent {
	if index < 0 || index >= len(p.events) {
		return nil
	}
	return p.events[index]
}

// EventCount returns the total number of events
func (p *Player) EventCount() int {
	return len(p.events)
}

// GetRecording returns the recording metadata
func (p *Player) GetRecording() *Recording {
	return p.recording
}

// FormatEvent formats an event for display
func FormatEvent(event *StreamEvent, verbose bool) string {
	var sb strings.Builder

	// Timestamp and sequence
	ts := event.Timestamp.Format("15:04:05.000")
	sb.WriteString(fmt.Sprintf("[%s] #%d ", ts, event.Sequence))

	if event.Parsed == nil {
		sb.WriteString(fmt.Sprintf("(%s)", event.Type))
		return sb.String()
	}

	parsed := event.Parsed

	switch parsed.Type {
	case "system":
		if parsed.Subtype == "init" {
			sb.WriteString("🚀 System initialized")
		} else {
			sb.WriteString(fmt.Sprintf("⚙️  System: %s", parsed.Subtype))
		}

	case "assistant":
		if parsed.ToolName != "" {
			sb.WriteString(formatToolCall(parsed))
		} else if parsed.Text != "" {
			// Truncate long text
			text := parsed.Text
			if len(text) > 100 && !verbose {
				text = text[:97] + "..."
			}
			sb.WriteString(fmt.Sprintf("💬 %s", strings.TrimSpace(text)))
		}

	case "user":
		sb.WriteString("📥 Tool result")

	case "result":
		if parsed.IsError {
			sb.WriteString(fmt.Sprintf("❌ Error: %s", truncate(parsed.Result, 80)))
		} else {
			sb.WriteString("✅ Completed")
			if parsed.InputTokens > 0 || parsed.OutputTokens > 0 {
				sb.WriteString(fmt.Sprintf(" (tokens: %d in, %d out)", parsed.InputTokens, parsed.OutputTokens))
			}
		}

	default:
		sb.WriteString(fmt.Sprintf("(%s)", parsed.Type))
	}

	return sb.String()
}

// formatToolCall formats a tool call for display
func formatToolCall(parsed *ParsedEvent) string {
	tool := parsed.ToolName
	var detail string

	switch tool {
	case "Read":
		if fp, ok := parsed.ToolInput["file_path"].(string); ok {
			detail = shortenPath(fp)
		}
	case "Write":
		if fp, ok := parsed.ToolInput["file_path"].(string); ok {
			detail = shortenPath(fp)
		}
	case "Edit":
		if fp, ok := parsed.ToolInput["file_path"].(string); ok {
			detail = shortenPath(fp)
		}
	case "Bash":
		if cmd, ok := parsed.ToolInput["command"].(string); ok {
			detail = truncate(cmd, 50)
		}
	case "Glob":
		if pattern, ok := parsed.ToolInput["pattern"].(string); ok {
			detail = pattern
		}
	case "Grep":
		if pattern, ok := parsed.ToolInput["pattern"].(string); ok {
			detail = truncate(pattern, 30)
		}
	case "Task":
		if desc, ok := parsed.ToolInput["description"].(string); ok {
			detail = truncate(desc, 40)
		}
	case "Skill":
		if skill, ok := parsed.ToolInput["skill"].(string); ok {
			detail = skill
		}
	}

	icon := getToolIcon(tool)
	if detail != "" {
		return fmt.Sprintf("%s %s: %s", icon, tool, detail)
	}
	return fmt.Sprintf("%s %s", icon, tool)
}

// getToolIcon returns an emoji for the tool
func getToolIcon(tool string) string {
	switch tool {
	case "Read":
		return "📖"
	case "Write":
		return "✏️"
	case "Edit":
		return "📝"
	case "Bash":
		return "💻"
	case "Glob":
		return "🔍"
	case "Grep":
		return "🔎"
	case "Task":
		return "🤖"
	case "Skill":
		return "⚡"
	case "WebFetch":
		return "🌐"
	case "WebSearch":
		return "🔍"
	default:
		return "🔧"
	}
}

// shortenPath shortens a file path for display
func shortenPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 3 {
		return path
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}

// truncate truncates a string with ellipsis
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// Analyzer analyzes a recording and generates a report
type Analyzer struct {
	recording *Recording
	events    []*StreamEvent
}

// NewAnalyzer creates a new recording analyzer
func NewAnalyzer(recording *Recording) (*Analyzer, error) {
	events, err := LoadStreamEvents(recording)
	if err != nil {
		return nil, fmt.Errorf("failed to load events: %w", err)
	}

	return &Analyzer{
		recording: recording,
		events:    events,
	}, nil
}

// Analyze generates an analysis report
func (a *Analyzer) Analyze() (*AnalysisReport, error) {
	report := &AnalysisReport{
		Recording: a.recording,
		TokenBreakdown: TokenBreakdown{
			ByPhase: make(map[string]TokenUsage),
			ByTool:  make(map[string]TokenUsage),
		},
		PhaseAnalysis:  make([]PhaseAnalysis, 0),
		ToolUsage:      make([]ToolUsageStats, 0),
		Errors:         make([]ErrorEvent, 0),
		DecisionPoints: make([]DecisionPoint, 0),
	}

	// Track tool usage
	toolStats := make(map[string]*ToolUsageStats)
	currentPhase := "Init"
	phaseStartTime := a.recording.StartTime
	phaseEvents := 0
	phaseTools := make(map[string]bool)

	for _, event := range a.events {
		if event.Parsed == nil {
			continue
		}

		parsed := event.Parsed

		// Track errors
		if parsed.IsError {
			report.Errors = append(report.Errors, ErrorEvent{
				Timestamp: event.Timestamp,
				Phase:     currentPhase,
				Tool:      parsed.ToolName,
				Message:   truncate(parsed.Result, 200),
				Sequence:  event.Sequence,
			})
		}

		// Track tool usage
		if parsed.ToolName != "" {
			if _, exists := toolStats[parsed.ToolName]; !exists {
				toolStats[parsed.ToolName] = &ToolUsageStats{
					Tool: parsed.ToolName,
				}
			}
			stats := toolStats[parsed.ToolName]
			stats.Count++
			stats.InputTokens += parsed.InputTokens
			stats.OutputTokens += parsed.OutputTokens
			if parsed.IsError {
				stats.ErrorCount++
			}
			phaseTools[parsed.ToolName] = true
		}

		// Track token usage by phase
		if parsed.InputTokens > 0 || parsed.OutputTokens > 0 {
			usage := report.TokenBreakdown.ByPhase[currentPhase]
			usage.InputTokens += parsed.InputTokens
			usage.OutputTokens += parsed.OutputTokens
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
			report.TokenBreakdown.ByPhase[currentPhase] = usage
		}

		// Detect phase changes from text
		if parsed.Text != "" {
			newPhase := detectPhaseFromText(parsed.Text)
			if newPhase != "" && newPhase != currentPhase {
				// Record previous phase
				if phaseEvents > 0 {
					tools := make([]string, 0, len(phaseTools))
					for t := range phaseTools {
						tools = append(tools, t)
					}
					duration := event.Timestamp.Sub(phaseStartTime)
					report.PhaseAnalysis = append(report.PhaseAnalysis, PhaseAnalysis{
						Phase:      currentPhase,
						Duration:   duration,
						Percentage: float64(duration) / float64(a.recording.Duration) * 100,
						EventCount: phaseEvents,
						ToolsUsed:  tools,
					})
				}

				// Start new phase
				currentPhase = newPhase
				phaseStartTime = event.Timestamp
				phaseEvents = 0
				phaseTools = make(map[string]bool)
			}

			// Detect decision points (Navigator patterns)
			if strings.Contains(parsed.Text, "WORKFLOW CHECK") ||
				strings.Contains(parsed.Text, "Decision:") ||
				strings.Contains(parsed.Text, "Approach:") {
				report.DecisionPoints = append(report.DecisionPoints, DecisionPoint{
					Timestamp:   event.Timestamp,
					Sequence:    event.Sequence,
					Description: truncate(parsed.Text, 100),
				})
			}
		}

		phaseEvents++
	}

	// Record final phase
	if phaseEvents > 0 {
		tools := make([]string, 0, len(phaseTools))
		for t := range phaseTools {
			tools = append(tools, t)
		}
		duration := a.recording.EndTime.Sub(phaseStartTime)
		report.PhaseAnalysis = append(report.PhaseAnalysis, PhaseAnalysis{
			Phase:      currentPhase,
			Duration:   duration,
			Percentage: float64(duration) / float64(a.recording.Duration) * 100,
			EventCount: phaseEvents,
			ToolsUsed:  tools,
		})
	}

	// Convert tool stats to slice
	for _, stats := range toolStats {
		report.ToolUsage = append(report.ToolUsage, *stats)
	}

	// Token breakdown by tool
	for _, stats := range toolStats {
		report.TokenBreakdown.ByTool[stats.Tool] = TokenUsage{
			InputTokens:  stats.InputTokens,
			OutputTokens: stats.OutputTokens,
			TotalTokens:  stats.InputTokens + stats.OutputTokens,
		}
	}

	return report, nil
}

// detectPhaseFromText extracts phase from Navigator text patterns
func detectPhaseFromText(text string) string {
	textLower := strings.ToLower(text)

	if strings.Contains(textLower, "phase:") {
		if strings.Contains(textLower, "research") {
			return "Research"
		}
		if strings.Contains(textLower, "impl") {
			return "Implementing"
		}
		if strings.Contains(textLower, "verify") {
			return "Verifying"
		}
		if strings.Contains(textLower, "complete") {
			return "Completing"
		}
		if strings.Contains(textLower, "init") {
			return "Init"
		}
	}

	if strings.Contains(text, "LOOP MODE ACTIVATED") ||
		strings.Contains(text, "TASK MODE ACTIVATED") {
		return "Init"
	}

	return ""
}

// FormatReport formats an analysis report for terminal display
func FormatReport(report *AnalysisReport) string {
	var sb strings.Builder

	rec := report.Recording

	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	sb.WriteString("EXECUTION ANALYSIS REPORT\n")
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Overview
	sb.WriteString(fmt.Sprintf("Recording:  %s\n", rec.ID))
	sb.WriteString(fmt.Sprintf("Task:       %s\n", rec.TaskID))
	sb.WriteString(fmt.Sprintf("Status:     %s\n", rec.Status))
	sb.WriteString(fmt.Sprintf("Duration:   %s\n", rec.Duration.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("Events:     %d\n", rec.EventCount))
	sb.WriteString("\n")

	// Token Usage
	if rec.TokenUsage != nil {
		sb.WriteString("TOKEN USAGE\n")
		sb.WriteString("───────────────────────────────────────\n")
		sb.WriteString(fmt.Sprintf("  Input:    %d tokens\n", rec.TokenUsage.InputTokens))
		sb.WriteString(fmt.Sprintf("  Output:   %d tokens\n", rec.TokenUsage.OutputTokens))
		sb.WriteString(fmt.Sprintf("  Total:    %d tokens\n", rec.TokenUsage.TotalTokens))
		sb.WriteString(fmt.Sprintf("  Cost:     $%.4f\n", rec.TokenUsage.EstimatedCostUSD))
		sb.WriteString("\n")
	}

	// Phase Analysis
	if len(report.PhaseAnalysis) > 0 {
		sb.WriteString("PHASE ANALYSIS\n")
		sb.WriteString("───────────────────────────────────────\n")
		for _, phase := range report.PhaseAnalysis {
			sb.WriteString(fmt.Sprintf("  %-12s %8s (%5.1f%%) %d events\n",
				phase.Phase+":",
				phase.Duration.Round(time.Second),
				phase.Percentage,
				phase.EventCount,
			))
		}
		sb.WriteString("\n")
	}

	// Tool Usage
	if len(report.ToolUsage) > 0 {
		sb.WriteString("TOOL USAGE\n")
		sb.WriteString("───────────────────────────────────────\n")
		for _, tool := range report.ToolUsage {
			errStr := ""
			if tool.ErrorCount > 0 {
				errStr = fmt.Sprintf(" (%d errors)", tool.ErrorCount)
			}
			sb.WriteString(fmt.Sprintf("  %-12s %4d calls%s\n", tool.Tool+":", tool.Count, errStr))
		}
		sb.WriteString("\n")
	}

	// Errors
	if len(report.Errors) > 0 {
		sb.WriteString("ERRORS\n")
		sb.WriteString("───────────────────────────────────────\n")
		for _, err := range report.Errors {
			sb.WriteString(fmt.Sprintf("  #%d [%s] %s: %s\n",
				err.Sequence,
				err.Timestamp.Format("15:04:05"),
				err.Tool,
				err.Message,
			))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")

	return sb.String()
}

// ExportToHTML exports a recording to HTML format
func ExportToHTML(recording *Recording, events []*StreamEvent) (string, error) {
	var sb strings.Builder

	sb.WriteString("<!DOCTYPE html>\n<html>\n<head>\n")
	sb.WriteString("<meta charset=\"UTF-8\">\n")
	sb.WriteString(fmt.Sprintf("<title>Execution Recording: %s</title>\n", recording.ID))
	sb.WriteString("<style>\n")
	sb.WriteString(`
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; margin: 40px; background: #1a1a2e; color: #eee; }
.header { background: #16213e; padding: 20px; border-radius: 8px; margin-bottom: 20px; }
.header h1 { margin: 0 0 10px 0; color: #0f4c75; }
.meta { display: flex; gap: 20px; flex-wrap: wrap; }
.meta-item { background: #0f3460; padding: 8px 16px; border-radius: 4px; }
.meta-label { color: #888; font-size: 12px; }
.meta-value { font-weight: bold; }
.event { padding: 12px 16px; border-left: 3px solid #333; margin: 8px 0; background: #16213e; border-radius: 0 4px 4px 0; }
.event:hover { background: #1a1a3e; }
.event-tool { border-left-color: #4a9eff; }
.event-text { border-left-color: #50c878; }
.event-result { border-left-color: #ffd700; }
.event-error { border-left-color: #ff4444; background: #2a1a1a; }
.timestamp { color: #666; font-size: 12px; margin-right: 10px; }
.sequence { color: #888; font-size: 11px; }
.tool-name { color: #4a9eff; font-weight: bold; }
.tool-detail { color: #aaa; margin-left: 8px; }
pre { background: #0a0a1a; padding: 12px; border-radius: 4px; overflow-x: auto; font-size: 13px; }
.section { margin: 24px 0; }
.section h2 { color: #0f4c75; border-bottom: 1px solid #333; padding-bottom: 8px; }
`)
	sb.WriteString("</style>\n</head>\n<body>\n")

	// Header
	sb.WriteString("<div class=\"header\">\n")
	sb.WriteString(fmt.Sprintf("<h1>📹 Recording: %s</h1>\n", recording.ID))
	sb.WriteString("<div class=\"meta\">\n")
	sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Task</div><div class=\"meta-value\">%s</div></div>\n", recording.TaskID))
	sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Status</div><div class=\"meta-value\">%s</div></div>\n", recording.Status))
	sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Duration</div><div class=\"meta-value\">%s</div></div>\n", recording.Duration.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Events</div><div class=\"meta-value\">%d</div></div>\n", recording.EventCount))
	if recording.TokenUsage != nil {
		sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Tokens</div><div class=\"meta-value\">%d</div></div>\n", recording.TokenUsage.TotalTokens))
		sb.WriteString(fmt.Sprintf("<div class=\"meta-item\"><div class=\"meta-label\">Cost</div><div class=\"meta-value\">$%.4f</div></div>\n", recording.TokenUsage.EstimatedCostUSD))
	}
	sb.WriteString("</div>\n</div>\n")

	// Events
	sb.WriteString("<div class=\"section\">\n<h2>Execution Events</h2>\n")
	for _, event := range events {
		class := "event"
		if event.Parsed != nil {
			if event.Parsed.IsError {
				class += " event-error"
			} else if event.Parsed.ToolName != "" {
				class += " event-tool"
			} else if event.Parsed.Text != "" {
				class += " event-text"
			} else if event.Parsed.Type == "result" {
				class += " event-result"
			}
		}

		sb.WriteString(fmt.Sprintf("<div class=\"%s\">\n", class))
		sb.WriteString(fmt.Sprintf("<span class=\"timestamp\">%s</span>", event.Timestamp.Format("15:04:05.000")))
		sb.WriteString(fmt.Sprintf("<span class=\"sequence\">#%d</span>\n", event.Sequence))

		if event.Parsed != nil {
			parsed := event.Parsed
			if parsed.ToolName != "" {
				sb.WriteString(fmt.Sprintf("<span class=\"tool-name\">%s</span>", parsed.ToolName))
				if detail := formatToolDetail(parsed); detail != "" {
					sb.WriteString(fmt.Sprintf("<span class=\"tool-detail\">%s</span>", escapeHTML(detail)))
				}
			} else if parsed.Text != "" {
				text := parsed.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				sb.WriteString(fmt.Sprintf("<div>%s</div>", escapeHTML(text)))
			} else if parsed.Type == "result" {
				if parsed.IsError {
					sb.WriteString(fmt.Sprintf("<div>❌ %s</div>", escapeHTML(truncate(parsed.Result, 200))))
				} else {
					sb.WriteString("<div>✅ Completed</div>")
				}
			}
		}

		sb.WriteString("</div>\n")
	}
	sb.WriteString("</div>\n")

	sb.WriteString("</body>\n</html>")

	return sb.String(), nil
}

// formatToolDetail formats tool input for display
func formatToolDetail(parsed *ParsedEvent) string {
	switch parsed.ToolName {
	case "Read", "Write", "Edit":
		if fp, ok := parsed.ToolInput["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := parsed.ToolInput["command"].(string); ok {
			return truncate(cmd, 80)
		}
	case "Glob":
		if pattern, ok := parsed.ToolInput["pattern"].(string); ok {
			return pattern
		}
	case "Grep":
		if pattern, ok := parsed.ToolInput["pattern"].(string); ok {
			return truncate(pattern, 40)
		}
	}
	return ""
}

// escapeHTML escapes HTML special characters
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// ExportToJSON exports a recording to JSON format
func ExportToJSON(recording *Recording, events []*StreamEvent) ([]byte, error) {
	export := struct {
		Recording *Recording     `json:"recording"`
		Events    []*StreamEvent `json:"events"`
	}{
		Recording: recording,
		Events:    events,
	}
	return json.MarshalIndent(export, "", "  ")
}
