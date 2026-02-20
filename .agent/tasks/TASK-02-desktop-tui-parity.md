# TASK-02: Desktop App TUI Parity

**Status**: In Progress
**Created**: 2026-02-20

---

## Context

**Problem**:
The desktop app (Wails + React) has a different layout and component styling than the terminal TUI dashboard. Users expect visual consistency between the two interfaces.

**Goal**:
Redesign desktop frontend to match TUI's single-column vertical layout, panel styling, and component rendering — while leveraging the wider desktop window for the git graph panel.

**Success Criteria**:
- [ ] Desktop layout matches TUI: single vertical column (left) + git graph (right)
- [ ] All 6 component gaps addressed (logo, queue, autopilot, history, logs, git graph)
- [ ] Same color palette: steel #7eb8da, sage #7ec699, amber #d4a054, rose #d48a8a, gray #8b949e, slate #3d4450

---

## Gaps (from visual comparison)

| # | Component | TUI | Desktop (current) | Priority |
|---|-----------|-----|-------------------|----------|
| 1 | **Layout** | Single vertical column + git graph right | 2x2 grid + git graph | P0 |
| 2 | **Header/Logo** | ASCII PILOT block art + version | Plain text "PILOT" | P1 |
| 3 | **Autopilot** | Dot-leader `Mode ........... stage` | Key-value without dots | P1 |
| 4 | **History** | `+ GH-1626  Fix CI failure...  6h ago` | Different card format | P1 |
| 5 | **Logs** | `[GH-1631] Starting: Init...` task-prefixed | Timestamp-only format | P1 |
| 6 | **Git Graph** | Unicode branch chars (●│├╌╮╯) + colored tracks | Plain text git log | P2 |

---

## Implementation Plan

### Single Issue (all files in desktop/frontend/src/)

Per serial conflict cascade lesson: all changes are in the same package (`desktop/frontend/`), so this MUST be a single issue — not decomposed into parallel issues.

**Changes:**

1. **`App.tsx`** — Restructure layout:
   - Left column: vertical stack (Header → Metrics → Queue → Autopilot → History → Logs)
   - Right column: Git Graph (full height)
   - Remove 2x2 grid, use `flex-col` for left column

2. **`components/Header.tsx`** — Add ASCII logo:
   - Render PILOT ASCII art from `banner.go` Logo constant as `<pre>` with steel blue color
   - Show version + "daemon offline/online" status

3. **`components/AutopilotPanel.tsx`** — Dot-leader pattern:
   - Render key-value pairs with CSS dot-leader: `Mode ........... dev`
   - Use `flex` with `overflow: hidden` dot pattern between key and value

4. **`components/HistoryPanel.tsx`** — Compact format:
   - Single-line entries: `+ GH-1626  Fix CI failure from PR #1622  6h ago`
   - `+` prefix for completed, `x` for failed
   - Right-aligned relative time

5. **`components/LogsPanel.tsx`** — Task-prefixed format:
   - Format: `[GH-1631] Starting: Initializing claude-code... (0%)`
   - Task ID prefix in steel blue, message in light gray

6. **`components/GitGraphPanel.tsx`** — Unicode branch rendering:
   - Parse GraphChars from Go backend (already Unicode: ● │ ├╌╮ ╯)
   - Apply per-track coloring from `branchColors` palette
   - Color refs: HEAD = steel bold, branches = sage, tags = amber bold

---

## Reference Files

- TUI layout: `internal/dashboard/tui.go` (renderDashboard, renderPanel, renderMetricsCards)
- TUI git graph: `internal/dashboard/gitgraph.go` (colorizeGraphChars, colorizeRefs, branchColors)
- ASCII logo: `internal/banner/banner.go` (Logo constant)
- Desktop app entry: `desktop/frontend/src/App.tsx`
- Desktop components: `desktop/frontend/src/components/*.tsx`
- Desktop styles: `desktop/frontend/src/styles/globals.css`

## Color Palette (from TUI)

| Name | Hex | Usage |
|------|-----|-------|
| Steel blue | #7eb8da | Running state, links, HEAD refs |
| Sage green | #7ec699 | Success/done, branch refs |
| Amber | #d4a054 | Tag refs, cost metric |
| Dusty rose | #d48a8a | Failed state |
| Mid gray | #8b949e | Queued state, secondary text |
| Slate | #3d4450 | Borders, empty progress |
| Light gray | #c9d1d9 | Primary text |
| Background | #1e222a | App background |

---

## Verify

```bash
# Build desktop frontend
cd desktop/frontend && npm run build

# Run in dev mode
PATH="$HOME/go/bin:$PATH" make desktop-dev

# Visual check: compare desktop window to TUI (pilot start --dashboard)
```

---

## Done

- [ ] Layout matches TUI: single vertical column + git graph right panel
- [ ] ASCII PILOT logo renders in header
- [ ] Autopilot panel uses dot-leader pattern
- [ ] History shows compact single-line entries with relative time
- [ ] Logs show `[GH-XXX]` task-prefixed entries
- [ ] Git graph renders Unicode branch art with colored tracks
- [ ] `npm run build` succeeds without errors

---

**Last Updated**: 2026-02-20
