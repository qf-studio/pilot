package dashboard

// Shared zoomed-list viewport helper for the dashboard's full-size list
// panels (queue, autopilot, history) opened via the grom-style zoom
// interaction. See TASK-398 (GH-4200) for the full navigation/zoom design;
// wiring these helpers into tui.go's zoomed Update/View paths lands in a
// subsequent subtask.

import "fmt"

// zoomListViewportHeight returns how many item rows fit in a zoomed list
// panel given the panel's total height h. Overhead mirrors
// gitGraphViewportHeight (gitgraph.go): top/bottom border(2) + padding(2) +
// scroll indicator(1) = 5.
func zoomListViewportHeight(h int) int {
	if h > 5 {
		return h - 5
	}
	return 1
}

// ensureSelVisible clamps sel to a valid index in a total-length list and
// adjusts scroll so sel stays within the visible window
// [scroll, scroll+visible). It returns the clamped (sel, scroll) pair.
//
// total <= 0 (empty list) always returns (0, 0).
func ensureSelVisible(sel, scroll, total, visible int) (int, int) {
	if total <= 0 {
		return 0, 0
	}
	if visible < 1 {
		visible = 1
	}
	if sel < 0 {
		sel = 0
	}
	if sel > total-1 {
		sel = total - 1
	}
	if sel < scroll {
		scroll = sel
	}
	if sel >= scroll+visible {
		scroll = sel - visible + 1
	}
	maxScroll := total - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	return sel, scroll
}

// zoomListIndicator renders the "[a-b of N]" bottom-right indicator for a
// zoomed list's visible window [start, end) of a total-length list — the
// same idiom as the git graph panel's scroll indicator (gitgraph.go).
// Returns "" when the list is empty.
func zoomListIndicator(start, end, total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf("[%d-%d of %d]", start+1, end, total)
}

// zoomListSelector returns a row's selection prefix: a dim "▸ " marker when
// selected, or two spaces otherwise — the same treatment as the queue
// panel's renderTask selector (tui.go).
func zoomListSelector(selected bool) string {
	if selected {
		return dimStyle.Render("▸") + " "
	}
	return "  "
}
