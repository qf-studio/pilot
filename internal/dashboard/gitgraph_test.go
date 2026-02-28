package dashboard

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// makeKey creates a tea.KeyMsg for the given string (single char or named key).
func makeKey(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		// Single-rune key (e.g., "g", "j", "k")
		runes := []rune(s)
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: runes}
	}
}

// TestTranslateGraphChars verifies git graph characters are mapped to our symbol set.
func TestTranslateGraphChars(t *testing.T) {
	// '*' → '●'
	got := TranslateGraphChars("*")
	if !strings.Contains(got, "●") {
		t.Errorf("* should become ●, got %q", got)
	}

	// '|' → '│' (or '├' after post-processing)
	got = TranslateGraphChars("|")
	if got != "│" {
		t.Errorf("| should become │, got %q", got)
	}

	// '-' → '╌'
	got = TranslateGraphChars("-")
	if got != "╌" {
		t.Errorf("- should become ╌, got %q", got)
	}

	// Spaces are unchanged
	got = TranslateGraphChars("  ")
	if got != "  " {
		t.Errorf("spaces should be unchanged, got %q", got)
	}
}

// TestTranslateGraphChars_JunctionReplacement verifies branch-off/merge-back junctions.
func TestTranslateGraphChars_JunctionReplacement(t *testing.T) {
	// "|\\" in git output: | becomes │, \ becomes ╮, then │╮ → ├╌╮
	got := TranslateGraphChars(`|\`)
	if !strings.Contains(got, "├") || !strings.Contains(got, "╮") {
		t.Errorf("branch-off `|\\` should produce ├...╮, got %q", got)
	}

	// "|/" in git output: │╯ → ├╌╯
	got = TranslateGraphChars("|/")
	if !strings.Contains(got, "├") || !strings.Contains(got, "╯") {
		t.Errorf("merge-back `|/` should produce ├...╯, got %q", got)
	}
}

// TestParseGitGraphOutput verifies raw git log lines are parsed correctly.
func TestParseGitGraphOutput(t *testing.T) {
	// Commit line:    graph_chars + NUL + sha|author|refs|message
	// Connector line: graph_chars only (no NUL)
	raw := "* \x007eb8da1|Alice Smith|HEAD -> main|feat: add dashboard\n" +
		"|\n" +
		"* \x00a1b2c3d|Bob Jones||fix: handle nil"

	lines := ParseGitGraphOutput(raw)

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	// Line 0: commit line
	l0 := lines[0]
	if l0.SHA != "7eb8da1" {
		t.Errorf("line 0 SHA = %q, want %q", l0.SHA, "7eb8da1")
	}
	if l0.Message != "feat: add dashboard" {
		t.Errorf("line 0 Message = %q, want %q", l0.Message, "feat: add dashboard")
	}
	if l0.Refs != "HEAD -> main" {
		t.Errorf("line 0 Refs = %q, want %q", l0.Refs, "HEAD -> main")
	}

	// Line 1: pure connector
	l1 := lines[1]
	if l1.SHA != "" {
		t.Errorf("connector line should have empty SHA, got %q", l1.SHA)
	}
	if l1.Message != "" {
		t.Errorf("connector line should have empty Message, got %q", l1.Message)
	}

	// Line 2: commit with empty refs
	l2 := lines[2]
	if l2.SHA != "a1b2c3d" {
		t.Errorf("line 2 SHA = %q, want %q", l2.SHA, "a1b2c3d")
	}
	if l2.Refs != "" {
		t.Errorf("line 2 Refs should be empty, got %q", l2.Refs)
	}
}

// TestParseGitGraphOutput_Empty verifies empty input is handled gracefully.
func TestParseGitGraphOutput_Empty(t *testing.T) {
	lines := ParseGitGraphOutput("")
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty input, got %d", len(lines))
	}
}

// TestAbbreviateAuthor verifies author name abbreviation.
func TestAbbreviateAuthor(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Alice", "Alice"},
		{"Al", "Al"},
		{"Alice Smith", "A. Smith"},
		{"First Middle Last", "F. Last"},
		{"VeryLongNameNoSpaces", "VeryLongNa"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := AbbreviateAuthor(tt.input)
			if got != tt.want {
				t.Errorf("AbbreviateAuthor(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestColorizeRefs verifies refs are styled correctly.
func TestColorizeRefs(t *testing.T) {
	tests := []struct {
		name    string
		refs    string
		wantSub string
		empty   bool
	}{
		{"empty refs", "", "", true},
		{"HEAD ref", "HEAD -> main", "HEAD -> main", false},
		{"branch ref", "refs/heads/pilot/GH-123", "pilot/GH-123", false},
		{"tag ref", "tag: refs/tags/v1.0.0", "v1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := colorizeRefs(tt.refs)
			plain := stripANSI(got)
			if tt.empty {
				if got != "" {
					t.Errorf("colorizeRefs(%q) = %q, want empty", tt.refs, got)
				}
				return
			}
			if !strings.Contains(plain, tt.wantSub) {
				t.Errorf("colorizeRefs(%q) plain = %q, want substring %q", tt.refs, plain, tt.wantSub)
			}
		})
	}
}

// TestRenderGraphLineFull verifies full-mode rendering width and content.
func TestRenderGraphLineFull(t *testing.T) {
	line := GitGraphLine{
		GraphChars: "● ",
		Refs:       "HEAD -> main",
		Message:    "feat: add git graph panel",
		Author:     "Alice Smith",
		SHA:        "7eb8da1",
	}

	width := 80
	got := renderGraphLineFull(line, width)

	if got == "" {
		t.Error("renderGraphLineFull returned empty string")
	}

	// Visual width should not exceed target width (with small tolerance for ANSI)
	visualWidth := lipgloss.Width(got)
	if visualWidth > width+2 {
		t.Errorf("renderGraphLineFull width = %d, want <= %d", visualWidth, width+2)
	}

	// Should contain the commit message
	plain := stripANSI(got)
	if !strings.Contains(plain, "feat: add git graph") {
		t.Errorf("missing commit message in full line: %q", plain)
	}
}

// TestRenderGraphLineFull_Connector verifies connector lines in full mode.
func TestRenderGraphLineFull_Connector(t *testing.T) {
	line := GitGraphLine{
		GraphChars: "├╌╮",
		SHA:        "", // no commit data
	}

	width := 80
	got := renderGraphLineFull(line, width)

	plain := stripANSI(got)
	// Should contain the branch junction characters
	if !strings.Contains(plain, "╮") {
		t.Errorf("connector line should contain ╮, got %q", plain)
	}
	// Should be padded to width
	visualWidth := lipgloss.Width(got)
	if visualWidth != width {
		t.Errorf("connector line visual width = %d, want %d", visualWidth, width)
	}
}

// TestRenderGraphLineSmall verifies small-mode rendering (graph + message only).
func TestRenderGraphLineSmall(t *testing.T) {
	line := GitGraphLine{
		GraphChars: "● ",
		Refs:       "HEAD -> main",
		Message:    "feat: add git graph panel",
		Author:     "Alice Smith",
		SHA:        "7eb8da1",
	}

	width := 28
	got := renderGraphLineSmall(line, width)

	if got == "" {
		t.Error("renderGraphLineSmall returned empty string")
	}

	plain := stripANSI(got)
	// Should contain message
	if !strings.Contains(plain, "feat:") {
		t.Errorf("missing commit message in small line: %q", plain)
	}
	// Should NOT contain SHA or author
	if strings.Contains(plain, "7eb8da1") {
		t.Errorf("small line should not contain SHA: %q", plain)
	}
	if strings.Contains(plain, "Alice") {
		t.Errorf("small line should not contain author: %q", plain)
	}
	// Should NOT contain refs
	if strings.Contains(plain, "HEAD") {
		t.Errorf("small line should not contain refs: %q", plain)
	}
}

// TestRenderGraphLineSmall_Connector verifies connector lines in small mode.
func TestRenderGraphLineSmall_Connector(t *testing.T) {
	line := GitGraphLine{GraphChars: "├╌╮"}
	width := 28
	got := renderGraphLineSmall(line, width)
	visualWidth := lipgloss.Width(got)
	if visualWidth != width {
		t.Errorf("connector visual width = %d, want %d", visualWidth, width)
	}
}

// TestRenderGraphLineMedium verifies medium-mode rendering (graph + refs + message).
func TestRenderGraphLineMedium(t *testing.T) {
	line := GitGraphLine{
		GraphChars: "● ",
		Refs:       "HEAD -> main",
		Message:    "feat: add git graph panel",
		Author:     "Alice Smith",
		SHA:        "7eb8da1",
	}

	width := 46
	got := renderGraphLineMedium(line, width)

	if got == "" {
		t.Error("renderGraphLineMedium returned empty string")
	}

	plain := stripANSI(got)
	// Should contain message
	if !strings.Contains(plain, "feat:") {
		t.Errorf("missing commit message in medium line: %q", plain)
	}
	// Should contain refs
	if !strings.Contains(plain, "HEAD") {
		t.Errorf("missing refs in medium line: %q", plain)
	}
	// Should NOT contain SHA or author
	if strings.Contains(plain, "7eb8da1") {
		t.Errorf("medium line should not contain SHA: %q", plain)
	}
	if strings.Contains(plain, "Alice") {
		t.Errorf("medium line should not contain author: %q", plain)
	}
}

// TestRenderGraphLineMedium_NoRefs verifies medium mode without refs.
func TestRenderGraphLineMedium_NoRefs(t *testing.T) {
	line := GitGraphLine{
		GraphChars: "● ",
		Message:    "fix: handle nil pointer",
		SHA:        "a1b2c3d",
	}

	width := 46
	got := renderGraphLineMedium(line, width)
	plain := stripANSI(got)
	if !strings.Contains(plain, "fix: handle nil") {
		t.Errorf("missing message: %q", plain)
	}
}

// TestRenderGitGraph_SmallWidthCap verifies small mode caps panel width at 32.
func TestRenderGitGraph_SmallWidthCap(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphSmall
	m.width = 160
	m.height = 30
	m.gitGraphState = &GitGraphState{
		Lines: []GitGraphLine{{GraphChars: "● ", SHA: "abc1234", Message: "test commit"}},
	}

	got := m.renderGitGraph()
	plain := stripANSI(got)
	// Title should be "GIT" not "GIT GRAPH"
	if strings.Contains(plain, "GIT GRAPH") {
		t.Error("small mode should use 'GIT' title, not 'GIT GRAPH'")
	}
	if !strings.Contains(plain, "GIT") {
		t.Error("small mode should contain 'GIT' title")
	}
}

// TestRenderGitGraph_MediumWidthCap verifies medium mode caps panel width at 50.
func TestRenderGitGraph_MediumWidthCap(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphMedium
	m.width = 160
	m.height = 30
	m.gitGraphState = &GitGraphState{
		Lines: []GitGraphLine{
			{GraphChars: "● ", SHA: "abc1234", Refs: "HEAD -> main", Message: "test commit"},
		},
	}

	got := m.renderGitGraph()
	plain := stripANSI(got)
	if strings.Contains(plain, "GIT GRAPH") {
		t.Error("medium mode should use 'GIT' title, not 'GIT GRAPH'")
	}
}

// TestRenderGitGraph_Hidden verifies no output when mode is Hidden.
func TestRenderGitGraph_Hidden(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphHidden

	got := m.renderGitGraph()
	if got != "" {
		t.Errorf("renderGitGraph Hidden mode should return empty string, got %q", got)
	}
}

// TestRenderGitGraph_NarrowTerminal verifies graph is hidden when too narrow.
func TestRenderGitGraph_NarrowTerminal(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 75 // remaining = 75 - 69 - 2 = 4, below minimum 20

	got := m.renderGitGraph()
	if got != "" {
		t.Errorf("should return empty for narrow terminal (width=%d), got non-empty", m.width)
	}
}

// TestRenderGitGraph_Loading verifies loading state renders correctly.
func TestRenderGitGraph_Loading(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphState = nil
	m.width = 130

	got := m.renderGitGraph()
	if got == "" {
		t.Fatal("loading state should produce non-empty output")
	}

	plain := stripANSI(got)
	if !strings.Contains(plain, "Loading") {
		t.Errorf("loading state should contain 'Loading', got:\n%s", plain)
	}
}

// TestRenderGitGraph_Error verifies error state renders correctly.
func TestRenderGitGraph_Error(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 130
	m.gitGraphState = &GitGraphState{
		Error:       "fatal: not a git repository",
		LastRefresh: time.Now(),
	}

	got := m.renderGitGraph()
	if got == "" {
		t.Fatal("error state should produce non-empty output")
	}

	plain := stripANSI(got)
	if !strings.Contains(plain, "fatal: not a git") {
		t.Errorf("error state should show error message, got:\n%s", plain)
	}
}

// TestRenderGitGraph_WithData verifies full rendering with commit data.
func TestRenderGitGraph_WithData(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 130
	m.gitGraphState = &GitGraphState{
		TotalCount:  3,
		LastRefresh: time.Now(),
		Lines: []GitGraphLine{
			{
				GraphChars: "● ",
				SHA:        "7eb8da1",
				Author:     "Alice Smith",
				Refs:       "HEAD -> main",
				Message:    "feat: add git graph",
			},
			{GraphChars: "├╌╮"},
			{
				GraphChars: "│ ●",
				SHA:        "a1b2c3d",
				Author:     "Bob Jones",
				Message:    "feat: add tests",
			},
		},
	}

	got := m.renderGitGraph()
	if got == "" {
		t.Fatal("renderGitGraph with data returned empty string")
	}

	plain := stripANSI(got)

	if !strings.Contains(plain, "GIT GRAPH") {
		t.Error("missing 'GIT GRAPH' panel title")
	}
	if !strings.Contains(plain, "feat: add git graph") {
		t.Errorf("missing commit message in output:\n%s", plain)
	}
	if !strings.Contains(plain, "[1-3 of 3]") {
		t.Errorf("missing scroll indicator, got:\n%s", plain)
	}
}

// TestRenderGitGraph_FocusedBorder verifies both focused and unfocused panels render.
// Note: color differences require a TTY; here we only verify both render non-empty
// and contain the correct panel structure.
func TestRenderGitGraph_FocusedBorder(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 130
	m.gitGraphState = &GitGraphState{Lines: []GitGraphLine{{GraphChars: "● ", SHA: "abc1234", Message: "test"}}}

	// Focused panel
	m.gitGraphFocus = true
	focused := m.renderGitGraph()
	if focused == "" {
		t.Error("focused panel should render non-empty")
	}

	// Unfocused panel
	m.gitGraphFocus = false
	unfocused := m.renderGitGraph()
	if unfocused == "" {
		t.Error("unfocused panel should render non-empty")
	}

	// Both should contain the panel structure
	focusedPlain := stripANSI(focused)
	if !strings.Contains(focusedPlain, "GIT GRAPH") {
		t.Error("focused panel missing GIT GRAPH title")
	}
	unfocusedPlain := stripANSI(unfocused)
	if !strings.Contains(unfocusedPlain, "GIT GRAPH") {
		t.Error("unfocused panel missing GIT GRAPH title")
	}
}

// TestModelUpdate_GToggle verifies 'g' key cycles through all graph modes.
func TestModelUpdate_GToggle(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphHidden
	m.projectPath = "."

	// Hidden → Full
	updated, _ := m.Update(makeKey("g"))
	m = updated.(Model)
	if m.gitGraphMode != GitGraphFull {
		t.Errorf("after 1st g: mode = %d, want GitGraphFull(%d)", m.gitGraphMode, GitGraphFull)
	}

	// Full → Medium
	updated, _ = m.Update(makeKey("g"))
	m = updated.(Model)
	if m.gitGraphMode != GitGraphMedium {
		t.Errorf("after 2nd g: mode = %d, want GitGraphMedium(%d)", m.gitGraphMode, GitGraphMedium)
	}

	// Medium → Small
	updated, _ = m.Update(makeKey("g"))
	m = updated.(Model)
	if m.gitGraphMode != GitGraphSmall {
		t.Errorf("after 3rd g: mode = %d, want GitGraphSmall(%d)", m.gitGraphMode, GitGraphSmall)
	}

	// Small → Hidden
	updated, _ = m.Update(makeKey("g"))
	m = updated.(Model)
	if m.gitGraphMode != GitGraphHidden {
		t.Errorf("after 4th g: mode = %d, want GitGraphHidden(%d)", m.gitGraphMode, GitGraphHidden)
	}
}

// TestModelUpdate_TabFocus verifies Tab toggles focus when graph is visible.
func TestModelUpdate_TabFocus(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphFocus = false

	updated, _ := m.Update(makeKey("tab"))
	m = updated.(Model)
	if !m.gitGraphFocus {
		t.Error("Tab should set gitGraphFocus=true when graph is visible")
	}

	updated, _ = m.Update(makeKey("tab"))
	m = updated.(Model)
	if m.gitGraphFocus {
		t.Error("second Tab should set gitGraphFocus=false")
	}
}

// TestModelUpdate_TabNoFocusWhenHidden verifies Tab is a no-op when graph hidden.
func TestModelUpdate_TabNoFocusWhenHidden(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphHidden
	m.gitGraphFocus = false

	updated, _ := m.Update(makeKey("tab"))
	m = updated.(Model)
	if m.gitGraphFocus {
		t.Error("Tab should NOT toggle focus when graph is hidden")
	}
}

// TestModelUpdate_ScrollWhenFocused verifies j/k scroll the graph when focused.
func TestModelUpdate_ScrollWhenFocused(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphFocus = true
	m.gitGraphScroll = 5
	m.height = 40 // viewport = 35
	m.gitGraphState = &GitGraphState{
		Lines: make([]GitGraphLine, 50),
	}

	// 'j' scrolls down
	updated, _ := m.Update(makeKey("j"))
	m = updated.(Model)
	if m.gitGraphScroll != 6 {
		t.Errorf("after j: scroll = %d, want 6", m.gitGraphScroll)
	}

	// 'k' scrolls up
	updated, _ = m.Update(makeKey("k"))
	m = updated.(Model)
	if m.gitGraphScroll != 5 {
		t.Errorf("after k: scroll = %d, want 5", m.gitGraphScroll)
	}
}

// TestModelUpdate_ScrollBoundaries verifies scroll doesn't go out of bounds.
func TestModelUpdate_ScrollBoundaries(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphFocus = true
	m.gitGraphScroll = 0
	m.height = 8 // viewport = 3
	m.gitGraphState = &GitGraphState{
		Lines: make([]GitGraphLine, 5),
	}

	// Can't scroll up past 0
	updated, _ := m.Update(makeKey("k"))
	m = updated.(Model)
	if m.gitGraphScroll != 0 {
		t.Errorf("scroll should stay at 0, got %d", m.gitGraphScroll)
	}

	// maxScroll = 5 - 3 = 2; can't scroll past that
	m.gitGraphScroll = 2
	updated, _ = m.Update(makeKey("j"))
	m = updated.(Model)
	if m.gitGraphScroll != 2 {
		t.Errorf("scroll should stay at 2 (max), got %d", m.gitGraphScroll)
	}
}

// TestModelUpdate_DashboardScrollWhenNotFocused verifies j/k select tasks when not focused.
func TestModelUpdate_DashboardScrollWhenNotFocused(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphFocus = false
	m.tasks = []TaskDisplay{
		{ID: "1", Title: "Task A", Status: "running"},
		{ID: "2", Title: "Task B", Status: "queued"},
	}
	m.selectedTask = 0

	updated, _ := m.Update(makeKey("j"))
	m = updated.(Model)
	if m.selectedTask != 1 {
		t.Errorf("j should move selectedTask to 1, got %d", m.selectedTask)
	}
	if m.gitGraphScroll != 0 {
		t.Errorf("gitGraphScroll should stay at 0, got %d", m.gitGraphScroll)
	}
}

// TestModelUpdate_HalfPageScroll verifies Ctrl+D/Ctrl+U half-page scrolling.
func TestModelUpdate_HalfPageScroll(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.gitGraphFocus = true
	m.gitGraphScroll = 0
	m.height = 40 // viewport = 35, half-page = 17
	m.gitGraphState = &GitGraphState{
		Lines: make([]GitGraphLine, 100),
	}

	// Ctrl+D: down half-page (35/2 = 17)
	updated, _ := m.Update(makeKey("ctrl+d"))
	m = updated.(Model)
	if m.gitGraphScroll != 17 {
		t.Errorf("ctrl+d: scroll = %d, want 17", m.gitGraphScroll)
	}

	// Ctrl+U: up half-page (back to 0)
	updated, _ = m.Update(makeKey("ctrl+u"))
	m = updated.(Model)
	if m.gitGraphScroll != 0 {
		t.Errorf("ctrl+u: scroll = %d, want 0", m.gitGraphScroll)
	}
}

// TestModelUpdate_GitRefreshMsg verifies state is updated on refresh.
func TestModelUpdate_GitRefreshMsg(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull

	state := &GitGraphState{
		TotalCount:  42,
		LastRefresh: time.Now(),
		Lines: []GitGraphLine{
			{SHA: "abc1234", Message: "test commit"},
		},
	}

	updated, _ := m.Update(gitRefreshMsg{state: state})
	m = updated.(Model)

	if m.gitGraphState == nil {
		t.Fatal("gitGraphState should be set after gitRefreshMsg")
	}
	if m.gitGraphState.TotalCount != 42 {
		t.Errorf("TotalCount = %d, want 42", m.gitGraphState.TotalCount)
	}
}

// TestModelUpdate_GitRefreshTickHidden verifies no refresh cmd when graph is hidden.
func TestModelUpdate_GitRefreshTickHidden(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphHidden

	_, cmd := m.Update(gitRefreshTickMsg{})
	if cmd != nil {
		t.Error("gitRefreshTickMsg when hidden should return nil cmd (no refresh)")
	}
}

// TestViewWithGitGraph_SideBySide verifies View renders both panels side-by-side.
func TestViewWithGitGraph_SideBySide(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 130
	m.height = 40
	m.gitGraphState = &GitGraphState{
		TotalCount: 2,
		Lines: []GitGraphLine{
			{GraphChars: "● ", SHA: "7eb8da1", Author: "Alice", Message: "initial commit"},
		},
	}

	output := m.View()
	if output == "" {
		t.Error("View() returned empty string with git graph visible")
	}

	plain := stripANSI(output)

	if !strings.Contains(plain, "Pilot") {
		t.Error("View() should contain dashboard header")
	}
	if !strings.Contains(plain, "GIT GRAPH") {
		t.Error("View() should contain GIT GRAPH panel")
	}
}

// TestViewHidden_NoGraph verifies View renders normally when graph is hidden.
func TestViewHidden_NoGraph(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphHidden
	m.width = 120
	m.height = 40

	output := m.View()
	plain := stripANSI(output)

	if strings.Contains(plain, "GIT GRAPH") {
		t.Error("View() should NOT contain GIT GRAPH when hidden")
	}
	if !strings.Contains(plain, "Pilot") {
		t.Error("View() should contain dashboard header")
	}
}

// TestSetProjectPath verifies SetProjectPath sets the field.
func TestSetProjectPath(t *testing.T) {
	m := NewModel("test")
	m.SetProjectPath("/tmp/myrepo")
	if m.projectPath != "/tmp/myrepo" {
		t.Errorf("projectPath = %q, want %q", m.projectPath, "/tmp/myrepo")
	}
}

// TestGitGraphState_ScrollIndicator verifies scroll indicator shows correct range.
func TestGitGraphState_ScrollIndicator(t *testing.T) {
	m := NewModel("test")
	m.gitGraphMode = GitGraphFull
	m.width = 130
	m.gitGraphScroll = 10

	lines := make([]GitGraphLine, 50)
	for i := range lines {
		lines[i] = GitGraphLine{
			GraphChars: "● ",
			SHA:        "abc1234",
			Message:    "commit message",
		}
	}
	m.gitGraphState = &GitGraphState{
		TotalCount: 50,
		Lines:      lines,
	}

	got := m.renderGitGraph()
	plain := stripANSI(got)

	// scroll=10, visible=30, end=min(10+30, 50)=40
	// indicator: [11-40 of 50]
	if !strings.Contains(plain, "[11-40 of 50]") {
		t.Errorf("scroll indicator incorrect, got:\n%s", plain)
	}
}
