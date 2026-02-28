package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// GitGraphMode represents the on/off toggle state.
type GitGraphMode int

const (
	GitGraphHidden  GitGraphMode = iota
	GitGraphVisible              // auto-selects Small/Medium/Full based on available width
)

// gitGraphSize is the effective display size, auto-selected from available width.
type gitGraphSize int

const (
	gitGraphSizeSmall  gitGraphSize = iota // graph + message only, title "GIT"
	gitGraphSizeMedium                     // graph + refs + message (no SHA/author)
	gitGraphSizeFull                       // graph + refs + message + SHA + author, title "GIT GRAPH"
)

// gitRefreshMsg is sent after a background git graph refresh completes.
type gitRefreshMsg struct {
	state *GitGraphState
}

// GitGraphState holds the parsed git graph data.
type GitGraphState struct {
	Lines       []GitGraphLine `json:"lines"`
	TotalCount  int            `json:"total_count"`
	Error       string         `json:"error,omitempty"`
	LastRefresh time.Time      `json:"last_refresh"`
}

// GitGraphLine represents one parsed line of git log --graph output.
type GitGraphLine struct {
	GraphChars string `json:"graph_chars"`           // Branch drawing characters (│ ├╌╮ etc.)
	Refs       string `json:"refs,omitempty"`        // Branch/tag decorations
	Message    string `json:"message,omitempty"`     // Commit message
	Author     string `json:"author,omitempty"`      // Author name (short)
	SHA        string `json:"sha,omitempty"`         // Short SHA (7 chars)
}

// branchColors cycles per track column (left → right).
// Track 0 (main trunk) is steel blue; others cycle through the palette.
var branchColors = []string{
	"#7eb8da", // steel blue  (track 0 — usually main)
	"#7ec699", // sage green
	"#d4a054", // amber
	"#d48a8a", // dusty rose
	"#8b949e", // mid gray
}

// Graph line styles (initialized once).
var (
	graphMsgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#c9d1d9")) // light gray
	graphAuthorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")) // mid gray
	graphSHAStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6e7681")) // gray dim
	graphBranchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7ec699")) // sage green
	graphTagStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#d4a054")) // amber bold
	graphHEADStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7eb8da")) // steel bold
	graphScrollStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))             // mid gray
)

// FetchGitGraph runs git fetch --prune then git log --graph, returns parsed state.
// Called from a background goroutine via tea.Cmd.
func FetchGitGraph(projectPath string, limit int) *GitGraphState {
	state := &GitGraphState{LastRefresh: time.Now()}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// git fetch --prune (best-effort; ignore errors so offline still works)
	fetchCmd := exec.CommandContext(ctx, "git", "-C", projectPath, "fetch", "--prune")
	if err := fetchCmd.Run(); err != nil {
		slog.Debug("git fetch --prune failed (non-fatal)", slog.String("err", err.Error()))
	}

	// git log --graph with custom format: sha|author|refs|message
	format := "%H|%aN|%D|%s"
	logCmd := exec.CommandContext(ctx, "git", "-C", projectPath,
		"log", "--graph", "--all", "--oneline",
		"--decorate=full",
		fmt.Sprintf("--pretty=format:%%x00%s", format),
		fmt.Sprintf("-n%d", limit),
	)
	out, err := logCmd.Output()
	if err != nil {
		state.Error = err.Error()
		return state
	}

	state.Lines = ParseGitGraphOutput(string(out))
	state.TotalCount = CountGitCommits(projectPath)
	return state
}

// CountGitCommits returns the total number of commits in the repo (best-effort).
func CountGitCommits(projectPath string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", projectPath, "rev-list", "--count", "--all")
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// ParseGitGraphOutput splits raw git log output into structured GitGraphLine entries.
// Git outputs each commit as: <graph_chars>\x00sha|author|refs|message
// Non-commit graph lines (pure branch connectors) get only GraphChars set.
func ParseGitGraphOutput(raw string) []GitGraphLine {
	var lines []GitGraphLine

	for _, rawLine := range strings.Split(raw, "\n") {
		if rawLine == "" {
			continue
		}

		// Does this line contain the null-byte separator we injected?
		nulIdx := strings.IndexByte(rawLine, 0x00)
		if nulIdx < 0 {
			// Pure graph connector line (│, ├╌╮, etc.)
			gl := GitGraphLine{
				GraphChars: TranslateGraphChars(rawLine),
			}
			lines = append(lines, gl)
			continue
		}

		graphPart := rawLine[:nulIdx]
		dataPart := rawLine[nulIdx+1:]

		parts := strings.SplitN(dataPart, "|", 4)
		gl := GitGraphLine{
			GraphChars: TranslateGraphChars(graphPart),
		}
		if len(parts) >= 1 {
			sha := strings.TrimSpace(parts[0])
			if len(sha) >= 7 {
				gl.SHA = sha[:7]
			} else {
				gl.SHA = sha
			}
		}
		if len(parts) >= 2 {
			gl.Author = AbbreviateAuthor(strings.TrimSpace(parts[1]))
		}
		if len(parts) >= 3 {
			gl.Refs = strings.TrimSpace(parts[2])
		}
		if len(parts) >= 4 {
			gl.Message = strings.TrimSpace(parts[3])
		}

		lines = append(lines, gl)
	}
	return lines
}

// TranslateGraphChars replaces standard git graph characters with our design set.
//
// Git outputs: * | \ / -
// Our set:     ● │ ╮ ╯ ╌
//
// The tricky part is branch-off and merge patterns:
//   git: |\ → our: ├╌╮
//   git: |/ → our: ├╌╯
//   git: |_ → our: ├╌╌
func TranslateGraphChars(s string) string {
	// Replace git graph chars with our aesthetic set.
	// Process character by character to handle multi-byte sequences.
	var b strings.Builder
	runes := []rune(s)
	n := len(runes)
	for i := 0; i < n; i++ {
		r := runes[i]
		switch r {
		case '*':
			b.WriteRune('●')
		case '|':
			// Check if next non-space char is '\' or '/'
			b.WriteRune('│')
		case '\\':
			// Branch off: replace with ╮, but prefix needs ╌
			// The preceding '|' became '│'; we need ├╌╮ pattern.
			// Simpler: just output ╮ and handle ├ separately in colorize.
			b.WriteRune('╮')
		case '/':
			b.WriteRune('╯')
		case '-':
			b.WriteRune('╌')
		case '_':
			b.WriteRune('╌')
		default:
			b.WriteRune(r)
		}
	}

	// Post-process: fix junction characters.
	// Git outputs "├" as "|" adjacent to "\" — we need to replace
	// "│╮" with "├╌╮" and "│╯" with "├╌╯".
	result := b.String()
	result = strings.ReplaceAll(result, "│╮", "├╌╮")
	result = strings.ReplaceAll(result, "│╯", "├╌╯")
	result = strings.ReplaceAll(result, "│╌", "├╌")

	return result
}

// AbbreviateAuthor shortens an author name to fit in the full-mode column.
// "Firstname Lastname" → "F. Lastname" if > 10 chars.
func AbbreviateAuthor(name string) string {
	if len(name) <= 10 {
		return name
	}
	parts := strings.Fields(name)
	if len(parts) >= 2 {
		return string([]rune(parts[0])[0:1]) + ". " + parts[len(parts)-1]
	}
	if len(name) > 10 {
		return name[:10]
	}
	return name
}

// colorizeGraphChars applies branch track colors to graph characters.
// Track position is determined by the column index of the first branch character.
func colorizeGraphChars(graphStr string) string {
	runes := []rune(graphStr)
	var b strings.Builder

	// Determine colors by tracking position (each 2-char cell = one track column).
	for i, r := range runes {
		track := i / 2 // rough track assignment by character position
		color := branchColors[track%len(branchColors)]
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))

		switch r {
		case '●', '│', '├', '╌', '╮', '╯':
			b.WriteString(style.Render(string(r)))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// colorizeRefs applies colors to refs string:
//   - HEAD → main: steel bold
//   - branch names: sage green
//   - tag names (refs/tags/): amber bold
func colorizeRefs(refs string) string {
	if refs == "" {
		return ""
	}

	// Parse individual ref tokens separated by ", "
	tokens := strings.Split(refs, ", ")
	var styled []string
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		// Strip the long-form prefixes from --decorate=full
		tok = strings.TrimPrefix(tok, "refs/remotes/")
		tok = strings.TrimPrefix(tok, "refs/heads/")
		tok = strings.TrimPrefix(tok, "refs/")

		switch {
		case strings.HasPrefix(tok, "HEAD"):
			styled = append(styled, graphHEADStyle.Render(tok))
		case strings.HasPrefix(tok, "tag: "):
			tagName := strings.TrimPrefix(tok, "tag: ")
			tagName = strings.TrimPrefix(tagName, "tags/")
			styled = append(styled, graphTagStyle.Render(tagName))
		default:
			styled = append(styled, graphBranchStyle.Render(tok))
		}
	}

	if len(styled) == 0 {
		return ""
	}
	return "(" + strings.Join(styled, ", ") + ")"
}

// renderGraphLineFull renders one line in Full mode:
//   graph + refs + message + author + SHA (fills width)
func renderGraphLineFull(line GitGraphLine, width int) string {
	graphColored := colorizeGraphChars(line.GraphChars)
	graphWidth := lipgloss.Width(line.GraphChars) // visual width without ANSI

	// If no commit data (pure graph connector), just return the graph part padded.
	if line.SHA == "" {
		padding := width - graphWidth
		if padding < 0 {
			padding = 0
		}
		return graphColored + strings.Repeat(" ", padding)
	}

	// Right-side fixed fields: SHA (7) + space (1) + author (10) = 18 chars
	const shaWidth = 7
	const authorWidth = 10
	const rightFixed = shaWidth + 1 + authorWidth // 18

	styledSHA := graphSHAStyle.Render(fmt.Sprintf("%-7s", line.SHA))
	styledAuthor := graphAuthorStyle.Render(fmt.Sprintf("%-10s", AbbreviateAuthor(line.Author)))
	right := styledSHA + " " + styledAuthor // 18 visual chars

	// Refs: rendered with colors; measure unstyled refs width.
	styledRefs := colorizeRefs(line.Refs)
	refsWidth := 0
	if styledRefs != "" {
		// Measure plain text width: "(" + refs + ") "
		plainRefs := "(" + collapseRefs(line.Refs) + ") "
		refsWidth = lipgloss.Width(plainRefs)
		styledRefs += " " // trailing space
	}

	// Message: fills remaining width
	msgWidth := width - graphWidth - refsWidth - rightFixed - 1 // -1 for space before right
	if msgWidth < 5 {
		msgWidth = 5
	}
	styledMsg := graphMsgStyle.Render(padOrTruncate(line.Message, msgWidth))

	return graphColored + styledRefs + styledMsg + " " + right
}

// renderGraphLineSmall renders one line in Small mode:
//   graph + truncated message only (no refs/author/SHA)
func renderGraphLineSmall(line GitGraphLine, width int) string {
	graphColored := colorizeGraphChars(line.GraphChars)
	graphWidth := lipgloss.Width(line.GraphChars)

	if line.SHA == "" {
		padding := width - graphWidth
		if padding < 0 {
			padding = 0
		}
		return graphColored + strings.Repeat(" ", padding)
	}

	msgWidth := width - graphWidth
	if msgWidth < 5 {
		msgWidth = 5
	}
	styledMsg := graphMsgStyle.Render(padOrTruncate(line.Message, msgWidth))
	return graphColored + styledMsg
}

// renderGraphLineMedium renders one line in Medium mode:
//   graph + refs + message (no author/SHA)
func renderGraphLineMedium(line GitGraphLine, width int) string {
	graphColored := colorizeGraphChars(line.GraphChars)
	graphWidth := lipgloss.Width(line.GraphChars)

	if line.SHA == "" {
		padding := width - graphWidth
		if padding < 0 {
			padding = 0
		}
		return graphColored + strings.Repeat(" ", padding)
	}

	// Refs
	styledRefs := colorizeRefs(line.Refs)
	refsWidth := 0
	if styledRefs != "" {
		plainRefs := "(" + collapseRefs(line.Refs) + ") "
		refsWidth = lipgloss.Width(plainRefs)
		styledRefs += " "
	}

	msgWidth := width - graphWidth - refsWidth
	if msgWidth < 5 {
		msgWidth = 5
	}
	styledMsg := graphMsgStyle.Render(padOrTruncate(line.Message, msgWidth))
	return graphColored + styledRefs + styledMsg
}

// collapseRefs returns a plain-text version of the refs string for width measurement.
// Strips long-form prefixes (refs/heads/, refs/remotes/, etc.).
func collapseRefs(refs string) string {
	if refs == "" {
		return ""
	}
	tokens := strings.Split(refs, ", ")
	var out []string
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		tok = strings.TrimPrefix(tok, "refs/remotes/")
		tok = strings.TrimPrefix(tok, "refs/heads/")
		tok = strings.TrimPrefix(tok, "refs/")
		tok = strings.TrimPrefix(tok, "tag: tags/")
		tok = strings.TrimPrefix(tok, "tag: ")
		if tok != "" {
			out = append(out, tok)
		}
	}
	return strings.Join(out, ", ")
}

// refreshGitGraphCmd returns a tea.Cmd that fetches git graph data in the background.
func refreshGitGraphCmd(projectPath string) tea.Cmd {
	return func() tea.Msg {
		state := FetchGitGraph(projectPath, 200)
		return gitRefreshMsg{state: state}
	}
}

// gitRefreshTickCmd returns a 15-second tick that triggers a graph refresh.
func gitRefreshTickCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(_ time.Time) tea.Msg {
		return gitRefreshTickMsg{}
	})
}

// gitRefreshTickMsg fires every 15 seconds when the graph is visible.
type gitRefreshTickMsg struct{}

// gitGraphViewportHeight returns how many content lines fit in the git graph panel.
// Panel structure: top border(1) + empty line(1) + [content] + empty line(1) + bottom border(1) + scroll indicator(1) = 5 overhead.
func (m Model) gitGraphViewportHeight() int {
	if m.height > 5 {
		return m.height - 5
	}
	return 1
}

// renderGitGraph renders the git graph panel for the current model state.
// Returns an empty string if hidden. Optional variadic args:
//   - opts[0] = forceWidth: panel width (stacked layout uses full terminal width)
//   - opts[1] = forceHeight: panel height (stacked layout uses remaining terminal space)
func (m Model) renderGitGraph(opts ...int) string {
	if m.gitGraphMode == GitGraphHidden {
		return ""
	}

	var graphWidth int
	var forceHeight int
	if len(opts) > 0 && opts[0] > 0 {
		graphWidth = opts[0]
	}
	if len(opts) > 1 && opts[1] > 0 {
		forceHeight = opts[1]
	}
	if graphWidth == 0 {
		// Side-by-side layout: calculate from remaining terminal width
		graphWidth = 60 // default when terminal width unknown
		if m.width > 0 {
			graphWidth = m.width - panelTotalWidth - 2
		}
	}
	if graphWidth < 20 {
		return ""
	}

	// Auto-select size based on available width
	size := gitGraphSizeFull
	if graphWidth < 46 {
		size = gitGraphSizeMedium
	}
	if graphWidth < 28 {
		size = gitGraphSizeSmall
	}

	// Title based on size
	title := "GIT"
	if size == gitGraphSizeFull {
		title = "GIT GRAPH"
	}

	// Build content lines
	innerWidth := graphWidth - 4 // border(1) + space(1) + space(1) + border(1)

	var contentLines []string
	var scrollIndicator string

	// Error or loading state
	if m.gitGraphState == nil {
		contentLines = append(contentLines, "  Loading...")
	} else if m.gitGraphState.Error != "" {
		contentLines = append(contentLines, "  "+truncateVisual(m.gitGraphState.Error, innerWidth-2))
	} else {
		lines := m.gitGraphState.Lines
		total := len(lines)

		// Apply scroll offset
		start := m.gitGraphScroll
		if start >= total {
			start = 0
		}

		// Calculate visible lines from panel height
		panelHeight := m.height
		if forceHeight > 0 {
			panelHeight = forceHeight
		}
		visibleLines := 30 // fallback when height unknown
		if panelHeight > 0 {
			visibleLines = panelHeight - 5 // borders(2) + padding(2) + scroll indicator(1)
			if visibleLines < 1 {
				visibleLines = 1
			}
		}

		// Clamp scroll offset
		maxScroll := total - visibleLines
		if maxScroll < 0 {
			maxScroll = 0
		}
		if start > maxScroll {
			start = maxScroll
		}

		end := start + visibleLines
		if end > total {
			end = total
		}

		for _, line := range lines[start:end] {
			var rendered string
			switch size {
			case gitGraphSizeSmall:
				rendered = renderGraphLineSmall(line, innerWidth)
			case gitGraphSizeMedium:
				rendered = renderGraphLineMedium(line, innerWidth)
			default:
				rendered = renderGraphLineFull(line, innerWidth)
			}
			contentLines = append(contentLines, rendered)
		}

		// Build scroll indicator
		if total > 0 {
			indicator := fmt.Sprintf("[%d-%d of %d]", start+1, end, total)
			scrollIndicator = padOrTruncate(graphScrollStyle.Render(indicator), innerWidth)
		}
	}

	// Full-height stretch: pad content lines to fill panel height
	stretchHeight := m.height
	if forceHeight > 0 {
		stretchHeight = forceHeight
	}
	if stretchHeight > 0 {
		contentArea := stretchHeight - 4
		if contentArea < 1 {
			contentArea = 1
		}
		indicatorReserve := 0
		if scrollIndicator != "" {
			indicatorReserve = 1
		}
		for len(contentLines) < contentArea-indicatorReserve {
			contentLines = append(contentLines, "")
		}
		if scrollIndicator != "" {
			contentLines = append(contentLines, scrollIndicator)
		}
	} else if scrollIndicator != "" {
		contentLines = append(contentLines, "")
		contentLines = append(contentLines, scrollIndicator)
	}

	return m.renderGraphPanel(title, contentLines, graphWidth)
}

// renderGraphPanel builds a bordered panel at the given total width.
// Focused state uses steel blue border; unfocused uses slate.
func (m Model) renderGraphPanel(title string, contentLines []string, totalWidth int) string {
	var borderSty lipgloss.Style
	if m.gitGraphFocus {
		borderSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#7eb8da")) // steel blue
	} else {
		borderSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#3d4450")) // slate
	}

	innerWidth := totalWidth - 4 // border + space + space + border
	titleUpper := strings.ToUpper(title)

	// Top border
	prefixStr := "╭─ " + titleUpper + " "
	prefixWidth := lipgloss.Width(prefixStr)
	dashCount := totalWidth - prefixWidth - 1
	if dashCount < 0 {
		dashCount = 0
	}
	topBorder := borderSty.Render("╭─ ") + labelStyle.Render(titleUpper) +
		borderSty.Render(" "+strings.Repeat("─", dashCount)+"╮")

	// Empty line
	emptyLine := borderSty.Render("│") + strings.Repeat(" ", totalWidth-2) + borderSty.Render("│")

	// Content lines
	border := borderSty.Render("│")
	var renderedLines []string
	renderedLines = append(renderedLines, topBorder)
	renderedLines = append(renderedLines, emptyLine)
	for _, line := range contentLines {
		adjusted := padOrTruncate(line, innerWidth)
		renderedLines = append(renderedLines, border+" "+adjusted+" "+border)
	}
	renderedLines = append(renderedLines, emptyLine)

	// Bottom border
	dashCount = totalWidth - 2
	bottomBorder := borderSty.Render("╰" + strings.Repeat("─", dashCount) + "╯")
	renderedLines = append(renderedLines, bottomBorder)

	return strings.Join(renderedLines, "\n")
}
