package dashboard

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func evt(stage memory.Stage) *memory.Event {
	return &memory.Event{Stage: stage}
}

func TestBuildStageStrip_NoEvents(t *testing.T) {
	if got := buildStageStrip(nil, false); got != "–" {
		t.Errorf("no events, not failed: got %q, want %q", got, "–")
	}
	if got := buildStageStrip(nil, true); got != "–" {
		t.Errorf("no events, failed: got %q, want %q", got, "–")
	}
}

func TestBuildStageStrip_HappyPath(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
		evt(memory.StagePRCreated),
		evt(memory.StageMerged),
	}
	got := buildStageStrip(events, false)
	want := "6/7 merged"
	if got != want {
		t.Errorf("happy path: got %q, want %q", got, want)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("happy path strip should not contain a failure marker: %q", got)
	}
}

func TestBuildStageStrip_Released(t *testing.T) {
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
	got := buildStageStrip(events, false)
	want := "7/7 released"
	if got != want {
		t.Errorf("released: got %q, want %q", got, want)
	}
}

func TestBuildStageStrip_SpecValidatedOnly(t *testing.T) {
	events := []*memory.Event{evt(memory.StageSpecValidated)}
	got := buildStageStrip(events, false)
	want := "1/7 spec_validated"
	if got != want {
		t.Errorf("spec_validated only: got %q, want %q", got, want)
	}
}

func TestBuildStageStrip_InProgress(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
	}
	got := buildStageStrip(events, false)
	want := "2/7 running"
	if got != want {
		t.Errorf("in-progress: got %q, want %q", got, want)
	}
}

func TestBuildStageStrip_FailedAtStageN(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageFailed),
	}
	got := buildStageStrip(events, true)
	want := "2/7 ✗ running"
	if got != want {
		t.Errorf("failed-at-stage-N: got %q, want %q", got, want)
	}
}

// TestBuildStageStrip_FailedAsFirstEvent covers the edge case where the only
// recorded event is itself a failure — there's no prior stage to walk back
// to, so the fraction reports 0 reached and names the failure stage itself.
func TestBuildStageStrip_FailedAsFirstEvent(t *testing.T) {
	events := []*memory.Event{evt(memory.StageFailed)}
	got := buildStageStrip(events, true)
	want := "0/7 ✗ failed"
	if got != want {
		t.Errorf("failed as first event: got %q, want %q", got, want)
	}
}

// TestBuildStageStrip_CIFailed asserts the ci_failed off-ramp resolves
// directly to its own ladder rung (pr_created, reached=4) rather than
// walking back — it names a real position, unlike a generic failure.
func TestBuildStageStrip_CIFailed(t *testing.T) {
	got := buildStageStrip([]*memory.Event{evt(memory.StageCIFailed)}, true)
	want := "4/7 ✗ ci_failed"
	if got != want {
		t.Errorf("ci_failed: got %q, want %q", got, want)
	}
}

func TestBuildStageStrip_Stalled(t *testing.T) {
	got := buildStageStrip([]*memory.Event{evt(memory.StageStalled)}, true)
	want := "0/7 ✗ stalled"
	if got != want {
		t.Errorf("stalled with no prior stage: got %q, want %q", got, want)
	}
}

func TestBuildStageStrip_AwaitingApproval(t *testing.T) {
	got := buildStageStrip([]*memory.Event{evt(memory.StageAwaitingApproval)}, false)
	want := "5/7 awaiting_approval"
	if got != want {
		t.Errorf("awaiting_approval: got %q, want %q", got, want)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("awaiting_approval is a gated in-flight state, not a failure: %q", got)
	}
}

// TestBuildStageStrip_RetriesDoNotInflate ensures a long retry-heavy
// timeline reports the same ladder-position fraction as a short one — the
// fraction reflects position, not event count.
func TestBuildStageStrip_RetriesDoNotInflate(t *testing.T) {
	events := make([]*memory.Event, 0, 20)
	for i := 0; i < 20; i++ {
		events = append(events, evt(memory.StageRunning))
	}
	got := buildStageStrip(events, false)
	want := "2/7 running"
	if got != want {
		t.Errorf("retries should not inflate the fraction: got %q, want %q", got, want)
	}
}

// TestBuildStageStrip_TruncatesLongLabel guards the fixed-width card layout
// against an oversized stage/detail value.
func TestBuildStageStrip_TruncatesLongLabel(t *testing.T) {
	events := []*memory.Event{evt(memory.Stage(strings.Repeat("x", 40)))}
	got := buildStageStrip(events, false)
	label := strings.SplitN(got, " ", 2)[1]
	if len(label) > maxStageStripLabelWidth {
		t.Errorf("label length = %d, want <= %d: %q", len(label), maxStageStripLabelWidth, label)
	}
}

// TestBuildStageStrip_MaxRungNoRegression covers GH-4023: a later
// pr_created/failed recorded after released must not pull the displayed
// rung backward. The reducer must track the maximum ladder position
// observed across the whole stream, and the terminal glyph attaches to
// whichever event defined that max — not to the last event in the stream.
func TestBuildStageStrip_MaxRungNoRegression(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StagePRCreated),
		evt(memory.StageMerged),
		evt(memory.StageReleased),
		evt(memory.StagePRCreated),
		evt(memory.StageFailed),
	}
	got := buildStageStrip(events, false)
	want := "7/7 released"
	if got != want {
		t.Errorf("max-rung no regression: got %q, want %q", got, want)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("max rung was reached at released, a later failed event must not add a failure marker: %q", got)
	}
}

// TestBuildStageStrip_MaxRungHealthyPath guards against an off-by-one in
// the running-max reducer on a full, unbroken ladder climb — every rung
// must be visited in order and the final fraction must still land on 7/7.
func TestBuildStageStrip_MaxRungHealthyPath(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
		evt(memory.StageCommit),
		evt(memory.StagePRCreated),
		evt(memory.StageCIPassed),
		evt(memory.StageMerged),
		evt(memory.StageReleased),
	}
	got := buildStageStrip(events, false)
	want := "7/7 released"
	if got != want {
		t.Errorf("max-rung healthy path: got %q, want %q", got, want)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("healthy full-ladder path should not contain a failure marker: %q", got)
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
