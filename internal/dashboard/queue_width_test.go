package dashboard

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestRenderTask_WidthAware covers GH-3970: QUEUE rows must flex their title
// column with the panel width instead of hardcoding a 65-inner-char layout,
// matching the HISTORY panel's iw-driven pattern.
func TestRenderTask_WidthAware(t *testing.T) {
	m := NewModel("test")
	longTitle := "a very long queued task title that definitely exceeds twenty visible characters by a lot"

	widths := []int{65, 90, 120}
	statuses := []string{"done", "running", "queued", "failed", "pending"}

	for _, iw := range widths {
		for _, status := range statuses {
			task := TaskDisplay{
				ID:       "GH-1234",
				Title:    longTitle,
				Status:   status,
				Progress: 42,
				Phase:    "build",
				PRURL:    "https://github.com/o/r/pull/42",
			}
			row := m.renderTask(task, false, 0, iw)
			if w := lipgloss.Width(row); w > iw {
				t.Errorf("iw=%d status=%s: row width %d exceeds iw", iw, status, w)
			}
		}
	}
}

// TestRenderTask_TitleExpandsWithWidth verifies the title column shows more
// characters as the panel widens, rather than staying clipped at 20 chars.
func TestRenderTask_TitleExpandsWithWidth(t *testing.T) {
	m := NewModel("test")
	longTitle := "a very long queued task title that definitely exceeds twenty visible characters by a lot"

	task := func() TaskDisplay {
		return TaskDisplay{ID: "GH-1234", Title: longTitle, Status: "running", Progress: 42}
	}

	narrow := m.renderTask(task(), false, 0, 65)
	wide := m.renderTask(task(), false, 0, 120)

	titleLen := func(row string) int {
		// Count visible chars of longTitle actually present in the row.
		n := 0
		for n < len(longTitle) && strings.Contains(row, longTitle[:n+1]) {
			n++
		}
		return n
	}

	narrowChars := titleLen(narrow)
	wideChars := titleLen(wide)

	if wideChars <= narrowChars {
		t.Errorf("expected wider panel to show more title chars: narrow=%d wide=%d", narrowChars, wideChars)
	}
}

// TestRenderTask_NarrowRegressionPin pins the exact byte output at iw=65 (the
// default, non-stacked panel content width) so the width-aware refactor does
// not alter today's rendering for the common case. TASK-390/GH-4064 moved the
// bar column from a bracketed block-char bar to the grot segment meter
// (■■■□□, no brackets) — same 16-cell column width, same alignment.
func TestRenderTask_NarrowRegressionPin(t *testing.T) {
	m := NewModel("test")
	task := TaskDisplay{
		ID:       "GH-1234",
		Title:    "a very long title that exceeds twenty chars for sure",
		Status:   "done",
		Progress: 42,
		PRURL:    "https://github.com/o/r/pull/42",
	}

	row := m.renderTask(task, false, 0, 65)
	const want = "  ✓ done    GH-1234 a very long title...  ■■■■■■■■■■■■■■■■   #42"
	if row != want {
		t.Errorf("narrow (iw=65) rendering changed:\ngot:  %q\nwant: %q", row, want)
	}
}

// TestRenderTask_BarColumnsAligned checks all five states keep the same
// 16-cell grot segment-meter bar column (no brackets) regardless of panel
// width (bar-width scaling is explicitly out of scope for GH-3970). Fill
// styling is stripped under go test (no TTY), so this asserts cell count and
// glyph, not color — see grot_chrome_test.go for the width-invariant pattern.
func TestRenderTask_BarColumnsAligned(t *testing.T) {
	m := NewModel("test")
	statuses := []string{"done", "running", "queued", "failed", "pending"}

	for _, iw := range []int{65, 90, 120} {
		for _, status := range statuses {
			task := TaskDisplay{ID: "GH-1", Title: "t", Status: status, Progress: 50, Phase: "x"}
			row := m.renderTask(task, false, 0, iw)
			if strings.ContainsAny(row, "[]") {
				t.Errorf("iw=%d status=%s: bracket bar found in %q", iw, status, row)
			}
			if n := strings.Count(row, "■"); n != 16 {
				t.Errorf("iw=%d status=%s: meter cell count = %d, want 16", iw, status, n)
			}
		}
	}
}
