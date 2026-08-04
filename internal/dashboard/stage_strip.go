package dashboard

import (
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

// StageInfo is the structured pipeline-progress summary for a history row:
// the highest ladder rung reached, the stage that defined that rung, and
// whether the run terminally failed. Known is false when no events were
// recorded (executions predating the events table, GH-3844, or before the
// dispatcher/runner emitted events per GH-3846) — there is no stage evidence
// in that case, so a terminal success is never fabricated into a full ladder.
type StageInfo struct {
	Reached int    // highest ladder rung observed (0..stageLadderTotal)
	Label   string // stage name that defined the rung (e.g. "ci_failed")
	Failed  bool   // rung-defining stage was terminal-failure, or execution failed
	Known   bool   // false when no events were recorded
	Muted   bool   // terminal non-ladder outcome (skipped/no_op/…): dim meter
}

// displayStatus maps an executions.status value to the history-row status
// vocabulary. "completed" renders as success; every other value (failed,
// skipped, no_op, declined, rate_limited, infra, stalled, running, pending)
// passes through so statusIconStyle picks its own glyph. Collapsing every
// non-failed status to success is how a skipped run — and a still-running
// one — rendered ✓ in HISTORY (GH-3927 / GH-4064).
func displayStatus(execStatus string) string {
	if execStatus == "completed" {
		return "success"
	}
	return execStatus
}

// mutedOutcomes are terminal statuses that never name a ladder rung: the row
// label shows the outcome itself (a skipped run must not read "running") and
// the meter renders muted. stalled is excluded — it keeps the rung label to
// show where the run died, like failed.
//
// declined-preflight (GH-4368): the intent judge declines a task before any
// work starts, so the row has zero execution_events — with no override it
// rendered an unknown-glyph meter and a blank "–" label (indistinguishable
// from a truly unaccounted-for row) instead of a muted, labeled one.
//
// cancelled (TASK-420/GH-4537): unlike failed/stalled, a cancellation is a
// pure executions.status write with no execution_events row of its own (see
// memory.terminalExecutionStatuses' dead-API note — it's written by operator
// surgery / claim-loss cleanup, not the normal event-emitting path). Without
// this entry buildStageInfo has no terminal signal to reduce over, so the
// label freezes at whatever rung the row last reached (typically "running")
// and HISTORY renders a task cancelled hours ago as still running — the
// GH-4531 ghost row captured 2026-07-24 22:17:07Z that motivated this task.
//
// canceled (GH-4678): the live single-L cancel status written by
// `pilot task cancel` / executor.ExecutionLifecycle.Cancel. It DOES journal
// a StageCanceled execution_events row, unlike the dead double-L value above
// — but it's listed here anyway so the label always reads the deliberate
// outcome even for a row cancelled before any event exists (e.g. a queued
// row that never reached "running").
//
// superseded (GH-4701): an operator's deliberate cleanup, not a pipeline
// failure — either a sibling/duplicate execution already delivered the same
// scope (GH-4656's dispatcher/PR-preflight guards write this status before
// any work starts or a PR opens), or a human closed the issue outright as
// not-planned (notifyExternalClose's supersededClose classification).
// Without this entry the row freezes at whatever ladder rung it last
// reached and renders `✗ pr_created` / `✗ ci_passed` — indistinguishable
// from a genuine failure (the 2026-08-03 #4655 cluster: #4660-#4665 closed
// en masse and re-filed as #4677 rendered as 6 of 43 rows misread this way).
var mutedOutcomes = map[string]bool{
	"skipped":            true,
	"no_op":              true,
	"declined":           true,
	"rate_limited":       true,
	"infra":              true,
	"declined-preflight": true,
	"cancelled":          true,
	"canceled":           true,
	"superseded":         true,
}

// stageStripCleanTerminalEvents are execution_events that close out a run
// successfully without ever naming a PR-side ladder rung of their own
// (pr_created/ci_passed/merged). A decompose-only epic parent ships no PR
// for its own row — its stream ends running -> spec_validated -> decomposed
// -> completed — so the running-max reducer in buildStageInfo freezes the
// label at the last *named* rung it saw, which is "running" (GH-4298).
var stageStripCleanTerminalEvents = map[memory.Stage]bool{
	memory.StageCompleted:  true,
	memory.StageNoOp:       true,
	memory.StageDecomposed: true,
}

// stageInfoForExecution derives the history-row StageInfo from the event
// timeline plus the authoritative executions.status. The label override is
// status-driven, never event-driven, so GH-4023's stray-late-event guard in
// buildStageInfo is untouched.
//
// A muted outcome forces Known=true even when the row has zero events (e.g.
// declined-preflight, GH-4368): unlike a genuinely unaccounted-for row (no
// stage evidence at all — predates the events table, or the run never got
// far enough to log one), a muted outcome's status IS the evidence, so it
// gets a labeled, dim meter instead of the blank "unknown" one.
func stageInfoForExecution(events []*memory.Event, status string) StageInfo {
	info := buildStageInfo(events, status == "failed" || status == "stalled")
	if mutedOutcomes[status] {
		info.Label = truncateString(status, maxStageStripLabelWidth)
		info.Muted = true
		info.Known = true
	}
	return info
}

// HistoryStatus is the icon-status + stage-meter pair every HISTORY call site
// renders together for one execution row.
type HistoryStatus struct {
	Status string    // icon/vocabulary value — see statusIconStyle
	Stage  StageInfo // stage-meter fraction + label for the same row
}

// resolveHistoryStatus is the single resolver every HISTORY-populating call
// site (tui.go's hydrateFromStore/storeRefreshCmd, zoom.go's historyZoomCmd)
// must go through for a memory.Execution row (TASK-420/GH-4537). Before this,
// each of the three call sites hand-chained `status := displayStatus(...)`
// into `stageInfoForExecution(events, status)` itself — correct by
// coincidence (the two happened to agree at every call site) but not
// enforced, which is exactly the "two independent strings" shape GH-4368's
// icon/text split came from. Routing every call site through one function
// makes that divergence structurally impossible instead of merely absent
// today.
func resolveHistoryStatus(execStatus string, events []*memory.Event) HistoryStatus {
	status := displayStatus(execStatus)
	return HistoryStatus{Status: status, Stage: stageInfoForExecution(events, status)}
}

// buildStageInfo reduces an execution's execution_events timeline to a
// StageInfo (GH-3849; revised to a fraction in TASK-383; revised to a
// running-max reducer in GH-4023; revised to structured output for the grom
// segment-meter rendering). Reached is the highest ladder position observed
// across the whole stream — derived via stageLadderPosition, never from
// len(events) — so retries never inflate the rung and a later regression
// (e.g. a stray pr_created/failed event recorded after released, GH-4023)
// never pulls the displayed rung backward. The Failed flag is attached to
// whichever event defined that max rung, not to the last event in the stream.
func buildStageInfo(events []*memory.Event, executionFailed bool) StageInfo {
	if len(events) == 0 {
		return StageInfo{}
	}

	var (
		reached       = -1
		label         memory.Stage
		failed        bool
		lastGoodPos   int
		lastGoodStage memory.Stage
	)

	for _, e := range events {
		// Resolve this event's own ladder position, walking back to the
		// last stage that named a real rung when this one doesn't (a
		// generic failure/stall/retry carries no rung of its own).
		pos := stageLadderPosition(e.Stage)
		resolvedPos := pos
		resolvedLabel := e.Stage
		if pos > 0 {
			lastGoodPos = pos
			lastGoodStage = e.Stage
		} else if lastGoodPos > 0 {
			resolvedPos = lastGoodPos
			resolvedLabel = lastGoodStage
		}

		// Only advance (or hold at) the running max — a resolved position
		// behind the current max is a regression and must not overwrite it.
		if resolvedPos >= reached {
			reached = resolvedPos
			label = resolvedLabel
			failed = stageStripFailureStages[e.Stage]
		}
	}

	// GH-4298: the stream's own terminal event, not just its ladder rung,
	// decides whether the run is done. A clean terminal (completed/no_op/
	// decomposed) as the LAST event means the run finished without ever
	// emitting a PR-side event of its own — promote it to the top of the
	// ladder instead of leaving it frozen at whatever rung it last named.
	// Failed executions are excluded so the "died at a rung" ✗ labeling
	// (GH-4212) is untouched, and a run that already reached pr_created or
	// higher keeps that (more specific) label.
	if !executionFailed && !failed && stageStripCleanTerminalEvents[events[len(events)-1].Stage] && reached < stageLadderPosition(memory.StagePRCreated) {
		reached = stageLadderTotal
		label = memory.StageCompleted
	}

	return StageInfo{
		Reached: reached,
		Label:   truncateString(string(label), maxStageStripLabelWidth),
		Failed:  failed || executionFailed,
		Known:   true,
	}
}
