# TASK-398: Dashboard grom-style panel navigation — spatial focus, zoom, fluid layout

**Status**: ✅ SHIPPED 2026-07-11 — scaffold PR #4201 (`576b38ae`, v2.237.0) + wiring PR #4204 (`9a9f375a`, via TASK-399/#4203). #4199 closed 2026-07-12.
**Created**: 2026-07-11
**Last Updated**: 2026-07-12
**Type**: feat(dashboard) epic, 4 phases

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4199 (epic)
- Sub-issue: #4200 → PR #4201 **merged** (`576b38ae`, v2.237.0)
- **Wiring follow-up: TASK-399 → #4203**

## ⚠️ Outcome (2026-07-11): under-delivered — scaffold only

The epic auto-decomposed to a **single** sub-issue (#4200, `inherited-spec: true`) which
scoped itself to scaffolding and deferred wiring to "a subsequent subtask" — but no sibling
was ever created. PR #4201 merged **green** having added only the three new helper files
(`grid.go`, `panels.go`, `zoomlist.go` + tests); `tui.go`/`gitgraph.go`/`grot_chrome.go` were
**untouched**. The helpers are correct + tested but **entirely uncalled** — none of the 5
requirements actually ship. CI passed because new-file unit tests are trivially green; no test
forced the integration. Parent #4199 auto-closed on sub-issue completion, orphaning the gap
(false-completion, TASK-395 class). **Remaining work = wiring, dispatched as TASK-399/#4203.**
See pitfall memory `pitfall_epic_collapse_scaffold_only.md`.

## Goal

Rework `internal/dashboard` panel UX to match the grom TUI (qf-studio/grom):

1. **hjkl/arrows move focus between panels** (spatial, not cycle-order)
2. **Enter opens the focused panel full-size (zoom); esc returns** — grid state preserved
3. **Full-size History/Autopilot list ALL issues/PRs** with up/down item navigation; Logs gets full scrollback. (Today: history capped 5, autopilot 4 rows + "+N more", logs = tail only.)
4. **Git graph panel visible by default** (today: hidden until `g`)
5. **Fluid layout** — left column stretches with terminal width (today: fixed `panelTotalWidth = 69`)

## Current state (research summary)

- `internal/dashboard/tui.go` (~2537 lines): single bubbletea Model, panels are Model methods returning strings, stitched in `renderDashboard()` (~line 1486). `View()` (~1215) has 3 ad-hoc layout branches (graph hidden / stacked `<90` cols / side-by-side). Key dispatch = flat switch in `Update()` (~962–1062).
- Focus today = single bool `gitGraphFocus` toggled by `tab`; `j/k` double duty (task selection vs git scroll). `enter` opens the selected queue task's issue URL. `l` toggles logs, `g` toggles graph, `b` banner, `u` upgrade.
- Panel chrome: `renderPanelInfo`/`renderPanelStyled` (`grot_chrome.go:56-66`) → `render.PanelInfo` from `github.com/qf-studio/grot/pkg/tui/render`; **`focusChrome` (accent border) already exists** at `grot_chrome.go:37` — used only by the git panel today.
- Git graph: `gitgraph.go` — own scroll state + viewport math (`gitGraphViewportHeight` ~445), modes Hidden/Visible, auto sizes Small/Medium/Full, 15s refresh tick. Default hidden (zero value in constructors ~466–527, 728).
- Data: push via `program.Send` (UpdateTasks/AddLog/UpdateTokens/AddCompletedTask), 1s tick, `storeRefreshCmd` every 5 ticks re-reads SQLite. Autopilot panel pulls `autopilotController.GetActivePRs()` live in View. History hydrates from `memory.Store`; `GetRecentExecutions(limit, scope)` limit is a param (`internal/memory/store.go` ~952); `Execution.PRUrl` exists (~504).
- Deps: bubbletea v1.3.10, lipgloss v1.1.0, NO bubbles library.

**Decision (port, don't promote):** grom's navigation code lives in the grot repo's `internal/app` — not importable. Port ~200 lines into `internal/dashboard/grid.go` with attribution comments. The verbatim source to port is embedded below — do NOT try to read the grot repo.

## Ported grom source (embed in `internal/dashboard/grid.go`, adapt package/docs)

```go
// Ported from qf-studio/grom internal/app/grid.go (focusMove/overlaps/Rect)
// and internal/app/model.go (cropVertical, ensureVisible pattern).

// Rect is a panel's placement in terminal cells, top-left origin.
type Rect struct{ X, Y, W, H int }

// focusMove returns the index of the panel nearest to cur in direction dir
// ('h' left, 'j' down, 'k' up, 'l' right), or cur when nothing lies that way.
// Horizontal moves only consider panels whose rows overlap the current one
// (and vertical moves, columns), then pick the nearest by edge distance — so
// focus tracks the visual row/column rather than drifting diagonally.
func focusMove(rects []Rect, cur int, dir byte) int {
	if cur < 0 || cur >= len(rects) {
		return cur
	}
	c := rects[cur]
	best, bestDist, found := cur, 0, false
	for i, r := range rects {
		if i == cur {
			continue
		}
		var dist int
		switch dir {
		case 'h':
			if r.X >= c.X || !overlaps(c.Y, c.H, r.Y, r.H) {
				continue
			}
			dist = c.X - r.X
		case 'l':
			if r.X <= c.X || !overlaps(c.Y, c.H, r.Y, r.H) {
				continue
			}
			dist = r.X - c.X
		case 'k':
			if r.Y >= c.Y || !overlaps(c.X, c.W, r.X, r.W) {
				continue
			}
			dist = c.Y - r.Y
		case 'j':
			if r.Y <= c.Y || !overlaps(c.X, c.W, r.X, r.W) {
				continue
			}
			dist = r.Y - c.Y
		default:
			return cur
		}
		if !found || dist < bestDist {
			bestDist, best, found = dist, i, true
		}
	}
	return best
}

// overlaps reports whether the spans [a, a+al) and [b, b+bl) intersect.
func overlaps(a, al, b, bl int) bool {
	return a < b+bl && b < a+al
}

// cropVertical returns the height lines of s starting at offset, for scroll.
func cropVertical(s string, offset, height int) string {
	lines := strings.Split(s, "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	end := offset + height
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[offset:end], "\n")
}
```

**Adaptation required:** add a secondary tie-break to `focusMove` — when two candidates tie on primary edge distance, prefer the one nearer on the perpendicular axis. (The tall git column Y-overlaps every left-stack row, so without a tie-break `l` results can be ambiguous.)

grom's zoom pattern (View switch — replicate, don't copy):

```go
// grom model.go View(): zoomed renders ONLY the focused widget at full content size.
if m.zoomed && len(m.widgets) > 0 {
	r := m.fetchRect(m.focus) // Rect{W: m.width, H: m.contentHeight()}
	return header + "\n" + safeRender(m.widgets[m.focus], r.W, r.H, m.th, true)
}
// grid path untouched by zoom → esc-return is free, no state to restore.
```

## Target architecture

**New files** in `internal/dashboard/`:

- `grid.go` — ported code above + tests (`grid_test.go`: focusMove geometry incl. tall-column tie-break, overlaps table, cropVertical bounds).
- `panels.go` — panel registry replacing ad-hoc composition:

  ```go
  type panelID int

  const (
      panelTokens panelID = iota
      panelCost
      panelTasksCard
      panelQueue
      panelAutopilot
      panelEval
      panelHistory
      panelLogs
      panelGit
  )

  type panelDef struct {
      id         panelID
      title      string
      visible    func(m *Model) bool           // eval conditional, git/logs toggleable
      gridHeight func(m *Model, w int) int     // content-driven height in grid mode
      minW, minH int
      render     func(m *Model, w, h int, focused bool) string // exact w×h block
      zoomable   bool
  }

  type placedPanel struct {
      def  *panelDef
      rect Rect
  }

  // computeLayout is a pure function of (width, height, visible panels,
  // content sizes) — recomputed each View, so rects never go stale.
  func computeLayout(m *Model) []placedPanel
  ```

- `zoomlist.go` — shared zoomed-list viewport helper: `visible = h - 5` (borders 2 + padding 2 + indicator 1, same overhead math as `gitGraphViewportHeight`), `ensureSelVisible` clamps scroll so selection stays in view, `[a–b of N]` bottom-right indicator (reuse git graph indicator idiom), `▸` + accent row for selection (reuse queue's selected-task treatment).

**Model state additions**: `focus panelID` (**ID, not slice index** — eval/git/logs appear and disappear; an ID survives visibility churn), `zoomed bool`, `zoomSel, zoomScroll int` (reset on zoom enter/exit), `zoomHistory []CompletedTask`, `browserOpener func(string) error` (defaults to current `openBrowser`; injectable for tests). Remove `gitGraphFocus`.

**Fluid layout** (`computeLayout`):
- Wide (`width >= 96`): two columns. Left stack = banner (non-focusable chrome), metric-cards row (3 cards side by side, each focusable), queue, autopilot, eval (when visible), history, logs (flex — absorbs remaining height). Git column on the right, width `max(28, width*2/5)` capped so left keeps >= 69. Left column stretches — `effectivePanelTotalWidth` becomes `leftColumnWidth()` derived from layout.
- Narrow (`< 96`): single full-width column, git stacked below with fixed height (current stacked behavior; name the threshold constant).
- Grid mode keeps today's caps (history 5, autopilot 4, logs tail) — **zoom is the "see all" path**.
- Zoomed View: only the focused panel rendered at `(m.width, m.height-1)`; help footer stays on the last line. Grid state untouched.

**Keymap** (user-confirmed: esc exits zoom everywhere; enter opens the selected item's URL in list panels, toggles zoom off on non-list panels):

| Grid mode | Action |
|---|---|
| `h j k l` / arrows | spatial panel focus (`focusMove`) |
| `tab` | cycle focus in registry order (fallback) |
| `enter` | zoom focused panel |
| `ctrl+d` / `ctrl+u` | git half-page scroll (git focused only) |
| `g` | toggle git graph panel |
| `L` | toggle logs panel (**rebound from `l`** — `l` is now focus-right) |
| `b` | toggle banner |
| `u` | trigger self-upgrade |
| `q` / `ctrl+c` | quit |

| Zoomed mode | Action |
|---|---|
| `esc` | exit zoom (always) |
| `enter` | list panels (queue/autopilot/history): open selected item URL; non-list panels (cards/git/logs): exit zoom |
| `o` | open selected item URL (alias) |
| `j k` / arrows | item selection (queue/autopilot/history) or line scroll (logs/git) |
| `ctrl+d ctrl+u` / `pgdn pgup` | half-page scroll |
| `g` / `G` | jump top / bottom (`G` resumes tail-follow in logs) |
| `q` / `ctrl+c` | quit |

Focused panel gets the existing `focusChrome` accent border (thread `focused bool` through all panels; delete the git-only special case). Help footer (`renderHelp` ~1551) becomes grid/zoom context-aware.

## Phase 1: Layout module + fluid width + git graph default-visible

Depends on: nothing (first phase).

Pure-layout refactor, **no keymap changes** — every key behaves exactly as today.

- Add `grid.go` (ported code above) + `grid_test.go`.
- Add `panels.go`: `panelID`, `panelDef` registry, `computeLayout`.
- `View()` composes from `computeLayout` — kills the 3 ad-hoc branches (~tui.go:1227–1249). Keep the pad/truncate-to-`m.height-1` + help-footer-last-line logic and `tea.ClearScreen` guards; centralize ClearScreen by comparing total layout height per Update and emitting on change (GH-1249 class ghost lines).
- Left column stretches at all widths (not only stacked mode).
- Git graph default visible: `GitGraphVisible` in all Model constructors (~tui.go:466–527, 728) and `Init()` (~861) batches `refreshGitGraphCmd`/`gitRefreshTickCmd` so the graph paints on first frame.
- ~+450/−200 LOC. Files: `tui.go`, `gitgraph.go`, +`grid.go`, +`panels.go`, +`grid_test.go`; update `gitgraph_test.go` (hidden-default assertions), `tui_test.go`, `grot_chrome_test.go`.

**AC:**
- Width-invariant matrix test: every rendered line is exactly `width` cols at 80/96/140/200, banner on/off × git on/off.
- Git graph visible on first frame without pressing `g`; `g` still toggles.
- All existing keybindings behave exactly as before. CI green.

## Phase 2: Spatial focus + zoom toggle + new keymap

Depends on: Phase 1.

- Model: `focus panelID`, `zoomed bool`. hjkl/arrows → `focusMove` over `computeLayout` rects (map rect index ↔ panelID). `enter` zooms, `esc` unzooms.
- Zoomed View path: focused panel rendered at `(m.width, m.height-1)` — every panel needs an exact-size render path (`render.Panel` already supports fixed height; see `renderLogs` flex branch ~tui.go:2354–2359).
- Focused accent border (`focusChrome`) on whichever panel holds focus; remove `gitGraphFocus` and the `tab` binary toggle (`tab` → cycle focus). Git scroll keys work when git focused (grid) and zoomed. Grid-mode `j/k` task-selection and git-scroll double duty removed (moves into zoom, Phase 3).
- Rebind logs toggle `l` → `L`. Rewrite `renderHelp` as context-aware (grid vs zoomed vs git-focused).
- ~+300/−150 LOC. Files: `tui.go` (Update key switch, View), `panels.go`, `gitgraph.go`, `grot_chrome.go`.

**AC:**
- From queue, `l`/right lands on git panel, `h` returns; `k` from queue reaches the cards row and `h/l` walks the 3 cards; `j/k` never moves diagonally (tie-break test).
- `enter` on any panel fills the screen with it; `esc` restores the grid with focus and git scroll intact.
- Update-level tests replace `TestModelUpdate_TabFocus`, `_ScrollWhenFocused`, `_DashboardScrollWhenNotFocused`, `_HalfPageScroll`; help-footer test updated. CI green.

## Phase 3: Zoomed item lists — queue + autopilot

Depends on: Phase 2. Parallel with Phase 4.

- Add `zoomlist.go` (shared viewport helper above) + table tests for the viewport math.
- Model: `zoomSel`, `zoomScroll` (reset on zoom enter/exit). Extract `openBrowser` behind `browserOpener func(string) error` field (inject fake in tests).
- Queue zoomed: all tasks (uncapped), `j/k` selection, `enter`/`o` opens `TaskDisplay.IssueURL` via `browserOpener`; selection change syncs `syncGitGraphToSelectedTask`.
- Autopilot zoomed: all `GetActivePRs()` — **sorted by PR number, selection keyed by PR number** (not index) so live-pull reorder between frames can't jump the cursor. Per-PR detail line from `GetPRFailures` (detail lines non-selectable; selection indexes PRs). `enter` opens `PRState.PRURL`. No `maxAutopilotRows` cap when zoomed; grid mode unchanged.
- ~+350/−30 LOC. Files: `tui.go`, `panels.go`, +`zoomlist.go`; `AutopilotPanel` gets a `ViewZoomed(w, h, selPR, scroll)`.

**AC:**
- 10 queued tasks all visible zoomed; selection clamps at both ends; `[a–b of N]` indicator correct.
- Autopilot with 8 PRs shows all 8, no "+N more"; selection stable across a simulated PR-list reorder.
- `enter` opens the correct URL (fake `browserOpener` asserts). CI green.

## Phase 4: Zoomed history (uncapped store fetch) + zoomed logs

Depends on: Phase 2. Parallel with Phase 3.

- History zoom-enter dispatches a `tea.Cmd` (`historyZoomCmd`): `store.GetRecentExecutions(500, scope)` → first N distinct by task (raise the distinct-task helper's n to 100) → hydrate rows including `ListExecutionEvents` stage strips **inside the Cmd** (off the Update goroutine; bounded N+1, on-zoom only; reuse cached grid `StageInfo` where present) → `historyZoomMsg` fills `m.zoomHistory`. Panel shows "loading…" until the msg lands; re-dispatch on `storeRefreshMsg` while zoomed.
- Add `PRUrl string` to `CompletedTask`, hydrated from `Execution.PRUrl`; zoomed `enter` opens it (rows without one no-op with a status flash in the panel legend). Grid history stays capped at 5, untouched.
- Logs: raise cap (~tui.go:1112) 100 → 1000. Zoomed logs = scroll viewport over `m.logs`: tail-follow by default, `j/k`/`ctrl+u/d` scroll breaks follow, `G` jumps to bottom and resumes follow. No selection.
- ~+300/−20 LOC. Files: `tui.go`, `panels.go`, `tui_history_test.go` (in-memory `memory.Store` pattern exists ~line 100).

**AC:**
- 30 distinct completed tasks in the store → zoomed history lists all 30 with epic grouping preserved; scroll indicator correct.
- Logs zoom shows deep scrollback; new pushed log lines do NOT yank the viewport while scrolled up; `G` resumes follow. CI green.

## Out of scope

Zoomed metric cards (full-width braille trend), `?` help overlay, whole-grid vertical scroll for very short terminals, mouse-wheel scroll. Separate task if wanted later.

## Risks

1. **Ghost lines / repaint** (GH-1249 class) — centralize `tea.ClearScreen` on layout-height change.
2. **Test blast radius** — old keymap/hidden-default tests rewritten in P1/P2; pure renderer tests (`stage_strip_test.go`, `queue_width_test.go`, `task_card_test.go`) must survive untouched.
3. **Autopilot live-pull reorder** — selection keyed by PR number.
4. **`l` rebind muscle memory** — call out in help footer + release note.
5. **Narrow terminals** — enforce per-panel `minW/minH` with a blank-box guard (grom `safeRender` idiom) to avoid underflow panics mid-resize.

## Verification (per PR)

`make test`, `make lint`, width-invariant matrix test, keymap Update-level tests, fake-`browserOpener` URL tests, zoom-list viewport table tests, `historyZoomCmd` against in-memory store. Live smoke: `./bin/pilot start` wide + narrow terminal — git graph on first frame, hjkl walk, enter/esc zoom on every panel, all-items lists zoomed, logs follow behavior.
