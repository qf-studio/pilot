# TASK-393: Pilot Throughput Acceleration

**Status**: 🟡 **Phase 1 shipped; phases 2–5 STALLED.** The M3 baseline window (opened 2026-07-13 09:38 UTC, closed ~07-20) **was never harvested** — no post-window histogram analysis exists, and every later phase is gated on it. Reviving this track means either harvesting what the box still holds (`time_to_pr`/`queue_wait`/`approval_wait`, pre-window snapshot n=93/104/31) or opening a fresh window. Note phase 3 (N-concurrent per repo via a `ProjectWorker` pool) is now **also** a reliability lever: the pool is the sole serialization point, which is why the pilot repo's queue saturates while sibling repos idle (observed 08-03). · **Last Updated**: 2026-08-03
**Created**: 2026-07-09
**Assignee**: Manual (Navigator plan → dispatch phases to Pilot as separate issues)

---

## Context

**Problem**:
Pilot ships issues reliably but slowly. Wall-clock per shipped issue is dominated
not by model speed but by structural waits: strictly serial per-repo execution,
fixed per-task ceremony regardless of task size, live repo re-exploration on every
run, and retries that double model spend. We currently cannot even *see* where
time goes — no time-to-PR, queue-wait, or phase-level breakdown exists.

**Goal**:
Increase issues-shipped-per-day per repo without sacrificing safety. Five levers,
sequenced so measurement lands first and every later lever is verifiable against it.

**Research basis** (3 navigator-research agents, 2026-07-09):

1. **Serialization point is `ProjectWorker`, nothing else.** All pollers (in-tree +
   SDK fan-out) funnel into `Dispatcher.QueueTask` keyed by `task.ProjectPath`;
   `ProjectWorker.processQueue` (`internal/executor/dispatcher.go:843-899`) holds a
   `processing.CompareAndSwap` guard and pulls one task at a time, executing
   `runner.Execute` synchronously. `orchestrator.max_concurrent` (default 2) only
   governs poller goroutines that then park in `WaitForExecution` — no practical
   effect. CI/merge is already decoupled (`autopilot.Controller.OnPRCreated`,
   non-blocking ticker) — `runner.Execute` returns at PR creation. **Unblocking
   `ProjectWorker` delivers both N-concurrent execution AND pipeline overlap.**
2. **Complexity routing already exists; lanes are an extension, not a mechanism.**
   `DetectComplexity()` already drives model/timeout/effort/Navigator-weight/research
   gating (`internal/executor/complexity.go`, `model_routing.go:66-128`). What is NOT
   tiered: quality gates (build/test/lint run unconditionally,
   `internal/quality/types.go:162-201`), pre-flight checks, pre-push `Gate`.
   `MinimalBuildGate()` exists (`types.go:242-260`) but is only an auto-detect fallback.
3. **Phase timing is 90% unwired.** `execution_events` ledger exists
   (`internal/memory/store.go:348`) but `StageClaudeStarted` is epic-path only
   (`runner.go:1919`), `StageImplementationStarted` declared-unused, CI-wait entry
   not logged. Only 3 duration histograms exported (`execution_duration`, `ci_wait`,
   `pr_time_to_merge` — `internal/gateway/prometheus.go:241-256`). Missing: time-to-PR,
   queue wait, approval wait, post-merge/release wait. Per-gate `Duration` in
   `quality.CheckResults` is computed but never persisted.
4. **Primer insertion points identified.** `loadProjectContext()`
   (`prompt_builder.go:678-712`) live-extracts DEVELOPMENT-README sections per prompt
   build; `ExecuteResearchPhase` (`parallel.go:99`) burns a multi-subagent exploration
   pass per Medium/Complex task (logged, not persisted). A pre-computed primer keyed
   by repo SHA replaces the first and can short-circuit the second.
5. **Risk-score slot exists in the merge gate.** `handleCIPassed`
   (`internal/autopilot/controller.go:1212-1277`) with `SizeFloorReason` /
   `ScopeDriftReason` (`scope_guard.go`) — escalate-only pattern, `files` already
   fetched. Note: `require_ci` gates the *release train* (`ReleaseConfig.RequireCI`),
   NOT the merge decision — do not conflate.

---

## Known Pitfalls & Patterns

- **PITFALL** (100%, mem-004): Navigator prefix in `BuildPrompt()` is Pilot's core
  value — any primer/lane change to prompt assembly MUST preserve it. → Phase 2/4
  tasks touch `prompt_builder.go` additively only.
- **PITFALL** (research, GH-1312): `CreateWorktreeWithBranch` (`worktree.go:768-775`)
  builds a fresh `WorktreeManager` (fresh `createMu`) per call — concurrent same-repo
  execution without a shared manager silently reintroduces the git worktree race.
  → Phase 3 prerequisite P3.1.
- **PITFALL** (85%, mem-083): `/tmp/pilot-worktree-*` paths fail strict repo-allowlist
  equality; resolve via `git rev-parse --git-common-dir`. → Phase 3 concurrent
  worktrees multiply exposure; keep resolution in any new pool code.
- **PITFALL** (90%, mem-023): epic decomposition can discard child work on worktree
  cleanup — Phase 3 concurrency must not touch epic sub-issue serialization
  (`SetSubIssueMergeWait`) in v1; epics stay serial.
- **PITFALL** (research): every retry loop (smart retry, quality-gate retry ×2,
  intent-judge self-correct) re-runs the full backend — first-pass success rate is a
  throughput lever; Phase 1 must measure retry wall-clock share.
- **PATTERN** (100%, mem-038): bot module dual-path (fast Responder vs executor) is
  the established precedent for tiered routing — Phase 2 lanes mirror it.
- **PATTERN** (research): merge gates are escalate-only, never silently-bypass
  (`scope_guard.go:15`) — Phase 5 risk score follows the same fail-safe convention.

---

## Acceptance Criteria

- [ ] A Grafana/grafterm panel answers "where does wall-clock go per shipped issue"
      (queue → execute → PR → CI → approval → merge → release) from persisted data
- [ ] Two non-overlapping issues in one repo execute concurrently end-to-end
      (two live worktrees, two PRs) with zero git races
- [ ] A trivial-class issue (docs/config/one-liner) ships in a measurably lighter
      pipeline: reduced gate set, haiku-tier model, no research phase
- [ ] Medium/Complex tasks on a primer-enabled repo skip or shrink the live
      research phase when the primer is fresh (SHA-keyed)
- [ ] Low-risk PRs merge unattended via a risk score extending `scope_guard.go`;
      score never bypasses an escalation that today's gates would raise
- [ ] Before/after: median issue-labeled → PR-merged wall-clock improves on the
      pilot repo fleet (baseline captured by Phase 1 before later phases land)

---

## Implementation

> Each phase is a separately dispatchable Pilot issue (or small issue group).
> Sequencing is deliberate: Phase 1 first — everything after is measured against it.

### Phase 1: Instrument the wall-clock breakdown (MEASURE FIRST)
**Goal**: Persisted, chartable per-execution timeline; baseline before any speedup ships.
**Dispatched**: 🚀 [#4127](https://github.com/qf-studio/pilot/issues/4127) (2026-07-09)

**Tasks**:
- [ ] Wire `StageClaudeStarted` + `StageImplementationStarted` on the direct
      (non-epic) path (`runner.go` — epic path already does it at :1919; GH-3938/GH-3840
      noted this as "future pass")
- [ ] Log a `waiting_ci` execution_event when entering `StageWaitingCI` (mirrors
      in-memory `CIWaitStartedAt`, `types.go:728` — today CI-wait is unpersisted)
- [ ] Persist per-gate durations from `quality.CheckResults` into `execution_events`
- [ ] New histograms in `prometheus.go`: `pilot_time_to_pr_seconds` (dispatch → PR),
      `pilot_queue_wait_seconds` (created_at → started_at, columns already exist),
      `pilot_approval_wait_seconds` (awaiting_approval → merged events)
- [ ] Record research-phase duration/tokens (currently `slog`-only,
      `runner.go:2286-2291`) into the ledger
- [ ] Retry accounting: tag retried attempts so retry wall-clock share is queryable
- [ ] grafterm/Grafana: add "pipeline breakdown" panel + chart the two existing but
      uncharted histograms (`pr_time_to_merge`, `ci_wait_duration`)

**Files**:
- `internal/executor/runner.go` — direct-path stage events
- `internal/autopilot/controller.go` — `waiting_ci` event emission
- `internal/gateway/prometheus.go` — new histograms
- `internal/memory/store.go` — event stages (schema additions if needed)
- `deploy/grafana/grafterm-pilot.json` — panels

### Phase 2: Execution lanes (fast / standard / heavy)
**Goal**: Trivial tasks stop paying feature-sized ceremony.

**Tasks**:
- [ ] Introduce a `Lane` resolved from `Complexity` bundling the scattered knobs:
      model+timeout+effort (exists), quality-gate subset (new), preflight subset (new),
      research on/off (exists)
- [ ] Fast lane: `MinimalBuildGate()` promoted from fallback to explicit tier;
      affected-tests-only where detectable; haiku model tier (already default for
      Trivial); skip research phase (already skipped)
- [ ] Heavy lane: current behavior + optionally stricter (unchanged v1)
- [ ] Lane recorded on the execution row (`complexity_level` column exists —
      add `lane` or reuse) so Phase 1 dashboards segment by lane
- [ ] Per-repo overrides respected (check `.pilot/workflow.yaml` interplay —
      research flagged as untraced)

**Files**:
- `internal/executor/complexity.go`, `model_routing.go` — lane resolution
- `internal/quality/types.go` (`ResolveConfig`/`GetRequiredGates`) — tiered gate sets
- `internal/executor/runner.go` — preflight tiering
- `configs/pilot.example.yaml` — lane config surface

### Phase 3: N-concurrent per repo + pipeline overlap
**Goal**: Two+ non-overlapping issues execute simultaneously; next issue starts while
previous PR sits in CI. (CI decoupling already exists — this is only the worker.)

**Tasks**:
- [ ] **P3.1 (prerequisite)**: route all worktree creation through one shared
      `WorktreeManager` per project (kill the per-call manager in
      `CreateWorktreeWithBranch` path) — GH-1312 race guard must be shared
- [ ] Replace `ProjectWorker` single-slot guard with a bounded pool
      (`processQueue`, `dispatcher.go:843-899`); config: per-project
      `max_concurrent_executions` (default 1 = today's behavior, opt-in rollout)
- [ ] Collision guard: reuse `groupByOverlappingScope` (`poller.go:1108`) at the
      worker layer — overlapping-scope issues stay ordered, disjoint ones run parallel
- [ ] Epics excluded in v1 (mem-023): decomposed sub-issues keep existing serialization
- [ ] Sequential-mode + SDK-poller interplay documented (SDK poller is auto-only,
      `poller_github.go:124-125`)
- [ ] Rollout: enable on pilot repo first, watch Phase 1 dashboards + conflict rate

**Files**:
- `internal/executor/dispatcher.go` — worker pool
- `internal/executor/worktree.go`, `runner.go:1767-1793` — shared manager
- `internal/config/config.go` — new knob (decide fate of dead `orchestrator.max_concurrent`)

### Phase 4: Repo primer + queue-time prefetch
**Goal**: Executions start hot instead of re-exploring the repo.

**Tasks**:
- [ ] Primer artifact: generated repo map (key components/files/conventions), keyed
      by repo HEAD SHA, regenerated post-merge (autopilot hook) — stored under
      `.agent/system/` or a cache dir
- [ ] Inject at `loadProjectContext()` call site (`prompt_builder.go:163-167`) —
      **additive; Navigator prefix untouched (mem-004)**
- [ ] Short-circuit/shrink `ExecuteResearchPhase` (`runner.go:2276-2296`) when a
      fresh primer exists; stale primer (SHA mismatch) → current behavior
- [ ] Prefetch (stretch): when issues sit queued, run the plan/research pre-pass
      during queue wait so execution starts with resolved file paths

**Files**:
- `internal/executor/prompt_builder.go`, `parallel.go`, `runner.go`
- `internal/autopilot/controller.go` — post-merge regeneration hook

### Phase 5: Trust-tier auto-merge
**Goal**: Low-risk PRs merge unattended; human review moves to post-merge batch.

**Tasks**:
- [ ] `RiskScoreReason(files, prState, issue)` in `scope_guard.go` alongside
      `SizeFloorReason`/`ScopeDriftReason` — inputs: lines/files touched, test delta,
      path sensitivity (auth/, migrations/, .github/), lane from Phase 2
- [ ] Escalate-only contract preserved: score can only ADD escalations, never
      suppress `SizeFloorReason`/`ScopeDriftReason`/`RequireApproval`
- [ ] Config: risk threshold per env (`prod` keeps `RequireApproval` semantics)
- [ ] Post-merge review support: daily digest of auto-merged PRs (bot/Telegram)
- [ ] Measure approval-wait delta on Phase 1 dashboards

**Files**:
- `internal/autopilot/scope_guard.go`, `controller.go:1212-1277` (`handleCIPassed`)
- `internal/autopilot/types.go` — config

---

## Out of Scope

- Epic/decomposed sub-issue concurrency (mem-023 risk — epics stay serial)
- Speculative/stacked execution on unmerged parent branches (revisit after Phase 3)
- Cross-repo scheduling changes (SDK poller fan-out per repo already shipped, M7 4d.2b)
- Retry-strategy redesign (Phase 1 measures retry share first; separate task if it
  dominates)
- Model/prompt-level token optimization (explicitly not the bottleneck)
- Legacy in-tree poller `sequential` mode changes

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where parallelism lives | poller semaphore, new scheduler service, ProjectWorker pool | ProjectWorker pool | Research: it is the sole serialization point; poller knobs are neutralized; CI already decoupled |
| Lanes mechanism | new routing system, per-repo yaml only, extend Complexity | extend `Complexity` → `Lane` | Tier routing already threaded end-to-end; lanes = bundling scattered knobs |
| Risk score placement | new gate stage, PR labels, extend scope_guard | extend `scope_guard.go` | Escalate-only pattern exists at the single chokepoint `handleCIPassed` |
| Phase ordering | speedups first, measure first | measure first (Phase 1) | No time-to-PR/queue/approval metrics exist today; later phases unverifiable without baseline |
| Concurrency default | on by default, opt-in per repo | opt-in (`default 1`) | Fail-safe rollout; pilot repo canaries first |

---

## Verify

```bash
make build && make test && make lint

# Phase 1: events on direct path
sqlite3 ~/.pilot/pilot.db "SELECT stage, occurred_at FROM execution_events WHERE execution_id=(SELECT id FROM executions ORDER BY created_at DESC LIMIT 1) ORDER BY occurred_at;"
curl -s localhost:9091/metrics | grep -E 'pilot_(time_to_pr|queue_wait|approval_wait)_seconds'

# Phase 3: two live worktrees for one repo
git worktree list | grep -c pilot-worktree   # expect ≥2 during concurrent run

# Phase 5: risk-score escalation still fires on oversized PR (fail-safe check)
go test ./internal/autopilot/ -run TestRiskScore -v
```

---

## Done

- [ ] Pipeline-breakdown dashboard live; baseline week captured (window: 2026-07-13 09:38 UTC → ~2026-07-20)
- [ ] Concurrent execution enabled on ≥1 repo with 0 git-race incidents
- [ ] Trivial issues segment shows reduced median wall-clock vs baseline
- [ ] Primer hit-rate metric exists; research-phase duration drops on primer hits
- [ ] Auto-merged low-risk PR count > 0 with 0 incorrect merges; approval-wait median drops
- [ ] Fleet median issue→merge wall-clock improved vs Phase 1 baseline

---

## Refs

- **Program roadmap (milestones M0–M8, gates, decision points): `.agent/system/throughput-roadmap.md`**
- Pilot issue (Phase 1): https://github.com/qf-studio/pilot/issues/4127
- Research: 3 navigator-research reports, 2026-07-09 (dispatcher/concurrency,
  executor overhead/lanes/primer, autopilot merge-gate/metrics) — findings inlined above
- GH-1312 (worktree create race), GH-3938/GH-3840 (direct-path stage events deferred),
  GH-3994/PR #4096 (`require_ci` — release train, not merge gate), GH-4033 (`started_at`),
  M7 4d.2b PR #4115 (per-repo SDK poller fan-out)
- TASK-390 (metrics multi-controller export), TASK-392 (success-rate semantics),
  TASK-379 (execution ledger / `execution_events`)

---

## Notes

Origin: "Pilot works but it's slow — think outside the box" (2026-07-09 session).
Reframe that drove the plan: latency is structural (serial queue, fixed ceremony,
cold-start re-exploration, invisible retries), not model speed. A sixth lever —
aggressive issue decomposition at planning time — needs no code: smaller issues
compound with lanes + concurrency; adopt as Navigator planning practice.

---

**Last Updated**: 2026-07-13
