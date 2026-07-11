package dashboard

// Panel registry and fluid layout computation, adapted from the grom TUI's
// per-view panel model (qf-studio/grom internal/app/model.go) for Pilot's
// fixed panel set. See TASK-398 (GH-4200) for the full navigation/zoom
// design this module supports. computeLayout replaces the three ad-hoc View()
// layout branches (git hidden / stacked / side-by-side) with a single
// geometry pass whose output feeds focusMove (grid.go) directly.

// panelID identifies one of the dashboard's spatially-navigable panels.
// Chrome that isn't a navigation target (banner, metrics cards, eval stats,
// update notice) has no panelID — it just occupies left-column space.
type panelID int

const (
	panelQueue panelID = iota
	panelAutopilot
	panelHistory
	panelLogs
	panelGit
)

// String returns the panel's display name, used in help text and zoom titles.
func (id panelID) String() string {
	switch id {
	case panelQueue:
		return "queue"
	case panelAutopilot:
		return "autopilot"
	case panelHistory:
		return "history"
	case panelLogs:
		return "logs"
	case panelGit:
		return "git"
	default:
		return "?"
	}
}

// panelDef describes one panel's identity and minimum renderable size. A
// panel smaller than MinW x MinH renders as a blank box (safeRender guard)
// rather than garbled content.
type panelDef struct {
	ID         panelID
	MinW, MinH int
}

// panelRegistry lists every navigable dashboard panel in left-column
// stacking order (top to bottom); panelGit is laid out separately by
// computeLayout (side-by-side or stacked beneath, per terminal width).
var panelRegistry = []panelDef{
	{ID: panelQueue, MinW: 40, MinH: 3},
	{ID: panelAutopilot, MinW: 40, MinH: 3},
	{ID: panelHistory, MinW: 40, MinH: 3},
	{ID: panelLogs, MinW: 40, MinH: 3},
	{ID: panelGit, MinW: 20, MinH: 8},
}

// panelByID returns the panel definition for id, or false if unregistered.
func panelByID(id panelID) (panelDef, bool) {
	for _, p := range panelRegistry {
		if p.ID == id {
			return p, true
		}
	}
	return panelDef{}, false
}

// panelIndex returns id's position in panelRegistry (and in the []Rect
// computeLayout returns), or -1 if unregistered.
func panelIndex(id panelID) int {
	for i, p := range panelRegistry {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// layoutHeights supplies each left-column panel's measured content height
// (in lines) for the current frame. Panel content is variable-height (task
// count, history depth, log tail), and computeLayout stays pure/testable by
// taking measured heights as input rather than rendering panels itself.
type layoutHeights struct {
	Queue, Autopilot, History, Logs int
}

// stackedLayoutThreshold mirrors panelTotalWidth+1+20 (tui.go isStackedMode):
// the minimum terminal width needed to place a git graph panel of useful
// width (20 cols) beside the panelTotalWidth-wide left column.
const stackedLayoutThreshold = panelTotalWidth + 1 + 20

// leftColumnWidth returns the left column's width for the given terminal
// width and git-visibility. The left column stretches fluidly to fill the
// terminal whenever nothing else needs the width (git hidden, or git
// stacked beneath rather than beside it); in side-by-side mode it holds at
// panelTotalWidth so the git panel gets the remaining space.
func leftColumnWidth(termW int, gitVisible, gitSideBySide bool) int {
	if gitVisible && gitSideBySide {
		return panelTotalWidth
	}
	if termW > panelTotalWidth {
		return termW
	}
	return panelTotalWidth
}

// computeLayout returns the screen Rect for every registered panel given the
// terminal size, each left-column panel's measured height, and whether the
// git graph panel is visible. The returned slice is indexed identically to
// panelRegistry (see panelIndex), so it can be passed directly to focusMove.
func computeLayout(termW, termH int, h layoutHeights, gitVisible bool) []Rect {
	gitSideBySide := gitVisible && termW >= stackedLayoutThreshold
	leftW := leftColumnWidth(termW, gitVisible, gitSideBySide)

	rects := make([]Rect, len(panelRegistry))
	heights := map[panelID]int{
		panelQueue:     h.Queue,
		panelAutopilot: h.Autopilot,
		panelHistory:   h.History,
		panelLogs:      h.Logs,
	}

	y := 0
	for i, p := range panelRegistry {
		if p.ID == panelGit {
			continue // placed below/beside once the left column's extent is known
		}
		ph := heights[p.ID]
		if ph < p.MinH {
			ph = p.MinH
		}
		rects[i] = Rect{X: 0, Y: y, W: leftW, H: ph}
		y += ph
	}

	if !gitVisible {
		return rects
	}

	gi := panelIndex(panelGit)
	gitDef := panelRegistry[gi]
	if gitSideBySide {
		gx := leftW + 1
		gw := termW - gx
		if gw < gitDef.MinW {
			gw = gitDef.MinW
		}
		rects[gi] = Rect{X: gx, Y: 0, W: gw, H: y}
		return rects
	}

	gh := termH - y - 1 // -1 reserves the help footer line
	if gh < gitDef.MinH {
		gh = gitDef.MinH
	}
	rects[gi] = Rect{X: 0, Y: y, W: termW, H: gh}
	return rects
}

// safeRender returns content when the panel's allotted Rect meets its
// minimum size, or a blank box of the correct dimensions otherwise — panels
// squeezed below MinW/MinH render as empty rather than garbled.
func safeRender(def panelDef, r Rect, content string) string {
	if r.W < def.MinW || r.H < def.MinH {
		return blankBox(r.W, r.H)
	}
	return content
}

// blankBox returns h lines of w spaces, joined by newlines.
func blankBox(w, h int) string {
	if w < 0 {
		w = 0
	}
	if h <= 0 {
		return ""
	}
	line := ""
	if w > 0 {
		b := make([]byte, w)
		for i := range b {
			b[i] = ' '
		}
		line = string(b)
	}
	out := line
	for i := 1; i < h; i++ {
		out += "\n" + line
	}
	return out
}
