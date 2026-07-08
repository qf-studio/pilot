---
name: dashboard chrome delegates to grot pkg/tui — one shared design system
description: The dashboard TUI renders all card chrome (borders, titles, legends, braille sparklines, stat cards) via github.com/qf-studio/grot pkg/tui/render + theme.Pilot instead of hand-rolled builders. Requires go >= 1.25. Glyph vocabulary replaces emoji on the TUI/daemon surface.
type: decision
---

Decided 2026-07-08 (TASK-390, worktree `grot-dashboard-redesign`). grot is our
btop-style Grafana TUI; its `pkg/tui/{render,theme}` packages are public
(v0.1.0, MIT), lipgloss-only, and `theme.Pilot` is byte-identical to the muted
palette the dashboard already used — so pilot **imports** them rather than
vendoring or re-implementing.

**Why import (not copy):** single source of truth for the design system across
both tools; grot's demo gallery is the visual spec; `pkg/tui` was unchanged
between v0.1.0 and HEAD at adoption time. Cost: pilot's `go` directive bumped
1.24.2 → 1.25.0 (grot requirement) and CI workflows pin `go-version: '1.25'`.

**How to apply:**
- All card chrome goes through `internal/dashboard/grot_chrome.go`:
  `renderPanel(title, content, tw)` / `renderPanelInfo(title, info, ...)` for
  border-embedded legends / `renderPanelStyled(...)` for warn (amber) and
  focus (accent) chrome. Titles are lowercased there — pass anything.
- Stat cards: `buildStatCard(title, value, detail, history, accentHex, cw)` —
  grot demo row-1 look (centered bold value, dim detail, braille band).
- **Gotcha:** grot's `render.BrailleArea` RIGHT-ALIGNS series shorter than the
  band's dot-columns (width*2) — a 7-day history renders as a stub at the right
  edge. `stretchSeries(values, iw*2)` nearest-index upsamples first. Same
  applies to any future grot timeseries use with sparse data.
- Glyph vocabulary (documented in grot_chrome.go, NO emoji on TUI/daemon
  surface): `●` active · `○` inactive · `◌` waiting · `▸` intake · `✓`/`✗`
  outcome · `!` warning · `↑` update · `⟲` retry · `·` separator. Task-intake
  log prefix is `IssueInfo.LogMark` (uniform `▸`). System log lines lowercase.
- Chat-adapter messages and CLI subcommands keep emoji — different surfaces;
  CLI sweep is a pending follow-up (see TASK-390).
- Regression guard: `internal/dashboard/grot_chrome_test.go` asserts every
  rendered line is exactly panelTotalWidth and the chrome landmarks exist.

Related: [[decision_release_pipeline_tag_only]] (go 1.25 must hold through the
goreleaser pipeline at next release).
