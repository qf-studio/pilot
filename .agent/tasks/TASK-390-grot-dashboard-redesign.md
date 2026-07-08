# TASK-390: Dashboard TUI redesign — grot design system

**Status:** ✅ Implemented in worktree `grot-dashboard-redesign` (4 commits, NOT pushed/merged yet)
**Branch:** `worktree-grot-dashboard-redesign`
**Date:** 2026-07-08
**Mode:** MANUAL (interactive session, user-directed worktree implementation)

## Goal

Restyle the whole `pilot start --dashboard` TUI to the grot design language
(github.com/qf-studio/grot — our btop-style Grafana terminal UI): rounded
cards with lowercase titles embedded in the border, border-embedded legends
(`┤ ● p95 4.7m ├`), big centered stat values, braille sparkline trend bands,
and a semantic glyph vocabulary instead of emoji.

## What shipped (worktree commits)

1. `57996da4` — **feat(dashboard): restyle TUI to grot design system**
   - New dep: `github.com/qf-studio/grot v0.1.0` (`pkg/tui/render` + `pkg/tui/theme`,
     MIT, lipgloss-only). `theme.Pilot` == the exact palette pilot already used.
   - `internal/dashboard/grot_chrome.go` (new): theme/chrome adapters
     (`renderPanel`/`renderPanelInfo`/`renderPanelStyled`), `buildStatCard`,
     `stretchSeries`, glyph-vocabulary doc block.
   - Banner: boxed 9-line `PILOT` frame → 2-line grot header (bold-accent
     ` pilot` + dim `·`-separated segments; adapter chips + uptime + clock).
   - Metrics cards → grot demo row-1 stat cards (`tokens`, `cost`, `queue depth`):
     big centered value, dim detail, 2-row braille trend with dim gradient.
   - All panels via grot chrome: `queue` (legend `┤ ● N running ├`), `autopilot`,
     `eval` (legend `global`), `history`, `logs`, git graph (accent focus border),
     update card (amber warn chrome, `u: upgrade` hint as border legend).
   - Deleted: hand-rolled border builders, orange panel variants, block-char
     sparkline code (~380 net lines removed).
   - **go 1.24.2 → 1.25.0** (grot requirement) + 4 CI workflows to `go-version: '1.25'`.
2. `343b9eb5` — **refactor(dashboard): replace emoji with design-system glyphs**
   - Dashboard log stream + daemon console startup/shutdown: emoji → glyphs.
   - `IssueInfo.LogEmoji` → `LogMark` (uniform `▸` intake mark).
   - Vocabulary: `●` active `○` inactive `◌` waiting `▸` intake `✓`/`✗` outcome
     `!` warning `↑` update `⟲` retry/restart `·` separator.

3. `1c8edb12` — **feat(dashboard): grot row grammar for banner, history, autopilot**
   - Banner → single grot line: ` pilot ●` wordmark liveness dot (sage,
     pulses on `sparklineTick`) · version · env · model, `up Xm · HH:MM utc`
     right. Narrow widths drop env→model segments, never wordmark/clock.
   - Adapter chips → queue border legend `┤ ● 2 running  ● gh  ○ 6 idle ├`
     (`buildAdapterLegend`); idle adapters collapse to a count; hardcoded
     `daemon ●` chip deleted (zero-information).
   - History rows: variable-width `N/7 label` strip → fixed columns with
     7-rung `render.SegmentMeter` ladder + dim stage label. `buildStageStrip`
     → `buildStageInfo` returning structured `StageInfo{Reached,Label,Failed,
     Known}`; GH-4023 max-rung reducer semantics preserved verbatim.
     Alignment fixes: ANSI-styled `%8s` time padding applied pre-style,
     RSS column fixed-width, epic `[##--]`/`[N/N]` → `■■□□`/`N/N`.
   - Autopilot: 5-node rail + always-on `0/3` fraction → one history-grammar
     row per PR (glyph, #id, title, 5-cell ci→rebase→merge→tag→release
     meter, stage label, age), `┤ ● N prs ├` border legend, amber
     `↳ ⟲ retry 2/3 · error` detail line only when failures exist.
     Rail spinner + panel tick plumbing removed.
   - `statusIconStyle` → glyph vocabulary (✓ ✗ ● ◌ ○ ⟲ ! ·).

Tests: full suite green (47 pkgs); added `grot_chrome_test.go`
(full-render width invariants + grot chrome landmarks, now exercising a
live two-PR autopilot fixture). Lint 0 issues.

## Key decisions

- **Import grot, don't vendor** — public v0.1.0 tag, `pkg/tui` unchanged since
  tag, single source of truth for the design system across both tools.
  See memory `decisions/decision_grot_design_system.md`.
- Emoji removed only from the **TUI/daemon surface**. Chat-adapter messages
  (Telegram/Slack) and CLI subcommands (`status`/`brief`/`replay`/`onboard`/
  `doctor`) intentionally left — different surfaces.

## Remaining / follow-ups

- [ ] Push branch + open PR + review + merge
- [ ] Post-merge: index `decision_grot_design_system.md` (+ this task) into
      `.agent/knowledge/graph.json` from the MAIN checkout (graph.json had
      concurrent uncommitted edits; indexing deferred to avoid conflicts)
- [ ] CLI emoji sweep (hundreds of sites, mechanical) — dispatch to Pilot with
      the glyph table from `internal/dashboard/grot_chrome.go` as spec
- [ ] Optional: adopt grot `render.SegmentMeter` (`■■■` bars) for progress bars
      in the queue panel to replace `[████░░]` — deferred, not in scope
- [ ] Verify goreleaser/homebrew builds are happy on go 1.25 at next release

## Refs

- grot: /Users/aleks.petrov/Projects/startups/grot · github.com/qf-studio/grot
- Design source: grot `cmd/grot/demo.go` gallery + `pkg/tui/theme.Pilot`
