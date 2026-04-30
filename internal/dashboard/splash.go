package dashboard

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/qf-studio/pilot/internal/banner"
)

// splashTickMsg is sent every 200ms during the splash animation.
type splashTickMsg time.Time

// splashLampCount is the number of boot indicator lamps.
const splashLampCount = 4

// splashMaxTicks controls how many 200ms ticks the splash runs before exiting.
// 12 ticks = 2.4 seconds.
const splashMaxTicks = 12

// lampLit and lampDim are the Unicode characters for active and inactive lamps.
const lampLit = "◉"
const lampDim = "◎"

// SplashModel is the bubbletea model for the startup splash screen.
// It shows the ASCII logo, animates 4 boot lamps at 200ms cadence,
// then signals READY and exits so the main dashboard can start.
type SplashModel struct {
	lampState int  // index of the currently lit lamp (0–3)
	tick      int  // total tick count since Init
	done      bool // true when splash has finished
}

// NewSplashModel creates a new SplashModel ready to run.
func NewSplashModel() SplashModel {
	return SplashModel{}
}

// Init starts the 200ms lamp ticker.
func (m SplashModel) Init() tea.Cmd {
	return splashTickCmd()
}

func splashTickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return splashTickMsg(t)
	})
}

// Update handles tick and key messages.
func (m SplashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case splashTickMsg:
		m.tick++
		m.lampState = m.tick % splashLampCount
		if m.tick >= splashMaxTicks {
			m.done = true
			return m, tea.Quit
		}
		return m, splashTickCmd()
	case tea.KeyMsg:
		// Any key skips the splash immediately.
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

// View renders the splash screen: ASCII logo + boot lamps + READY footer.
func (m SplashModel) View() string {
	if m.done {
		return ""
	}
	var sb strings.Builder

	// ASCII logo (trim leading newline added by the banner constant)
	logo := strings.TrimPrefix(banner.Logo, "\n")
	sb.WriteString(titleStyle.Render(logo))
	sb.WriteString("\n")

	// Boot lamps row
	sb.WriteString("   " + renderSplashLamps(m.lampState) + "\n")
	sb.WriteString("\n")

	// READY footer appears in the last two ticks before exit
	if m.tick >= splashMaxTicks-2 {
		sb.WriteString("   " + statusCompletedStyle.Render("READY") + "\n")
	} else {
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderSplashLamps returns a string of 4 lamps with the active one highlighted.
func renderSplashLamps(active int) string {
	parts := make([]string, splashLampCount)
	for i := range parts {
		if i == active {
			parts[i] = titleStyle.Render(lampLit)
		} else {
			parts[i] = dimStyle.Render(lampDim)
		}
	}
	return strings.Join(parts, "  ")
}
