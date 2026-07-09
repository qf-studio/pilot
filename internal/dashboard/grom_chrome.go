package dashboard

// Card chrome shared with grom (github.com/qf-studio/grom): the dashboard
// renders with the same pkg/tui/render primitives and "pilot" theme grom
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
// colors; system messages are lowercase to match the grom card titles.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/qf-studio/grom/pkg/tui/render"
	"github.com/qf-studio/grom/pkg/tui/theme"
)

// gromTheme is the shared design system ("pilot" theme).
var gromTheme = theme.Pilot

// panelChrome is the default card chrome: slate border, bold label title.
var panelChrome = render.PanelStyle{
	Border: gromTheme.BorderStyle(),
	Title:  gromTheme.TitleStyle(),
}

// focusChrome highlights the focused card with an accent border.
var focusChrome = render.PanelStyle{
	Border: gromTheme.FocusBorderStyle(),
	Title:  gromTheme.TitleStyle(),
}

// warnChrome is the amber chrome used by the update-notification card.
var warnChrome = render.PanelStyle{
	Border: gromTheme.WarningStyle(),
	Title:  gromTheme.WarningStyle().Bold(true),
}

// renderPanel builds a grom-style card: lowercase title embedded in the top
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

// buildStatCard renders a grom-style stat card: big bold centered value, a
// centered detail line, and a subdued braille trend band at the bottom —
// the row-1 card from the grom demo gallery.
func buildStatCard(title, value, detail string, history []float64, accentHex string, cw int) string {
	iw, ih := render.InnerSize(cw, statCardHeight)
	chartRows := ih - 2
	lines := []string{
		render.Center(value, iw),
		render.Center(detail, iw),
	}
	// Uniform tone: at 2 chart rows a per-row gradient collapses into two
	// flat bands (dark dots below, light dots above), so render one color.
	tone := render.Dim(accentHex, 0.85)
	gradient := render.GradientStyles([]string{tone, tone}, chartRows)
	// BrailleArea right-aligns short series; stretch the 7-day history across
	// the full band width (iw*2 dot columns) so the trend fills the card.
	stretched := stretchSeries(history, iw*2)
	lines = append(lines, render.BrailleArea(stretched, iw, chartRows, gradient)...)
	return render.Panel(title, strings.Join(lines, "\n"), cw, statCardHeight, panelChrome)
}

// buildStatCardStacked is buildStatCard with a two-tone trend band:
// render.BrailleStacked draws series[0] as the base mass and later series
// stacked on top, each in a FLAT per-series color (no per-row gradient, so
// no banding). Pair a dim base with a bright cap so the split reads without
// a legend — the card's detail line carries the color key.
func buildStatCardStacked(title, value, detail string, series [][]float64, colors []string, cw int) string {
	iw, ih := render.InnerSize(cw, statCardHeight)
	chartRows := ih - 2
	lines := []string{
		render.Center(value, iw),
		render.Center(detail, iw),
	}
	// Same stretch as buildStatCard: BrailleStacked right-aligns short
	// series, so resample each onto the full dot-column width first.
	stretched := make([][]float64, len(series))
	for i, s := range series {
		stretched[i] = stretchSeries(s, iw*2)
	}
	lines = append(lines, render.BrailleStacked(stretched, iw, chartRows, colors)...)
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

// boldLabelStyle renders stat-card values: bold, label color (grom Stat default).
var boldLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(gromTheme.Label))

// Daemon liveness dot (banner wordmark): sage bright/dim pair pulsed on the
// animation tick.
var (
	daemonDotBright = lipgloss.NewStyle().Foreground(lipgloss.Color(gromTheme.Success))
	daemonDotDim    = lipgloss.NewStyle().Foreground(lipgloss.Color(render.Dim(gromTheme.Success, 0.55)))
)

// meterTrack is the unfilled portion of segment meters (btop-style dark cells).
var meterTrack = lipgloss.NewStyle().Foreground(lipgloss.Color(gromTheme.DimMore))

// meterStops builds the dim→full gradient stops for a segment meter.
func meterStops(hex string) []string {
	return []string{render.Dim(hex, 0.6), hex}
}

// segmentMeter renders a done/total grom segment meter (■■■□□) in the accent.
func segmentMeter(done, total, width int, accentHex string) string {
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
	}
	return render.SegmentMeter(frac, width, meterStops(accentHex), meterTrack)
}

// stageMeter renders the 7-rung pipeline ladder as a grom segment meter.
// Color carries the outcome: sage when the run climbed the whole ladder,
// rose when it died on a rung, accent while still climbing; a run with no
// stage evidence (StageInfo.Known == false) renders as an all-track meter.
func stageMeter(info StageInfo, width int) string {
	if !info.Known {
		return meterTrack.Render(strings.Repeat("■", width))
	}
	hex := gromTheme.Accent
	switch {
	case info.Muted:
		hex = gromTheme.Dim // terminal non-ladder outcome: not climbing, not failed
	case info.Failed:
		hex = gromTheme.Error
	case info.Reached >= stageLadderTotal:
		hex = gromTheme.Success
	}
	return segmentMeter(info.Reached, stageLadderTotal, width, hex)
}
