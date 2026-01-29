# TUI Design Skill

## Purpose

Design and implement terminal user interfaces with a Kali Linux-inspired aesthetic - clean, professional, hacker-style terminals that convey technical competence.

## Design Philosophy

**Inspired by**: Kali Linux tools (Metasploit, Hydra, Aircrack-ng), htop, lazygit, k9s

### Core Principles

| Principle | Description |
|-----------|-------------|
| **Fixed-width panels** | All boxes use same width (70 chars) |
| **Monospace alignment** | Everything aligns to grid |
| **ASCII only** | No emojis - use `+`, `x`, `*`, `o`, `>` |
| **Cyber aesthetic** | Green/cyan accents, dark backgrounds |
| **Information density** | Maximum data, minimum chrome |
| **Status at a glance** | Symbols > words |

---

## Color Palette

### Primary Colors (Lipgloss)

```go
var (
    // Background tones
    ColorBgDark    = lipgloss.Color("#0d1117")  // GitHub dark
    ColorBgPanel   = lipgloss.Color("#161b22")  // Slightly lighter

    // Accent colors (Kali-inspired)
    ColorCyan      = lipgloss.Color("#00d4ff")  // Primary accent
    ColorGreen     = lipgloss.Color("#00ff41")  // Success, active
    ColorRed       = lipgloss.Color("#ff0055")  // Error, critical
    ColorYellow    = lipgloss.Color("#ffcc00")  // Warning
    ColorMagenta   = lipgloss.Color("#ff00ff")  // Highlight
    ColorBlue      = lipgloss.Color("#0099ff")  // Info, links

    // Text
    ColorTextDim   = lipgloss.Color("#6e7681")  // Muted text
    ColorTextMid   = lipgloss.Color("#8b949e")  // Secondary text
    ColorText      = lipgloss.Color("#c9d1d9")  // Primary text
    ColorTextBright= lipgloss.Color("#ffffff")  // Emphasis

    // Borders
    ColorBorder    = lipgloss.Color("#30363d")  // Panel borders
    ColorBorderFocus = lipgloss.Color("#00d4ff") // Active panel
)
```

### Status Indicators (ASCII Only)

| State | Color | Symbol | Usage |
|-------|-------|--------|-------|
| Success | `#00ff41` | `+` | Completed, passed |
| Running | `#00d4ff` | `*` | Active, in progress |
| Warning | `#ffcc00` | `!` | Degraded, attention |
| Error | `#ff0055` | `x` | Failed, critical |
| Pending | `#6e7681` | `o` | Waiting, queued |
| Disabled | `#30363d` | `-` | Not configured |
| Selected | - | `>` | Current selection |

---

## Layout System

### Panel Width

```go
const (
    panelWidth   = 69 // Total width including borders
    contentWidth = 65 // Inner content (panelWidth - 4)
)
```

### Panel Structure (Title in Border)

```
╭─ PANEL TITLE ───────────────────────────────────────────────────╮
│                                                                 │
│   Content here...                                               │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
```

**Key**:
- Title UPPERCASE in top border
- Empty line padding top and bottom
- Content indented 2 spaces
- All panels exactly same width

---

## Component Patterns

### 1. Header

Minimal, no boxes:
```
PILOT

```

### 2. Token Usage (Dot Leaders)

```go
func dotLeader(label, value string, width int) string {
    prefix := "  " + label + " "
    suffix := " " + value
    dots := width - len(prefix) - len(suffix)
    return prefix + strings.Repeat(".", dots) + suffix
}
```

Output:
```
╭─ TOKEN USAGE ───────────────────────────────────────────────────╮
│                                                                 │
│   Input ............................................... 12,450  │
│   Output ...............................................  3,200  │
│   Total ............................................... 15,650  │
│   Est. Cost .......................................... $0.0470  │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
```

### 3. Tasks Panel

```go
fmt.Sprintf("%s%s %-7s  %-20s  %s (%3d%%)",
    selector, status, task.ID,
    truncate(task.Title, 20),
    progressBar, task.Progress)
```

Output:
```
╭─ TASKS ─────────────────────────────────────────────────────────╮
│                                                                 │
│ > + GH-156  Verify dashboard upda...  [██████████████] (100%)   │
│   + GH-153  Wire budget enforcer...   [██████████████] (100%)   │
│   * GH-157  Add rate limiting to...   [████████░░░░░░]  (42%)   │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
```

### 4. Progress Bar

```go
func renderProgressBar(percent, width int) string {
    filled := percent * width / 100
    bar := progressStyle.Render(strings.Repeat("█", filled)) +
           dimStyle.Render(strings.Repeat("░", width-filled))
    return "[" + bar + "]"
}
```

### 5. History Panel

```
╭─ HISTORY ───────────────────────────────────────────────────────╮
│                                                                 │
│   + GH-156  Verify dashboard updates...          5m12s   4m ago │
│   + GH-153  Wire budget enforcer...              3m04s   1m ago │
│   x GH-150  Auto-decompose complex tasks        12m33s  15m ago │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
```

### 6. Logs Panel

```
╭─ LOGS ──────────────────────────────────────────────────────────╮
│                                                                 │
│   [GH-153] Implementing: Creating main.go                (55%)  │
│   [GH-153] Testing: Running tests...                     (75%)  │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
```

---

## Implementation Guidelines

### 1. Fixed Width Rendering

**Problem**: Variable content causes ragged right edges.

**Solution**: Always set explicit `Width()` on box styles.

```go
// BAD - width varies with content
boxStyle.Render(content)

// GOOD - fixed width enforced
boxStyle.Width(60).Render(content)
```

### 2. Column Alignment

**Problem**: Columns don't align across rows.

**Solution**: Use `fmt.Sprintf` with explicit widths.

```go
// BAD
fmt.Sprintf("%s %s %s", status, id, title)

// GOOD
fmt.Sprintf("%-2s %-8s %-30s", status, id, title)
```

### 3. Content Width Calculation

```go
func contentWidth(boxWidth int) int {
    // Box adds: 2 (padding) + 2 (border) = 4 chars
    return boxWidth - 4
}
```

### 4. Dynamic Terminal Width

```go
func (m Model) View() string {
    width := m.width
    if width == 0 {
        width = 80 // Default fallback
    }

    // Adjust panel widths based on terminal
    if width >= 120 {
        // Wide terminal: side-by-side panels
        return lipgloss.JoinHorizontal(
            lipgloss.Top,
            leftPanel(60),
            rightPanel(60),
        )
    }

    // Narrow terminal: stacked panels
    return lipgloss.JoinVertical(
        lipgloss.Left,
        topPanel(width),
        bottomPanel(width),
    )
}
```

---

## Anti-Patterns

| Don't | Do |
|-------|-----|
| Variable-width boxes | Fixed-width boxes with `Width(70)` |
| Emojis anywhere | ASCII symbols: `+ x * o >` |
| Deep nesting | Flat panel structure |
| Colors everywhere | Accent colors sparingly |
| Long status text | Single-char symbols |
| Verbose headers | Short, UPPERCASE headers |
| Different panel widths | Same width for all panels |

---

## Reference Implementations

### Kali Linux Tools
- Metasploit console: Banner + module tree + prompt
- Aircrack-ng: Progress bars, station tables
- Hydra: Parallel task display, rate metrics

### Go TUI Examples
- [lazygit](https://github.com/jesseduffield/lazygit) - Multi-panel layout
- [k9s](https://github.com/derailed/k9s) - Resource tables, status bars
- [htop](https://htop.dev/) - Meters, process list

### Lipgloss/Bubbletea
- [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea)

---

## Quick Checklist

When designing a new TUI panel:

- [ ] Fixed width: 69 chars total, 65 content
- [ ] UPPERCASE title in top border
- [ ] Empty line padding top and bottom
- [ ] Content indented 2 spaces
- [ ] Columns use fixed-width `fmt.Sprintf`
- [ ] Status uses ASCII symbols: `+ x * o >`
- [ ] Dot leaders for label-value pairs
- [ ] Colors from palette only
- [ ] Truncate long text with `...`
- [ ] No emojis anywhere

---

## File Reference

- **Implementation**: `internal/dashboard/tui.go`
- **Styles guide**: `.agent/sops/development/ASCII-TUI-STYLE.md`
- **Progress display**: `internal/executor/progress.go`
