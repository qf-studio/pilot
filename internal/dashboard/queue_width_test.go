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
	statuses := []QueueStatus{QueueStatusDone, QueueStatusRunning, QueueStatusQueued, QueueStatusFailed, QueueStatusPending}

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
			row := m.renderTask(task, false, 0, iw, queueIDColumnWidth([]TaskDisplay{task}))
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

	idW := queueIDColumnWidth([]TaskDisplay{task()})
	narrow := m.renderTask(task(), false, 0, 65, idW)
	wide := m.renderTask(task(), false, 0, 120, idW)

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
// bar column from a bracketed block-char bar to the grom segment meter
// (■■■□□, no brackets) — same 16-cell column width, same alignment.
func TestRenderTask_NarrowRegressionPin(t *testing.T) {
	m := NewModel("test")
	task := TaskDisplay{
		ID:       "GH-1234",
		Title:    "a very long title that exceeds twenty chars for sure",
		Status:   QueueStatusDone,
		Progress: 42,
		PRURL:    "https://github.com/o/r/pull/42",
	}

	row := m.renderTask(task, false, 0, 65, queueIDColumnWidth([]TaskDisplay{task}))
	const want = "  ✓ done    GH-1234 a very long title...  ■■■■■■■■■■■■■■■■   #42"
	if row != want {
		t.Errorf("narrow (iw=65) rendering changed:\ngot:  %q\nwant: %q", row, want)
	}
}

// TestRenderTask_BarColumnsAligned checks all five states keep the same
// 16-cell grom segment-meter bar column (no brackets) regardless of panel
// width (bar-width scaling is explicitly out of scope for GH-3970). Fill
// styling is stripped under go test (no TTY), so this asserts cell count and
// glyph, not color — see grom_chrome_test.go for the width-invariant pattern.
func TestRenderTask_BarColumnsAligned(t *testing.T) {
	m := NewModel("test")
	statuses := []QueueStatus{QueueStatusDone, QueueStatusRunning, QueueStatusQueued, QueueStatusFailed, QueueStatusPending}

	for _, iw := range []int{65, 90, 120} {
		for _, status := range statuses {
			task := TaskDisplay{ID: "GH-1", Title: "t", Status: status, Progress: 50, Phase: "x"}
			row := m.renderTask(task, false, 0, iw, queueIDColumnWidth([]TaskDisplay{task}))
			if strings.ContainsAny(row, "[]") {
				t.Errorf("iw=%d status=%s: bracket bar found in %q", iw, status, row)
			}
			if n := strings.Count(row, "■"); n != 16 {
				t.Errorf("iw=%d status=%s: meter cell count = %d, want 16", iw, status, n)
			}
		}
	}
}

// TestRenderTasks_MixedIDColumnAlignment covers GH-4338: sub-issue IDs like
// "GH-4328-1" (9 chars) used to overflow the hardcoded 7-char ID column and
// push every other column out of alignment. The ID column now widens to fit
// the longest visible ID (capped at 12), so mixed-length ID rows keep their
// progress bars aligned and stay within the panel's inner width.
func TestRenderTasks_MixedIDColumnAlignment(t *testing.T) {
	m := NewModel("test")
	tasks := []TaskDisplay{
		{ID: "GH-11", Title: "Short ID task", Status: "done", Progress: 100, PRURL: "https://github.com/o/r/pull/11"},
		{ID: "GH-4328", Title: "Epic parent task", Status: "running", Progress: 50},
		{ID: "GH-4328-1", Title: "Sub-issue task", Status: "queued"},
	}

	for _, iw := range []int{65, 90, 120} {
		idW := queueIDColumnWidth(tasks)

		var barStart int
		for i, task := range tasks {
			row := m.renderTask(task, false, i, iw, idW)

			if w := lipgloss.Width(row); w > iw {
				t.Errorf("iw=%d id=%s: row width %d exceeds inner width %d", iw, task.ID, w, iw)
			}

			start := strings.IndexRune(row, '■')
			if start < 0 {
				t.Fatalf("iw=%d id=%s: no progress-bar glyph found in row %q", iw, task.ID, row)
			}
			if i == 0 {
				barStart = start
			} else if start != barStart {
				t.Errorf("iw=%d id=%s: bar starts at col %d, want %d (matching row 0)", iw, task.ID, start, barStart)
			}
		}
	}
}

// TestRenderTask_ParentTitleFallback covers GH-4338: a sub-issue row whose
// title is empty or echoes its own ID renders "<parent-title> · i/n" when
// the parent linkage resolves from the sibling ID pattern (e.g. "GH-4328-1"
// under sibling "GH-4328"), instead of duplicating the bare ID into the
// title column. When no parent can be resolved, it falls back to the
// (dimmed) ID without panicking or breaking row width.
func TestRenderTask_ParentTitleFallback(t *testing.T) {
	m := NewModel("test")
	m.tasks = []TaskDisplay{
		{ID: "GH-4328", Title: "Epic: ship the thing", Status: "running", Progress: 50},
		{ID: "GH-4328-1", Title: "", Status: "queued"},
	}
	idW := queueIDColumnWidth(m.tasks)

	sub := m.tasks[1]
	row := m.renderTask(sub, false, 0, 90, idW)
	if !strings.Contains(row, "Epic: ship the thing · 1/1") {
		t.Errorf("expected parent-title fallback in row, got: %q", row)
	}

	orphan := TaskDisplay{ID: "GH-9999-1", Title: "GH-9999-1", Status: "queued"}
	orphanRow := m.renderTask(orphan, false, 0, 90, idW)
	if w := lipgloss.Width(orphanRow); w > 90 {
		t.Errorf("orphan fallback row width %d exceeds inner width 90", w)
	}
}
