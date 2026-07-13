# Roadmap: Pilot Throughput Acceleration (TASK-393 Program)

**Source plan**: `.agent/tasks/TASK-393-throughput-acceleration.md`
**Feeder fixes**: `.agent/tasks/TASK-394-epic-subissue-execution-ledger.md` (→ [#4140](https://github.com/qf-studio/pilot/issues/4140)), `.agent/tasks/TASK-395-epic-reconcile-false-positives.md` (→ [#4143](https://github.com/qf-studio/pilot/issues/4143))
**As of**: 2026-07-13 — **M3 baseline window OPEN: 2026-07-13 09:38 UTC → ~2026-07-20** (daemon PID 11530 on `6e60eb4b`)

## Status Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Done / merged |
| 🚀 | Dispatched to Pilot, in queue or in flight |
| ⏳ | Blocked — waiting on an entry-criteria gate |
| 📋 | Planned, not yet dispatched |
| 🔜 | Next up once current gate clears |

---

## Milestone Table (today's state inline)

| Milestone | Status (2026-07-09) | Entry Criteria | Exit Criteria | Effort | Issues |
|---|---|---|---|---|---|
| M0 — Daemon restart onto instrumented build | ✅ **DONE 2026-07-09 20:11 UTC** — daemon on `877e08c3` (incl. both wedge fixes #4125+#4146); `pilot_queue_wait_seconds` live on **:9091** | PRs #4133/#4136/#4137/#4138 merged to `main` (✅ done same-day 2026-07-09) | Daemon process running ≥ commit `3ee1d7ee` (or later); `execution_events` populated on the **direct** (non-epic) path; `curl localhost:9091/metrics \| grep pilot_time_to_pr_seconds` etc. return samples | S (ops action, 0 issues) | 0 |
| M1 — Sub-issue execution ledger | ✅ **SHIPPED 2026-07-09** — #4140 closed, PRs #4144/#4145 merged (verify FK-silence on next live epic). Reconciler false-positive reproduced on #4140 itself — validates M2's spec | None (code-complete plan, independent of M0) | Zero `FOREIGN KEY constraint failed (787)` warnings on an epic run; `GetDailyMetrics` credits sub-issue completions; merged-PR short-circuit prevents `no_op` overwrites; poller regression test green | M (1 issue, 3 internal phases) | 1 (#4140) |
| M2 — Epic reconcile false-positive fixes | ✅ **MERGED 2026-07-10** — PR [#4147](https://github.com/qf-studio/pilot/pull/4147) | #4140 merged (avoid `epic.go`/`store.go` vs `epic_reconcile.go` merge friction — no file conflict expected but explicitly sequenced) | GH-4127 replay produces zero vetoes; parent never in its own child set; closed parents skipped + label cleaned; unmerged-PR children defer instead of veto | M (1 issue, single reconciler-local diff) | 1 (#4143) |
| M3 — Baseline week (measurement window) | 🚀 **WINDOW OPEN 2026-07-13 09:38 UTC → ~2026-07-20** — daemon restarted onto `6e60eb4b` (canary rows excluded #4256, truthful ledger #4248, FK-787 unrepresentable #4257). Window deliberately NOT backdated to M0 (2026-07-09): Jul-12 cascade incident + hardening churn would contaminate. Pre-window histogram snapshot (ledger-hydrated history, subtract for in-window deltas): `time_to_pr` n=93, `queue_wait` n=104, `approval_wait` n=31. Canary contamination guard: sandbox rows carry `canary:true` and are metrics-excluded since #4256 | **Hard gate**: M0 done (instrumented daemon live) AND M1 merged (uncontaminated success metrics — M1 explicitly fixes no_op overwrites that would pollute baseline success metrics). **Soft gate**: M2 recommended but not wall-clock-blocking (affects epic label churn, not timing) — see Decision Point D5 | ≥5–7 days production data across direct + epic paths; `pilot_queue_wait_seconds`/`pilot_time_to_pr_seconds`/`pilot_approval_wait_seconds` have enough samples for a median; retry wall-clock share is a known number | S effort / ~1 week calendar, 0 issues | 0 |
| M4 — Phase 2: Execution lanes | 📋 Planned | M3 baseline captured; **Decision Point D2** resolved (lane-first vs concurrency-first) | `lane` recorded on execution rows; trivial-class median wall-clock measurably lower than M3 baseline on the lane-segmented dashboard; fast lane uses `MinimalBuildGate()` + haiku tier + no research (already-partial today) | M, ~3 issues | 3 |
| M5 — Phase 3: N-concurrent + pipeline overlap | 📋 Planned | M3 baseline captured; **P3.1 shared `WorktreeManager` must land and merge before the worker-pool change in the same phase** (hard intra-phase dependency, GH-1312 race); Decision D2 resolved | Two live worktrees for one repo confirmed (`git worktree list \| grep -c pilot-worktree` ≥2 during concurrent run); zero git-race incidents on pilot-repo canary; conflict rate visible on M0 dashboards | L, ~4 issues | 4 |
| M6 — Phase 4: Repo primer + prefetch | 📋 Planned | M3 baseline captured (to measure research-phase duration delta). Low file overlap with M4/M5 (`prompt_builder.go`, `parallel.go` vs `complexity.go`/`dispatcher.go`) — **can dispatch in parallel with M4/M5** if desired | Primer hit-rate metric exists; research-phase duration drops on primer hits vs M3 baseline; Navigator prefix in `BuildPrompt()` unchanged (mem-004 regression check) | M, ~3 issues | 3 |
| M7 — Phase 5: Trust-tier auto-merge | 📋 Planned | **Hard dependency: Phase 2 lane classification (M4)** — `RiskScoreReason` explicitly takes "lane from Phase 2" as an input signal (TASK-393 Phase 5 task list) | Auto-merged low-risk PR count > 0 with 0 incorrect merges; `TestRiskScore` fail-safe suite green; approval-wait median dropped vs M3 baseline; escalate-only contract preserved (score never suppresses `SizeFloorReason`/`ScopeDriftReason`/`RequireApproval`) | M, ~3 issues | 3 |
| M8 — Program validation / close-out | 📋 Planned | M4–M7 all shipped and each individually validated against M3 baseline | Fleet median issue-labeled→PR-merged wall-clock improved vs M3 baseline (TASK-393 Done checklist, final line); all 6 TASK-393 acceptance criteria checked off | S, 0–1 issues (report/dashboard review) | 0–1 |

---

## Dependency Graph

```
M0 (daemon restart) ──► M3 (baseline week) ◄── M1 (#4140, ledger)
                                │                    ▲
                                │                    │ sequenced before
M2 (#4143, reconcile) ─────────┘ (soft gate)    ─────┘  (no hard code dep,
                                                          avoids merge friction)

M3 ──► M4 (Phase 2: lanes) ──► M7 (Phase 5: risk score)
   │        [lane column]         [needs lane as risk input]
   │
   └──► M5 (Phase 3: concurrency)
             │
        P3.1 shared WorktreeManager
        (must merge before worker-pool
         change within M5 — GH-1312 race)

M3 ──► M6 (Phase 4: primer)   [low file overlap with M4/M5 — can run parallel]

M4 + M5 + M6 + M7 ──► M8 (program validation)
```

Explicit edges:

- **M1 → M3**: M3 cannot start collecting a *trustworthy* baseline until #4140 lands — otherwise `no_op` overwrites and starved `completed` counts (TASK-394 defect 3) corrupt the very success-rate numbers M3 exists to capture.
- **M0 → M3**: baseline recording literally does not start until the daemon runs the instrumented build (Phase 1 histograms/events only exist ≥`3ee1d7ee`).
- **P3.1 (shared WorktreeManager) → rest of M5**: `CreateWorktreeWithBranch` today builds a fresh `WorktreeManager` per call (`worktree.go:768-775`); concurrent same-repo execution without a shared manager silently reintroduces the GH-1312 git worktree race (mem-102). Must be the first issue merged inside M5.
- **M4 (lane column) → lane-segmented dashboard**: the `lane` field must be persisted on the execution row before the Phase 1 dashboard panels can segment by lane — sequence the lane-resolution issue before the dashboard-segmentation issue within M4.
- **M4 → M7**: Phase 5's `RiskScoreReason` takes lane as one of its inputs (alongside lines/files touched, test delta, path sensitivity) — M7 cannot start until M4's lane field exists and is populated on live rows.
- **M2 sequenced after M1** (not a hard code dependency — different files, `epic.go`/`store.go` vs `epic_reconcile.go`); TASK-395 explicitly defers to avoid merge friction, and #4140 reduces (not eliminates) the reconciler's exposure to the eventually-consistent evidence problem.

---

## Decision Points (human-in-the-loop)

**D1 — Retry-redesign task, triggered by M3 data.**
TASK-393 measures "retry wall-clock share" in Phase 1 but keeps retry-strategy redesign **out of scope** pending that number. After M3: if retried attempts (smart retry, quality-gate retry ×2, intent-judge self-correct) account for a large share of total wall-clock, spin a dedicated task; otherwise fold into general backlog. No threshold pre-committed — set one when the M3 number is in hand.

**D2 — Phase 2 (lanes) vs Phase 3 (concurrency) ordering.**
Both gate on M3 but are not mutually exclusive (different files: `complexity.go`/`quality/types.go` vs `dispatcher.go`/`worktree.go`, low conflict risk). Choose by what the baseline shows dominates:
- **Fixed per-task ceremony** dominates trivial/small issues → M4 first — cheaper, lower risk, unlocks M7 sooner via the lane dependency.
- **Queue-wait / serialization** dominates (issues stacked on one repo) → M5 first — higher effort/risk (L) but the larger lever.
- Parallel dispatch is possible given low file overlap, but "measure first, verify each phase against baseline" argues for serial landing so each phase's delta stays attributable — recommend serial unless queue pressure forces parallel.

**D3 — Concurrency rollout repo order (within M5).**
Default is fixed (opt-in, `max_concurrent_executions=1`) and the first canary is the pilot repo itself. Repo #2+ is a human pick after the canary shows zero git-race incidents over a meaningful sample — informed by repo activity volume and blast-radius tolerance.

**D4 — Fate of dead `orchestrator.max_concurrent` config.**
Flagged undecided in TASK-393 (`internal/config/config.go`). Options: remove it (no practical effect today — only gates poller goroutines that park in `WaitForExecution`, mem-101), or repurpose as an alias for the new `max_concurrent_executions` knob. Decide during M5's worker-pool issue — repurposing changes migration/back-compat scope.

**D5 — Does M2 (#4143) gate the M3 baseline?**
M2 fixes epic-parent label churn and false escalations — it does not touch wall-clock instrumentation or success-count fields (M1's job). Recommendation: **do not hard-gate M3 on M2** — start the baseline week as soon as M0+M1 clear. Revisit only if epic volume during the baseline week is high enough that false `pilot-needs-clarification` escalations visibly distort dispatch cadence.

**D6 — Epic concurrency in a future v2.**
Excluded from M5 v1 (mem-023: epic decomposition can discard child work on worktree cleanup; sub-issues stay serialized via `SetSubIssueMergeWait`). Revisit only after M5 v1 is stable in production and M1's ledger has proven epic-path execution tracking solid.

---

## Suggested Issue Decomposition (Phases 2–5)

Phase 1 precedent: 1 epic (#4127) auto-decomposed into 4 sub-issues (#4128–#4131). Sizing mirrors that granularity.

**M4 — Phase 2: Execution lanes (~3 issues)**
1. **Lane resolution + config surface** — `internal/executor/complexity.go`, `model_routing.go`, `configs/pilot.example.yaml`. Introduce `Lane` bundling model/timeout/effort/research-on-off (all exist) into one resolved value.
2. **Tiered quality gates + preflight tiering** — `internal/quality/types.go` (`ResolveConfig`/`GetRequiredGates`), `internal/executor/runner.go`. Promote `MinimalBuildGate()` from fallback to explicit fast-lane tier; affected-tests-only detection.
3. **Lane persistence + dashboard segmentation** — add/reuse `lane` column on execution rows (`complexity_level` exists), segment Phase 1 grafterm panels by lane; verify `.pilot/workflow.yaml` per-repo override interplay (untraced in TASK-393).

**M5 — Phase 3: N-concurrent + pipeline overlap (~4 issues)**
1. **P3.1 shared WorktreeManager (prerequisite, must merge first)** — `internal/executor/worktree.go`, `runner.go:1767-1793`. Kill the per-call manager in `CreateWorktreeWithBranch` (mem-102).
2. **Bounded worker pool + config knob** — `internal/executor/dispatcher.go` (`processQueue`, single-slot at :843-899), `internal/config/config.go` (new `max_concurrent_executions`, default 1; resolve D4).
3. **Collision guard at worker layer** — reuse `groupByOverlappingScope` (`poller.go:1108`); overlapping-scope issues stay ordered, disjoint run parallel; epics excluded (mem-023).
4. **Canary rollout + monitoring** — enable on pilot repo, wire conflict-rate tracking to M0 dashboards, document sequential-mode/SDK-poller interplay (SDK poller is auto-only, `poller_github.go:124-125`).

**M6 — Phase 4: Repo primer + prefetch (~3 issues)**
1. **Primer generation + post-merge regeneration hook** — repo-map artifact keyed by HEAD SHA, under `.agent/system/` or a cache dir; hook in `internal/autopilot/controller.go`.
2. **Injection + research short-circuit (additive only)** — `internal/executor/prompt_builder.go` (`loadProjectContext()` call site :163-167), `parallel.go`/`runner.go` (`ExecuteResearchPhase` :2276-2296). Navigator prefix untouched (mem-004).
3. **Prefetch during queue wait (stretch — may fold into issue 2 or defer)** — plan/research pre-pass while queued so execution starts with resolved file paths.

**M7 — Phase 5: Trust-tier auto-merge (~3 issues)**
1. **`RiskScoreReason` + escalate-only fail-safe tests** — `internal/autopilot/scope_guard.go` alongside `SizeFloorReason`/`ScopeDriftReason` (mem-103); inputs: lane (M4), path sensitivity (auth/, migrations/, .github/), test delta; `TestRiskScore` proves the score never suppresses existing escalations.
2. **Config threshold + post-merge digest** — `internal/autopilot/types.go` (per-env threshold, `prod` keeps `RequireApproval`); daily digest of auto-merged PRs via bot/Telegram.
3. **Approval-wait dashboard delta** — wire `pilot_approval_wait_seconds` comparison vs M3 baseline into the M0 grafterm panel.

---

## Success Metrics (program-level "done")

From TASK-393 Acceptance Criteria / Done checklist — all queryable from M0 instrumentation:

1. **Pipeline breakdown dashboard live** — one panel answers "where does wall-clock go per shipped issue" (queue → execute → PR → CI → approval → merge → release) from persisted `execution_events` + histograms (`pilot_time_to_pr_seconds`, `pilot_queue_wait_seconds`, `pilot_approval_wait_seconds`, `pr_time_to_merge`, `ci_wait_duration`). Panel: `deploy/grafana/grafterm-pilot.json`.
2. **Concurrency safety** — ≥1 repo running N-concurrent with 0 git-race incidents; `git worktree list | grep -c pilot-worktree` ≥2 during a live concurrent run.
3. **Trivial-lane speedup** — trivial-class issues show measurably lower median wall-clock than M3 baseline on the lane-segmented dashboard (reduced gates + haiku tier + no research).
4. **Primer effectiveness** — primer hit-rate metric exists; research-phase duration measurably drops on primer hits vs M3 baseline (SHA-keyed freshness).
5. **Auto-merge safety + throughput** — auto-merged low-risk PR count > 0, 0 incorrect merges, approval-wait median dropped vs M3; escalate-only contract never bypassed.
6. **Headline**: fleet median issue-labeled → PR-merged wall-clock improves vs the M3 baseline — the single number that closes the program.

Query anchors (NOTE: the live store is `~/.pilot/data/pilot.db`, NOT the empty legacy `~/.pilot/pilot.db`):
```bash
# Ledger-backed timeline for the latest execution
sqlite3 ~/.pilot/data/pilot.db "SELECT stage, occurred_at FROM execution_events WHERE execution_id=(SELECT id FROM executions ORDER BY created_at DESC LIMIT 1) ORDER BY occurred_at;"

# Phase 1 histograms present and populated
curl -s localhost:9091/metrics | grep -E 'pilot_(time_to_pr|queue_wait|approval_wait)_seconds'

# Daily success-count sanity (post-M1: no pollution from no_op overwrites)
sqlite3 ~/.pilot/data/pilot.db "SELECT task_id,status FROM executions WHERE date(created_at)=date('now');"
```

---

## Notes for whoever picks this up next

- **M3 window is live (2026-07-13 09:38 UTC → ~2026-07-20).** During the week, collect the two decision inputs: D1 (retry wall-clock share) and D2 (queue-wait vs per-task ceremony dominance → picks M4-first vs M5-first). End-of-window: compute medians from in-window samples only (subtract pre-window snapshot counts 93/104/31 from histogram `_count`s, or filter `execution_events` by `occurred_at >= '2026-07-13 09:38'`).
- Do not dispatch M4/M5/M6/M7 before M3 has at least a few days of clean data — the program's "measure first" ordering is why Phase 1 shipped ahead of everything, and M1 exists to keep that data clean.
- `.agent/tasks/TASK-393-throughput-acceleration.md` carries the per-phase Files lists and Technical Decisions — use those as literal issue-body content when dispatching M4–M7, mirroring how #4127 was decomposed for Phase 1.

---

**Last Updated**: 2026-07-13
