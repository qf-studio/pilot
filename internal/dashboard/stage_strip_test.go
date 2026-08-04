package dashboard

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func evt(stage memory.Stage) *memory.Event {
	return &memory.Event{Stage: stage}
}

func TestBuildStageInfo_NoEvents(t *testing.T) {
	if got := buildStageInfo(nil, false); got.Known {
		t.Errorf("no events, not failed: got %+v, want Known=false", got)
	}
	if got := buildStageInfo(nil, true); got.Known {
		t.Errorf("no events, failed: got %+v, want Known=false", got)
	}
}

func TestBuildStageInfo_HappyPath(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
		evt(memory.StagePRCreated),
		evt(memory.StageMerged),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 6, Label: "merged", Failed: false, Known: true}
	if got != want {
		t.Errorf("happy path: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_Released(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
		evt(memory.StagePRCreated),
		evt(memory.StageCIPassed),
		evt(memory.StageMerged),
		evt(memory.StageReleased),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 7, Label: "released", Failed: false, Known: true}
	if got != want {
		t.Errorf("released: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_SpecValidatedOnly(t *testing.T) {
	events := []*memory.Event{evt(memory.StageSpecValidated)}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 1, Label: "spec_validated", Failed: false, Known: true}
	if got != want {
		t.Errorf("spec_validated only: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_InProgress(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 2, Label: "running", Failed: false, Known: true}
	if got != want {
		t.Errorf("in-progress: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_FailedAtStageN(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageFailed),
	}
	got := buildStageInfo(events, true)
	want := StageInfo{Reached: 2, Label: "running", Failed: true, Known: true}
	if got != want {
		t.Errorf("failed-at-stage-N: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_FailedAsFirstEvent covers the edge case where the only
// recorded event is itself a failure — there's no prior stage to walk back
// to, so Reached reports 0 and Label names the failure stage itself.
func TestBuildStageInfo_FailedAsFirstEvent(t *testing.T) {
	events := []*memory.Event{evt(memory.StageFailed)}
	got := buildStageInfo(events, true)
	want := StageInfo{Reached: 0, Label: "failed", Failed: true, Known: true}
	if got != want {
		t.Errorf("failed as first event: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_CIFailed asserts the ci_failed off-ramp resolves
// directly to its own ladder rung (pr_created, reached=4) rather than
// walking back — it names a real position, unlike a generic failure.
func TestBuildStageInfo_CIFailed(t *testing.T) {
	got := buildStageInfo([]*memory.Event{evt(memory.StageCIFailed)}, true)
	want := StageInfo{Reached: 4, Label: "ci_failed", Failed: true, Known: true}
	if got != want {
		t.Errorf("ci_failed: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_Stalled(t *testing.T) {
	got := buildStageInfo([]*memory.Event{evt(memory.StageStalled)}, true)
	want := StageInfo{Reached: 0, Label: "stalled", Failed: true, Known: true}
	if got != want {
		t.Errorf("stalled with no prior stage: got %+v, want %+v", got, want)
	}
}

func TestBuildStageInfo_AwaitingApproval(t *testing.T) {
	got := buildStageInfo([]*memory.Event{evt(memory.StageAwaitingApproval)}, false)
	want := StageInfo{Reached: 5, Label: "awaiting_approval", Failed: false, Known: true}
	if got != want {
		t.Errorf("awaiting_approval: got %+v, want %+v", got, want)
	}
	if got.Failed {
		t.Errorf("awaiting_approval is a gated in-flight state, not a failure: %+v", got)
	}
}

// TestBuildStageInfo_RetriesDoNotInflate ensures a long retry-heavy
// timeline reports the same ladder position as a short one — Reached
// reflects position, not event count.
func TestBuildStageInfo_RetriesDoNotInflate(t *testing.T) {
	events := make([]*memory.Event, 0, 20)
	for i := 0; i < 20; i++ {
		events = append(events, evt(memory.StageRunning))
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 2, Label: "running", Failed: false, Known: true}
	if got != want {
		t.Errorf("retries should not inflate the rung: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_TruncatesLongLabel guards the fixed-width card layout
// against an oversized stage/detail value.
func TestBuildStageInfo_TruncatesLongLabel(t *testing.T) {
	events := []*memory.Event{evt(memory.Stage(strings.Repeat("x", 40)))}
	got := buildStageInfo(events, false)
	if len(got.Label) > maxStageStripLabelWidth {
		t.Errorf("label length = %d, want <= %d: %q", len(got.Label), maxStageStripLabelWidth, got.Label)
	}
}

// TestBuildStageInfo_MaxRungNoRegression covers GH-4023: a later
// pr_created/failed recorded after released must not pull the displayed
// rung backward. The reducer must track the maximum ladder position
// observed across the whole stream, and the Failed flag attaches to
// whichever event defined that max — not to the last event in the stream.
func TestBuildStageInfo_MaxRungNoRegression(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StagePRCreated),
		evt(memory.StageMerged),
		evt(memory.StageReleased),
		evt(memory.StagePRCreated),
		evt(memory.StageFailed),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 7, Label: "released", Failed: false, Known: true}
	if got != want {
		t.Errorf("max-rung no regression: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_MaxRungHealthyPath guards against an off-by-one in
// the running-max reducer on a full, unbroken ladder climb — every rung
// must be visited in order and the final rung must still land on 7.
func TestBuildStageInfo_MaxRungHealthyPath(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
		evt(memory.StagePRCreated),
		evt(memory.StageCIPassed),
		evt(memory.StageMerged),
		evt(memory.StageReleased),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 7, Label: "released", Failed: false, Known: true}
	if got != want {
		t.Errorf("max-rung healthy path: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_DecomposeOnlyEpicParentTerminal covers GH-4298: a
// decompose-only epic parent ships no PR of its own — its stream ends
// running -> spec_validated -> decomposed -> completed, never touching
// pr_created/ci_passed/merged. Before the fix, the running-max reducer froze
// the label at "running" (pos 2, the highest *named* rung) forever; the run
// must instead render a terminal label at the top of the ladder.
func TestBuildStageInfo_DecomposeOnlyEpicParentTerminal(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageRunning),
		evt(memory.StageSpecValidated),
		evt(memory.StageDecomposed),
		evt(memory.StageCompleted),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: stageLadderTotal, Label: "completed", Failed: false, Known: true}
	if got != want {
		t.Errorf("decompose-only epic parent: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_CleanTerminalDoesNotOverrideFailure is the GH-4298
// regression guard for GH-4212-style failure labeling: a run that died at a
// rung must keep showing ✗ at its stage of death, never get promoted to a
// terminal label just because the stream happens to end elsewhere.
func TestBuildStageInfo_CleanTerminalDoesNotOverrideFailure(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageFailed),
	}
	got := buildStageInfo(events, true)
	want := StageInfo{Reached: 2, Label: "running", Failed: true, Known: true}
	if got != want {
		t.Errorf("failed-at-a-rung must not be promoted: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_GenuinelyRunningStaysRunning guards the in-flight case:
// no terminal event has been recorded yet, so the label must still read
// "running" rather than being promoted to a terminal stage.
func TestBuildStageInfo_GenuinelyRunningStaysRunning(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 2, Label: "running", Failed: false, Known: true}
	if got != want {
		t.Errorf("in-flight run must not be promoted: got %+v, want %+v", got, want)
	}
}

// TestBuildStageInfo_HealedRowStillTerminal guards against a regression on
// rows healed by the #4294 SelfHealExecutionAfterMerge/SelfHealExecutionByPRURL
// backfill: those append a StageMerged event, which already names its own
// ladder rung (pos 6) — the GH-4298 clean-terminal promotion must not be
// needed (and must not interfere) for that path to keep rendering terminal.
func TestBuildStageInfo_HealedRowStillTerminal(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageImplementationStarted),
		evt(memory.StageMerged), // backfilled by the #4294 heal path
	}
	got := buildStageInfo(events, false)
	want := StageInfo{Reached: 6, Label: "merged", Failed: false, Known: true}
	if got != want {
		t.Errorf("healed row: got %+v, want %+v", got, want)
	}
}

func TestStageLadderPosition_AllStages(t *testing.T) {
	cases := []struct {
		stage memory.Stage
		want  int
	}{
		{memory.StageQueued, 0},
		{memory.StageSpecValidated, 1},
		{memory.StageRunning, 2},
		{memory.StageCommit, 3},
		{memory.StagePRCreated, 4},
		{memory.StageCIPassed, 5},
		{memory.StageCIFailed, 4},
		{memory.StageAwaitingApproval, 5},
		{memory.StageMerged, 6},
		{memory.StageReleased, 7},
		{memory.StageFailed, 0},
		{memory.StageNoOp, 0},
		{memory.StageSkipped, 0},
		{memory.StageStalled, 0},
	}
	for _, c := range cases {
		if got := stageLadderPosition(c.stage); got != c.want {
			t.Errorf("stageLadderPosition(%q) = %d, want %d", c.stage, got, c.want)
		}
	}
}

// GH-3927 regression: a skipped execution (events end in failed → skipped)
// must not render as a succeeded run stuck at "running" — the authoritative
// executions.status owns the label and mutes the meter. Event stream is the
// verbatim timeline from the production run that surfaced the bug.
func TestStageInfoForExecution_SkippedRun(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageRunning),
		evt(memory.StageSpecValidated),
		evt(memory.StageClaudeStarted),
		evt(memory.StageFailed),
		evt(memory.StageSkipped),
	}
	got := stageInfoForExecution(events, "skipped")
	want := StageInfo{Reached: 2, Label: "skipped", Failed: false, Known: true, Muted: true}
	if got != want {
		t.Errorf("skipped run: got %+v, want %+v", got, want)
	}
}

// GH-4064 regression: a genuinely running execution keeps its ladder label
// (not muted) — the row glyph ● carries the live state.
func TestStageInfoForExecution_RunningPassthrough(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageRunning),
		evt(memory.StageSpecValidated),
	}
	got := stageInfoForExecution(events, "running")
	want := StageInfo{Reached: 2, Label: "running", Failed: false, Known: true, Muted: false}
	if got != want {
		t.Errorf("running run: got %+v, want %+v", got, want)
	}
}

// stalled keeps the rung label (shows where it died) and flags failure,
// like failed — it is deliberately not a muted outcome.
func TestStageInfoForExecution_StalledKeepsRung(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
	}
	got := stageInfoForExecution(events, "stalled")
	want := StageInfo{Reached: 3, Label: "commit", Failed: true, Known: true, Muted: false}
	if got != want {
		t.Errorf("stalled run: got %+v, want %+v", got, want)
	}
}

// TestStageInfoForExecution_DeclinedPreflightMutedWithZeroEvents is the
// GH-4368 regression: the intent judge declines a task before any work
// starts, so the row has zero execution_events. Before the fix, a muted
// status only got its label/Muted override when info.Known was already true
// (i.e. events existed) — a zero-event muted row fell through to the
// all-blank StageInfo{}, rendering an unknown-glyph meter and a blank "–"
// label instead of a dim, labeled one.
func TestStageInfoForExecution_DeclinedPreflightMutedWithZeroEvents(t *testing.T) {
	got := stageInfoForExecution(nil, "declined-preflight")
	want := StageInfo{Reached: 0, Label: "declined-preflight", Failed: false, Known: true, Muted: true}
	if got != want {
		t.Errorf("declined-preflight with zero events: got %+v, want %+v", got, want)
	}
}

// TestStageInfoForExecution_MutedOutcomesTable is table-driven coverage of
// every mutedOutcomes status, including the zero-events case for each.
func TestStageInfoForExecution_MutedOutcomesTable(t *testing.T) {
	tests := []struct {
		status string
		events []*memory.Event
		want   StageInfo
	}{
		{
			status: "declined-preflight",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "declined-preflight", Failed: false, Known: true, Muted: true},
		},
		{
			status: "skipped",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "skipped", Failed: false, Known: true, Muted: true},
		},
		{
			status: "no_op",
			events: []*memory.Event{evt(memory.StageRunning)},
			want:   StageInfo{Reached: 2, Label: "no_op", Failed: false, Known: true, Muted: true},
		},
		{
			status: "declined",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "declined", Failed: false, Known: true, Muted: true},
		},
		{
			status: "rate_limited",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "rate_limited", Failed: false, Known: true, Muted: true},
		},
		{
			status: "infra",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "infra", Failed: false, Known: true, Muted: true},
		},
		{
			// TASK-420/GH-4537: cancellation is written directly to the
			// executions row (claim-loss cleanup / operator surgery) without
			// ever emitting an execution_events row of its own — before this
			// status was added to mutedOutcomes, the running-max reducer
			// froze on the last real event ("running") and HISTORY kept
			// showing a ghost "running Nh ago" row for a task that was
			// actually dead (GH-4531 capture). Zero-events case.
			status: "cancelled",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "cancelled", Failed: false, Known: true, Muted: true},
		},
		{
			// Same ghost-row scenario but with a prior "running" event on
			// record (the realistic case — the task really was running
			// before it got cancelled) — the muted-outcome override must
			// still win over the running-max reducer's frozen label.
			status: "cancelled",
			events: []*memory.Event{evt(memory.StageRunning)},
			want:   StageInfo{Reached: 2, Label: "cancelled", Failed: false, Known: true, Muted: true},
		},
		{
			// GH-4701: an operator's deliberate not-planned close (or a
			// sibling/duplicate execution already delivering this issue's
			// scope, GH-4656) is not a pipeline failure — the row must
			// render muted "superseded", not the blank/unknown meter a
			// zero-event row would otherwise get.
			status: "superseded",
			events: nil,
			want:   StageInfo{Reached: 0, Label: "superseded", Failed: false, Known: true, Muted: true},
		},
		{
			// Same outcome, but with the realistic #4655-cluster shape: the
			// run had already reached pr_created before the operator closed
			// the issue. The muted-outcome override must still win over the
			// running-max reducer's frozen "pr_created" label — without it
			// this row renders "✗ pr_created", indistinguishable from a
			// genuine failure.
			status: "superseded",
			events: []*memory.Event{
				evt(memory.StageSpecValidated),
				evt(memory.StageRunning),
				evt(memory.StageCommit),
				evt(memory.StagePRCreated),
			},
			want: StageInfo{Reached: 4, Label: "superseded", Failed: false, Known: true, Muted: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := stageInfoForExecution(tt.events, tt.status)
			if got != tt.want {
				t.Errorf("stageInfoForExecution(%v, %q) = %+v, want %+v", tt.events, tt.status, got, tt.want)
			}
		})
	}
}

func TestDisplayStatus(t *testing.T) {
	cases := map[string]string{
		"completed":    "success",
		"failed":       "failed",
		"skipped":      "skipped",
		"no_op":        "no_op",
		"running":      "running",
		"rate_limited": "rate_limited",
	}
	for in, want := range cases {
		if got := displayStatus(in); got != want {
			t.Errorf("displayStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveHistoryStatus_SharedByAllCallSites is the TASK-420/GH-4537
// regression test: resolveHistoryStatus is now the one place hydrateFromStore
// (tui.go), storeRefreshCmd (tui.go), and historyZoomCmd (zoom.go) go through
// to turn an executions.status + its event ladder into a rendered HISTORY
// row. A genuinely-running execution must never resolve to a done/success
// status, and a cancelled (terminal) execution must never resolve to
// "running" even though its last real event was a running rung — reproducing
// the exact divergence captured on the dashboard: GH-A stuck "running" for
// hours while other panels called it done, GH-B cancelled but HISTORY still
// showed "running Nh ago".
func TestResolveHistoryStatus_SharedByAllCallSites(t *testing.T) {
	t.Run("running task never resolves to a done status", func(t *testing.T) {
		hs := resolveHistoryStatus("running", []*memory.Event{evt(memory.StageQueued), evt(memory.StageRunning)})
		if hs.Status != "running" {
			t.Errorf("Status = %q, want %q (a running execution must never render as done/success)", hs.Status, "running")
		}
		if hs.Stage.Label == "success" || hs.Stage.Label == "completed" {
			t.Errorf("Stage.Label = %q, must not read as a completed outcome for a running task", hs.Stage.Label)
		}
	})

	t.Run("cancelled task never resolves to running, even with a running event on record", func(t *testing.T) {
		hs := resolveHistoryStatus("cancelled", []*memory.Event{evt(memory.StageQueued), evt(memory.StageRunning)})
		if hs.Status == "running" {
			t.Errorf("Status = %q, want non-running (a cancelled/terminal execution must never render as running)", hs.Status)
		}
		if hs.Stage.Label == "running" {
			t.Errorf("Stage.Label = %q, want %q (muted-outcome override must beat the running-max reducer's frozen label)", hs.Stage.Label, "cancelled")
		}
		if hs.Stage.Label != "cancelled" {
			t.Errorf("Stage.Label = %q, want %q", hs.Stage.Label, "cancelled")
		}
		if !hs.Stage.Muted {
			t.Error("expected cancelled outcome to be muted, matching other terminal-without-ladder-rung statuses")
		}
	})
}

// TestResolveHistoryStatus_SupersededNotRenderedAsFailure is the GH-4701
// regression test: an issue an operator closes as not-planned / superseded
// (or a sibling/duplicate execution that already delivered the same scope,
// GH-4656) must render a distinct muted "superseded" row, not "✗ <rung>" —
// indistinguishable from a genuine pipeline failure (the 2026-08-03 #4655
// cluster: #4660-#4665 closed en masse and re-filed as #4677 rendered as 6
// of 43 HISTORY rows misread this way). Table-driven per the three cases
// called out in the acceptance criteria: a superseded row, a genuine
// ci_failed row (unchanged ✗ rendering), and a legacy row with no events
// (unchanged Known=false — no stage evidence must never be fabricated into
// either outcome).
func TestResolveHistoryStatus_SupersededNotRenderedAsFailure(t *testing.T) {
	tests := []struct {
		name       string
		execStatus string
		events     []*memory.Event
		want       HistoryStatus
	}{
		{
			name:       "superseded row renders muted, not a failure",
			execStatus: "superseded",
			events: []*memory.Event{
				evt(memory.StageSpecValidated),
				evt(memory.StageRunning),
				evt(memory.StageCommit),
				evt(memory.StagePRCreated),
			},
			want: HistoryStatus{
				Status: "superseded",
				Stage:  StageInfo{Reached: 4, Label: "superseded", Failed: false, Known: true, Muted: true},
			},
		},
		{
			name:       "genuine ci_failed row keeps the unmuted failure rendering",
			execStatus: "failed",
			events:     []*memory.Event{evt(memory.StageCIFailed)},
			want: HistoryStatus{
				Status: "failed",
				Stage:  StageInfo{Reached: 4, Label: "ci_failed", Failed: true, Known: true, Muted: false},
			},
		},
		{
			name:       "legacy row with no events stays unknown, not fabricated into a failure or superseded",
			execStatus: "completed",
			events:     nil,
			want: HistoryStatus{
				Status: "success",
				Stage:  StageInfo{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveHistoryStatus(tt.execStatus, tt.events)
			if got != tt.want {
				t.Errorf("resolveHistoryStatus(%q, %v) = %+v, want %+v", tt.execStatus, tt.events, got, tt.want)
			}
			if tt.execStatus == "superseded" && (got.Stage.Failed || !got.Stage.Muted) {
				t.Errorf("superseded row must render muted and non-failed: got %+v", got.Stage)
			}
			if tt.execStatus == "failed" && (!got.Stage.Failed || got.Stage.Muted) {
				t.Errorf("genuine failure row must keep the unmuted ✗ rendering: got %+v", got.Stage)
			}
		})
	}
}
