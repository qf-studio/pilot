package executor

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/qf-studio/pilot/internal/memory"
)

const (
	// consoleLearningsLimit is how many of a project's newest console-review
	// learnings are considered for the prompt (GH-6011).
	consoleLearningsLimit = 10
	// consoleLearningsCharCap bounds the learnings text in the prompt block.
	// Entries that would exceed it are dropped whole, newest kept first; a
	// single oversized newest entry is truncated instead of dropped.
	consoleLearningsCharCap = 2048

	consoleLearningsHeader = "## Reviewer feedback from the console (rejected work — heed these)\n\n"
	consoleLearningsFooter = "\nDo not repeat what was rejected.\n"
)

// consoleLearningsProjectID is the memories.project_id key for a project path:
// the same filepath.Base convention the Relevant Knowledge lookup uses, and the
// key the gateway writes learnings under. "" when there is no path.
func consoleLearningsProjectID(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	return filepath.Base(projectPath)
}

// buildConsoleLearningsBlock renders the newest console-review learnings for
// the project at projectPath, or "" when there is no knowledge store, no
// project, nothing stored, or the lookup fails (fail-open, like graph recall).
// Shared by the task prompt and the chat first-turn prompt, so the next chat
// turn and the next task for a project see the same reject reasons (GH-6011).
func (r *Runner) buildConsoleLearningsBlock(projectPath string) string {
	if r.knowledge == nil {
		return ""
	}
	projectID := consoleLearningsProjectID(projectPath)
	if projectID == "" {
		return ""
	}
	learnings, err := r.knowledge.QueryBySource(memory.MemoryTypeLearning, memory.SourceConsoleReview, projectID, consoleLearningsLimit)
	if err != nil {
		slog.Warn("Failed to load console learnings", slog.String("project", projectID), slog.Any("error", err))
		return ""
	}
	return renderConsoleLearnings(learnings)
}

// renderConsoleLearnings renders one bullet per learning: the stored subject
// header line, then the reason flattened to a single line. Total learnings text
// stays within consoleLearningsCharCap bytes.
func renderConsoleLearnings(learnings []*memory.Memory) string {
	var list strings.Builder
	for _, m := range learnings {
		header, reason, _ := strings.Cut(m.Content, "\n")
		reason = strings.Join(strings.Fields(reason), " ")
		entry := fmt.Sprintf("- %s: %s\n", strings.TrimSpace(header), reason)
		if list.Len()+len(entry) > consoleLearningsCharCap {
			if list.Len() > 0 {
				break
			}
			entry = truncateUTF8(entry, consoleLearningsCharCap-len("…\n")) + "…\n"
		}
		list.WriteString(entry)
	}
	if list.Len() == 0 {
		return ""
	}
	return consoleLearningsHeader + list.String() + consoleLearningsFooter
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
