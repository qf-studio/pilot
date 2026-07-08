# TASK-390: Dashboard TUI redesign — grot design system

**Status:** 🚀 PR opened — implementation complete in worktree (7 commits incl. docs)
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

4. `be788b94` — **feat(dashboard): stacked series colors for stat cards**
   (session 3). User feedback: uniform-tone bands made all three cards look
   identical; the earlier 2-row per-cell-row gradient collapsed into
   dark/light dot banding. Now `render.BrailleStacked` with flat per-series
   colors (banding impossible by construction): tokens = dim-accent cached
   mass + bright-accent fresh cap (detail line in cached tone = color key);
   queue depth = sage succeeded + rose failed caps (matches ✓/✗ detail);
   cost = uniform `Dim(sage, 0.85)`. Plumbing: `GetDailyMetrics` aggregates
   `tokens_cache_read/write` per day; new `CachedTokenHistory` /
   `SuccessHistory` / `FailedHistory` arrays.
5. `d567e34b` — **test(cmd): banner meta test to banner + legend contract**
   — was silently failing since 1c8edb12 (asserted adapter chips in banner);
   rewritten against the split surfaces via new `AdapterLegendForTest` hook.

Tests: full suite green (47 pkgs); added `grot_chrome_test.go`
(full-render width invariants + grot chrome landmarks, now exercising a
live two-PR autopilot fixture). Lint 0 issues.

Visual verification (session 3): dashboard Model rendered against the
real `~/.pilot/data/pilot.db` with forced truecolor; per-series ANSI codes
confirmed (bright/dim blue, sage/rose, dim sage). Note for reruns: the
live TUI under a scripted pty stalls on termenv OSC/CSI queries — answer
`]11;?` and `[6n` or render the Model directly.

## Key decisions

- **Import grot, don't vendor** — public v0.1.0 tag, `pkg/tui` unchanged since
  tag, single source of truth for the design system across both tools.
  See memory `decisions/decision_grot_design_system.md`.
- Emoji removed only from the **TUI/daemon surface**. Chat-adapter messages
  (Telegram/Slack) and CLI subcommands (`status`/`brief`/`replay`/`onboard`/
  `doctor`) intentionally left — different surfaces.

## Remaining / follow-ups

- [x] Push branch + open PR (URL in ## Refs) — review + merge pending
- [ ] Release after merge (self-upgrade distributes; until then dev binary
      in `~/.local/bin` can be overwritten by hot-upgrade pulling 2.233.10)
- [ ] Post-merge: index `decision_grot_design_system.md` (+ this task) into
      `.agent/knowledge/graph.json` from the MAIN checkout (graph.json had
      concurrent uncommitted edits; indexing deferred to avoid conflicts)
- [ ] CLI emoji sweep (hundreds of sites, mechanical) — dispatch to Pilot with
      the glyph table from `internal/dashboard/grot_chrome.go` as spec
- [ ] Optional: adopt grot `render.SegmentMeter` (`■■■` bars) for progress bars
      in the queue panel to replace `[████░░]` — deferred, not in scope
- [ ] Verify goreleaser/homebrew builds are happy on go 1.25 at next release

## Refs

- PR: https://github.com/qf-studio/pilot/pull/4061
- grot: /Users/aleks.petrov/Projects/startups/grot · github.com/qf-studio/grot
- Design source: grot `cmd/grot/demo.go` gallery + `pkg/tui/theme.Pilot`
