package dashboard

import (
	"strings"

	"github.com/qf-studio/pilot/internal/memory"
)

// maxStageStripGlyphs caps how many stage-event glyphs are shown on a single
// execution card, so the strip stays compact regardless of how many stage
// transitions (including retries) accumulated over a long-running execution.
const maxStageStripGlyphs = 8

// maxStageStripLabelWidth caps the trailing stage-name label so a long enum
// value (e.g. "awaiting_approval") can't blow the card's fixed layout budget.
const maxStageStripLabelWidth = 20

// stageStripFailureStages are the terminal stages that render as a ✗ glyph in
// the stage strip; every other recorded stage renders as ✓.
var stageStripFailureStages = map[memory.Stage]bool{
	memory.StageFailed:   true,
	memory.StageCIFailed: true,
	memory.StageStalled:  true,
}

// buildStageStrip renders a compact per-event glyph strip (e.g. "✓✓✓✗ running")
// from an execution's execution_events timeline (GH-3849). The trailing label
// names the current stage — or, when the last event is a failure, the last
// stage successfully reached before it failed.
//
// Falls back to a single ✓/✗ glyph derived from the execution's terminal
// status when no events are recorded: executions predating the events table
// (GH-3844), or before the dispatcher/runner emit events per GH-3846.
func buildStageStrip(events []*memory.Event, executionFailed bool) string {
	if len(events) == 0 {
		if executionFailed {
			return "✗"
		}
		return "✓"
	}

	shown := events
	if len(shown) > maxStageStripGlyphs {
		shown = shown[len(shown)-maxStageStripGlyphs:]
	}

	var glyphs strings.Builder
	for _, e := range shown {
		if stageStripFailureStages[e.Stage] {
			glyphs.WriteString("✗")
		} else {
			glyphs.WriteString("✓")
		}
	}

	labelStage := events[len(events)-1].Stage
	if stageStripFailureStages[labelStage] && len(events) >= 2 {
		labelStage = events[len(events)-2].Stage
	}

	return glyphs.String() + " " + truncateString(string(labelStage), maxStageStripLabelWidth)
}
