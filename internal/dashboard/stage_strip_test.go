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
	if got := buildStageStrip(nil, false); got != "✓" {
		t.Errorf("no events, not failed: got %q, want %q", got, "✓")
	}
	if got := buildStageStrip(nil, true); got != "✗" {
		t.Errorf("no events, failed: got %q, want %q", got, "✗")
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
	want := "✓✓✓✓✓✓ merged"
	if got != want {
		t.Errorf("happy path: got %q, want %q", got, want)
	}
	if strings.Contains(got, "✗") {
		t.Errorf("happy path strip should not contain a failure glyph: %q", got)
	}
}

func TestBuildStageStrip_InProgress(t *testing.T) {
	events := []*memory.Event{
		evt(memory.StageQueued),
		evt(memory.StageSpecValidated),
		evt(memory.StageRunning),
	}
	got := buildStageStrip(events, false)
	want := "✓✓✓ running"
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
	want := "✓✓✓✗ running"
	if got != want {
		t.Errorf("failed-at-stage-N: got %q, want %q", got, want)
	}
}

// TestBuildStageStrip_FailedAsFirstEvent covers the edge case where the only
// recorded event is itself a failure — there's no prior stage to name, so the
// label falls back to the failure stage's own name.
func TestBuildStageStrip_FailedAsFirstEvent(t *testing.T) {
	events := []*memory.Event{evt(memory.StageFailed)}
	got := buildStageStrip(events, true)
	want := "✗ failed"
	if got != want {
		t.Errorf("failed as first event: got %q, want %q", got, want)
	}
}

// TestBuildStageStrip_CapsGlyphCount ensures a long retry-heavy timeline stays
// compact rather than growing unboundedly.
func TestBuildStageStrip_CapsGlyphCount(t *testing.T) {
	events := make([]*memory.Event, 0, 20)
	for i := 0; i < 20; i++ {
		events = append(events, evt(memory.StageRunning))
	}
	got := buildStageStrip(events, false)
	glyphs := strings.SplitN(got, " ", 2)[0]
	if len([]rune(glyphs)) != maxStageStripGlyphs {
		t.Errorf("glyph count = %d, want capped at %d", len([]rune(glyphs)), maxStageStripGlyphs)
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

func TestBuildStageStrip_CIFailedAndStalledAreFailureGlyphs(t *testing.T) {
	if got := buildStageStrip([]*memory.Event{evt(memory.StageCIFailed)}, true); !strings.HasPrefix(got, "✗") {
		t.Errorf("ci_failed should render as a failure glyph: %q", got)
	}
	if got := buildStageStrip([]*memory.Event{evt(memory.StageStalled)}, true); !strings.HasPrefix(got, "✗") {
		t.Errorf("stalled should render as a failure glyph: %q", got)
	}
}
