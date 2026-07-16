# TASK-406: QUEUE panel — sub-issue rows break column alignment + render bare IDs as titles

**Status**: ✅ Completed (shipped 2026-07-15: #4335 decomposed → #4338/PR#4340 render fix + #4339/PR#4341 title persist, both merged)
**Created**: 2026-07-15
**Assignee**: Pilot

---

## Context

**Problem**:
Epic decomposition produces sub-issue task IDs like `GH-4328-1` (9+ chars). The QUEUE panel's row renderer hardcodes the ID column at 7 chars (`%-7s`, `internal/dashboard/tui.go:2035`), so every sub-issue row overflows the column by 2+ chars and pushes title/progress-bar/meta out of alignment with sibling rows (observed live 2026-07-15 with GH-4328-1…5). Companion defect: those sub-issue rows (and occasionally normal rows, e.g. GH-76) render the bare task ID in the title column (`GH-4328-1  GH-4328-1`) because the execution row's title is empty at creation and the display falls back to ID.

**Goal**:
Queue rows stay column-aligned regardless of ID length, and sub-issue rows show meaningful titles.

---

## Known Pitfalls & Patterns

- **PITFALL** (90%, mem-151): Epic collapse → scaffold-only under-delivery that passes CI green — helpers added but `tui.go` never edited, parent auto-closed. → Acceptance below REQUIRES the diff to modify `renderTask`/`renderTasks` in `tui.go` and includes rendered-output integration assertions, not just helper unit tests.
- **PITFALL** (95%, mem-024): ANSI-styled strings break naive width math in this panel (`truncateVisual` bug blanked a styled line). → All width assertions must use `lipgloss.Width`, following `queue_width_test.go` (GH-3970).
- **LEARNING** (95%, mem-019 context): dashboard task rows hydrate from the executions store; title emptiness is a data-side issue — prior art backfilled `task_title` at resolution sites (#4285, `UpdateExecutionTitle` #4283).

---

## Acceptance Criteria

- [ ] With a mixed queue (`GH-11`, `GH-4328`, `GH-4328-1`), all rows' progress bars start at the same column and no row exceeds panel inner width (assert with `lipgloss.Width`).
- [ ] ID column width is computed per render pass (max visible ID, sane cap ~12 with ellipsis beyond), and the title flex width compensates so total row width is unchanged (`fixedCols` derivation at `tui.go:1963` adjusted accordingly).
- [ ] Sub-issue rows show a real title: prefer stored `task_title`; when empty, fall back to `<parent-title> · N/M` (or the planned-subtask title at creation), never the bare ID duplicated.
- [ ] The diff modifies `internal/dashboard/tui.go` (`renderTask` and/or `renderTasks`) — helper-only additions do not satisfy this task (mem-151 guard).
- [ ] Existing `queue_width_test.go` suite still green.

---

## Implementation

### Phase 1: Dynamic ID column (render side)
**Goal**: alignment independent of ID length

**Tasks**:
- [ ] In `renderTasks` (tui.go:1890): compute `idW = max(7, min(12, longest visible task ID))` over `sorted`; pass to `renderTask`.
- [ ] In `renderTask` (tui.go:1962): replace `%-7s` (tui.go:2035) with width-parameterized padding; derive `fixedCols` from `idW` instead of the constant 45; keep min title width 20.
- [ ] IDs longer than the cap: truncate with `…` (use existing `truncateVisual`).

**Files**:
- `internal/dashboard/tui.go` — renderTasks/renderTask
- `internal/dashboard/queue_width_test.go` — extend with sub-issue-ID table cases

### Phase 2: Title fallback (data + render side)
**Goal**: no bare-ID titles

**Tasks**:
- [ ] Executor: when epic decomposition creates sub-issue executions, persist the planned subtask title via the existing lifecycle/`UpdateExecutionTitle` seam (prior art #4283/#4285) so the monitor feed carries it.
- [ ] Dashboard fallback: in row hydration or `renderTask`, when `Title == "" || Title == ID`, render `parentTitle · i/n` when parent linkage is resolvable, else dim the ID rather than duplicating it.

**Files**:
- `internal/executor/epic.go` (or the decomposition create site) — title propagation
- `internal/dashboard/tui.go` — fallback rendering

---

## Out of Scope

- HISTORY panel layout (already width-aware per GH-3970 pattern; separate dedup rules #4288).
- Zoom view re-layout beyond inheriting the shared `sortedTasks` order.
- Any change to task ID format.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| ID column sizing | fixed wider (9), per-render max, global config | per-render max, capped 12 | no wasted width when no sub-issues visible; cap bounds pathological IDs |
| Empty-title fix layer | render-only fallback, data-side persist, both | both | render fallback covers legacy rows; data-side fixes it at source (prior art #4283/#4285) |

---

## Verify

```bash
go test ./internal/dashboard/ -run 'TestRenderTask|Queue' -v
make test
make lint
```

---

## Done

- [ ] Mixed-ID queue renders aligned at widths 65/90/120 (table-driven, `lipgloss.Width` assertions).
- [ ] Sub-issue execution rows created after this change carry titles; legacy empty-title rows render the fallback.
- [ ] `make test` / `make lint` green.

---

## Refs

- Live repro: dashboard 2026-07-15 (GH-4328-1…5 rows shifted +2 cols; `GH-76 GH-76` bare-ID title).
- Prior art: GH-3970 width-aware rows (`queue_width_test.go`); #4283 `UpdateExecutionTitle`; #4285 title backfill; #4288 HISTORY title dedup.
- Pilot issue: https://github.com/qf-studio/pilot/issues/4335

---

**Last Updated**: 2026-07-15
