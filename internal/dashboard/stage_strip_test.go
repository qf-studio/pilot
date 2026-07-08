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
