package replay

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ViewerModel is the bubbletea model for the interactive replay viewer
type ViewerModel struct {
	recording   *Recording
	events      []*StreamEvent
	current     int
	playing     bool
	speed       float64 // 0.5, 1.0, 2.0, 4.0
	filter      EventFilter
	filteredIdx []int // Indices into events that match filter
	width       int
	height      int
	scrollY     int
	showHelp    bool
	quit        bool
}

// EventFilter controls which events are displayed
type EventFilter struct {
	ShowTools   bool
	ShowText    bool
	ShowResults bool
	ShowSystem  bool
	ShowErrors  bool
}

// DefaultEventFilter returns a filter showing all events
func DefaultEventFilter() EventFilter {
	return EventFilter{
		ShowTools:   true,
		ShowText:    true,
		ShowResults: true,
		ShowSystem:  true,
		ShowErrors:  true,
	}
}

// NewViewerModel creates a new interactive viewer
func NewViewerModel(recording *Recording) (*ViewerModel, error) {
	events, err := LoadStreamEvents(recording)
	if err != nil {
		return nil, fmt.Errorf("failed to load events: %w", err)
	}

	m := &ViewerModel{
		recording: recording,
		events:    events,
		current:   0,
		playing:   false,
		speed:     1.0,
		filter:    DefaultEventFilter(),
		width:     80,
		height:    24,
		scrollY:   0,
		showHelp:  false,
	}

	m.applyFilter()
	return m, nil
}

// Styles
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("39")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	currentEventStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("212"))

	eventStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Padding(0, 1)

	progressStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))
)

// Init implements tea.Model
func (m *ViewerModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model
func (m *ViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quit = true
			return m, tea.Quit

		case " ", "p": // Space/P = play/pause toggle
			m.playing = !m.playing
			if m.playing {
				return m, m.tickCmd()
			}

		case "enter", "n": // Next event
			m.playing = false
			m.nextEvent()

		case "N", "shift+n": // Previous event
			m.playing = false
			m.prevEvent()

		case "g": // Go to start
			m.current = 0
			m.updateScroll()

		case "G": // Go to end
			if len(m.filteredIdx) > 0 {
				m.current = len(m.filteredIdx) - 1
				m.updateScroll()
			}

		case "1": // Speed 0.5x
			m.speed = 0.5
		case "2": // Speed 1x
			m.speed = 1.0
		case "3": // Speed 2x
			m.speed = 2.0
		case "4": // Speed 4x
			m.speed = 4.0

		case "t": // Toggle tools
			m.filter.ShowTools = !m.filter.ShowTools
			m.applyFilter()
		case "x": // Toggle text
			m.filter.ShowText = !m.filter.ShowText
			m.applyFilter()
		case "r": // Toggle results
			m.filter.ShowResults = !m.filter.ShowResults
			m.applyFilter()
		case "s": // Toggle system
			m.filter.ShowSystem = !m.filter.ShowSystem
			m.applyFilter()
		case "e": // Toggle errors
			m.filter.ShowErrors = !m.filter.ShowErrors
			m.applyFilter()
		case "a": // Show all
			m.filter = DefaultEventFilter()
			m.applyFilter()

		case "?", "h": // Toggle help
			m.showHelp = !m.showHelp

		case "up", "k":
			m.playing = false
			m.prevEvent()
		case "down", "j":
			m.playing = false
			m.nextEvent()

		case "pgup":
			for i := 0; i < 10; i++ {
				m.prevEvent()
			}
		case "pgdown":
			for i := 0; i < 10; i++ {
				m.nextEvent()
			}
		}

	case tickMsg:
		if m.playing {
			m.nextEvent()
			if m.current >= len(m.filteredIdx)-1 {
				m.playing = false
				return m, nil
			}
			return m, m.tickCmd()
		}
	}

	return m, nil
}

type tickMsg struct{}

func (m *ViewerModel) tickCmd() tea.Cmd {
	delay := time.Duration(float64(200*time.Millisecond) / m.speed)
	return tea.Tick(delay, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m *ViewerModel) nextEvent() {
	if m.current < len(m.filteredIdx)-1 {
		m.current++
		m.updateScroll()
	}
}

func (m *ViewerModel) prevEvent() {
	if m.current > 0 {
		m.current--
		m.updateScroll()
	}
}

func (m *ViewerModel) updateScroll() {
	visibleLines := m.height - 8 // Header + footer
	if m.current < m.scrollY {
		m.scrollY = m.current
	} else if m.current >= m.scrollY+visibleLines {
		m.scrollY = m.current - visibleLines + 1
	}
}

func (m *ViewerModel) applyFilter() {
	m.filteredIdx = nil
	for i, event := range m.events {
		if m.eventMatchesFilter(event) {
			m.filteredIdx = append(m.filteredIdx, i)
		}
	}
	if m.current >= len(m.filteredIdx) {
		m.current = 0
	}
}

func (m *ViewerModel) eventMatchesFilter(event *StreamEvent) bool {
	if event.Parsed == nil {
		return m.filter.ShowSystem
	}

	p := event.Parsed

	// Error filter takes precedence
	if p.IsError && m.filter.ShowErrors {
		return true
	}

	switch p.Type {
	case "system":
		return m.filter.ShowSystem
	case "assistant":
		if p.ToolName != "" {
			return m.filter.ShowTools
		}
		if p.Text != "" {
			return m.filter.ShowText
		}
		return m.filter.ShowSystem
	case "user":
		return m.filter.ShowResults
	case "result":
		if p.IsError {
			return m.filter.ShowErrors
		}
		return m.filter.ShowResults
	default:
		return m.filter.ShowSystem
	}
}

// View implements tea.Model
func (m *ViewerModel) View() string {
	if m.quit {
		return ""
	}

	if m.showHelp {
		return m.renderHelp()
	}

	var sb strings.Builder

	// Header
	header := m.renderHeader()
	sb.WriteString(header)
	sb.WriteString("\n")

	// Events
	visibleLines := m.height - 8
	if visibleLines < 5 {
		visibleLines = 5
	}

	start := m.scrollY
	end := start + visibleLines
	if end > len(m.filteredIdx) {
		end = len(m.filteredIdx)
	}

	for i := start; i < end; i++ {
		eventIdx := m.filteredIdx[i]
		event := m.events[eventIdx]

		isCurrent := i == m.current
		line := m.renderEvent(event, eventIdx, isCurrent)
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Pad remaining lines
	for i := end - start; i < visibleLines; i++ {
		sb.WriteString("\n")
	}

	// Footer
	footer := m.renderFooter()
	sb.WriteString(footer)

	return sb.String()
}

func (m *ViewerModel) renderHeader() string {
	var sb strings.Builder

	// Title bar
	title := fmt.Sprintf(" ▶ %s ", m.recording.ID)
	taskInfo := fmt.Sprintf(" Task: %s ", m.recording.TaskID)

	titleStyled := headerStyle.Render(title)
	taskStyled := statusStyle.Render(taskInfo)

	sb.WriteString(titleStyled)
	sb.WriteString(" ")
	sb.WriteString(taskStyled)
	sb.WriteString("\n")

	// Progress bar
	if len(m.filteredIdx) > 0 {
		progress := float64(m.current+1) / float64(len(m.filteredIdx))
		barWidth := m.width - 30
		if barWidth < 10 {
			barWidth = 10
		}
		filled := int(progress * float64(barWidth))
		bar := strings.Repeat("━", filled) + strings.Repeat("─", barWidth-filled)

		playState := "⏸"
		if m.playing {
			playState = "▶"
		}

		speedStr := fmt.Sprintf("%.1fx", m.speed)
		progressStr := fmt.Sprintf("%s %s [%d/%d] %s",
			playState,
			progressStyle.Render(bar),
			m.current+1,
			len(m.filteredIdx),
			speedStr,
		)
		sb.WriteString(progressStr)
	}
	sb.WriteString("\n")

	// Separator
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	return sb.String()
}

func (m *ViewerModel) renderEvent(event *StreamEvent, idx int, isCurrent bool) string {
	var sb strings.Builder

	// Timestamp and sequence
	ts := event.Timestamp.Format("15:04:05")
	prefix := fmt.Sprintf("%s #%-4d ", timestampStyle.Render(ts), event.Sequence)

	if isCurrent {
		prefix = "▶ " + prefix
	} else {
		prefix = "  " + prefix
	}

	sb.WriteString(prefix)

	// Event content
	content := m.formatEventContent(event)

	style := eventStyle
	if isCurrent {
		style = currentEventStyle
	}
	if event.Parsed != nil && event.Parsed.IsError {
		style = errorStyle
	}

	// Truncate to fit width
	maxLen := m.width - len(prefix) - 2
	if maxLen < 10 {
		maxLen = 10
	}
	if len(content) > maxLen {
		content = content[:maxLen-3] + "..."
	}

	sb.WriteString(style.Render(content))

	return sb.String()
}

func (m *ViewerModel) formatEventContent(event *StreamEvent) string {
	if event.Parsed == nil {
		return fmt.Sprintf("(%s)", event.Type)
	}

	p := event.Parsed

	switch p.Type {
	case "system":
		if p.Subtype == "init" {
			return "🚀 System initialized"
		}
		return fmt.Sprintf("⚙️ System: %s", p.Subtype)

	case "assistant":
		if p.ToolName != "" {
			icon := getToolIcon(p.ToolName)
			detail := formatToolDetail(p)
			if detail != "" {
				return fmt.Sprintf("%s %s: %s", icon, p.ToolName, detail)
			}
			return fmt.Sprintf("%s %s", icon, p.ToolName)
		}
		if p.Text != "" {
			text := strings.ReplaceAll(p.Text, "\n", " ")
			text = strings.TrimSpace(text)
			return fmt.Sprintf("💬 %s", text)
		}
		return "📝 (assistant)"

	case "user":
		return "📥 Tool result"

	case "result":
		if p.IsError {
			return fmt.Sprintf("❌ Error: %s", truncate(p.Result, 60))
		}
		tokens := ""
		if p.InputTokens > 0 || p.OutputTokens > 0 {
			tokens = fmt.Sprintf(" (%d in, %d out)", p.InputTokens, p.OutputTokens)
		}
		return fmt.Sprintf("✅ Completed%s", tokens)

	default:
		return fmt.Sprintf("(%s)", p.Type)
	}
}

func (m *ViewerModel) renderFooter() string {
	var sb strings.Builder

	// Separator
	sb.WriteString(strings.Repeat("─", m.width))
	sb.WriteString("\n")

	// Filter status
	filters := []string{}
	if m.filter.ShowTools {
		filters = append(filters, "Tools")
	}
	if m.filter.ShowText {
		filters = append(filters, "Text")
	}
	if m.filter.ShowResults {
		filters = append(filters, "Results")
	}
	if m.filter.ShowSystem {
		filters = append(filters, "System")
	}
	if m.filter.ShowErrors {
		filters = append(filters, "Errors")
	}

	filterStr := fmt.Sprintf("Showing: %s", strings.Join(filters, ", "))
	sb.WriteString(statusStyle.Render(filterStr))
	sb.WriteString("\n")

	// Help hints
	help := "Space: Play/Pause │ ←→: Navigate │ 1-4: Speed │ t/x/r/s/e: Filter │ ?: Help │ q: Quit"
	sb.WriteString(helpStyle.Render(help))

	return sb.String()
}

func (m *ViewerModel) renderHelp() string {
	var sb strings.Builder

	sb.WriteString(headerStyle.Render(" Interactive Replay Viewer - Help "))
	sb.WriteString("\n\n")

	help := `
  NAVIGATION
  ─────────────────────────────────────
  Space, p      Play/Pause playback
  n, Enter, ↓   Next event
  N, ↑          Previous event
  g             Go to start
  G             Go to end
  PgUp/PgDn     Jump 10 events

  SPEED CONTROL
  ─────────────────────────────────────
  1             0.5x speed (slow)
  2             1.0x speed (normal)
  3             2.0x speed (fast)
  4             4.0x speed (fastest)

  FILTERS
  ─────────────────────────────────────
  t             Toggle tool calls
  x             Toggle text/assistant
  r             Toggle results
  s             Toggle system events
  e             Toggle errors
  a             Show all events

  OTHER
  ─────────────────────────────────────
  ?, h          Toggle this help
  q, Ctrl+C     Quit viewer
`
	sb.WriteString(help)
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("Press any key to close help..."))

	return sb.String()
}

// RunViewer starts the interactive TUI viewer
func RunViewer(recording *Recording) error {
	model, err := NewViewerModel(recording)
	if err != nil {
		return err
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// RunViewerWithOptions starts the viewer with custom options
func RunViewerWithOptions(recording *Recording, startAt int, filter EventFilter) error {
	model, err := NewViewerModel(recording)
	if err != nil {
		return err
	}

	model.filter = filter
	model.applyFilter()

	// Find the filtered index for startAt
	for i, idx := range model.filteredIdx {
		if idx >= startAt {
			model.current = i
			break
		}
	}
	model.updateScroll()

	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// CheckTerminalSupport checks if the terminal supports interactive mode
func CheckTerminalSupport() bool {
	// Check if we're running in a TTY
	if fi, _ := os.Stdout.Stat(); (fi.Mode() & os.ModeCharDevice) == 0 {
		return false
	}
	return true
}
