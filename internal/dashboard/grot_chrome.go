package dashboard

// Card chrome shared with grot (github.com/qf-studio/grot): the dashboard
// renders with the same pkg/tui/render primitives and "pilot" theme grot
// ships, so both tools look identical in the terminal.
//
// Glyph vocabulary (no emoji — single-width semantic marks only):
//
//	●  active / online / running        ○  inactive / stopping / none
//	◌  waiting / queued / draining      ▸  intake / selection
//	✓  success                          ✗  failure
//	!  warning / attention              ↑  update available
//	⟲  retry / restart / recovery       ◐◓◑◒  in-progress spinner
//	·  segment separator                →  transition (a → b)
//
// Log lines and panel content compose these marks with the muted theme
// colors; system messages are lowercase to match the grot card titles.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/qf-studio/grot/pkg/tui/render"
	"github.com/qf-studio/grot/pkg/tui/theme"
)

// grotTheme is the shared design system ("pilot" theme).
var grotTheme = theme.Pilot

// panelChrome is the default card chrome: slate border, bold label title.
var panelChrome = render.PanelStyle{
	Border: grotTheme.BorderStyle(),
	Title:  grotTheme.TitleStyle(),
}

// focusChrome highlights the focused card with an accent border.
var focusChrome = render.PanelStyle{
	Border: grotTheme.FocusBorderStyle(),
	Title:  grotTheme.TitleStyle(),
}

// warnChrome is the amber chrome used by the update-notification card.
var warnChrome = render.PanelStyle{
	Border: grotTheme.WarningStyle(),
	Title:  grotTheme.WarningStyle().Bold(true),
}

// renderPanel builds a grot-style card: lowercase title embedded in the top
// border, one padding line above and below the content.
func renderPanel(title, content string, tw int) string {
	return renderPanelInfo(title, "", content, tw)
}

// renderPanelInfo is renderPanel with a legend embedded in the top-right
// border: ╭─ title ────┤ info ├─╮.
func renderPanelInfo(title, info, content string, tw int) string {
	return renderPanelStyled(title, info, content, tw, panelChrome)
}

// renderPanelStyled renders the card with explicit chrome (warn, focus).
// Height is derived from the content so panels stay content-sized.
func renderPanelStyled(title, info, content string, tw int, ps render.PanelStyle) string {
	padded := "\n" + content + "\n"
	h := strings.Count(padded, "\n") + 3 // inner lines + top/bottom border
	return render.PanelInfo(strings.ToLower(title), info, padded, tw, h, ps)
}

// statCardHeight: border + value + detail + 2 braille rows + border.
const statCardHeight = 6

// buildStatCard renders a grot-style stat card: big bold centered value, a
// centered detail line, and a subdued braille trend band at the bottom —
// the row-1 card from the grot demo gallery.
func buildStatCard(title, value, detail string, history []float64, accentHex string, cw int) string {
	iw, ih := render.InnerSize(cw, statCardHeight)
	chartRows := ih - 2
	lines := []string{
		render.Center(value, iw),
		render.Center(detail, iw),
	}
	gradient := render.GradientStyles(
		[]string{render.Dim(accentHex, 0.35), render.Dim(accentHex, 0.75)}, chartRows)
	// BrailleArea right-aligns short series; stretch the 7-day history across
	// the full band width (iw*2 dot columns) so the trend fills the card.
	stretched := stretchSeries(history, iw*2)
	lines = append(lines, render.BrailleArea(stretched, iw, chartRows, gradient)...)
	return render.Panel(title, strings.Join(lines, "\n"), cw, statCardHeight, panelChrome)
}

// stretchSeries nearest-index resamples values up to n points so short
// histories fill the full chart width instead of right-aligning.
func stretchSeries(values []float64, n int) []float64 {
	if len(values) == 0 || n <= len(values) {
		return values
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = values[i*len(values)/n]
	}
	return out
}

// boldLabelStyle renders stat-card values: bold, label color (grot Stat default).
var boldLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(grotTheme.Label))
