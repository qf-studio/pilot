# TASK-420: Dashboard panels disagree about the same task — queue marks live work "done", history keeps dead work "running"

**Status**: 🚀 Dispatched to Pilot
**Created**: 2026-07-24
**Assignee**: Pilot

---

## Context

**Problem**:
The TUI's three panels read three different sources and are reconciled by
nothing. At a single observed moment they were wrong about two different
tasks **in opposite directions**, which makes cross-checking them useless —
the operator cannot tell which panel is lying without dropping to sqlite.

**Captured reproduction (2026-07-24 22:17:07Z, daemon v2.245.2):**

| Panel | Displayed | Ground truth (`executions` table) | |
|---|---|---|---|
| queue | `✓ done  GH-4536` | `running`, started 21:49:31, `completed_at` NULL, **3 live claude/node procs** | ❌ false-complete |
| history | `GH-4536 … running 26m ago` | same — running | ✅ correct |
| history | `GH-4531 … running 3h ago` | **6 rows, all `cancelled`** (set 19:09Z); issue closed | ❌ ghost row |
| autopilot | `idle · no active PR` | no PR existed for GH-4536 yet | ✅ correct, but PR-only — it says nothing about executions and is routinely misread as "nothing is happening" |

So on one screen: a running task shown as done, a cancelled task shown as
running, and a panel whose "idle" means something narrower than it looks.

**Why this matters**: the operational workaround is already codified as a hard
rule in the `pilot-aws` skill — *"Trust the ledger over dashboard panels"* —
and in the memory `tui-subtask-ghost-rows` ("restart clears; trust ledger").
A dashboard that must be distrusted is worse than no dashboard: it costs a
sqlite round-trip on every question and has repeatedly sent debugging down the
wrong path (this session: ~15 min lost, and an incorrect "it's done" call).

**Known sources**:
- **queue** renders `[]TaskDisplay` (`internal/dashboard/tui.go:354-365`),
  pushed in wholesale via `dashboard.UpdateTasks` (`tui.go:2677`) →
  `m.tasks = msg` (`tui.go:1132`). Producers are outside the dashboard
  package: `cmd/pilot/main.go:2171`, `cmd/pilot/main.go:3137`,
  `cmd/pilot/commands.go:2528,2552,2574`. `TaskDisplay.Status` is a plain
  string with no defined vocabulary and no tie to `executions.status`.
- **history** renders `[]CompletedTask`; the zoomed view reads the store
  directly (`internal/dashboard/zoom.go:535` `historyZoomCmd(store, projectPath)`),
  while the inline view is served from in-memory `completedTasks`
  (`tui.go:561,579,599,616`) that nothing reconciles against the ledger.
- **status vocabulary** is split by design: `displayStatus`
  (`internal/dashboard/stage_strip.go:69-75`) maps `executions.status` →
  icon, while the status *text* comes from a running-max reducer over
  `execution_events` (`StageInfo`, `stage_strip.go:61-68`). GH-3927/GH-4064
  already fixed one half of this (a skipped/running run rendering ✓).

**Goal**:
One source of truth for "what is this task doing right now", so no two panels
can disagree about the same task, and no panel can show a terminal task as
running or a running task as terminal.

---

## Known Pitfalls & Patterns

- **PITFALL** `tui-subtask-ghost-rows`: completion never closes the in-memory
  dashboard row and no ledger row reconciles it — restart clears. This task
  must fix the reconciliation, not add another restart-to-clear path.
- **GH-4368** (open): the ✓/✗ icon (`executions.status`) and the status text
  (`execution_events` ladder running-max) disagree **permanently** for rows
  whose event ladder never got a terminal rung. That issue covers only this
  icon-vs-text split *within* history — not the queue false-complete, and not
  the ghost rows. Land them consistently; do not duplicate or contradict it.
- **GH-3927 / GH-4064**: prior fixes in exactly this area collapsed every
  non-failed status to success. Any new mapping must keep a *still-running*
  status distinguishable from a *successful* one.

---

## Acceptance Criteria

- [ ] A task that is `running` in `executions` (no `completed_at`) is never
      rendered as done/complete in **any** panel.
- [ ] A task that is terminal in `executions` (`completed`/`failed`/
      `cancelled`/`stalled`/`no_op`/…) is never rendered as running in **any**
      panel, without requiring a daemon restart to clear.
- [ ] queue and history cannot disagree about the same task ID at the same
      render: they derive status from one shared resolver, not two independent
      strings.
- [ ] `TaskDisplay.Status` uses a defined, closed vocabulary tied to
      `executions.status` (reuse the `ExecStatus*` vocabulary from
      `internal/executor/lifecycle.go`, TASK-404) rather than a free-form
      string set by five separate call sites in `cmd/pilot`.
- [ ] Ghost rows are reconciled: an in-memory dashboard row whose ledger row
      has gone terminal is updated or dropped on the next refresh.
- [ ] The autopilot panel's `idle` state is unambiguous about scope — it must
      not read as "nothing is running" when executions are in flight.
      Wording/labelling change is acceptable; no new data source needed.
- [ ] Regression test reproducing the captured divergence: given a ledger with
      GH-A `running` and GH-B `cancelled`, no panel renders GH-A complete and
      none renders GH-B running.

---

## Implementation

### Phase 1: One status resolver
**Goal**: Kill the two-independent-strings design.

**Tasks**:
- [ ] Introduce a single resolver that maps a ledger row (+ its
      `execution_events` ladder) to the display status used by every panel.
      Fold `displayStatus` (`stage_strip.go:69`) and the `StageInfo`
      running-max reducer into it so icon and text can never diverge — this is
      GH-4368's fix; reference that issue and close it with this work.
- [ ] Define the closed vocabulary against `ExecStatus*`
      (`internal/executor/lifecycle.go`). Preserve the GH-3927/GH-4064
      invariant: running ≠ success.

### Phase 2: Queue panel reads the same truth
**Goal**: No more false-complete.

**Tasks**:
- [ ] Have the queue panel derive its status through the Phase 1 resolver.
- [ ] Audit the five `UpdateTasks` producers (`cmd/pilot/main.go:2171,3137`;
      `cmd/pilot/commands.go:2528,2552,2574`) — find which one publishes a
      completed-looking `TaskDisplay` for a task whose ledger row is still
      `running`, and fix at the source rather than papering over it in the
      renderer.

### Phase 3: Reconcile ghost rows
**Goal**: Terminal tasks stop rendering as running without a restart.

**Tasks**:
- [ ] On each refresh, reconcile in-memory `completedTasks`/`tasks` against the
      ledger for the visible set; update or drop rows whose ledger row is
      terminal.
- [ ] Keep it bounded — reconcile only the rows on screen (plus the zoom
      dataset), not the whole table, so the TUI refresh stays cheap.

**Files**:
- `internal/dashboard/stage_strip.go` — the shared resolver
- `internal/dashboard/tui.go` — `TaskDisplay`, queue render, in-memory reconcile
- `internal/dashboard/zoom.go` — zoomed history path
- `cmd/pilot/main.go`, `cmd/pilot/commands.go` — `UpdateTasks` producers

---

## Out of Scope

- Visual redesign of the panels. This is a correctness fix; layout, colours,
  and the stage strip's appearance stay as they are.
- The underlying reason GH-4531's rows went `cancelled` by operator surgery
  (that is TASK-419 / #4536).
- Adding new telemetry or a new store. The `executions` table is already the
  source of truth; this task makes the UI use it.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Source of truth | Reconcile panels against each other; make ledger authoritative for all | Ledger authoritative | It has been right every time; the operational runbook already says to trust it |
| Fix location for the queue false-complete | Renderer-side clamp; fix the producer | Fix the producer, resolver as backstop | A renderer clamp hides a producer publishing wrong state, which will resurface elsewhere (e.g. Slack/web views) |
| Status vocabulary | New dashboard enum; reuse `ExecStatus*` | Reuse `ExecStatus*` | TASK-404 already made this the one lifecycle vocabulary; a second enum re-creates the drift this task exists to fix |
| Relationship to GH-4368 | Land separately; fold in | Fold in and close GH-4368 | Same root cause (icon and text from independent sources); fixing one without the other leaves the panel half-reconciled |

---

## Verify

```bash
make build
go test ./internal/dashboard/... ./cmd/pilot/...
make lint
```

---

## Done

- [ ] A test builds a ledger fixture with one `running` and one `cancelled`
      task and asserts every panel's rendered status matches the ledger —
      reproducing the 22:17:07Z divergence and failing before the fix.
- [ ] `displayStatus` and the `StageInfo` reducer no longer produce
      independently-derived statuses; GH-4368 is closed by this work.
- [ ] No panel renders a `running` task as done or a terminal task as running,
      verified without a daemon restart.
- [ ] `make build`, `make lint`, `go test ./internal/dashboard/... ./cmd/pilot/...`
      green.

---

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4537 (`no-decompose`)
- Captured divergence 2026-07-24 22:17:07Z: queue `✓ done GH-4536` while the
  ledger had it `running` with 3 live claude/node processes; history
  `GH-4531 running 3h ago` while all 6 of its rows were `cancelled` since
  19:09Z and the issue was closed.
- GH-4368 — icon-vs-text split within history (to be closed by this task).
- GH-3927 / GH-4064 — prior fixes; the running ≠ success invariant.
- TASK-404 — `ExecutionLifecycle` / `ExecStatus*` vocabulary.
- Memory `tui-subtask-ghost-rows`; `pilot-aws` skill hard rule 5 ("trust the
  ledger over dashboard panels") — both are workarounds this task retires.

---

**Last Updated**: 2026-07-24
