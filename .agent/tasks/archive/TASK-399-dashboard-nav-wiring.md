# TASK-399: Dashboard grom-nav WIRING — consume the merged grid/panels/zoomlist helpers

**Status**: ✅ SHIPPED 2026-07-11 — #4203 → PR #4204 merged (`9a9f375a`, 14m dispatch→merge). All 9 TestNav_* integration ACs delivered by exact name; helpers live in tui.go/zoom.go; git graph default-visible.
**Created**: 2026-07-11
**Last Updated**: 2026-07-12
**Type**: feat(dashboard)
**Follows**: TASK-398 / #4199 (scaffold shipped as #4200→PR #4201, in `main` @ `576b38ae`, v2.237.0)

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4203

## Why this exists

TASK-398/#4199 was decomposed into a **single** sub-issue (#4200) that scoped itself to
scaffolding only. PR #4201 added `internal/dashboard/grid.go`, `panels.go`, `zoomlist.go`
(+ tests) and **merged green** — but never touched `tui.go`, `gitgraph.go`, or
`grot_chrome.go`. The helpers (`focusMove`, `computeLayout`, `renderZoomList` primitives)
are **defined, tested, and completely uncalled** — dead code. None of the 5 user
requirements actually ship. CI passed because new-file unit tests are trivially green; no
test forced the integration.

**This task is the missing wiring half.** It MUST consume the already-merged helpers —
do NOT reimplement or duplicate them.

## Already in tree (consume these — do not re-create)

`internal/dashboard/grid.go`:
- `type Rect struct{ X, Y, W, H int }`
- `focusMove(rects []Rect, cur int, dir byte) int` — dir is `'h'/'j'/'k'/'l'`; returns new index (or `cur` if nothing that way). Has perpendicular-axis tie-break for the tall git column.
- `cropVertical(s string, offset, height int) string`

`internal/dashboard/panels.go`:
- `type panelID int` — `panelQueue, panelAutopilot, panelHistory, panelLogs, panelGit` (String() gives display name)
- `var panelRegistry []panelDef` — 5 navigable panels in left-column stack order; `panelGit` laid out separately. Banner/metric-cards/eval/update-notice are **chrome (non-navigable)** by design.
- `panelIndex(id) int` / `panelByID(id) (panelDef, bool)` — map panelID ↔ rect slice index
- `type layoutHeights struct{ Queue, Autopilot, History, Logs int }`
- `computeLayout(termW, termH int, h layoutHeights, gitVisible bool) []Rect` — returns rects indexed identically to `panelRegistry`; handles git hidden / side-by-side (`termW >= stackedLayoutThreshold`) / stacked-below. **Feed its output straight to `focusMove`.**
- `safeRender(def panelDef, r Rect, content string) string` — blank-box guard below MinW/MinH
- `stackedLayoutThreshold` = `panelTotalWidth + 1 + 20`

`internal/dashboard/zoomlist.go`:
- `zoomListViewportHeight(h int) int` — rows that fit (overhead 5)
- `ensureSelVisible(sel, scroll, total, visible int) (int, int)` — clamps selection + scroll
- `zoomListIndicator(start, end, total int) string` — `[a-b of N]`
- `zoomListSelector(selected bool) string` — `▸ ` / `  `

## Wiring work

### 1. Model state (`tui.go` Model struct)
Add: `focus panelID` (default `panelQueue`), `zoomed bool`, `zoomSel int`, `zoomScroll int`,
`zoomHistory []CompletedTask`, `browserOpener func(string) error` (defaults to the existing
`openBrowser`; injectable for tests). **Remove** `gitGraphFocus`.

### 2. `View()` — compose from `computeLayout`
Replace the three ad-hoc layout branches (git-hidden / stacked / side-by-side, ~tui.go:1227–1249) with:
- Measure each left panel's rendered height → build `layoutHeights`.
- `rects := computeLayout(m.width, m.height, heights, gitVisible)`.
- **Grid mode**: render each visible panel into its `rects[panelIndex(id)]` (via `safeRender`), compose with lipgloss; keep the pad/truncate-to-`m.height-1` + help-footer-on-last-line logic and `tea.ClearScreen` guards. Centralize ClearScreen: emit only when total layout height changes frame-to-frame.
- **Zoomed mode** (`m.zoomed`): render ONLY `m.focus` at `(m.width, m.height-1)`; grid untouched so esc-return is free.
- Left column stretches at all widths (kills the fixed-69 behavior — already encoded in `leftColumnWidth`).

### 3. `Update()` key dispatch — new keymap
Grid mode:
- `h/j/k/l` + arrows → `m.focus = panelRegistry[focusMove(rects, panelIndex(m.focus), dir)].ID` (recompute rects or cache them on the Model each View).
- `tab` → cycle focus in registry order (fallback for non-vim users).
- `enter` → `m.zoomed = true`; reset `zoomSel=0, zoomScroll=0`; if focus==history dispatch `historyZoomCmd`.
- `ctrl+d`/`ctrl+u` → git half-page scroll **only when `m.focus == panelGit`**.
- `g` → toggle git graph. `L` → toggle logs (**rebound from `l`**). `b` banner. `u` upgrade. `q`/`ctrl+c` quit.
- **Remove** the old `gitGraphFocus`/`tab`-binary logic and the grid-mode `j/k` task-selection + git-scroll double duty.

Zoomed mode:
- `esc` → `m.zoomed = false` (always).
- `j/k` + arrows → list panels: `zoomSel±1` then `ensureSelVisible`; scroll panels (logs/git): move `zoomScroll`.
- `ctrl+d/u`, `pgdn/pgup` → half-page. `g`/`G` → top/bottom (`G` resumes logs tail-follow).
- `enter`/`o` → list panels open the selected item's URL via `m.browserOpener`; non-list panels (git/logs) `enter` exits zoom.
- `q`/`ctrl+c` quit.

### 4. Focus chrome
Thread `focused bool` through every panel renderer so the focused panel gets the existing
`focusChrome` accent border (`grot_chrome.go:37`). Delete the git-only special case.

### 5. Git graph visible by default
Set `gitGraphMode: GitGraphVisible` in every Model constructor (~tui.go:466–527, 728) and
have `Init()` batch `refreshGitGraphCmd`/`gitRefreshTickCmd` so it paints on the first frame.

### 6. Zoomed list rendering (consume `zoomlist.go`)
- Queue zoomed: all `m.tasks` (uncapped), `▸` selection, `enter/o` opens `TaskDisplay.IssueURL`, selection syncs `syncGitGraphToSelectedTask`.
- Autopilot zoomed: all `GetActivePRs()` **sorted by PR number, selection keyed by PR number** (survives live-pull reorder), per-PR `GetPRFailures` detail lines (non-selectable), `enter` opens `PRState.PRURL`. No `maxAutopilotRows` cap when zoomed.
- History zoomed: `historyZoomCmd` → `store.GetRecentExecutions(500, scope)` → up-to-100 distinct-by-task (raise the distinct helper's n) → hydrate rows incl. `ListExecutionEvents` stage strips **inside the Cmd** → `historyZoomMsg` fills `m.zoomHistory`; re-dispatch on `storeRefreshMsg` while zoomed. Add `PRUrl string` to `CompletedTask` (from `Execution.PRUrl`); `enter` opens it (no-op flash if empty).
- Logs zoomed: raise cap (~tui.go:1112) 100 → 1000; scroll viewport over `m.logs`, tail-follow default, breaks on scroll-up, `G` resumes.
- Grid-mode caps (history 5, autopilot 4, logs tail) stay UNCHANGED — zoom is the "see all" path.

## Acceptance criteria (the guard that was missing)

**Integration tests at the `Update`/`View` level — these MUST fail if the wiring is absent:**

1. `TestNav_FocusMovesOnHJKL`: seed a Model with tasks+PRs+history+logs+git visible; send `KeyMsg{'l'}` / `'h'` / `'j'` / `'k'` and assert `m.focus` changes to the spatially-correct panel (e.g. from `panelQueue`, `l` → `panelGit`; `h` back to a left panel).
2. `TestNav_EnterZoomsEscRestores`: `enter` sets `m.zoomed==true` and `View()` output contains only the focused panel's chrome (not the sibling panels' titles); `esc` restores and `m.focus`/scroll are intact.
3. `TestNav_GitGraphVisibleOnFirstFrame`: a freshly-constructed Model has `gitGraphMode == GitGraphVisible` and `View()` (before any `g` press) renders the git panel.
4. `TestNav_LogsRebind`: `L` toggles logs; `l` does NOT toggle logs (it moves focus).
5. `TestNav_ZoomedQueueOpensURL`: zoom queue, select item 2, `enter` → injected `browserOpener` received that task's `IssueURL`.
6. `TestNav_ZoomedAutopilotUncapped`: 8 active PRs → zoomed autopilot lists all 8 (no "+N more"); selection stable across a simulated PR-list reorder.
7. `TestNav_ZoomedHistoryUncapped`: in-memory store with 30 distinct completed tasks → `historyZoomCmd`/`historyZoomMsg` populates `m.zoomHistory` with all 30, epic grouping preserved.
8. `TestNav_ZoomedLogsFollow`: pushing new log lines while scrolled up does NOT move the viewport; `G` resumes tail-follow.
9. Width-invariant matrix: every rendered line is exactly `m.width` cols at 80/96/140/200, grid AND zoomed.

Plus: `make test`, `make lint` green; existing pure-renderer tests (`stage_strip_test.go`,
`queue_width_test.go`, `task_card_test.go`) still pass. Rewrite the old keymap tests
(`TestModelUpdate_TabFocus`, `_ScrollWhenFocused`, `_DashboardScrollWhenNotFocused`,
`_HalfPageScroll`, `TestViewHidden_NoGraph`, help-footer test) to the new keymap.

Live smoke: `./bin/pilot start` wide + narrow — git graph on first frame, hjkl walk across
all 5 panels, enter/esc zoom on each, all-items lists in zoomed history/autopilot, logs follow.

## Out of scope

Zoomed metric cards, `?` help overlay, whole-grid vertical scroll, mouse wheel. Metric cards
and banner remain chrome (non-navigable) per the merged `panelRegistry` design.

## Risks

- **Do NOT reimplement the merged helpers** — import/call them. A duplicate `focusMove` or
  `computeLayout` is a review-reject.
- **False-completion guard**: this issue is single-scope precisely because the epic split
  collapsed and the executor scoped itself to "scaffold, wire later in a sibling" that never
  existed. The AC integration tests are the backstop — they fail loudly if `Update`/`View`
  aren't actually wired. (See pitfall memory on decomposition-collapse under-delivery.)
- Ghost lines (GH-1249 class): centralize `tea.ClearScreen` on layout-height change.
