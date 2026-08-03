# fix(executor): wire ProjectConfig.Canary → Task.IsCanary at intake — GH-4240's canary exclusion is a no-op (is_canary=0 on every row)

**Status**: 🟡 Main leg delivered, follow-up PR stuck — #4648 → PR#4652 merged 07-31 (intake wire) + #4650 → PR#4651 merged (exclusion test); released in **v2.251.1**. Follow-up #4649 (fresh-intake sites) → **PR#4653 OPEN, autopilot stage `failed`, CI pending** as of 08-03 08:04Z board — needs investigation before this doc archives. · **Last Updated**: 2026-08-03

## Context

GH-4240 shipped canary isolation in two halves that were never connected:

- **Config half**: `ProjectConfig.Canary bool` (`internal/config/config.go:283`) marks a project as a synthetic sandbox.
- **Read half**: ~20 SQL filters `COALESCE(is_canary, 0) = 0` across `internal/memory/store.go` + `internal/memory/metrics.go` (success-rate, cost, model, dashboard, lifetime-counts queries), plus live-path guards `!task.IsCanary` around `metricsRecorder` at `internal/executor/runner.go:2030/3164/3555`.

**The intake wire between them is missing.** `Task.IsCanary`'s own doc comment (`runner.go:482–487`) says "Set once at intake from the project config (ProjectConfig.Canary, GH-4240)" — but the only assignment sites in the tree are propagation, not intake:

- `internal/executor/epic.go:2849` — epic sub-task inherits `parent.IsCanary`
- `internal/executor/dispatcher.go:2926` — task restored from an execution row copies `exec.IsCanary`
- `internal/executor/lifecycle.go:130` — task → execution row (write-through)

No site ever reads `ProjectConfig.Canary`. Consequence, operator-verified on the box ledger: **`is_canary = 0` on every row ever written.** Every read-side filter is dead code; the runner metric guards never fire; canary executions count in every aggregate. Where exclusion appears to work, it's only because the caller happens to scope by `project_path` — unscoped callers include canary rows: the autopilot metrics hydrator (`internal/autopilot/metrics_hydrator.go:52`, `GetLifetimeTaskCounts("")`) and the gateway dashboard aggregates.

Corroborating breadcrumb: `dispatcher.go:1554–1559` (a prior investigation) already records that the daemon "never inspects IsCanary" — the flag was known-inert on the read path there; this issue fixes the write path so the whole mechanism finally works as documented.

## Acceptance

1. **Every fresh-intake `Task` construction sets `IsCanary` from the owning project's config.** Known fresh-build sites: `dispatcher.go:~2426` (ProjectWorker build — the main poller intake) and `dispatcher.go:~2301`. Decomposed children (`decompose.go:~430`) inherit `parent.IsCanary` (same posture as `epic.go:2849` — do not re-resolve config). The AC is the **invariant, not this site list** — audit every `Task{...}` literal in `internal/executor` and classify it fresh-intake (wire it) vs propagation (leave it); note the classification in the PR description. Existing propagation sites stay as-is.
2. **Regression tests**: (a) a task built for a canary-configured project produces an execution row with `is_canary=1` through the `ExecutionLifecycle.Begin` chokepoint; (b) a decomposed child of a canary parent inherits it; (c) a non-canary project row lands `0` (no false positives).
3. **One read-side exclusion test**: a representative query (e.g. `GetLifetimeTaskCounts`) excludes an `is_canary=1` row — converting the dormant filters from dead code into asserted behavior.
4. If the mechanism ends up anywhere other than "once at intake", update `Task.IsCanary`'s doc comment to match reality.
5. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

- The ProjectWorker knows its project path; check whether it already holds (or can cheaply receive) the project's `ProjectConfig` — mirror however other per-project settings reach task construction. If the config isn't in reach at a build site, thread the single bool, not the whole config.
- **Out of scope**: backfilling historical rows on any live ledger (operator action — path-scoped UPDATE per ops SOP, never string time-ranges) · removing or consolidating the read-side filters · per-tenant semantics on hosted instances (a canary-only tenant zeroing its own metrics is a product question, not this bug).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-31 · https://github.com/qf-studio/pilot/issues/4648 (label: pilot)
- GH-4240 (original feature — closed; shipped config + read halves only) · GH-4536/TASK-419 (source of the restore-path propagation site)
- `.agent/.context-markers/2026-07-31_first-self-upgrade-trains-unblocked-4646.md` WATCH item 4 (the unfiled-candidate note this resolves)
