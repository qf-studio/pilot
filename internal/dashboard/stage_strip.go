package dashboard

import (
	"fmt"

	"github.com/qf-studio/pilot/internal/memory"
)

// maxStageStripLabelWidth caps the trailing stage-name label so a long enum
// value (e.g. "awaiting_approval") can't blow the card's fixed layout budget.
const maxStageStripLabelWidth = 20

// stageLadderTotal is the canonical pipeline length the HISTORY fraction is
// measured against: spec_validated -> running -> commit -> pr_created ->
// ci_passed -> merged -> released.
const stageLadderTotal = 7

// stageStripFailureStages are the terminal stages that render with a ✗
// prefix in the HISTORY fraction; every other recorded stage renders plain.
var stageStripFailureStages = map[memory.Stage]bool{
	memory.StageFailed:   true,
	memory.StageCIFailed: true,
	memory.StageStalled:  true,
}

// stageLadderPosition maps a memory.Stage to its 1-indexed position on the
// canonical 7-rung pipeline ladder; released == stageLadderTotal. Off-ramp
// stages that still name a real rung (awaiting_approval, ci_failed) resolve
// to the last rung actually reached. Generic terminal outcomes that don't
// name a rung of their own (failed, stalled) — plus pre-ladder / non-ladder
// stages (queued, no_op, skipped) — return 0; buildStageStrip resolves those
// by walking back to the prior event that does have a ladder position.
func stageLadderPosition(s memory.Stage) int {
	switch s {
	case memory.StageSpecValidated:
		return 1
	case memory.StageRunning:
		return 2
	case memory.StageCommit:
		return 3
	case memory.StagePRCreated:
		return 4
	case memory.StageCIPassed:
		return 5
	case memory.StageMerged:
		return 6
	case memory.StageReleased:
		return 7
	case memory.StageAwaitingApproval:
		return 5 // reached ci_passed, gated on approval
	case memory.StageCIFailed:
		return 4 // reached pr_created, died at ci
	}
	return 0
}

// buildStageStrip renders a fixed-denominator pipeline-progress fraction
// (e.g. "4/7 ✗ ci_failed", "7/7 released") from an execution's
// execution_events timeline (GH-3849; revised to a fraction in TASK-383).
// `reached` is the ladder position of the last stage actually completed —
// derived via stageLadderPosition, never from len(events) — so retries never
// inflate the fraction.
//
// Falls back to "–" when no events are recorded: executions predating the
// events table (GH-3844), or before the dispatcher/runner emitted events per
// GH-3846. There is no stage evidence in that case, so a terminal success is
// never fabricated into "7/7".
func buildStageStrip(events []*memory.Event, executionFailed bool) string {
	if len(events) == 0 {
		return "–"
	}

	current := events[len(events)-1].Stage
	failed := stageStripFailureStages[current] || executionFailed

	label := current
	reached := stageLadderPosition(current)
	if reached == 0 {
		// current stage carries no ladder rung of its own — walk back to
		// the last event that does, so a generic failure/stall/retry run
		// still reports how far the pipeline actually got.
		for i := len(events) - 2; i >= 0; i-- {
			if pos := stageLadderPosition(events[i].Stage); pos > 0 {
				reached = pos
				label = events[i].Stage
				break
			}
		}
	}

	prefix := ""
	if failed {
		prefix = "✗ "
	}

	return fmt.Sprintf("%d/%d %s%s", reached, stageLadderTotal, prefix, truncateString(string(label), maxStageStripLabelWidth))
}
