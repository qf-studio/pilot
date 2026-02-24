package comms

import (
	"fmt"
	"strings"
	"time"
)

// internalSignals lists patterns to strip from output before sending to users.
var internalSignals = []string{
	"EXIT_SIGNAL: true",
	"EXIT_SIGNAL:true",
	"LOOP COMPLETE",
	"TASK MODE COMPLETE",
	"NAVIGATOR_STATUS",
	"━━━━━━━━━━",
	"[EXIT_SIGNAL]",
	"[NAV_COMPLETE]",
	"[RESEARCH_DONE]",
	"[IMPL_DONE]",
}

// CleanInternalSignals removes internal Navigator/Pilot signals from output text.
func CleanInternalSignals(text string) string {
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	var clean []string
	skipBlock := false

	for _, line := range lines {
		if strings.Contains(line, "NAVIGATOR_STATUS") {
			skipBlock = true
			continue
		}
		if skipBlock {
			if strings.HasPrefix(strings.TrimSpace(line), "━") && len(clean) > 0 {
				skipBlock = false
			}
			continue
		}

		shouldSkip := false
		for _, sig := range internalSignals {
			if strings.Contains(line, sig) {
				shouldSkip = true
				break
			}
		}
		if shouldSkip {
			continue
		}

		// Skip leading blank lines
		if len(clean) == 0 && strings.TrimSpace(line) == "" {
			continue
		}

		clean = append(clean, line)
	}

	// Trim trailing blank lines
	for len(clean) > 0 && strings.TrimSpace(clean[len(clean)-1]) == "" {
		clean = clean[:len(clean)-1]
	}

	return strings.Join(clean, "\n")
}

// ChunkContent splits text into chunks of at most maxLen characters,
// preferring to break at newline boundaries.
func ChunkContent(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		breakPoint := maxLen
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			breakPoint = idx + 1
		}

		chunks = append(chunks, strings.TrimSpace(remaining[:breakPoint]))
		remaining = strings.TrimSpace(remaining[breakPoint:])
	}

	return chunks
}

// TruncateText truncates text to maxLen characters, adding "..." if truncated.
func TruncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}

// GenerateProgressBar returns a 10-segment text progress bar like "█████░░░░░".
func GenerateProgressBar(progress int) string {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	filled := progress / 10
	empty := 10 - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// FormatTimeAgo formats a time as a human-readable relative duration.
func FormatTimeAgo(t time.Time) string {
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 2")
	}
}
