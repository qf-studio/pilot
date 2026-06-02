package dashboard

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderTaskCard_NarrowDoesNotBlankFailed reproduces the v2.166.10 bug: with
// all non-failure buckets populated, the breakdown suffix overflowed the narrow
// mini-card and truncation of the styled multi-segment string blanked the whole
// "failed" line. The headline must always remain visible. TASK-358.
func TestRenderTaskCard_NarrowDoesNotBlankFailed(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{
		Succeeded: 1508, Failed: 234,
		NoOp: 120, Infra: 305, Skipped: 81, RateLimited: 34, Stalled: 10,
	}

	out := m.renderTaskCard(23) // real-world narrow card width (ciw = 17)

	if !strings.Contains(out, "234 failed") {
		t.Errorf("narrow card dropped the failed headline; got:\n%s", out)
	}
	if !strings.Contains(out, "1508 succeeded") {
		t.Errorf("narrow card dropped the succeeded line; got:\n%s", out)
	}
	// Every rendered line must fit the card width exactly (no overflow / blanking).
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != 23 {
			t.Errorf("line width = %d, want 23: %q", w, line)
		}
	}
}

// TestRenderTaskCard_WideShowsBreakdown verifies the breakdown suffix appears when
// the card is wide enough to hold it.
func TestRenderTaskCard_WideShowsBreakdown(t *testing.T) {
	m := NewModel("test")
	m.metricsCard = MetricsCardData{Succeeded: 1508, Failed: 234, NoOp: 120, Infra: 305}

	out := m.renderTaskCard(90) // wide card: suffix fits

	for _, want := range []string{"234 failed", "120 no-op", "305 infra"} {
		if !strings.Contains(out, want) {
			t.Errorf("wide card missing %q; got:\n%s", want, out)
		}
	}
}

// TestTruncateVisualStyled verifies ANSI-aware truncation: a styled string longer
// than the target keeps its leading visible text and respects the visible width
// budget instead of breaking mid-escape.
func TestTruncateVisualStyled(t *testing.T) {
	styled := statusFailedStyle.Render("✗ 234 failed") +
		statusPendingStyle.Render(" (120 no-op · 305 infra · 81 skipped)")

	out := truncateVisual(styled, 17)

	if w := lipgloss.Width(out); w > 17 {
		t.Errorf("visible width = %d, want <= 17", w)
	}
	if !strings.Contains(out, "234 failed") {
		t.Errorf("truncation dropped leading visible text; got %q", out)
	}
}
