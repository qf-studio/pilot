# TASK-383: Dashboard HISTORY — replace checkmark strip with pipeline-progress fraction

**Status**: ✅ Completed 2026-07-04 — [#3879](https://github.com/qf-studio/pilot/issues/3879) → PR [#3880](https://github.com/qf-studio/pilot/pull/3880) merged (`cdaccb42`), released v2.214.0
**Created**: 2026-07-05
**Last Updated**: 2026-07-05 (archived; shipped via #3880)
**Assignee**: Pilot

---

## Context

The dashboard HISTORY panel renders each task's pipeline progress as a
variable-length checkmark run built by `buildStageStrip`
(`internal/dashboard/stage_strip.go`):

```
✓✓ spec_validated GH-3873  fix(autopilot): pre-mer...  1m ago
✓✓✓✓✓✓✓ released GH-3874  fix(canary): stop h...  28m ago 426M
```

Two problems:
1. **Not readable** — the reader must *count* identical glyphs to gauge progress;
   `✓✓` vs `✓✓✓✓✓✓✓` conveys nothing at a glance.
2. **Wrong for retried tasks** — the strip counts raw `execution_events`
   (retries included) and truncates at `maxStageStripGlyphs = 8`, so a churny
   task shows a meaningless run like `✓✓✗✓✓✗✓✓` that does not reflect actual
   pipeline position.

Replace the glyph run with a fixed-denominator **`reached/total` fraction** that
reads in O(1) and is retry-proof (reflects *how far*, not *how many events*).

Approved representation (fraction, denominator 7, **no in-panel legend**):

```
╭─ HISTORY ─────────────────────────────────────────────────────────╮
│                                                                   │
│  7/7  released        GH-3874  fix(canary): stop hardc…  28m 426M │
│  1/7  spec_validated  GH-3873  fix(autopilot): pre-mer…   1m      │
│  4/7  ✗ ci_failed     GH-3901  feat(api): add rate lim…   3m 1.2G │
│                                                                   │
╰───────────────────────────────────────────────────────────────────╯
```

Color (already applied via the row's status style) carries state — **no legend
row, no key, no caption is added to the panel**:
- sage `#7ec699` = shipped (`7/7 released`)
- steel `#7eb8da` = in-flight (`1/7 spec_validated`)
- rose `#d48a8a` = failed (`4/7 ✗ ci_failed`)

The `✗` prefix on the stage word is the only extra glyph on a failed row and is
self-explanatory. Peak-RSS indicator (`426M` / `1.2G`, GH-3028) is unchanged.

## Acceptance Criteria

- [ ] `buildStageStrip` returns `"<reached>/<total> <stage>"` (e.g. `7/7 released`)
      instead of a `✓`-run. On a failure the stage word is prefixed with `✗ `
      (e.g. `4/7 ✗ ci_failed`).
- [ ] `total` is the fixed canonical ladder length **7**
      (`spec_validated → running → commit → pr_created → ci_passed → merged → released`).
- [ ] `reached` = 1-indexed ladder position of the **last successfully completed**
      stage, computed from a new `stageLadderPosition(memory.Stage) int` helper —
      NOT from `len(events)`. Retries do not inflate it.
- [ ] `released` renders `7/7`; `spec_validated` renders `1/7`; a task that failed
      at CI renders `4/7 ✗ ci_failed` (reached pr_created=4, died at ci).
- [ ] Fallback when there are zero events (pre-events executions): keep today's
      behavior conceptually — success → `7/7 <terminal stage>` is NOT assumed;
      instead render the single-glyph fallback replaced by a minimal
      `<pos>/7 <stage>` derived from the terminal status, or `–` when no stage is
      known. (Do not fabricate a `7/7`.)
- [ ] **No legend / key / caption** is added anywhere in the HISTORY panel. The
      color+`✗` convention stands alone.
- [ ] Applied consistently in BOTH the standalone row (`renderStandaloneLine`,
      `internal/dashboard/tui.go`) and the epic-group rows (`historyGroup` /
      `renderHistory`, same file) so grouped children match.
- [ ] Existing columns unchanged: id (7), title, time-ago (8), peak-RSS suffix.
      The fraction column is dynamic-width; the title width calc
      (`titleWidth := iw - 2 - stripWidth - ...`) must use the actual rendered
      fraction width via `lipgloss.Width`.
- [ ] `maxStageStripGlyphs` cap logic is removed/retired for HISTORY (no longer a
      glyph run). `maxStageStripLabelWidth` truncation of the stage word is kept.

## Implementation

### 1. `stageLadderPosition` helper (`internal/dashboard/stage_strip.go`)

Add, modeled on `pipelineStagePosition` (`tui.go:232`) but over `memory.Stage`:

```go
// Canonical 7-stage pipeline the HISTORY fraction is measured against.
// Position is 1-indexed; released == stageLadderTotal.
const stageLadderTotal = 7

func stageLadderPosition(s memory.Stage) int {
    switch s {
    case memory.StageSpecValidated: return 1
    case memory.StageRunning:       return 2
    case memory.StageCommit:        return 3
    case memory.StagePRCreated:     return 4
    case memory.StageCIPassed:      return 5
    case memory.StageMerged:        return 6
    case memory.StageReleased:      return 7
    // off-ramp / terminal stages resolve to the last completed ladder rung:
    case memory.StageAwaitingApproval: return 5 // reached ci_passed, gated
    case memory.StageCIFailed:         return 4 // reached pr_created, died at ci
    case memory.StageFailed, memory.StageStalled: return 0 // filled at build time from prior event
    case memory.StageQueued, memory.StageNoOp, memory.StageSkipped: return 0
    }
    return 0
}
```

For `StageFailed`/`StageStalled` (generic terminal failures that don't name a
ladder rung), `buildStageStrip` should compute `reached` from the **previous**
event's stage (the last good rung before the failure), mirroring the existing
label fallback at `stage_strip.go:56-59`.

### 2. Rewrite `buildStageStrip` body

- Determine `current` = last event's stage; `failed` = `stageStripFailureStages[current] || executionFailed`.
- `reached`:
  - if `failed` → position of the last **non-failure** stage (walk back from the
    end past failure stages), matching the label-fallback logic already present.
  - else → `stageLadderPosition(current)`.
- Build `"%d/%d %s%s"` = reached, `stageLadderTotal`, (`"✗ "` if failed else `""`),
  truncated stage word.
- Keep the zero-events fallback (see acceptance) — no `✓`-run anymore.

### 3. Row width (`renderStandaloneLine`, `tui.go:2602` + epic-group renderer)

`stripText` already flows from `task.StageStrip`; `stripWidth :=
lipgloss.Width(strip)` already handles a dynamic width, so the fraction slots in.
Verify the epic-group child lines use the same `StageStrip` field/formatter.

## Testing

- Unit (`internal/dashboard/stage_strip_test.go`): table-driven over
  `buildStageStrip` — assert `released→"7/7 released"`, `spec_validated→"1/7
  spec_validated"`, `ci_failed→"4/7 ✗ ci_failed"`, a retried event stream still
  yields the ladder-position fraction (not the event count), and the zero-events
  fallback. Add `stageLadderPosition` cases for every `memory.Stage`.
- Visual: run `pilot start --dashboard`; confirm HISTORY shows fractions, colors
  map (sage/steel/rose), no legend row appears, columns still align, RSS suffix
  intact.

## Scope Fence

HISTORY panel only. Do **not** touch the live ACTIVE-card pipeline rail
(`renderAutopilotRail` / `pipelineStagePosition`, `tui.go:232-286`) — its named
5-node rail (`✓ ci ── ✓ rebase ── ✓ merge ── ✓ tag ── ◐ release`) is already
readable and stays as-is.

## Refs

- `internal/dashboard/stage_strip.go` (`buildStageStrip`, `maxStageStripGlyphs`,
  `stageStripFailureStages`)
- `internal/dashboard/tui.go` (`renderStandaloneLine:2602`, `renderHistory:2547`,
  `historyGroup:2404`, `formatRSSMB:2636`, `pipelineStagePosition:232` for pattern)
- `internal/memory/store.go` (`Stage` enum `688-706`)
- Design: pilot-design skill (muted palette: sage `#7ec699`, steel `#7eb8da`,
  rose `#d48a8a`); 65-char inner width
- Pilot issue: https://github.com/qf-studio/pilot/issues/3879
