# GH-158: Fix Dashboard TUI Layout

**Status**: 🚧 In Progress
**Created**: 2026-01-29
**Assignee**: Pilot

---

## Context

**Problem**:
Dashboard panels have inconsistent widths - right borders don't align. Manual border construction with title injection doesn't match lipgloss's internal width calculations. ANSI color codes affect string length but not visual width, causing misalignment.

**Goal**:
All dashboard panels must have identical visual width with perfectly aligned borders.

**Success Criteria**:
- [ ] All 4 panels (Token Usage, Tasks, History, Logs) have identical width
- [ ] Right borders align perfectly in terminal
- [ ] Works with ANSI-styled content (colored cost, status symbols)
- [ ] Title appears in top border (`╭─ TOKEN USAGE ───╮`)

---

## Design Reference

Target layout (all panels 69 chars wide):

```
PILOT

╭─ TOKEN USAGE ───────────────────────────────────────────────────╮
│                                                                 │
│   Input ............................................... 12,450  │
│   Output ...............................................  3,200  │
│   Total ............................................... 15,650  │
│   Est. Cost .......................................... $0.0470  │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
╭─ TASKS ─────────────────────────────────────────────────────────╮
│                                                                 │
│ > + GH-156  Verify dashboard upda...  [██████████████] (100%)   │
│   + GH-153  Wire budget enforcer...   [██████████████] (100%)   │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
╭─ HISTORY ───────────────────────────────────────────────────────╮
│                                                                 │
│   + GH-156  Verify dashboard updates...          5m12s   4m ago │
│   + GH-153  Wire budget enforcer...              3m04s   1m ago │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
╭─ LOGS ──────────────────────────────────────────────────────────╮
│                                                                 │
│   [GH-153] Implementing: Creating main.go                (55%)  │
│   [GH-153] Testing: Running tests...                     (75%)  │
│                                                                 │
╰─────────────────────────────────────────────────────────────────╯
q: quit  l: logs  j/k: select
```

**Style Guide**:
- Kali Linux-inspired aesthetic (ASCII only, no emojis)
- Status symbols: `+` success, `x` fail, `*` running, `o` pending, `>` selected
- Dot leaders for label-value pairs
- UPPERCASE titles in border
- Empty line padding top/bottom

---

## Implementation Plan

### Phase 1: Research - Visual Width Calculation
**Goal**: Understand how to properly calculate visual width with ANSI codes

**Tasks**:
- [x] Study ccusage implementation (uses `string-width` npm package)
- [x] Verify lipgloss.Width() handles ANSI codes correctly
- [ ] Identify root cause of width mismatch

**Reference**:
- ccusage uses `cli-table3` which handles alignment internally
- ccusage `utils.ts` uses `stringWidth()` for visual width
- Go equivalent: `lipgloss.Width()` or `runewidth` package

### Phase 2: Fix Panel Rendering
**Goal**: Build panels with guaranteed consistent width

**Approach Options**:

| Option | Pros | Cons |
|--------|------|------|
| A: Manual construction | Full control | Error-prone, must handle all edge cases |
| B: lipgloss borders only | Automatic alignment | Can't put title in border |
| C: lipgloss + post-process | Uses lipgloss alignment | Complex title injection |
| D: Use table library | Proven solution | New dependency |

**Recommended**: Option A with strict width validation

**Tasks**:
- [ ] Create `renderLine(content, width)` that guarantees exact visual width
- [ ] Use `lipgloss.Width()` for all width calculations
- [ ] Build borders character by character with width tracking
- [ ] Add width assertion/validation in debug mode

**Files**:
- `internal/dashboard/tui.go` - Panel rendering

### Phase 3: Content Width Management
**Goal**: Ensure all content fits within panel width

**Tasks**:
- [ ] Calculate available content width: `panelWidth - 4` (borders + padding)
- [ ] Truncate/pad all content to exact width before rendering
- [ ] Handle styled text (calculate width before styling, pad, then apply style)
- [ ] Test with various content lengths

### Phase 4: Verify & Test
**Goal**: Confirm alignment works in all cases

**Tasks**:
- [ ] Test with 0 values (Token Usage)
- [ ] Test with large numbers (12,450,000)
- [ ] Test with long task titles
- [ ] Test with colored output
- [ ] Test in different terminal widths

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Width calculation | len(), runewidth, lipgloss.Width() | lipgloss.Width() | Already using lipgloss, handles ANSI |
| Border construction | lipgloss borders, manual | Manual | Need title in border |
| Panel width | 67, 69, 70 | 69 | Fits 80-char terminal with margin |

---

## Key Implementation Details

### Width Calculation Formula

```go
const (
    panelWidth   = 69  // Total visual width including borders
    contentWidth = 65  // panelWidth - 2 (borders) - 2 (padding)
)
```

### Guaranteed-Width Line Builder

```go
func buildLine(content string, targetWidth int) string {
    visualWidth := lipgloss.Width(content)
    if visualWidth > targetWidth {
        // Truncate (need to handle ANSI codes)
        return truncateVisual(content, targetWidth-3) + "..."
    }
    if visualWidth < targetWidth {
        // Pad with spaces
        return content + strings.Repeat(" ", targetWidth-visualWidth)
    }
    return content
}
```

### Panel Structure

```go
func renderPanel(title, content string) string {
    lines := []string{
        buildTopBorder(title, panelWidth),      // ╭─ TITLE ───...───╮
        buildEmptyLine(panelWidth),              // │                 │
    }
    for _, c := range strings.Split(content, "\n") {
        lines = append(lines, buildContentLine(c, contentWidth)) // │  content    │
    }
    lines = append(lines,
        buildEmptyLine(panelWidth),              // │                 │
        buildBottomBorder(panelWidth),           // ╰─────────────────╯
    )
    return strings.Join(lines, "\n")
}
```

---

## Dependencies

**Requires**:
- lipgloss package (already installed)

**Blocks**:
- None

---

## Verify

```bash
# Build
make build

# Run dashboard
./bin/pilot start --dashboard

# Visual inspection: all right borders must align
```

---

## Done

Observable outcomes that prove completion:

- [ ] All 4 panels have visually identical width
- [ ] Right borders form a straight vertical line
- [ ] Token Usage dot leaders align to right border
- [ ] Colored cost value doesn't break alignment
- [ ] Works with empty state (0 tokens, no tasks)
- [ ] Works with populated state

---

## Notes

**Root Cause Analysis**:
The issue is mixing lipgloss's automatic border rendering with manual title injection. Lipgloss calculates width internally, but when we replace the top border line, our calculation might differ due to:
1. ANSI escape codes in borderStyle.Render()
2. Unicode character width assumptions
3. Off-by-one errors in dash counting

**ccusage Reference**:
- Uses `cli-table3` library which handles all width calculations internally
- `stringWidth` npm package for visual width (equivalent to lipgloss.Width)
- Doesn't manually construct borders

---

**Last Updated**: 2026-01-29
