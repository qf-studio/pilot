# TASK-390: Dashboard TUI redesign — grot design system

**Status:** ✅ SHIPPED & RELEASED (2026-07-08, v2.234.0 → v2.235.8; PRs #4061 #4065 #4067 #4071 #4072 #4080 #4091)
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

## Release ledger (sessions 4–5, 2026-07-08 evening)

- **v2.234.0** — #4061 merged; tag by hand. goreleaser + homebrew green on
  go 1.25 (flagged risk didn't materialize) — but the **Docker workflow did**:
  Dockerfile builder still pinned `golang:1.24-alpine`, every tag's image
  failed until #4091 (verified via `workflow_dispatch`).
- **v2.234.1** — #4065: logs panel flexes to terminal bottom; content-sized
  in stacked mode so the git graph below keeps its height.
- **v2.235.0** — #4067 CLI emoji sweep + #4072 queue `SegmentMeter` rows:
  implemented by Pilot, merged + released by autopilot **fully autonomously**.
- **v2.235.1** — #4071: history rows show real execution status
  (`displayStatus` passthrough; muted-outcome labels — a skipped run no
  longer renders ✓ "running"; GH-4023 reducer untouched).
- **v2.235.8** — #4080: `pr_title` persisted in `autopilot_pr_state`
  (ALTER + gh3903 rebuild schema) with branch-name row fallback — autopilot
  rows survive `--replace` restarts with titles intact.
- Post-ship investigation (nav-research agent) of duplicate completed
  executions → **no dedup bug**; filed **#4100** (`--replace` graceful drain
  + exit-137/oom_killed split) and **#4101** (stale-recovery
  `execution_events` audit trail). See memories
  `pattern_selfheal_duplicate_completed_rows` /
  `pitfall_replace_restart_kills_inflight`.

## Remaining / follow-ups

- [x] Push branch + open PR + review + merge + release
- [x] Index `decision_grot_design_system.md` into graph (mem-090, forced by
      the CI drift gate on PR #4061 — deferral resolved early)
- [x] CLI emoji sweep → #4063 → PR #4067 (Pilot)
- [x] Queue progress bars → `SegmentMeter` → #4064 → PR #4072 (Pilot)
- [x] goreleaser/homebrew on go 1.25 verified (v2.234.0)
- [ ] #4100 / #4101 restart-safety follow-ups in Pilot queue

## Refs

- PR: https://github.com/qf-studio/pilot/pull/4061
- grot: /Users/aleks.petrov/Projects/startups/grot · github.com/qf-studio/grot
- Design source: grot `cmd/grot/demo.go` gallery + `pkg/tui/theme.Pilot`
