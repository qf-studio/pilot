# Autopilot Card Redesign — Variant A

**Status**: 🚧 Ready to file
**Created**: 2026-05-04
**Execution**: Sonnet 4.6 via Pilot
**Target file**: `internal/dashboard/tui.go`

---

## Context

**Problem.** Current autopilot card (GH-2455 "avionics") wastes 5 lines on 3 lines of signal:

```
╭─ AUTOPILOT ───────────────────────────────────────────────────────╮
│                                                                   │
│   STATE  ci_passed    PR  #2565    AGE  1m                        │
│   CI [████████]  MRG [░░░░░░░░]  RTY [░░░░░░░░] 0/3               │
│   ● ci-wait ── ○ rebase ── ○ merge ── ○ tag ── ○ release          │
│                                                                   │
╰───────────────────────────────────────────────────────────────────╯
```

Three problems:
1. **Fake progress bars.** CI is binary (pending/running/success/failure), MRG is binary (merged/not), RTY is a count not a percentage. Rendering them as 8-slot fills is theatre.
2. **STATE field duplicates the pipeline rail.** `ci_passed` text + `● ci-wait` dots = same info, two places, inconsistent (text says passed, dot says current).
3. **Wasted vertical space.** Two padding rows at top and bottom.

**Goal.** Replace with Variant A: PR identity on line 1, pipeline-as-status on line 2, conditional failure reason on line 3 only when relevant. Idle state collapses to one line.

---

## Target Layouts

**Steady state — waiting on CI:**
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  #2565  fix(upgrade): atomic binary replacement         1m42s   │
│  ◐ ci ── ○ rebase ── ○ merge ── ○ tag ── ○ release      0/3 ⟲   │
╰─────────────────────────────────────────────────────────────────╯
```

**CI failed, mid-retry (3rd line conditional):**
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  #2565  fix(upgrade): atomic binary replacement         4m10s   │
│  ✗ ci ── ○ rebase ── ○ merge ── ○ tag ── ○ release      2/3 ⟲   │
│  ↳ TestInstallToBinaryPath_Cleanup failed · linux-amd64         │
╰─────────────────────────────────────────────────────────────────╯
```

**Mid-pipeline (rebasing, CI already green):**
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  #2565  fix(upgrade): atomic binary replacement         3m08s   │
│  ✓ ci ── ◐ rebase ── ○ merge ── ○ tag ── ○ release      0/3 ⟲   │
╰─────────────────────────────────────────────────────────────────╯
```

**Idle:**
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  idle · no active PR                                            │
╰─────────────────────────────────────────────────────────────────╯
```

---

## Glyph + Color Spec

| Glyph | Meaning | Style (existing) | Color |
|---|---|---|---|
| `✓` | stage done | `statusCompletedStyle` | sage green `#7ec699` |
| `◐` | stage in progress (rotates) | `statusRunningStyle` | steel blue `#7eb8da` |
| `○` | stage pending | `dimStyle` | mid gray `#8b949e` |
| `✗` | stage failed | `statusFailedStyle` | dusty rose `#d48a8a` |
| `⟲` | retry indicator | `warningStyle` | amber `#d4a054` |

Animation: `◐` rotates `◐ → ◓ → ◑ → ◒ → ◐` on each 1s tick. Reuse the existing tick that drives `sparklineTick` (see `internal/dashboard/tui.go:398`). Pass tick into `AutopilotPanel` via a setter or struct field bumped from the parent `tea.Tick` handler.

---

## Files to Modify

| File | What |
|---|---|
| `internal/dashboard/tui.go` | Rewrite `AutopilotPanel.View()` (lines 130–204), `renderAutopilotRail()` (267–294). Delete `autopilotCIProgressPct()`, `renderAutopilotBar()`, and progress-bar usage (206–239). Add 1s tick field + setter on `AutopilotPanel`. |
| `internal/dashboard/tui_test.go` | Update `TestRenderAutopilotRailPositions` and any other autopilot tests for new glyphs/format. Add table-driven test covering: idle, ci-running, ci-failed, mid-pipeline, released. |
| caller in `internal/dashboard/tui.go` (parent model) | Wire animation tick into `autopilotPanel` (search for `sparklineTick` toggling — same place increments autopilot tick). |

**Untouched:**
- `internal/autopilot/*.go` — state machine stays exactly as-is. Only the rendering changes.
- All other panels (TOKENS, COST, TASKS, HISTORY, queue, etc.).
- `pipelineStagePosition()` (line 242) — reuse as-is for stage→index mapping.

---

## Implementation Steps

### Step 1 — Add tick field for `◐` rotation
Add to `AutopilotPanel` struct (line 120):
```go
type AutopilotPanel struct {
    controller *autopilot.Controller
    panelWidth int
    tick       int // increments per 1s animation tick, drives ◐ rotation
}
```
Add setter `SetTick(int)`. Wire from parent model where `sparklineTick` toggles.

### Step 2 — Rewrite `View()` (lines 130–204)
Three rendering branches:

**A. Disabled** (controller nil) — keep current `"  Disabled"` content.
**B. Idle** (no active PRs) — single line `"  idle · no active PR"` using `dimStyle`. (Optional enhancement: append last-release info if accessible cheaply.)
**C. Active PR** — two or three lines:

```
  #{PRNumber}  {truncatedTitle}{padding}{age}
  {rail}{padding}{retries}/{maxFailures} ⟲
  ↳ {truncatedError}                              ← only if Stage == Failed && Error != ""
```

Width budget (inner = 65 chars):
- Line 1: `  #NNNN  ` (9) + title (truncated to 47) + ` ` + age right-padded to 6 = 65
- Line 2: rail (~50 visible chars without ANSI) + padding + retries (`0/3 ⟲` = 5) = 65
- Line 3 (failure): `  ↳ ` (4) + truncated error (≤61) = 65

Use `lipgloss.Width()` for ANSI-safe length, or strip ANSI when computing padding. Existing helper pattern: see `renderPanel`.

### Step 3 — Rewrite `renderAutopilotRail(stage, ciStatus, tick)`
Drop the lamp-then-label approach, switch to glyph-then-stage-name:

```go
nodes := []string{"ci", "rebase", "merge", "tag", "release"}
spinner := []rune{'◐', '◓', '◑', '◒'}
pos := pipelineStagePosition(stage)

for i, name := range nodes {
    var glyph rune
    var glyphStyle, labelStyle lipgloss.Style
    switch {
    case i == 0 && ciStatus == autopilot.CIFailure:
        glyph, glyphStyle, labelStyle = '✗', statusFailedStyle, statusFailedStyle
    case i < pos:
        glyph, glyphStyle, labelStyle = '✓', statusCompletedStyle, statusCompletedStyle
    case i == pos:
        glyph = spinner[tick%4]
        glyphStyle, labelStyle = statusRunningStyle, titleStyle
    default:
        glyph, glyphStyle, labelStyle = '○', dimStyle, dimStyle
    }
    // append glyph + " " + name + (" ── " if not last)
}
```

Important: `pipelineStagePosition` already maps `StageFailed → 0`. The `✗` override on the failed CI stage is correct only when `ciStatus == CIFailure`. For other failure modes (e.g. rebase failed), the `✗` should land on the actual failed stage — but the current state machine collapses all failures to `StageFailed`. **Out of scope to fix.** For now: render `✗` on stage 0 only when CI failed; otherwise on `StageFailed` render `✗` at position 0 with error line 3 carrying the detail.

### Step 4 — Title source
PR struct exposes `PRTitle` (see `internal/autopilot/auto_merger.go:91`). Use directly. Truncate with the existing `truncateString` helper (line 318) to fit the width budget.

### Step 5 — Retries
Reuse `cfg.MaxFailures` and `controller.GetPRFailures(pr.PRNumber)` exactly as today (lines 162–167). Render as `{n}/{max} ⟲` with the count in `dimStyle` and the `⟲` in `warningStyle`. Suppress the `⟲` glyph when `n == 0` (replace with a space) to avoid noise on healthy runs.

### Step 6 — Multi-PR overflow
Keep current behaviour (`+ N more PR(s)`) on its own line if `len(prs) > 1`. Variant B (one-row-per-PR) is **out of scope** for this ticket.

### Step 7 — Delete dead code
Remove `autopilotCIProgressPct` (206–219) and `renderAutopilotBar` (221–239) once nothing references them. Verify with `grep -r renderAutopilotBar internal/`.

---

## Acceptance Criteria

- [ ] Card height: 4 lines (border + content + border) when idle; 4 when active steady-state; 5 when failed.
- [ ] No `[████░░░░]`-style progress bars anywhere in autopilot output.
- [ ] No `STATE` literal text anywhere in autopilot output.
- [ ] Stage glyphs render correct color per spec table.
- [ ] `◐` rotates through `◐◓◑◒` once per second when a stage is in progress.
- [ ] On `CIFailure`, the `ci` stage renders `✗` in dusty rose; failure reason appears on line 3, prefixed with `↳`, truncated to fit.
- [ ] Idle state is exactly one content line.
- [ ] All existing autopilot tests pass after updates.
- [ ] New table-driven test covers: disabled, idle, ci-running, ci-failed, rebase-in-progress, merged, released, failed-with-error.
- [ ] `make lint` clean.

---

## Test Approach

Extend `internal/dashboard/tui_test.go`:

1. **Update** `TestRenderAutopilotRailPositions` — assert new glyph set (`✓ ◐ ○ ✗`), new stage labels (`ci` not `ci-wait`), and that `tick` parameter rotates the in-progress glyph.
2. **Add** `TestAutopilotPanelView_AllStates` — table-driven, one row per state. Build a fake `*autopilot.Controller` (or mock the minimum surface) and assert the rendered output:
   - Contains the expected glyph for the current stage
   - Does NOT contain `[█` or `[░` (no fake bars)
   - Does NOT contain `STATE`
   - Width of every line == `panelTotalWidth`
   - Failure state contains `↳ ` prefix and the error substring
3. **Add** `TestAutopilotPanelTick_RotatesSpinner` — call `View()` 4 times incrementing tick, assert all 4 spinner runes appear.

---

## Forbidden Actions

- ❌ Do **not** modify `internal/autopilot/*.go` — state machine stays as-is.
- ❌ Do **not** touch other dashboard panels (TOKENS, COST, TASKS, HISTORY, queue, splash).
- ❌ Do **not** add new color constants — every required style already exists in `tui.go:48–117`.
- ❌ Do **not** introduce variant B (multi-PR table) — separate ticket if wanted.
- ❌ Do **not** change `pipelineStagePosition()` mapping.
- ❌ Do **not** add new dependencies.

---

## Verify

```bash
go build ./...
go test ./internal/dashboard/... -run Autopilot -v
make lint
```

Manual smoke (after merge):
```bash
make build && ./bin/pilot dashboard
```
Trigger a Pilot task, watch the autopilot card cycle through states.

---

## Issue Body Ready to File

```
Title: feat(dashboard): redesign autopilot card (variant A — pipeline-as-status)

Body:
Replace the current autopilot card (GH-2455 "avionics" layout) with a denser,
honest variant. Three problems with current:

1. Fake progress bars CI/MRG/RTY — CI is binary, MRG is binary, RTY is a count.
   Rendering them as 8-slot fills is theatre.
2. STATE field duplicates the pipeline rail and can disagree with it.
3. 5 lines of card; only 3 carry signal.

## Target — variant A

Steady state (waiting CI):
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  #2565  fix(upgrade): atomic binary replacement         1m42s   │
│  ◐ ci ── ○ rebase ── ○ merge ── ○ tag ── ○ release      0/3 ⟲   │
╰─────────────────────────────────────────────────────────────────╯
```

Failed (third line conditional, only on Stage==Failed && Error != ""):
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  #2565  fix(upgrade): atomic binary replacement         4m10s   │
│  ✗ ci ── ○ rebase ── ○ merge ── ○ tag ── ○ release      2/3 ⟲   │
│  ↳ TestInstallToBinaryPath_Cleanup failed · linux-amd64         │
╰─────────────────────────────────────────────────────────────────╯
```

Idle:
```
╭─ AUTOPILOT ─────────────────────────────────────────────────────╮
│  idle · no active PR                                            │
╰─────────────────────────────────────────────────────────────────╯
```

## Glyphs
- `✓` done — `statusCompletedStyle` (sage `#7ec699`)
- `◐` in progress, animated `◐◓◑◒` on 1s tick — `statusRunningStyle` (steel blue `#7eb8da`)
- `○` pending — `dimStyle` (mid gray `#8b949e`)
- `✗` failed — `statusFailedStyle` (dusty rose `#d48a8a`)
- `⟲` retry — `warningStyle` (amber `#d4a054`); suppress when retries == 0

All styles already exist in `internal/dashboard/tui.go:48-117`. No new colors.

## Files
- `internal/dashboard/tui.go` — rewrite `AutopilotPanel.View()` (130-204) and
  `renderAutopilotRail()` (267-294). Delete `autopilotCIProgressPct` (206-219)
  and `renderAutopilotBar` (221-239). Add `tick int` field + `SetTick(int)` to
  `AutopilotPanel`; wire from the parent model where `sparklineTick` toggles.
- `internal/dashboard/tui_test.go` — update `TestRenderAutopilotRailPositions`,
  add `TestAutopilotPanelView_AllStates` (table-driven), add
  `TestAutopilotPanelTick_RotatesSpinner`.

Reuse `pipelineStagePosition` (line 242) and `truncateString` (318) as-is.
PR title via `pr.PRTitle` (already on the struct, see auto_merger.go:91).

## Forbidden
- Do NOT modify `internal/autopilot/*.go` — state machine unchanged.
- Do NOT touch other dashboard panels.
- Do NOT add new color constants.
- Do NOT add variant B (multi-PR table) — separate ticket if wanted.

## Acceptance
- 4-line card steady state; 5 lines on failure; 3 lines idle.
- No `[████░░░░]` anywhere in autopilot output.
- No `STATE` literal in autopilot output.
- ◐ rotates through ◐◓◑◒ on 1s tick.
- On CIFailure, `ci` stage renders `✗` and reason appears on line 3.
- All autopilot tests pass; new table-driven test covers every state branch.
- `make lint` clean.

## Verify
```
go build ./...
go test ./internal/dashboard/... -run Autopilot -v
make lint
```
```

---

## Notes

- Plan doc lives at `.agent/tasks/autopilot-card-redesign.md`. Once filed, rename to `.agent/tasks/gh-NNNN.md` to match repo convention.
- If Pilot picks Sonnet 4.6 vs Opus, this is medium-effort scoped work — fits Sonnet's wheelhouse. Routing should pick Sonnet by default; no override needed.
