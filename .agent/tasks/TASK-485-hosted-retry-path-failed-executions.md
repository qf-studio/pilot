# TASK-485: Hosted retry path for failed executions (#5008)

**Status**: ✅ Legs 1+2 SHIPPED + REVIEWED same day (2026-08-25). Leg 1: #5211 → **PR#5214 merged 14:21Z, review APPROVE-w-notes** (carve-out correct; follow-up FILED as [#5217](https://github.com/qf-studio/pilot/issues/5217): warning alert on persistent env-class streak). Leg 2: #5212 → **PR#5215 review APPROVE-w-notes posted pre-merge** (autopilot merges on green; Pilot verified the pilot-blocked-invisible-to-admission problem and built a parallel sweep loop — correct; notes: ~16min+tick re-arm latency after label-cycle · reclassify/label-remove not atomic · manual pilot-blocked strip bypasses the probe, degrades to re-stall). **Both merged post-14:00Z train → ride tomorrow's v2.270.0.** Next: after that train lands on the box → dispatch Leg 3 (console C8 label-cycle, Phase 3) → Phase 4 live validation on ship-test-js#6 (expect the ≤16min latency).
**Created**: 2026-08-25
**Assignee**: Pilot (via `pilot`-labeled issues)

---

## Context

**Problem** ([#5008](https://github.com/qf-studio/pilot/issues/5008), live repro `pilot-ship-test-js#6`):
A hosted tenant's task failed in 4s (missing `ANTHROPIC_API_KEY` — 0 tokens, no diff, exit 1). After the credential was fixed, the daemon refused re-dispatch forever. On the founder box the operator falls back to ledger surgery; a hosted tenant has no lever at all.

**Root cause (nav-research 2026-08-25 — corrects the issue narrative):**
The #5008 framing "failed counts as terminal for dedupe" is **wrong**. `Store.HasTerminalCompletion` (`internal/memory/store.go:1290-1311`) treats only completed+deliverable, no_op+no-error, and canceled rows as terminal — a bare `failed` row retries normally via `nextRetryGeneration` (`internal/executor/dispatcher.go:1527-1568`, the #4372 fix). The actual blocker:

1. An env-class failure reproduces **byte-identically** every attempt (same error text, ~4s, 0 tokens).
2. `consecutiveIdenticalFailureThreshold = 2` (`dispatcher.go:1427`) trips after two attempts → `escalateIdenticalFailureStreak` → `escalateStalledTask` (`dispatcher.go:1994/2016`) marks the row `status='stalled'` and `surfaceStalledIssue` (`dispatcher.go:2094`) applies the **`pilot-blocked`** label.
3. The poller **unconditionally excludes** `pilot-blocked` issues from its candidate list, before any admission logic, with zero log lines (`dispatcher.go:2086-2089`; pitfall `hard-cap-rearm-in-memory-gate`).
4. Nothing probes for "operator fixed the cause" — GH-5139's re-arm machinery (`cmd/pilot/rearm_canceled.go`) covers **canceled rows only** (`LatestCanceledExecution`, hard `status='canceled'` filter).

**Goal** (founder scoping answers, 2026-08-25 — recorded on #5008):
1. **Q1**: C8 dispatch verb = explicit customer intent → supersedes a failed/stalled execution as a new generation.
2. **Q2**: Env-class failures (0 tokens, instant exit, no diff) are non-terminal for dedupe and auto-retriable — distinct from genuine code failures.
3. **Q3**: Label-cycle (strip + re-add trigger label) re-arms a failed/stalled task on hosted boxes.

---

## Known Pitfalls & Patterns

- **PITFALL** (`hard-cap-rearm-in-memory-gate`): DB-only surgery doesn't re-arm a blocked task — five things must clear, including the `pilot-blocked` label the poller filters silently. Any re-arm leg MUST clear the label AND the row state, atomically enough that neither survives alone. → Phase 2 clears both, label first.
- **PITFALL** (`noop_terminal_state_invisible_to_dispatch_guards`, GH-4347): one guard can't see a terminal state another guard wrote. New `stalled` re-arm must demote the row into the state the ordinary retry path already understands (`failed`), exactly like `ReclassifyCanceledForRearm` — never invent a parallel bypass.
- **PITFALL** (`sequential-gates-on-execution-not-merge-fastfollow-misbase`): closing an issue arms a clean retry; never invert. Label-cycle semantics must not conflict with the close→refile recovery.
- **PATTERN** (GH-5139, `rearm_canceled.go`): probe GitHub for post-terminal operator evidence (reopen/relabel event after the terminal timestamp), then `Reclassify*` the row out of the blocking predicate, throttled via `repickBackoff`. Phase 2 extends this template verbatim to `stalled`.
- **DECISION** (#4319): claims live in `execution_claims`, key `(task_id, project_path, generation)`, permanent rows, retries claim generation+1 via `nextRetryGeneration` → `lifecycle.Begin`. All re-arm paths funnel through this seam; no new claim mechanics.
- **DECISION** (TASK-444): C8 dispatch is deliberately tracker-only — no console→daemon write path. Supersede semantics (Q1) are delivered by making C8 emit the Phase 2 re-arm evidence (label cycle), not by a new endpoint.

---

## Acceptance Criteria

- [ ] An execution failing with the env-class signature (0 tokens, sub-threshold duration, no commit/PR, matching error class) never trips `consecutiveIdenticalFailureThreshold` — it retries with backoff and logs a distinct env-class reason.
- [ ] A `stalled` + `pilot-blocked` task whose issue receives a trigger-label re-add (or reopen) event AFTER the stall timestamp is re-armed: row demoted `stalled→failed`, `pilot-blocked` removed, next poll dispatches generation+1.
- [ ] C8 dispatch on a card whose task is failed/stalled produces that evidence (label cycle incl. `pilot-blocked` removal) — verified behavior, not assumed (founder: establish current behavior first).
- [ ] Live repro `pilot-ship-test-js#6` un-wedges through the product path with zero ledger surgery.
- [ ] A genuine code failure repeating identically still stalls at threshold 2 — the carve-out is env-class only.
- [ ] Re-arm probe is throttled (existing `repickBackoff` window) — no hot per-tick GitHub API loop.

---

## Implementation

### Phase 1: Env-class failure classifier (daemon — Q2)
**Goal**: recognize env/infra-credential failures and route them away from the identical-failure streak.

**Tasks**:
- [ ] New predicate (runner.go signature-set idiom, alongside `permanentFailurePatterns` / `rateLimitedSignatures`): error-text patterns for missing/invalid credentials (`ANTHROPIC_API_KEY`, OAuth token, backend `IsAvailable()==false` construction errors) AND structural corroboration from the execution row (tokens_total=0, no commit_sha/pr_url, duration below threshold). No `exit_code` column exists — do not add one; text + structure suffices.
- [ ] Route env-class failures around `priorClaimsHadIdenticalFailureStreak` the same way the existing stall/infra carve-outs do (`dispatcher.go:1746-1848`). Backoff still paces retries (`repickBackoff`).
- [ ] Table-driven tests: env-class signature → no streak escalation after N identical failures; genuine code failure → stalls at 2 as today.

**Files**:
- `internal/executor/runner.go` — signature set + predicate
- `internal/executor/dispatcher.go` — carve-out in the streak path
- `internal/executor/dispatcher_test.go`

### Phase 2: Stalled re-arm probe (daemon — Q3, GH-5139 template)
**Goal**: label-cycle / reopen after a stall re-arms the task through the sanctioned path.

**Tasks**:
- [ ] `LatestStalledExecution` + `ReclassifyStalledForRearm` (stalled→failed) in `internal/memory/store.go`, mirroring the canceled pair (store.go:1326-1366).
- [ ] `tryRearmStalled` in `cmd/pilot/` mirroring `rearm_canceled.go`: evidence = `labeled(triggerLabel)` or `reopened` event after the stall timestamp; on success reclassify AND remove `pilot-blocked` (label must not survive the row demotion — pitfall above).
- [ ] Wire into the admission path next to `tryRearmCanceled` (`cmd/pilot/main.go:4262-4285`), throttled by the same `repickBackoff` window. Note: since `pilot-blocked` excludes the issue from the candidate list entirely, the probe must run from a path that still sees the issue — follow how GH-5139 hooked its probe; if candidate exclusion happens first, probe from the exclusion site or a periodic sweep.
- [ ] Tests incl. the ordering trap: fresh queued row alongside old stalled row (GH-4347 any-row lesson).

**Files**:
- `internal/memory/store.go` + tests
- `cmd/pilot/rearm_stalled.go` (new) + tests
- `cmd/pilot/main.go` — wiring

### Phase 3: C8 dispatch verb supersede (console repo — Q1)
**Goal**: explicit dispatch click on a failed/stalled card re-arms it.

**Tasks**:
- [ ] Establish current behavior: what C8 does today for a card whose task has a failed/stalled execution (founder: untested).
- [ ] Make dispatch on such a card cycle the trigger label (remove if present, re-add) and remove `pilot-blocked` — producing exactly the Phase 2 evidence. Tracker-only; no daemon write path (TASK-444 decision stands).
- [ ] Depends on Phase 2 being live on the box first (sequence the issues).

**Files** (sibling repo `qf-studio/pilot-console`):
- `internal/boardapi/dispatch.go` + tests

### Phase 4: Live repro validation (operator)
- [ ] After Phases 1–2 ride a train to the tenant box: cycle the label on `pilot-ship-test-js#6` → verify pick-up, execution, PR. Close the fixture issue only after it ships.

---

## Out of Scope

- Changing `HasTerminalCompletion`'s predicate — failed rows are already non-terminal there; the fix targets the streak escalation, not the terminal predicate.
- `exit_code` schema column / executions migration.
- `internal/autopilot/controller.go` call-site sweep of the shared predicates (follow-up if Phase 2 touches them).
- Auto-detecting "env var fixed" without operator/customer signal — evidence stays event-based (label/reopen/dispatch click).
- Retry semantics for the `repickBackoff` hard cap (5) — unchanged.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Env-class detection | exit_code column · text-only regex · text+structural signature | text + structural (0 tokens, no deliverable, short duration) | No schema change; structure alone can't distinguish env from instant code failure, text alone is brittle |
| Re-arm seam | new bypass in poller · extend GH-5139 reclassify pattern | extend GH-5139 (`stalled→failed` demotion) | Proven idiom; keeps ONE retry path (`nextRetryGeneration`); avoids GH-4347 parallel-guard class |
| C8 supersede | console→daemon endpoint · tracker label-cycle | tracker label-cycle | TASK-444 decision stands (tracker is the message bus); C8 reuses Phase 2 evidence for free |
| Retry admission | auto-clear on any failure · env-class carve-out only | env-class carve-out only | Founder Q2; genuine code failures keep once-failed/streak protection (#4372 lineage) |

---

## Verify

```bash
make test                                  # full suite
go test ./internal/executor/ ./internal/memory/ -run 'Streak|Rearm|EnvClass'
make lint
```

Live: `pilot-ship-test-js#6` label-cycle → dispatched (Phase 4).

---

## Done

- [ ] Env-class predicate exists with table-driven tests; streak carve-out proven by test.
- [ ] `rearm_stalled` probe wired at admission, throttled; stalled→failed demotion + label removal covered by tests against real SQLite.
- [ ] Console C8 leg merged in sibling repo.
- [ ] Fixture issue shipped through the product path; #5008 closed.

---

## Refs

- [#5008](https://github.com/qf-studio/pilot/issues/5008) — design issue + founder answers (2026-08-25 comment)
- Research report (nav-research 2026-08-25): real blocker = identical-failure streak → stalled + pilot-blocked, not HasTerminalCompletion
- GH-5139 (canceled re-arm template) · GH-4372 (once-failed-block fix) · GH-4347 (any-row terminal check) · TASK-444 (C8 tracker-only decision) · #4319 (execution_claims generation idiom)

---

**Last Updated**: 2026-08-25
